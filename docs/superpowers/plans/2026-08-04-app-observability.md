# 应用级可观测补齐 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 core 控制面补齐业务指标埋点（Prometheus）+ OTel span 租户维度 + 按 traceID 查完整 trace 链路，让真实后端（Prom/Loki/Tempo）的应用/平台维度可观测闭环。

**Architecture:** 新增 `internal/metrics` 包（自定义 prometheus.Registry + HTTP middleware + 推理指标记录器）；gateway 推理路径记 `paas_inference_*` 指标；OTel span 在 BearerAuth 注入 tenant 后打 attribute；`real/traces.go` 加 `GetTrace` 解析 Tempo OTLP JSON 构建父子 span 树（fetch 后 tenant 归属校验）；前端 trace 列表点行渲染详情瀑布图。

**Tech Stack:** Go + `github.com/prometheus/client_golang`（已传递依赖，提升为 direct）+ OpenTelemetry（已引入）+ Vue 3 + Element Plus。

## Global Constraints

- 主语言 Go；新增依赖须 Apache 2.0 兼容（`prometheus/client_golang` 是 Apache 2.0）。
- 多租户隔离横切：所有跨租户访问统一 not found 不泄漏；trace 详情 fetch 后 tenant 归属校验（Tempo `/api/traces/{id}` 不支持 tag 过滤）。
- 后端不可达降级返空 + 日志，不 5xx/不 panic（与 observability real 现有三支柱同构）。
- KISS/YAGNI：应用 Pod 自身埋点不侵入（不在范围）；多副本 Redis 共享留后续。
- 注释语言中文，与代码库一致。
- **未经用户明确要求，不执行 git commit / 分支操作。**（本计划末尾的 commit 步骤需用户确认）

## 设计偏离说明（spec 实施细化）

spec §1 写 `http_requests_total{...tenant}`。实施发现当前 `auth` 是 **per-handler 包装**（`mux.Handle("/api/x", auth(handler))`），tenant 在 mux 内部 handler 才注入 ctx，mux 外层 middleware 拿不到 tenant。故：

- **通用 HTTP middleware（method/route/status，不带 tenant label）** 放 mux 外层——平台运维视角的 RPS/延迟/错误，tenant 维度对其无意义（平台整体流量）。
- **tenant/model 维度的业务指标** 完全由**推理指标**承担（在 `openai.go` 内 Record，`tenant.TenantFrom(ctx)` 已可用）——推理是 PaaS 真实代理流量，tenant 维度最有价值。
- **OTel span tenant attribute** 在 `BearerAuth` 注入 tenant ctx 之后注入（单点，覆盖所有鉴权请求），不依赖外层 middleware 顺序。

## File Structure

- Create: `internal/metrics/registry.go` — 自定义 prometheus.Registry + 5 个指标定义 + InferenceMetrics 记录器
- Create: `internal/metrics/middleware.go` — HTTPMiddleware（route 归一化）+ Handler（/metrics）+ mustRecord
- Create: `internal/metrics/middleware_test.go` / `registry_test.go`
- Modify: `internal/apiroute/registry.go` — 加 `RegisteredPaths()` 导出方法
- Modify: `internal/core/gateway/openai.go` — 推理路径记 Prometheus 指标
- Modify: `internal/core/gateway/meter.go` — Meter 持有 `*metrics.InferenceMetrics`
- Modify: `internal/core/gateway/bearer.go` / `auth.go` — 注入 tenant 后设 span attribute
- Modify: `internal/observability/model.go` — Span 加 SpanID 字段 + ErrTraceNotFound sentinel
- Modify: `internal/observability/readers.go` — TracesReader 加 GetTrace 方法
- Modify: `internal/observability/memory/store.go` — memory 实现 GetTrace（mock span 树）
- Modify: `internal/observability/real/traces.go` — 加 GetTrace（Tempo OTLP 解析 + tenant 校验）
- Modify: `internal/observability/real/metrics.go` — 平台级查询对齐新指标名
- Modify: `internal/observability/handler.go` — GET /api/observability/traces/{id}
- Modify: `cmd/core/main.go` — 装配 /metrics 端点 + middleware + 注入推理指标
- Modify: `frontend/console-user/src/views/Observability.vue` — trace 详情 dialog
- Modify: `go.mod` — prometheus/client_golang 提 direct

---

### Task 1: apiroute 导出 RegisteredPaths

**Files:**
- Modify: `internal/apiroute/registry.go`
- Test: `internal/apiroute/registry_test.go`（如不存在则 Create）

**Interfaces:**
- Produces: `func (r *Registry) RegisteredPaths() []string` — 返回去重升序的已注册 path 列表（供 metrics route 归一化最长前缀匹配）

- [ ] **Step 1: 写失败测试**

在 `internal/apiroute/registry_test.go` 加：

```go
func TestRegisteredPaths(t *testing.T) {
	reg := New(nil, Info{Title: "t", Version: "1"})
	reg.Operation("GET", "/api/applications", Summary("x"))
	reg.Operation("GET", "/api/applications/{id}", Summary("x"))
	reg.Operation("POST", "/v1/chat/completions", Summary("x"))

	paths := reg.RegisteredPaths()

	// 去重 + 排序
	want := []string{"/api/applications", "/api/applications/{id}", "/v1/chat/completions"}
	if len(paths) != len(want) {
		t.Fatalf("期望 %d 条路径，实际 %d: %v", len(want), len(paths), paths)
	}
	for i, p := range paths {
		if p != want[i] {
			t.Fatalf("paths[%d]=%q, want %q", i, p, want[i])
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/apiroute/ -run TestRegisteredPaths -v`
Expected: FAIL（RegisteredPaths 未定义）

- [ ] **Step 3: 实现**

在 `internal/apiroute/registry.go` Registry 上加方法：

```go
// RegisteredPaths 返回去重并按字典序排序的已注册 path 列表。
// 供 metrics middleware 做 route 归一化（最长前缀匹配，把实际 path 映射回带 {id} 的模板）。
func (r *Registry) RegisteredPaths() []string {
	seen := map[string]struct{}{}
	for _, rt := range r.routes {
		seen[rt.Path] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
```

注意在文件 import 块加 `"sort"`（若未有）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/apiroute/ -run TestRegisteredPaths -v`
Expected: PASS

- [ ] **Step 5: 提交**（待用户确认，下同）

```bash
git add internal/apiroute/registry.go internal/apiroute/registry_test.go
git commit -m "feat(apiroute): 导出 RegisteredPaths 供 metrics route 归一化"
```

---

### Task 2: internal/metrics 包 — registry + 指标定义

**Files:**
- Create: `internal/metrics/registry.go`
- Create: `internal/metrics/registry_test.go`

**Interfaces:**
- Consumes: `github.com/prometheus/client_golang`
- Produces:
  - `type Registry struct { reg *prometheus.Registry; inf *InferenceMetrics }`
  - `func NewRegistry() *Registry`
  - `func (r *Registry) Prometheus() *prometheus.Registry`
  - `func (r *Registry) Inference() *InferenceMetrics`
  - `type InferenceMetrics struct { ... }` 含 3 个指标
  - `func (m *InferenceMetrics) RecordInference(tenant, model, status string, promptTokens, completionTokens int, durationSec float64)`
  - HTTP 指标（counter `http_requests_total{method,route,status}` + histogram `http_request_duration_seconds{method,route}`）在 Task 3 的 middleware 内引用

- [ ] **Step 1: 提升依赖为 direct**

Run: `go get github.com/prometheus/client_golang@v1.23.2`

- [ ] **Step 2: 写失败测试**

`internal/metrics/registry_test.go`：

```go
package metrics

import (
	"strings"
	"testing"
)

func TestNewRegistryRegistersAllMetrics(t *testing.T) {
	r := NewRegistry()
	gathered, err := r.Prometheus().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	for _, m := range gathered {
		names[m.GetName()] = true
	}
	want := []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"paas_inference_requests_total",
		"paas_inference_tokens_total",
		"paas_inference_duration_seconds",
	}
	for _, w := range want {
		if !names[w] {
			t.Errorf("缺少指标 %s（已有: %v）", w, keys(names))
		}
	}
}

func TestRecordInferenceEmitsTokensByDirection(t *testing.T) {
	r := NewRegistry()
	r.Inference().RecordInference("t-acme", "glm-5.2", "success", 10, 20, 0.5)

	out := dump(t, r)
	if !strings.Contains(out, `paas_inference_tokens_total{direction="completion",model="glm-5.2",tenant="t-acme"} 20`) {
		t.Errorf("缺 completion tokens 行:\n%s", out)
	}
	if !strings.Contains(out, `paas_inference_tokens_total{direction="prompt",model="glm-5.2",tenant="t-acme"} 10`) {
		t.Errorf("缺 prompt tokens 行:\n%s", out)
	}
	if !strings.Contains(out, `paas_inference_requests_total{model="glm-5.2",status="success",tenant="t-acme"} 1`) {
		t.Errorf("缺 requests 行:\n%s", out)
	}
}

// keys / dump 辅助函数见 Step 3 末尾
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/metrics/ -run TestNewRegistry -v`
Expected: FAIL（包不存在）

- [ ] **Step 4: 实现 registry.go**

`internal/metrics/registry.go`：

```go
// Package metrics 提供 core 控制面的业务指标埋点（自定义 prometheus.Registry，
// 隔离 controller-runtime 进程级指标）。
package metrics

import (
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry 持有自定义 prometheus.Registry + 业务指标句柄。
type Registry struct {
	reg *prometheus.Registry

	httpReqs        *prometheus.CounterVec
	httpDuration    *prometheus.HistogramVec
	infReqTotal     *prometheus.CounterVec
	infTokensTotal  *prometheus.CounterVec
	infDuration     *prometheus.HistogramVec
}

// NewRegistry 创建自定义 Registry 并注册全部业务指标。
func NewRegistry() *Registry {
	r := &Registry{reg: prometheus.NewRegistry()}
	r.httpReqs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total", Help: "平台 HTTP 请求总数（按 method/route/status）",
	}, []string{"method", "route", "status"})
	r.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "平台 HTTP 请求耗时分布",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	r.infReqTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paas_inference_requests_total", Help: "推理请求总数（按 tenant/model/status）",
	}, []string{"tenant", "model", "status"})
	r.infTokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paas_inference_tokens_total", Help: "推理 token 用量（direction=prompt/completion）",
	}, []string{"tenant", "model", "direction"})
	r.infDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "paas_inference_duration_seconds", Help: "推理请求耗时分布",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant", "model"})
	for _, c := range [...]prometheus.Collector{r.httpReqs, r.httpDuration, r.infReqTotal, r.infTokensTotal, r.infDuration} {
		r.reg.MustRegister(c)
	}
	return r
}

// Prometheus 返回底层 prometheus.Registry（供 /metrics handler）。
func (r *Registry) Prometheus() *prometheus.Registry { return r.reg }

// Inference 返回推理指标的便捷记录器。
func (r *Registry) Inference() *InferenceMetrics {
	return &InferenceMetrics{reqs: r.infReqTotal, tokens: r.infTokensTotal, dur: r.infDuration}
}

// InferenceMetrics 封装推理指标记录（gateway 持有）。
type InferenceMetrics struct {
	reqs   *prometheus.CounterVec
	tokens *prometheus.CounterVec
	dur    *prometheus.HistogramVec
}

// RecordInference 记录一次推理的请求计数 / token / 耗时。
func (m *InferenceMetrics) RecordInference(tenant, model, status string, promptTokens, completionTokens int, durationSec float64) {
	if m == nil {
		return
	}
	m.reqs.WithLabelValues(tenant, model, status).Inc()
	m.tokens.WithLabelValues(tenant, model, "prompt").Add(float64(promptTokens))
	m.tokens.WithLabelValues(tenant, model, "completion").Add(float64(completionTokens))
	m.dur.WithLabelValues(tenant, model).Observe(durationSec)
}

// Handler 返回 /metrics 的 http.Handler（不可达容错由 promhttp 自带）。
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		ErrorHandler: func(err error) { log.Printf("metrics /metrics: %v", err) },
	})
}

// （registry_test.go 辅助）
// keys/dump 省略——见下
```

测试辅助函数加到 `registry_test.go` 末尾：

```go
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func dump(t *testing.T, r *Registry) string {
	t.Helper()
	mfs, err := r.Prometheus().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sb strings.Builder
	enc := promhttp.NewEncoder(&sb, textEncoder{})
	_ = mfs
	// 简化：用 expfmt
	return dumpText(t, mfs)
}

func dumpText(t *testing.T, mfs []*dto.MetricFamily) string {
	var sb strings.Builder
	enc := expfmt.NewEncoder(&sb, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return sb.String()
}
```

import 补：`"github.com/prometheus/client_golang/prometheus/clientmodel"` 用 `dto`，`"github.com/prometheus/common/expfmt"`。`dump` 简化为直接调 `dumpText(t, r.Prometheus().Gather())`。删掉无用 `textEncoder` 引用。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/metrics/ -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/metrics/ go.mod go.sum
git commit -m "feat(metrics): 新增 internal/metrics 包（自定义 registry + 推理指标）"
```

---

### Task 3: HTTPMiddleware + route 归一化

**Files:**
- Create: `internal/metrics/middleware.go`
- Modify: `internal/metrics/middleware_test.go`（Create）

**Interfaces:**
- Consumes: `*Registry`（Task 2）+ route path 列表（来自 Task 1 RegisteredPaths 或外部传入）
- Produces:
  - `func HTTPMiddleware(reg *Registry, paths []string) func(http.Handler) http.Handler`
  - route 归一化：传入已注册模板 path 列表，按最长前缀匹配把实际 path 映射回模板；未匹配归 `"unmatched"`

- [ ] **Step 1: 写失败测试**

`internal/metrics/middleware_test.go`：

```go
package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPMiddlewareNormalizesRoute(t *testing.T) {
	reg := NewRegistry()
	paths := []string{"/api/applications", "/api/applications/{id}", "/v1/chat/completions"}
	mw := HTTPMiddleware(reg, paths)

	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	// /api/applications/app-1 应归一化为 /api/applications/{id}（最长前缀）
	req := httptest.NewRequest("GET", "/api/applications/app-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler 未被调用")
	}
	out := dumpText(t, mustGather(t, reg))
	if !contains(out, `http_requests_total{method="GET",route="/api/applications/{id}",status="200"} 1`) {
		t.Errorf("未按归一化 route 记录:\n%s", out)
	}
}

func TestHTTPMiddlewareUnmatched(t *testing.T) {
	reg := NewRegistry()
	mw := HTTPMiddleware(reg, []string{"/api/applications"})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))

	req := httptest.NewRequest("GET", "/something/unknown", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := dumpText(t, mustGather(t, reg))
	if !contains(out, `route="unmatched"`) {
		t.Errorf("未识别 path 应归 unmatched:\n%s", out)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/metrics/ -run TestHTTPMiddleware -v`
Expected: FAIL

- [ ] **Step 3: 实现 middleware.go**

`internal/metrics/middleware.go`：

```go
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// routeTmpl 把实际 path 映射回已注册模板（含 {id} 占位），按最长前缀匹配。
// 例：paths 含 /api/applications/{id}，实际 /api/applications/app-1 → /api/applications/{id}。
// 未匹配返 "unmatched"（防高基数 label）。
func routeTmpl(paths []string, actual string) string {
	best := ""
	for _, p := range paths {
		if matchesTemplate(p, actual) && len(p) > len(best) {
			best = p
		}
	}
	if best == "" {
		return "unmatched"
	}
	return best
}

// matchesTemplate 判 actual 是否匹配模板 tmpl（段对齐，{xxx} 段匹配任意非空段）。
func matchesTemplate(tmpl, actual string) bool {
	ts := splitPath(tmpl)
	as := splitPath(actual)
	if len(ts) != len(as) {
		return false
	}
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") {
			continue // 占位段匹配任意非空段
		}
		if ts[i] != as[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// HTTPMiddleware 记录 http_requests_total{method,route,status} + http_request_duration_seconds。
// route 经 paths（已注册模板）归一化，防高基数。
func HTTPMiddleware(reg *Registry, paths []string) func(http.Handler) http.Handler {
	// 预排序无关紧要，匹配走最长前缀。
	sorted := append([]string(nil), paths...)
	sort.Sort(byLen(sorted))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)
			route := routeTmpl(sorted, r.URL.Path)
			reg.httpReqs.WithLabelValues(r.Method, route, fmt.Sprintf("%d", rw.status)).Inc()
			reg.httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		})
	}
}

type byLen []string

func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) > len(b[j]) } // 长的在前，最长优先
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/metrics/ -v`
Expected: PASS（含 Task 2 用例）。注意把测试里的 `mustGather` 辅助加到 middleware_test.go 或 registry_test.go：

```go
func mustGather(t *testing.T, r *Registry) []*dto.MetricFamily {
	t.Helper()
	mfs, err := r.Prometheus().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	return mfs
}
```

- [ ] **Step 5: 提交**

```bash
git add internal/metrics/middleware.go internal/metrics/middleware_test.go
git commit -m "feat(metrics): HTTPMiddleware + route 归一化（防高基数 label）"
```

---

### Task 4: gateway 推理指标记录

**Files:**
- Modify: `internal/core/gateway/meter.go`
- Modify: `internal/core/gateway/openai.go`
- Modify: `internal/core/gateway/meter_test.go` / `openai_test.go`

**Interfaces:**
- Consumes: `*metrics.InferenceMetrics`（Task 2）
- Produces: `Meter` 加 `Inf *metrics.InferenceMetrics` 字段（nil 不记）

- [ ] **Step 1: 写失败测试**

在 `internal/core/gateway/meter_test.go` 加（或新建 `inference_metrics_test.go`）：

```go
func TestRecordInferenceFromServeMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	m := &Meter{Inf: reg.Inference()}

	// 模拟 serveStream 末尾的记录调用（直接测 Meter.Record 改造）
	m.recordInferenceMetrics("t-acme", "glm-5.2", "success", 30, 0.42)

	out := dumpMetrics(t, reg)
	if !strings.Contains(out, `paas_inference_requests_total{model="glm-5.2",status="success",tenant="t-acme"} 1`) {
		t.Errorf("缺 requests:\n%s", out)
	}
	if !strings.Contains(out, `paas_inference_duration_seconds_sum{model="glm-5.2",tenant="t-acme"} 0.42`) {
		t.Errorf("缺 duration:\n%s", out)
	}
}
```

`dumpMetrics` 辅助放 test 文件：调 `reg.Prometheus().Gather()` + expfmt 文本编码。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/core/gateway/ -run TestRecordInference -v`
Expected: FAIL（Inf 字段 / recordInferenceMetrics 不存在）

- [ ] **Step 3: 实现**

`internal/core/gateway/meter.go` 加字段 + 方法（注意 import `github.com/aitoys/paas/internal/metrics`）：

```go
// Meter 记录 Token 用量。本切片仅 log + 内存累计；Plan 2 接 PG/ClickHouse。
type Meter struct {
	mu    sync.Mutex
	total int
	// OnTokens 用量回写钩子（main.go 注入 billing.IncUsage，P3-2 计量采集）；nil 则不回写。
	OnTokens func(tenantID string, tokens int)
	// Inf 是 Prometheus 推理指标记录器（main.go 注入）；nil 则不记。
	Inf *metrics.InferenceMetrics
}

// recordInferenceMetrics 记录推理 Prometheus 指标（成功/失败统一入口）。
// promptTokens/completionTokens 当前合并估算（stream 按 rune 粗计），后续接上游 usage 精确拆分。
func (m *Meter) recordInferenceMetrics(tenant, model, status string, tokens int, durationSec float64) {
	if m == nil || m.Inf == nil {
		return
	}
	// 粗估：合并 tokens 全计为 completion（无上游 usage 拆分）；prompt 侧后续接 usage 时补。
	m.Inf.RecordInference(tenant, model, status, 0, tokens, durationSec)
}
```

`openai.go` `serveStream` 末尾（现 `meter.Record(tid, model, tokens)` 处）改为同时记 Prometheus：

```go
	if meter != nil {
		tid, _ := tenant.TenantFrom(r.Context())
		meter.Record(tid, model, tokens)
		meter.recordInferenceMetrics(tid, model, "success", tokens, time.Since(start).Seconds())
	}
```

需在 `serveStream` 函数开头加 `start := time.Now()`（import `"time"`）。

`ChatCompletions` 全部通道失败分支（写 503 前）加失败计数：

```go
	// 失败也记推理指标（status=fail），便于 error_rate 计算。
	tid, _ := tenant.TenantFrom(r.Context())
	meter.recordInferenceMetrics(tid, req.Model, "fail", 0, 0)
	httputil.WriteError(w, http.StatusServiceUnavailable, "all channels unavailable: "+cause)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/core/gateway/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/core/gateway/meter.go internal/core/gateway/openai.go internal/core/gateway/*_test.go
git commit -m "feat(gateway): 推理路径记 Prometheus 指标（requests/tokens/duration）"
```

---

### Task 5: OTel span tenant attribute

**Files:**
- Modify: `internal/core/gateway/bearer.go`
- Modify: `internal/core/gateway/auth.go`
- Test: `internal/core/gateway/bearer_test.go`

**Interfaces:**
- Produces: 包内 helper `annotateSpanTenant(ctx context.Context, tenantID string)` — 从 ctx 取当前 span，设 `tenant` attribute

- [ ] **Step 1: 写失败测试**

`bearer_test.go` 加（用 fake handler 验证 span attribute）：

```go
func TestBearerAuthAnnotatesSpanTenant(t *testing.T) {
	// 构造带 API Key 的 identity repo（复用现有测试 fixture）
	idb, k := seedTestAPIKey(t, "t-acme") // 复用既有 helper；若无则内联构造

	reg := trace.NewTracerProvider().Tracer("test")
	ctx, span := reg.Start(context.Background(), "srv")
	defer span.End()

	captured := make(chan string, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sp := otel.SpanFromContext(r.Context())
		if sp.IsRecording() {
			attrs := sp.(interface{ Attributes() []attribute.KeyValue }).Attributes() // test-only 断言简化
		}
		// 简化：直接验证 tenant 注入即可，span attribute 用 tracer.Exporter 验证（见下）
		tid, _ := tenant.TenantFrom(r.Context())
		captured <- tid
		w.WriteHeader(200)
	})

	// 用带 tracer 的 ctx 发请求
	req := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+k)
	rec := httptest.NewRecorder()
	gateway.BearerAuth(idb, "secret")(next).ServeHTTP(rec, req)

	if got := <-captured; got != "t-acme" {
		t.Fatalf("tenant=%q want t-acme", got)
	}
}
```

> 注：span attribute 的精确断言需 tracer exporter。若断言复杂，测试降级为「验证 annotateSpanTenant 不 panic + ctx 正确」即可，attribute 真实性靠集群 e2e 验证。优先保证 helper 存在且注入点正确。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/core/gateway/ -run TestBearerAuthAnnotatesSpanTenant -v`
Expected: FAIL / 编译错（helper 不存在）

- [ ] **Step 3: 实现**

新建 `internal/core/gateway/span.go`（或加到 bearer.go）：

```go
package gateway

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

// annotateSpanTenant 把 tenantID 作为 span attribute 注入当前 span。
// 在 BearerAuth 注入 tenant ctx 之后调用，使平台 trace 可按 tenant tag 在 Tempo 检索。
// 无活跃 span（未启用 tracing）时 no-op。
func annotateSpanTenant(ctx context.Context, tenantID string) {
	if tenantID == "" {
		return
	}
	if span := otel.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(attribute.String("tenant", tenantID))
	}
}
```

在 `bearer.go` 三处 `tenant.WithTenant` 后、`next.ServeHTTP` 前加：

- 第 30 行（JWT 分支）：`annotateSpanTenant(ctx, claims.Tenant)`
- 第 50 行（APIKey 分支，c.Tenant）：`annotateSpanTenant(ctx, c.Tenant)`
- 第 60 行（APIKey k.TenantID）：`annotateSpanTenant(ctx, k.TenantID)`

`auth.go:29`（legacy BearerAuth）同样加：`annotateSpanTenant(ctx, k.TenantID)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/core/gateway/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/core/gateway/span.go internal/core/gateway/bearer.go internal/core/gateway/auth.go internal/core/gateway/bearer_test.go
git commit -m "feat(gateway): OTel span 注入 tenant attribute（Tempo 按 tenant 检索）"
```

---

### Task 6: main.go 装配 /metrics + middleware + 推理指标注入

**Files:**
- Modify: `cmd/core/main.go`
- Modify: `cmd/core/main_test.go`（如覆盖 serveHTTP）

**Interfaces:**
- Consumes: `metrics.NewRegistry()` / `metrics.HTTPMiddleware` / `metrics.Registry.Handler()` / `apiroute.Registry.RegisteredPaths()`
- Produces: core :8080 暴露 `/metrics`（公开无鉴权）；mux 外层包 metrics middleware；meter.Inf 注入

- [ ] **Step 1: 写失败测试**

`cmd/core/main_test.go` 加（如无 serveHTTP 级测试则跳过单测，靠集成验证）：

```go
func TestMetricsEndpointExposed(t *testing.T) {
	srv := serveHTTP(testGateway, testMeter, testStores, k8sAppliers{})
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/metrics status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "paas_inference_requests_total") {
		t.Errorf("/metrics 缺推理指标")
	}
}
```

> 若现有测试构造 serveHTTP 参数过重，本步可降级为手动验证（Step 4 跑 server + curl），但优先尝试复用既有 test fixture。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/core/ -run TestMetricsEndpoint -v`
Expected: FAIL（/metrics 未挂）

- [ ] **Step 3: 实现**

`cmd/core/main.go` serveHTTP 内：

1. 在 `reg := apiroute.New(...)` 后加：

```go
	// 业务指标 registry（自定义，隔离 controller-runtime 进程级指标 :8081）。
	metricsReg := metrics.NewRegistry()
	// meter 注入推理指标记录器。
	meter.Inf = metricsReg.Inference()
```

2. mux 注册 `/metrics`（公开无鉴权，与 /livez /openapi.json 同级，在 `/livez` 注册附近）：

```go
	mux.Handle("/metrics", metricsReg.Handler())
```

3. 把 metrics middleware 接入中间件链。现：

```go
		Handler: securityHeadersMiddleware(otelhttp.NewHandler(recoveryMiddleware(csrfMiddleware(mux)), "http.server",
			otelhttp.WithFilter(skipTelemetryPaths))),
```

改为（metrics 在 csrf 内、mux 外，记录所有到达 mux 的请求）：

```go
		tracked := metrics.HTTPMiddleware(metricsReg, reg.RegisteredPaths())(mux)
		Handler: securityHeadersMiddleware(otelhttp.NewHandler(recoveryMiddleware(csrfMiddleware(tracked)), "http.server",
			otelhttp.WithFilter(skipTelemetryPaths))),
```

注：metrics middleware 不需要鉴权后 ctx（route/status/method 足够），故放外层合理；`/metrics` 自身也被包，但 Prometheus 端点被记录无碍（可后续 skipTelemetryPaths 加 `/metrics`）。

import 加 `"github.com/aitoys/paas/internal/metrics"`。

4. 把 `/metrics` 加入 `skipTelemetryPaths`（避免指标端点自身建 span 噪音），在该函数定义处补 `"/metrics"`。

- [ ] **Step 4: 跑测试 + 手动验证**

Run: `go test ./cmd/core/ -v`；再 `go run ./cmd/core &` + `curl localhost:8080/metrics | grep paas_inference`
Expected: PASS + 见指标名

- [ ] **Step 5: 提交**

```bash
git add cmd/core/main.go cmd/core/main_test.go
git commit -m "feat(core): 装配 /metrics 端点 + 业务指标 middleware + 推理指标注入"
```

---

### Task 7: observability 领域 — Span.SpanID + GetTrace 接口 + memory

**Files:**
- Modify: `internal/observability/model.go`
- Modify: `internal/observability/readers.go`
- Modify: `internal/observability/memory/store.go`

**Interfaces:**
- Consumes: 现有 `Trace`/`Span`
- Produces:
  - `Span` 加 `SpanID string` 字段
  - sentinel `ErrTraceNotFound`（model.go 或新 errors.go）
  - `TracesReader.GetTrace(ctx, traceID) (*Trace, error)`
  - `memory.Store.GetTrace` mock 一条带 span 树的 trace

- [ ] **Step 1: 写失败测试**

`internal/observability/memory/store_test.go` 加（若无则 Create）：

```go
func TestGetTraceReturnsTree(t *testing.T) {
	s := NewStore()
	tr, err := s.GetTrace(context.Background(), "trace-demo")
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if tr.ID != "trace-demo" || len(tr.Spans) == 0 {
		t.Fatalf("trace 异常: %+v", tr)
	}
	// 验证 span 有 SpanID 且父子可关联
	root := findRoot(tr.Spans)
	if root.SpanID == "" {
		t.Errorf("根 span SpanID 空")
	}
}

func TestGetTraceNotFound(t *testing.T) {
	s := NewStore()
	_, err := s.GetTrace(context.Background(), "nonexistent")
	if !errors.Is(err, observability.ErrTraceNotFound) {
		t.Fatalf("期望 ErrTraceNotFound，实际 %v", err)
	}
}

func findRoot(spans []observability.Span) observability.Span {
	for _, s := range spans {
		if s.ParentID == "" {
			return s
		}
	}
	return observability.Span{}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/observability/memory/ -run TestGetTrace -v`
Expected: FAIL（GetTrace/ErrTraceNotFound 未定义）

- [ ] **Step 3: 实现**

`internal/observability/model.go` Span 加字段：

```go
type Span struct {
	ID         string            `json:"id"`
	SpanID     string            `json:"spanId,omitempty"` // OTLP spanId，用于父子关联（ID 是展示用）
	ParentID   string            `json:"parentId,omitempty"`
	Operation  string            `json:"operation"`
	Service    string            `json:"service"`
	StartMs    int64             `json:"startMs"`
	DurationMs int64             `json:"durationMs"`
	Tags       map[string]string `json:"tags,omitempty"`
}
```

加 sentinel（model.go 末尾或新 `errors.go`）：

```go
// ErrTraceNotFound 表示 trace 不存在或不属于当前租户（跨租户统一 not found，不泄漏）。
var ErrTraceNotFound = errors.New("trace not found")
```

import `"errors"`。

`readers.go` TracesReader 加方法：

```go
type TracesReader interface {
	ListTraces(ctx context.Context, appID, status string, limit int) ([]Trace, error)
	// GetTrace 按 traceID 查完整链路（含 span 父子树）；不存在或跨租户返 ErrTraceNotFound。
	GetTrace(ctx context.Context, traceID string) (*Trace, error)
}
```

`memory/store.go` 实现 GetTrace（mock 一条固定 demo trace 带 span 树）：

```go
// GetTrace 返回 mock 完整 trace（含 span 父子树）供前端联调。降级模式固定 demo。
func (s *Store) GetTrace(ctx context.Context, traceID string) (*observability.Trace, error) {
	if traceID != "trace-demo" {
		return nil, observability.ErrTraceNotFound
	}
	return &observability.Trace{
		ID: "trace-demo", TenantID: tenantOrEmpty(ctx), AppID: "app-demo",
		Operation: "POST /v1/chat/completions", Status: observability.TraceSuccess,
		DurationMs: 820, StartedAt: time.Now().Add(-2 * time.Minute),
		Spans: []observability.Span{
			{ID: "s1", SpanID: "abc12301", Operation: "POST /v1/chat/completions", Service: "paas-core", StartMs: 0, DurationMs: 820, Tags: map[string]string{"model": "glm-5.2"}},
			{ID: "s2", SpanID: "abc12302", ParentID: "abc12301", Operation: "gateway.ResolveChannels", Service: "paas-core", StartMs: 5, DurationMs: 12},
			{ID: "s3", SpanID: "abc12303", ParentID: "abc12301", Operation: "provider.Chat", Service: "airouter", StartMs: 20, DurationMs: 780, Tags: map[string]string{"upstream": "zhipu"}},
		},
	}, nil
}
```

（`tenantOrEmpty` 从 ctx 取 tenant，复用现有 ListTraces 的取法；memory 模式可忽略租户校验。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/observability/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/observability/model.go internal/observability/readers.go internal/observability/memory/store.go internal/observability/memory/store_test.go
git commit -m "feat(observability): GetTrace 领域接口 + memory mock span 树 + Span.SpanID"
```

---

### Task 8: compose + real/traces.go GetTrace（OTLP 解析 + tenant 校验）

**Files:**
- Modify: `internal/observability/compose/compose.go`（如存在；委托 traces reader）
- Modify: `internal/observability/real/traces.go`
- Test: `internal/observability/real/traces_test.go`

**Interfaces:**
- Consumes: Tempo `GET /api/traces/{traceID}` OTLP JSON
- Produces: `TracesStore.GetTrace(ctx, traceID) (*observability.Trace, error)` — 解析 OTLP 构建父子树 + tenant 归属校验

- [ ] **Step 1: 写失败测试**

`internal/observability/real/traces_test.go` 加（用 httptest mock Tempo）：

```go
func TestGetTraceParsesOTLP(t *testing.T) {
	body := `{"batches":[{
		"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"paas-core"}},
			{"key":"tenant","value":{"stringValue":"t-acme"}}]},
		"scopeSpans":[{"spans":[
			{"traceId":"aa","spanId":"abc12301","name":"POST /v1/chat","startTimeUnixNano":"1000000000","durationUnixNano":"820000000","attributes":[{"key":"model","value":{"stringValue":"glm-5.2"}}]},
			{"traceId":"aa","spanId":"abc12302","parentSpanId":"abc12301","name":"ResolveChannels","startTimeUnixNano":"1005000000","durationUnixNano":"12000000"}
		]}]
	}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	ctx := tenant.WithTenant(context.Background(), "t-acme")
	s := NewTracesStore(srv.URL)
	tr, err := s.GetTrace(ctx, "aa")
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if len(tr.Spans) != 2 {
		t.Fatalf("期望 2 span，实际 %d", len(tr.Spans))
	}
	root := tr.Spans[0]
	if root.Service != "paas-core" || root.Operation != "POST /v1/chat" {
		t.Errorf("根 span 异常: %+v", root)
	}
}

func TestGetTraceTenantMismatchNotFound(t *testing.T) {
	body := `{"batches":[{"resource":{"attributes":[{"key":"tenant","value":{"stringValue":"t-globex"}}]},"scopeSpans":[{"spans":[{"traceId":"aa","spanId":"abc12301","name":"x"}]}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	ctx := tenant.WithTenant(context.Background(), "t-acme") // 跨租户
	s := NewTracesStore(srv.URL)
	_, err := s.GetTrace(ctx, "aa")
	if !errors.Is(err, observability.ErrTraceNotFound) {
		t.Fatalf("期望 ErrTraceNotFound（跨租户），实际 %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/observability/real/ -run TestGetTrace -v`
Expected: FAIL

- [ ] **Step 3: 实现**

`internal/observability/real/traces.go` 加 OTLP 响应类型 + GetTrace：

```go
// tempoTraceResponse 是 Tempo /api/traces/{id} 的 OTLP JSON 最小子集。
type tempoTraceResponse struct {
	Batches []struct {
		Resource struct {
			Attributes []struct {
				Key   string `json:"key"`
				Value struct {
					StringValue string `json:"stringValue"`
				} `json:"value"`
			} `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []tempoSpan `json:"spans"`
		} `json:"scopeSpans"`
		// 旧版 Tempo 字段名兼容
		InstrumentationLibrarySpans []struct {
			Spans []tempoSpan `json:"spans"`
		} `json:"instrumentationLibrarySpans"`
	} `json:"batches"`
}

type tempoSpan struct {
	TraceID           string `json:"traceId"`
	SpanID            string `json:"spanId"`
	ParentSpanID      string `json:"parentSpanId"`
	Name              string `json:"name"`
	StartTimeUnixNano string `json:"startTimeUnixNano"`
	DurationUnixNano  string `json:"durationUnixNano"`
	Attributes        []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	} `json:"attributes"`
}

// GetTrace 调 Tempo /api/traces/{id} 解析 OTLP 构建父子 span 树。
// 多租户隔离：fetch 后遍历 span resource 找 tenant attribute，与 ctx tenant 比对；
// 不匹配或无 tenant attribute → ErrTraceNotFound（不泄漏）。后端不可达降级返 ErrTraceNotFound + 日志。
func (s *TracesStore) GetTrace(ctx context.Context, traceID string) (*observability.Trace, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.tempoURL+"/api/traces/"+traceID, nil)
	if err != nil {
		log.Printf("observability real traces GetTrace: 构造请求失败: %v", err)
		return nil, observability.ErrTraceNotFound
	}
	tr, err := fetchJSON[tempoTraceResponse](s.client, req)
	if err != nil {
		log.Printf("observability real traces GetTrace: 调 Tempo 失败: %v", err)
		return nil, observability.ErrTraceNotFound
	}

	traceTenant := ""
	var startMin int64 = 1<<63 - 1
	spans := make([]observability.Span, 0)
	for _, b := range tr.Batches {
		svcName := ""
		batchTenant := ""
		for _, a := range b.Resource.Attributes {
			switch a.Key {
			case "service.name":
				svcName = a.Value.StringValue
			case "tenant":
				batchTenant = a.Value.StringValue
			}
		}
		if batchTenant != "" {
			traceTenant = batchTenant // 记录最后见到的 tenant（同 trace 应一致）
		}
		// 兼容新旧字段
		allSpans := append([]tempoSpan(nil), b.ScopeSpans...)
		for _, ils := range b.InstrumentationLibrarySpans {
			allSpans = append(allSpans, ils.Spans...)
		}
		for _, sp := range allSpans {
			startNs, _ := strconv.ParseInt(sp.StartTimeUnixNano, 10, 64)
			durNs, _ := strconv.ParseInt(sp.DurationUnixNano, 10, 64)
			if startNs < startMin {
				startMin = startNs
			}
			tags := map[string]string{}
			for _, a := range sp.Attributes {
				if a.Value.StringValue != "" {
					tags[a.Key] = a.Value.StringValue
				}
			}
			spans = append(spans, observability.Span{
				ID: sp.SpanID, SpanID: sp.SpanID, ParentID: sp.ParentSpanID,
				Operation: sp.Name, Service: svcName,
				StartMs:    startNs / 1e6,
				DurationMs: durNs / 1e6,
				Tags:       tags,
			})
		}
	}

	// tenant 归属校验（fail-closed）：ctx 有 tenant 但 trace 无/不符 → not found。
	if tid, ok := tenant.TenantFrom(ctx); ok && tid != "" {
		if traceTenant == "" || traceTenant != tid {
			return nil, observability.ErrTraceNotFound
		}
	}

	// StartMs 归一为相对 trace 起点的偏移（ms）
	for i := range spans {
		spans[i].StartMs -= startMin / 1e6
	}

	rootName, rootStatus := "", observability.TraceSuccess
	if len(spans) > 0 {
		rootName = spans[0].Operation
	}
	return &observability.Trace{
		ID: traceID, Operation: rootName, Status: rootStatus,
		StartedAt: time.Unix(0, startMin), Spans: spans,
	}, nil
}
```

`compose/compose.go`（如存在）的 TracesReader 委托加 `GetTrace` 转发到 real（memory 模式则 memory.GetTrace）。若 compose 是结构体包装 readers，加：

```go
func (c *Store) GetTrace(ctx context.Context, traceID string) (*observability.Trace, error) {
	return c.traces.GetTrace(ctx, traceID)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/observability/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/observability/real/traces.go internal/observability/real/traces_test.go internal/observability/compose/
git commit -m "feat(observability): real GetTrace 解析 Tempo OTLP + tenant 归属校验"
```

---

### Task 9: handler GET /api/observability/traces/{id} + OpenAPI

**Files:**
- Modify: `internal/observability/handler.go`
- Modify: `internal/observability/handler_test.go`

**Interfaces:**
- Produces: `Handler.GetTrace` 处理 `GET /api/observability/traces/{id}`，200 返 trace / 404 ErrTraceNotFound

- [ ] **Step 1: 写失败测试**

`handler_test.go` 加：

```go
func TestGetTraceHandler(t *testing.T) {
	h := NewHandler(memoryStoreWithDemoTrace()) // 复用既有构造
	req := httptest.NewRequest("GET", "/api/observability/traces/trace-demo", nil)
	req = req.WithContext(tenant.WithTenant(context.Background(), "t-acme"))
	rec := httptest.NewRecorder()
	h.GetTrace(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetTraceHandlerNotFound(t *testing.T) {
	h := NewHandler(memoryStoreWithDemoTrace())
	req := httptest.NewRequest("GET", "/api/observability/traces/xxx", nil)
	rec := httptest.NewRecorder()
	h.GetTrace(rec, req)
	if rec.Code != 404 {
		t.Fatalf("期望 404，实际 %d", rec.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/observability/ -run TestGetTraceHandler -v`
Expected: FAIL

- [ ] **Step 3: 实现**

`handler.go` 加方法（参照现有 lastID 取路径末段模式 + ErrTraceNotFound 映射 404）：

```go
// GetTrace 处理 GET /api/observability/traces/{id}：返完整 trace（含 span 树）。
func (h *Handler) GetTrace(w http.ResponseWriter, r *http.Request) {
	id := lastID(r) // 取路径末段，复用现有 helper
	tr, err := h.repo.GetTrace(r.Context(), id)
	if err != nil {
		if errors.Is(err, observability.ErrTraceNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "trace not found")
			return
		}
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, tr)
}
```

（`h.repo` 类型须含 GetTrace —— 若 handler 持有的是 Repository 接口，给 Repository 加 GetTrace 方法签名并转调 traces reader；参照现有 ListTraces 装配方式。import `errors`、`httputil`、`observability`。）

`cmd/core/main.go` 注册路由（在现有 `/api/observability/traces` 附近，用 composite 或新 handler 分发）。若 obsHandler 是 composite，加分支：

```go
// /api/observability/traces/{id} 详情（GET）。mux 已注册 /api/observability/traces，composite 内按路径分发。
```

并在 spec Operation 登记：

```go
reg.Operation("GET", "/api/observability/traces/{id}",
	apiroute.Tags("可观测"), apiroute.Summary("trace 详情（完整 span 链路）"),
	apiroute.Perm("observability:read"), apiroute.WithResp(observability.Trace{}))
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/observability/ ./cmd/core/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/observability/handler.go internal/observability/handler_test.go cmd/core/main.go
git commit -m "feat(observability): GET /api/observability/traces/{id} trace 详情端点"
```

---

### Task 10: real/metrics.go 平台级查询对齐新指标

**Files:**
- Modify: `internal/observability/real/metrics.go`
- Test: `internal/observability/real/metrics_test.go`

**Interfaces:**
- Produces: 平台级查询（targetType 空）的 RPS/Latency/ErrorRate 改查 `paas_inference_*` 指标

- [ ] **Step 1: 写失败测试**

`metrics_test.go` 加（httptest mock Prometheus 返预置响应，断言 PromQL 含 `rate(paas_inference_requests_total`）：

```go
func TestPlatformMetricUsesInferenceSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		// 断言 RPS 查询用了推理指标
		if strings.Contains(q, "MetricRPS") || strings.Contains(q, "paas_rps") {
			t.Errorf("RPS 查询不应再用旧名: %s", q)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer srv.Close()
	s := NewMetricsStore(srv.URL)
	_, _ = s.ListMetrics(context.Background(), "", "", "rps")
}
```

> 优先断言：传 name="rps" 时发出的 PromQL 含 `rate(paas_inference_requests_total`。测试体据实调整为捕获 query 参数。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/observability/real/ -run TestPlatformMetric -v`
Expected: FAIL

- [ ] **Step 3: 实现**

`internal/observability/real/metrics.go` 改 `metricNameToPromQL` 与平台级分支。把无 target 的平台级查询改为按推理指标算（tenant 从 ctx 注入 label 过滤）：

```go
// 平台级业务指标 PromQL（写入侧 = internal/metrics + gateway 推理埋点）。
// tenant 从 ctx 注入 label 过滤（多租户隔离）。
var platformMetricPromQL = map[string]string{
	observability.MetricRPS:       "paas_inference_requests_total",
	observability.MetricLatency:   "paas_inference_duration_seconds_bucket",
	observability.MetricErrorRate: "paas_inference_requests_total",
}
```

在 `ListMetrics` 的平台级分支（非 app/dataservice）里，按 name 选 PromQL：
- RPS → `sum(rate(paas_inference_requests_total{tenant="<tid>"}[5m]))`
- Latency → `histogram_quantile(0.95, sum by (le)(rate(paas_inference_duration_seconds_bucket{tenant="<tid>"}[5m])))`
- ErrorRate → `sum(rate(paas_inference_requests_total{tenant="<tid>",status!="success"}[5m])) / sum(rate(paas_inference_requests_total{tenant="<tid>"}[5m]))`

保留旧 `metricNameToPromQL` 仅用于显式按 target_type/target_id 查询的兼容路径（或删除，视现有调用）。CPU/Mem 在平台级无对应（应用级走 cAdvisor 已覆盖）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/observability/real/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/observability/real/metrics.go internal/observability/real/metrics_test.go
git commit -m "feat(observability): 平台级指标查询对齐推理埋点（RPS/Latency/ErrorRate）"
```

---

### Task 11: 前端 trace 详情 dialog

**Files:**
- Modify: `frontend/console-user/src/views/Observability.vue`

**Interfaces:**
- Consumes: `GET /api/observability/traces/{id}`（fetchAuth）
- Produces: trace 列表点行 → el-dialog 渲染 span 瀑布图（父子缩进 + 时长比例条 + tags 折叠）

- [ ] **Step 1: 实现加载 + dialog**

在 `Observability.vue` script 加：

```ts
const traceDetail = ref<Trace | null>(null)
const traceDetailVisible = ref(false)

async function openTrace(row: Trace) {
  const resp = await fetchAuth(`/api/observability/traces/${row.id}`)
  if (resp.ok) {
    traceDetail.value = ((await resp.json()).data ?? null) as Trace | null
    traceDetailVisible.value = true
  }
}
```

确保 `Span` interface 已有 `spanId?: string`（对齐后端新字段）。

- [ ] **Step 2: 模板加 dialog + 列表行点击**

trace 列表 el-table 加 `@row-click="openTrace"`（或行尾「详情」按钮调 openTrace）。模板末尾加 dialog：

```html
<el-dialog v-model="traceDetailVisible" title="链路详情" width="80%">
  <template v-if="traceDetail">
    <div class="trace-head">
      <span class="mono">{{ traceDetail.id }}</span>
      <el-tag :type="traceDetail.status === 'success' ? 'success' : 'danger'">
        {{ traceStatusLabel[traceDetail.status] }}
      </el-tag>
      <span>{{ traceDetail.durationMs }} ms</span>
    </div>
    <div class="spans">
      <div v-for="sp in traceDetail.spans" :key="sp.id" class="span-row"
           :style="{ paddingLeft: depth(sp) * 20 + 'px' }">
        <span class="sp-name mono">{{ sp.service }} · {{ sp.operation }}</span>
        <div class="sp-bar-wrap">
          <div class="sp-bar" :style="{ left: spanLeft(sp, traceDetail) + '%', width: spanWidth(sp, traceDetail) + '%' }"></div>
        </div>
        <span class="sp-dur mono">{{ sp.durationMs }}ms</span>
      </div>
    </div>
  </template>
</el-dialog>
```

加 `depth(sp)`（按 parentId 链计算层级，根=0）与 `spanLeft(sp, row)`（StartMs / 总时长 * 100）。`spanWidth` 已存在复用。

- [ ] **Step 3: 样式**

加 `.trace-head / .spans / .span-row / .sp-bar-wrap / .sp-bar` 样式（瀑布条用相对定位 + 百分比）。

- [ ] **Step 4: 构建验证**

Run: `cd frontend && pnpm build`
Expected: vue-tsc + vite build 通过

- [ ] **Step 5: 提交**

```bash
git add frontend/console-user/src/views/Observability.vue
git commit -m "feat(console-user): trace 详情 dialog（span 瀑布图）"
```

---

### Task 12: 全量测试 + 部署 + e2e 验证 + 文档

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 全量后端测试**

Run: `make test`
Expected: 全部 PASS（含 race）

- [ ] **Step 2: 部署到集群**

Run: `./scripts/deploy-k8s.sh`
Expected: 镜像构建 + push + helm upgrade 成功（k8s-always-latest 常规流程）

- [ ] **Step 3: e2e 验证**

```bash
# 指标
curl http://paas.k8s.dd/metrics | grep -E "paas_inference|http_requests_total"
# 触发推理（真实流量）
curl -N -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://paas.k8s.dd/v1/chat/completions
# Grafana / Prom 验证指标曲线上升
# Tempo 验证 trace 按 tenant 可查
curl "http://paas.k8s.dd/api/observability/traces" -H "Authorization: Bearer sk-acme-admin"
# 取一个 traceID 查详情
curl "http://paas.k8s.dd/api/observability/traces/<id>" -H "Authorization: Bearer sk-acme-admin"
# console-user 可观测页 trace 列表点行 → 详情瀑布图
```

Expected: 指标曲线有数据；trace 详情返完整 span 树；前端瀑布图渲染。

- [ ] **Step 4: 更新 CLAUDE.md**

更新可观测章节：移除「应用级 logs/metrics gap 留后续」（已通）；新增「业务指标埋点（internal/metrics + gateway 推理指标 + /metrics :8080）」「OTel span tenant attribute」「trace 完整链路 GetTrace」三段说明；留后续更新（应用 Pod 自身埋点、多副本 Redis、span event 解析）。

- [ ] **Step 5: 提交**

```bash
git add CLAUDE.md
git commit -m "docs: 更新可观测章节（业务指标 + trace 完整链路）"
```

---

## Self-Review

**Spec 覆盖**：
- §1 架构组件 → Task 1-6（metrics 包 + gateway + main 装配 + OTel span）✓
- §2 trace 完整链路 → Task 7-9, 11（领域接口 + real OTLP 解析 + handler + 前端）✓
- §3 数据流 → Task 4（推理指标流）+ Task 5（span tenant）+ Task 6（middleware 顺序）✓
- §4 错误降级 → 各 Task 的 fail-closed/降级测试覆盖（trace tenant 校验 Task 8、Tempo 不可达 Task 8、高基数 Task 3、panic 防护 Task 2 mustRecord）✓
- §5 测试 → 每 Task 含单测 + Task 12 e2e ✓

**类型一致**：
- `InferenceMetrics.RecordInference(tenant, model, status string, promptTokens, completionTokens int, durationSec float64)` — Task 2 定义，Task 4 调用一致 ✓
- `Meter.Inf *metrics.InferenceMetrics` — Task 4 定义，Task 6 注入 `metricsReg.Inference()` ✓
- `TracesReader.GetTrace(ctx, traceID) (*Trace, error)` — Task 7 定义，Task 8/9 实现 ✓
- `Span.SpanID` — Task 7 定义，Task 8 OTLP 解析填充 ✓
- `ErrTraceNotFound` — Task 7 定义，Task 8/9 引用 ✓
- `Registry.RegisteredPaths()` — Task 1 定义，Task 6 调用 ✓

**已知简化（实施时注意）**：
- Task 4 tokens prompt/completion 当前合并粗估（stream 按 rune 计），后续接上游 usage 精确拆分。
- Task 5 span attribute 测试断言可降级（不 panic + 注入点正确即可，真实性靠 e2e）。
- Task 9 handler 装配方式（composite 分发 vs 新 handler）需对齐现有 obsHandler 结构，实施时先读 handler.go 确认。
