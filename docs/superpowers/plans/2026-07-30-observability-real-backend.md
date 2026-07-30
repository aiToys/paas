# 可观测接真实后端（Prometheus/Loki/Tempo）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 observability 增加真实后端适配（Prometheus/Loki/Tempo HTTP API），按 `PAAS_PROM_URL`/`PAAS_LOKI_URL`/`PAAS_TEMPO_URL` 在「惰性 mock」与「真实后端」间切换（三支柱独立、可混用），handler/领域模型零改动。

**Architecture:** 引入细粒度 reader 接口（`MetricsReader`/`LogsReader`/`TracesReader`），`internal/observability/real/` 三 store 用纯 `net/http` + JSON 调真实后端实现对应 reader；`internal/observability/compose` 聚合完整 `Repository`（alert rules 始终委托 memory，metrics/logs/traces 委托 real 或 memory reader，`ListAlerts` 基于 metrics reader 取当前值即时评估）。`cmd/core` 的 `buildObservabilityStore(env)` 按三个 URL 各自选 real/memory，注入 handler。未配 URL 行为与现状完全一致。

**Tech Stack:** 纯 `net/http` + `encoding/json`（零新依赖，与 maas `OpenAICompatibleProvider` 同款）；Prometheus `/api/v1/query_range`、Loki `/loki/api/v1/query_range`、Tempo `/api/search`；`httptest` mock 后端测试。

## Global Constraints

- **未配 URL（空）→ 该支柱保持 memory 惰性 mock**，dev/echo 路径零依赖、行为与现状一致。
- **后端不可达/出错 → 返回空切片 + `log.Printf` 错误，不 panic、不 5xx**（降级，接口仍 200）。
- **三支柱独立开关可混用**（metrics real + logs mock 等），handler 不感知组合。
- **alert rules 始终 memory**（规则配置非时序数据，不进 Prometheus）。
- `ListAlerts` 即时评估：遍历 memory rules，对每规则调 metrics reader 取匹配 series 当前值（real 模式来自 Prometheus、memory 模式来自 mock seed）评估 `Breached`。
- **metric/label 命名约定**（real 查询用，写入端归埋点切片）：Prometheus metric `paas_<domain>`（cpu→`paas_cpu_usage`、mem→`paas_mem_usage`、rps→`paas_rps`、latency→`paas_latency_p95_ms`、errorRate→`paas_error_rate`）；labels `tenant_id`/`target_type`/`target_id`；Loki labels `app`/`level`；Tempo tag `app`。
- 纯 `net/http`，无新 Go 依赖；Prometheus/Loki/Tempo 均 Apache 2.0。
- 不改 handler、不改领域模型、不改 memory.Store 行为（只新增 reader 接口 + real/compose 包 + 接线）。
- 注释用中文；未经用户明确要求不 `git commit` / 建分支。
- 时间窗口固定最近 1h，点数上限 60（与 mock `MaxPoints` 对齐），`step` 参数控制。

## 文件结构

- `internal/observability/readers.go`（新建）：3 reader 接口 + RuleStore 接口（单方法细粒度，memory/real 均满足）。
- `internal/observability/real/metrics.go`（新建）：`MetricsStore`（Prometheus query_range → `[]MetricSeries`）。
- `internal/observability/real/logs.go`（新建）：`LogsStore`（Loki query_range → `[]LogEntry`）。
- `internal/observability/real/traces.go`（新建）：`TracesStore`（Tempo search → `[]Trace`）。
- `internal/observability/compose/compose.go`（新建）：`Repo` 聚合 `Repository`（rules→memory，metrics/logs/traces→reader，ListAlerts 评估）。
- `internal/observability/real/*_test.go`（新建）：httptest mock 后端。
- `internal/observability/compose/compose_test.go`（新建）：聚合行为 + ListAlerts 评估测试。
- `cmd/core/main.go`（修改）：`buildObservabilityStore(env)` 收口 + 注入。
- `CHANGELOG.md`/`CLAUDE.md`（修改）：同步。

---

### Task 1: reader 接口 + real.MetricsStore（Prometheus）

**Files:**
- Create: `internal/observability/readers.go`
- Create: `internal/observability/real/metrics.go`
- Create: `internal/observability/real/metrics_test.go`

**Interfaces:**
- Consumes: `observability.MetricSeries`/`MetricPoint`（领域模型，已存在）。
- Produces: `MetricsReader`/`LogsReader`/`TracesReader`/`RuleStore` 接口；`real.NewMetricsStore(url string) *MetricsStore`（实现 `MetricsReader`）。

- [ ] **Step 1: 写 readers.go（4 细粒度接口）**

```go
package observability

import "context"

// MetricsReader 是指标读取能力（memory 与 real 均实现，compose 聚合用）。
type MetricsReader interface {
	ListMetrics(ctx context.Context, targetType, targetID, name string) ([]MetricSeries, error)
}

// LogsReader 是日志读取能力。
type LogsReader interface {
	ListLogs(ctx context.Context, appID, level, q string, limit int) ([]LogEntry, error)
}

// TracesReader 是链路读取能力。
type TracesReader interface {
	ListTraces(ctx context.Context, appID, status string, limit int) ([]Trace, error)
}

// RuleStore 是告警规则配置存取（始终 memory，compose 委托）。
type RuleStore interface {
	ListAlertRules(ctx context.Context) ([]AlertRule, error)
	CreateAlertRule(ctx context.Context, rule AlertRule) (AlertRule, error)
	DeleteAlertRule(ctx context.Context, id string) error
}
```

- [ ] **Step 2: 写 real/metrics.go（Prometheus query_range 适配）**

```go
// Package real 提供 observability reader 的真实后端实现（Prometheus/Loki/Tempo HTTP API）。
// 纯 net/http + JSON；后端不可达返空切片 + 日志，不 panic。
package real

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aitoys/paas/internal/observability"
)

// metricNameToPromQL 把领域 metric 名映射为 Prometheus metric 约定名（写入端归埋点切片）。
var metricNameToPromQL = map[string]string{
	observability.MetricCPU:       "paas_cpu_usage",
	observability.MetricMem:       "paas_mem_usage",
	observability.MetricRPS:       "paas_rps",
	observability.MetricLatency:   "paas_latency_p95_ms",
	observability.MetricErrorRate: "paas_error_rate",
}

// MetricsStore 调 Prometheus HTTP API 实现 MetricsReader。
type MetricsStore struct {
	promURL string
	client  *http.Client
}

// NewMetricsStore 创建 Prometheus 适配。promURL 为 Prometheus 根地址（如 http://prom:9090）。
func NewMetricsStore(promURL string) *MetricsStore {
	return &MetricsStore{promURL: promURL, client: &http.Client{Timeout: 10 * time.Second}}
}

// promResponse 是 Prometheus /api/v1/query_range 响应的最小子集。
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"` // [ts, "value"]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// ListMetrics 调 Prometheus 查最近 1h 时序，按领域维度映射为 []MetricSeries。
func (s *MetricsStore) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	out := make([]observability.MetricSeries, 0)
	// 无 name 指定时查全部约定 metric；否则查单个。
	names := []string{name}
	if name == "" {
		for n := range metricNameToPromQL {
			names = append(names, n)
		}
	}
	now := time.Now()
	end := now.Unix()
	start := now.Add(-time.Hour).Unix()
	step := int64(time.Hour.Seconds() / float64(observability.MaxPoints)) // ~60s，对齐 MaxPoints
	for _, mn := range names {
		promMetric, ok := metricNameToPromQL[mn]
		if !ok {
			// 非约定 metric 名：原样用作 PromQL metric（允许扩展）。
			promMetric = mn
		}
		q := promMetric
		if targetType != "" {
			q += fmt.Sprintf("{target_type=%q", targetType)
			if targetID != "" {
				q += fmt.Sprintf(",target_id=%q", targetID)
			}
			q += "}"
		}
		v := url.Values{}
		v.Set("query", q)
		v.Set("start", strconv.FormatInt(start, 10))
		v.Set("end", strconv.FormatInt(end, 10))
		v.Set("step", strconv.FormatInt(step, 10))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.promURL+"/api/v1/query_range?"+v.Encode(), nil)
		if err != nil {
			log.Printf("observability real metrics: 构造请求失败: %v", err)
			continue
		}
		resp, err := s.client.Do(req)
		if err != nil {
			log.Printf("observability real metrics: 调 Prometheus 失败: %v", err)
			continue
		}
		var pr promResponse
		dec := json.NewDecoder(resp.Body)
		_ = dec.Decode(&pr)
		resp.Body.Close()
		if pr.Status != "success" {
			log.Printf("observability real metrics: Prometheus 返回非 success: %s", pr.Error)
			continue
		}
		for _, r := range pr.Data.Result {
			series := toMetricSeries(r.Metric, r.Values)
			out = append(out, series)
		}
	}
	return out, nil
}

// toMetricSeries 把 Prometheus matrix 结果转为领域 MetricSeries。
func toMetricSeries(metric map[string]string, values [][]any) observability.MetricSeries {
	name := metric["__name__"]
	// 反查领域名（找不到保留原 PromQL 名）。
	for domain, prom := range metricNameToPromQL {
		if prom == name {
			name = domain
			break
		}
	}
	pts := make([]observability.MetricPoint, 0, len(values))
	var current float64
	for _, v := range values {
		if len(v) < 2 {
			continue
		}
		ts, _ := toFloat(v[0])
		val, _ := toFloat(v[1])
		pts = append(pts, observability.MetricPoint{TS: time.Unix(int64(ts), 0), Value: val})
		current = val
	}
	return observability.MetricSeries{
		ID:         fmt.Sprintf("%s|%s|%s", metric["target_type"], metric["target_id"], name),
		TargetType: metric["target_type"],
		TargetID:   metric["target_id"],
		Name:       name,
		Current:    current,
		Points:     pts,
	}
}

// toFloat 把 Prometheus 值（string 或 float64）转为 float64。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	case float64:
		return x, true
	}
	return 0, false
}
```

- [ ] **Step 3: 写 metrics_test.go（httptest mock Prometheus，验证解析 + 降级）**

```go
package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetricsStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("路径应为 /api/v1/query_range，实际 %s", r.URL.Path)
		}
		q := r.URL.Query().Get("query")
		if q != `paas_cpu_usage{target_type="app",target_id="app-cs"}` {
			t.Fatalf("PromQL 不符: %s", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{
					{
						"metric": map[string]any{"__name__": "paas_cpu_usage", "target_type": "app", "target_id": "app-cs"},
						"values": [][]any{{1719500000, "62"}, {1719500060, "64"}},
					},
				},
			},
		})
	}))
	defer srv.Close()
	s := NewMetricsStore(srv.URL)
	out, err := s.ListMetrics(context.Background(), "app", "app-cs", "cpu")
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if len(out) != 1 || out[0].Name != "cpu" || out[0].Current != 64 {
		t.Fatalf("解析错误: %+v", out)
	}
	if len(out[0].Points) != 2 {
		t.Fatalf("应解析 2 个点，实际 %d", len(out[0].Points))
	}
}

func TestMetricsStoreBackendDown(t *testing.T) {
	// 指向不存在的端口 → 降级返空切片，不报错。
	s := NewMetricsStore("http://127.0.0.1:1")
	out, err := s.ListMetrics(context.Background(), "app", "", "cpu")
	if err != nil {
		t.Fatalf("后端不可达应降级返空非报错: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("后端不可达应返空切片，实际 %d 条", len(out))
	}
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/observability/real/ -run TestMetricsStore -v`
Expected: PASS。

- [ ] **Step 5: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/observability/readers.go internal/observability/real/
git commit -m "feat(observability): real MetricsStore 接 Prometheus + reader 接口"
```

---

### Task 2: real.LogsStore（Loki）

**Files:**
- Create: `internal/observability/real/logs.go`
- Create: `internal/observability/real/logs_test.go`

**Interfaces:**
- Produces: `real.NewLogsStore(url string) *LogsStore`（实现 `LogsReader`）。

- [ ] **Step 1: 写 real/logs.go（Loki query_range 适配）**

```go
package real

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aitoys/paas/internal/observability"
)

// LogsStore 调 Loki HTTP API 实现 LogsReader。
type LogsStore struct {
	lokiURL string
	client  *http.Client
}

// NewLogsStore 创建 Loki 适配。lokiURL 为 Loki 根地址（如 http://loki:3100）。
func NewLogsStore(lokiURL string) *LogsStore {
	return &LogsStore{lokiURL: lokiURL, client: &http.Client{Timeout: 10 * time.Second}}
}

// lokiResponse 是 Loki /loki/api/v1/query_range 响应的最小子集。
type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"` // [tsNs, "line"]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// ListLogs 调 Loki 查最近 1h 日志，按 appID/level/q 过滤，时间倒序。
func (s *LogsStore) ListLogs(ctx context.Context, appID, level, q string, limit int) ([]observability.LogEntry, error) {
	if limit <= 0 || limit > observability.MaxLogs {
		limit = 100
	}
	// LogQL：{app="...",level="..."} |= "关键字"
	selector := "{"
	if appID != "" {
		selector += fmt.Sprintf("app=%q,", appID)
	}
	if level != "" {
		selector += fmt.Sprintf("level=%q,", level)
	}
	selector += "}"
	if q != "" {
		selector += fmt.Sprintf(" |= %q", q)
	}
	now := time.Now()
	v := url.Values{}
	v.Set("query", selector)
	v.Set("start", strconv.FormatInt(now.Add(-time.Hour).UnixNano(), 10))
	v.Set("end", strconv.FormatInt(now.UnixNano(), 10))
	v.Set("limit", strconv.Itoa(limit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.lokiURL+"/loki/api/v1/query_range?"+v.Encode(), nil)
	if err != nil {
		log.Printf("observability real logs: 构造请求失败: %v", err)
		return []observability.LogEntry{}, nil
	}
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("observability real logs: 调 Loki 失败: %v", err)
		return []observability.LogEntry{}, nil
	}
	var lr lokiResponse
	_ = json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	if lr.Status != "success" {
		log.Printf("observability real logs: Loki 返回非 success: %s", lr.Error)
		return []observability.LogEntry{}, nil
	}
	out := make([]observability.LogEntry, 0, limit)
	for _, r := range lr.Data.Result {
		for _, val := range r.Values {
			if len(val) < 2 {
				continue
			}
			tsNs, _ := strconv.ParseInt(val[0], 10, 64)
			out = append(out, observability.LogEntry{
				ID:        fmt.Sprintf("%s/%s", r.Stream["app"], val[0]),
				AppID:     r.Stream["app"],
				Level:     r.Stream["level"],
				Message:   val[1],
				Timestamp: time.Unix(0, tsNs),
			})
		}
	}
	// Loki 已按时间倒序返回；截断 limit。
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
```

- [ ] **Step 2: 写 logs_test.go（mock Loki 成功 + 不可达降级）**

```go
package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogsStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Fatalf("路径不符: %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("query"); q != `{app="app-cs",level="error",} |= "timeout"` {
			t.Fatalf("LogQL 不符: %s", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "logs",
				"result": []map[string]any{
					{"stream": map[string]any{"app": "app-cs", "level": "error"},
						"values": [][]string{{"1719500000000000000", "upstream timeout"}}},
				},
			},
		})
	}))
	defer srv.Close()
	s := NewLogsStore(srv.URL)
	out, err := s.ListLogs(context.Background(), "app-cs", "error", "timeout", 50)
	if err != nil || len(out) != 1 {
		t.Fatalf("解析错误: %v len=%d", err, len(out))
	}
	if out[0].Message != "upstream timeout" || out[0].AppID != "app-cs" {
		t.Fatalf("字段错误: %+v", out[0])
	}
}

func TestLogsStoreBackendDown(t *testing.T) {
	s := NewLogsStore("http://127.0.0.1:1")
	out, err := s.ListLogs(context.Background(), "", "", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("后端不可达应降级返空非报错: %v len=%d", err, len(out))
	}
}
```

- [ ] **Step 3: 跑测试**

Run: `go test ./internal/observability/real/ -run TestLogsStore -v`
Expected: PASS。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/observability/real/logs.go internal/observability/real/logs_test.go
git commit -m "feat(observability): real LogsStore 接 Loki"
```

---

### Task 3: real.TracesStore（Tempo）

**Files:**
- Create: `internal/observability/real/traces.go`
- Create: `internal/observability/real/traces_test.go`

**Interfaces:**
- Produces: `real.NewTracesStore(url string) *TracesStore`（实现 `TracesReader`）。

- [ ] **Step 1: 写 real/traces.go（Tempo search 适配）**

```go
package real

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/aitoys/paas/internal/observability"
)

// TracesStore 调 Tempo HTTP API 实现 TracesReader。
type TracesStore struct {
	tempoURL string
	client   *http.Client
}

// NewTracesStore 创建 Tempo 适配。tempoURL 为 Tempo 根地址（如 http://tempo:3200）。
func NewTracesStore(tempoURL string) *TracesStore {
	return &TracesStore{tempoURL: tempoURL, client: &http.Client{Timeout: 10 * time.Second}}
}

// tempoSearchResponse 是 Tempo /api/search 响应的最小子集。
type tempoSearchResponse struct {
	Traces []struct {
		TraceID         string  `json:"traceID"`
		RootServiceName string  `json:"rootServiceName"`
		RootTraceName   string  `json:"rootTraceName"`
		DurationSeconds float64 `json:"durationSeconds"`
		StartTimeUnixNs uint64  `json:"startTimeUnixNs"`
	} `json:"traces"`
}

// ListTraces 调 Tempo search 查最近 trace，按 appID/status 过滤。
// 注：span 详情（OTLP 格式 /api/traces/{id}）解析复杂，本期 search 返回基本信息 + 空 Spans，留后续。
func (s *TracesStore) ListTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	if limit <= 0 || limit > observability.MaxTraces {
		limit = 50
	}
	v := url.Values{}
	v.Set("limit", strconv.Itoa(limit))
	if appID != "" {
		v.Set("tags", fmt.Sprintf("app=%s", appID)) // Tempo tag 过滤
	}
	now := time.Now()
	v.Set("start", strconv.FormatInt(now.Add(-time.Hour).Unix(), 10))
	v.Set("end", strconv.FormatInt(now.Unix(), 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.tempoURL+"/api/search?"+v.Encode(), nil)
	if err != nil {
		log.Printf("observability real traces: 构造请求失败: %v", err)
		return []observability.Trace{}, nil
	}
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("observability real traces: 调 Tempo 失败: %v", err)
		return []observability.Trace{}, nil
	}
	var tr tempoSearchResponse
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	resp.Body.Close()
	out := make([]observability.Trace, 0, len(tr.Traces))
	for _, t := range tr.Traces {
		// status 过滤（Tempo search 无原生 status，按 rootTraceName 约定或 span error tag 归后续；本期客户端过滤留空匹配）。
		trc := observability.Trace{
			ID:         t.TraceID,
			AppID:      appID,
			Operation:  t.RootTraceName,
			Status:     observability.TraceSuccess,
			DurationMs: int64(t.DurationSeconds * 1000),
			StartedAt:  time.Unix(0, int64(t.StartTimeUnixNs)),
		}
		if status != "" && trc.Status != status {
			continue
		}
		out = append(out, trc)
	}
	return out, nil
}
```

- [ ] **Step 2: 写 traces_test.go（mock Tempo 成功 + 不可达降级）**

```go
package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTracesStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Fatalf("路径不符: %s", r.URL.Path)
		}
		if tags := r.URL.Query().Get("tags"); tags != "app=app-cs" {
			t.Fatalf("tags 过滤不符: %s", tags)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"traces": []map[string]any{
				{"traceID": "abc123", "rootTraceName": "POST /v1/chat", "durationSeconds": 0.12, "startTimeUnixNs": uint64(1719500000000000000)},
			},
		})
	}))
	defer srv.Close()
	s := NewTracesStore(srv.URL)
	out, err := s.ListTraces(context.Background(), "app-cs", "", 20)
	if err != nil || len(out) != 1 {
		t.Fatalf("解析错误: %v len=%d", err, len(out))
	}
	if out[0].ID != "abc123" || out[0].DurationMs != 120 || out[0].Operation != "POST /v1/chat" {
		t.Fatalf("字段错误: %+v", out[0])
	}
}

func TestTracesStoreBackendDown(t *testing.T) {
	s := NewTracesStore("http://127.0.0.1:1")
	out, err := s.ListTraces(context.Background(), "", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("后端不可达应降级返空非报错: %v len=%d", err, len(out))
	}
}
```

- [ ] **Step 3: 跑测试**

Run: `go test ./internal/observability/real/ -run TestTracesStore -v`
Expected: PASS。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/observability/real/traces.go internal/observability/real/traces_test.go
git commit -m "feat(observability): real TracesStore 接 Tempo"
```

---

### Task 4: compose 聚合 Repository（含 ListAlerts 评估）

**Files:**
- Create: `internal/observability/compose/compose.go`
- Create: `internal/observability/compose/compose_test.go`

**Interfaces:**
- Consumes: `observability.Repository`（rules 委托对象，用 memory.Store）+ `MetricsReader`/`LogsReader`/`TracesReader`。
- Produces: `compose.New(rules, metrics, logs, traces) *Repo`（实现 `observability.Repository` 完整 7 方法）。

- [ ] **Step 1: 写 compose.go**

```go
// Package compose 聚合 observability 的多源 reader 为单一 Repository：
// alert rules 始终委托 memory（规则配置），metrics/logs/traces 委托任意 reader
// （real 真实后端或 memory 惰性 mock，可混用）。ListAlerts 基于 metrics reader 即时评估。
package compose

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/aitoys/paas/internal/observability"
)

// Repo 聚合多源 reader 实现 observability.Repository。
type Repo struct {
	rules   observability.RuleStore
	metrics observability.MetricsReader
	logs    observability.LogsReader
	traces  observability.TracesReader
}

// New 创建聚合 Repository。rules 始终是 memory 规则存储；metrics/logs/traces 可为 real 或 memory。
func New(rules observability.RuleStore, metrics observability.MetricsReader, logs observability.LogsReader, traces observability.TracesReader) *Repo {
	return &Repo{rules: rules, metrics: metrics, logs: logs, traces: traces}
}

func (r *Repo) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	return r.metrics.ListMetrics(ctx, targetType, targetID, name)
}

func (r *Repo) ListLogs(ctx context.Context, appID, level, q string, limit int) ([]observability.LogEntry, error) {
	return r.logs.ListLogs(ctx, appID, level, q, limit)
}

func (r *Repo) ListTraces(ctx context.Context, appID, status string, limit int) ([]observability.Trace, error) {
	return r.traces.ListTraces(ctx, appID, status, limit)
}

func (r *Repo) ListAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	return r.rules.ListAlertRules(ctx)
}

func (r *Repo) CreateAlertRule(ctx context.Context, rule observability.AlertRule) (observability.AlertRule, error) {
	return r.rules.CreateAlertRule(ctx, rule)
}

func (r *Repo) DeleteAlertRule(ctx context.Context, id string) error {
	return r.rules.DeleteAlertRule(ctx, id)
}

// ListAlerts 即时评估：遍历 rules，对每 enabled 规则调 metrics reader 取匹配 series 当前值评估。
// real 模式 metrics 来自 Prometheus，memory 模式来自 mock seed——统一在此评估。
func (r *Repo) ListAlerts(ctx context.Context) ([]observability.Alert, error) {
	rules, err := r.rules.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	series, err := r.metrics.ListMetrics(ctx, "", "", "")
	if err != nil {
		log.Printf("observability compose ListAlerts: 取 metrics 失败: %v", err)
		series = nil
	}
	alerts := make([]observability.Alert, 0)
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		for _, s := range series {
			if !rule.Matches(s) {
				continue
			}
			if rule.Breached(s.Current) {
				alerts = append(alerts, observability.Alert{
					RuleID:     rule.ID,
					RuleName:   rule.Name,
					TargetType: s.TargetType,
					TargetID:   s.TargetID,
					MetricName: s.Name,
					Value:      s.Current,
					Threshold:  rule.Threshold,
					Operator:   rule.Operator,
					Severity:   rule.Severity,
					Status:     "firing",
					FiredAt:    time.Now(),
				})
			}
		}
	}
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Severity != alerts[j].Severity {
			return alerts[i].Severity == observability.SeverityCritical
		}
		return alerts[i].RuleName < alerts[j].RuleName
	})
	return alerts, nil
}
```

- [ ] **Step 2: 写 compose_test.go（验证委托 + ListAlerts 基于 fake metrics 评估）**

```go
package compose

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/internal/observability/memory"
)

// fakeMetrics 是可控的 MetricsReader（注入 series 供 ListAlerts 评估）。
type fakeMetrics struct{ series []observability.MetricSeries }

func (f *fakeMetrics) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	return f.series, nil
}

func TestListAlertsEvaluatesAgainstMetrics(t *testing.T) {
	rules := memory.NewStore()
	rule, _ := rules.CreateAlertRule(ctxWithTenant(), observability.AlertRule{
		Name: "CPU 高", MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
		Operator: observability.OpGT, Threshold: 50, Severity: observability.SeverityWarning, Enabled: true,
	})
	_ = rule
	metrics := &fakeMetrics{series: []observability.MetricSeries{
		{TargetType: observability.TargetApp, TargetID: "app-cs", Name: observability.MetricCPU, Current: 80},
	}}
	r := New(rules, metrics, memory.NewStore(), memory.NewStore())
	alerts, err := r.ListAlerts(ctxWithTenant())
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Value != 80 {
		t.Fatalf("应基于 metrics 评估出 1 条告警，实际: %+v", alerts)
	}
}

func TestDelegatesLogsTraces(t *testing.T) {
	r := New(memory.NewStore(), memory.NewStore(), memory.NewStore(), memory.NewStore())
	logs, err := r.ListLogs(ctxWithTenant(), "", "", "", 10)
	if err != nil {
		t.Fatalf("ListLogs 委托失败: %v", err)
	}
	_ = logs
	traces, err := r.ListTraces(ctxWithTenant(), "", "", 10)
	if err != nil {
		t.Fatalf("ListTraces 委托失败: %v", err)
	}
	_ = traces
}

func ctxWithTenant() context.Context {
	return context.WithValue(context.Background(), tenantCtxKey{}, "t-acme")
}

type tenantCtxKey struct{}
```

> **注**：`ctxWithTenant` 用本地 key 是为测试隔离；实际生产 ctx 由 `tenant.WithTenant` 注入（`memory.Store` 经 `tenant.TenantFrom` 读取）。若 `tenant` 包 key 不导出导致测试无法构造 ctx，则改为引入测试 helper 或用 `memory` 包既有测试 ctx 构造方式（执行时核对 `memory/store_test.go` 的 `acmeCtx()`，若可复用则直接调用——需把测试放能访问到该 helper 的位置，或复制其实现）。

- [ ] **Step 3: 跑测试**

Run: `go test ./internal/observability/compose/ -v`
Expected: PASS（若 ctx 构造方式需调整，执行时按 memory 测试模式对齐）。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/observability/compose/
git commit -m "feat(observability): compose 聚合多源 reader + ListAlerts 基于 metrics 评估"
```

---

### Task 5: cmd/core buildObservabilityStore 收口 + 验收 + 文档

**Files:**
- Modify: `cmd/core/main.go`（line 215 `obsmemory.NewStore()` 改为 `buildObservabilityStore(env)`；新增 `buildObservabilityStore` 函数）
- Modify: `CHANGELOG.md`、`CLAUDE.md`

**Interfaces:**
- Consumes: Task 1-4 的 reader/compose。
- Produces: env 驱动的 observability store 切换；不配 URL 时行为与现状一致。

- [ ] **Step 1: main.go 改注入点**

把 `obsHandler := observability.NewHandler(obsmemory.NewStore())` 改为：

```go
	obsHandler := observability.NewHandler(buildObservabilityStore())
```

- [ ] **Step 2: 新增 buildObservabilityStore 函数（main.go 或新文件 cmd/core/observability.go）**

```go
// buildObservabilityStore 按环境变量构造 observability.Repository：
// alert rules 始终 memory；metrics/logs/traces 按 PAAS_PROM_URL/LOKI_URL/TEMPO_URL
// 非空则接真实后端，否则保持 memory 惰性 mock（三支柱独立、可混用）。未配任何 URL 时行为与现状一致。
func buildObservabilityStore() observability.Repository {
	rules := obsmemory.NewStore() // 规则配置始终 memory（含 seed）
	metrics := observability.MetricsReader(rules)
	if u := os.Getenv("PAAS_PROM_URL"); u != "" {
		metrics = obreal.NewMetricsStore(u)
	}
	logs := observability.LogsReader(rules)
	if u := os.Getenv("PAAS_LOKI_URL"); u != "" {
		logs = obreal.NewLogsStore(u)
	}
	traces := observability.TracesReader(rules)
	if u := os.Getenv("PAAS_TEMPO_URL"); u != "" {
		traces = obreal.NewTracesStore(u)
	}
	return obscompose.New(rules, metrics, logs, traces)
}
```

import 块加：
```go
	obcompose "github.com/aitoys/paas/internal/observability/compose"
	obreal "github.com/aitoys/paas/internal/observability/real"
```
（`obsmemory` 别名保留。）

- [ ] **Step 3: 编译 + 全量验证**

Run:
```bash
go build ./... && echo "build OK"
go vet ./... && echo "vet OK"
go test ./internal/observability/... -count=1 2>&1 | tail -5
```
Expected: 全绿。

- [ ] **Step 4: 启动 core 验证「未配 URL 行为与现状一致」**

Run:
```bash
./bin/core & echo $! > /tmp/paas-core.pid
until curl -sf http://localhost:8080/livez >/dev/null 2>&1; do sleep 0.3; done
echo "=== metrics（mock 惰性补点，非空）==="
curl -s -H "Authorization: Bearer sk-acme-admin" "http://localhost:8080/api/observability/metrics?targetType=app&targetId=app-cs&name=cpu" | python3 -c "import json,sys;d=json.load(sys.stdin);print('series 数:',len(d['data']),'current:',d['data'][0]['current'] if d['data'] else 'N/A')"
echo "=== alerts（基于 mock metrics 评估）==="
curl -s -H "Authorization: Bearer sk-acme-admin" "http://localhost:8080/api/observability/alerts" | python3 -c "import json,sys;d=json.load(sys.stdin);print('告警数:',len(d['data']))"
kill $(cat /tmp/paas-core.pid) 2>/dev/null; rm -f /tmp/paas-core.pid
```
Expected: metrics 返回 mock 惰性补点数据（series 数 ≥1，current 有值）；alerts 正常评估。

- [ ] **Step 5: CHANGELOG 加条目**

在 `[Unreleased] > Added` 区（OpenAPI /docs 条目后）追加：

```markdown
- 可观测接真实后端（observability real store）：新增 `internal/observability/real`（MetricsStore/LogsStore/TracesStore 纯 net/http 适配 Prometheus/Loki/Tempo）+ `internal/observability/compose`（聚合 Repository：alert rules 始终 memory，metrics/logs/traces 按 `PAAS_PROM_URL`/`PAAS_LOKI_URL`/`PAAS_TEMPO_URL` 非空接真实后端、空则惰性 mock，三支柱独立可混用；`ListAlerts` 基于 metrics reader 即时评估）。后端不可达降级返空 + 日志（不 5xx/panic）。未配 URL 行为与现状完全一致。
```

- [ ] **Step 6: CLAUDE.md 可观测小节更新**

把「切片**不接真实采集**（Prometheus/Loki/Tempo）」改为已支持（env 开关），并补一段说明。

- [ ] **Step 7: 全量回归**

Run: `go test ./... -race -count=1 2>&1 | grep -c "^FAIL"`
Expected: 0。

- [ ] **Step 8: Commit（用户未要求 commit 时跳过）**

```bash
git add cmd/core/ CHANGELOG.md CLAUDE.md
git commit -m "feat(observability): env 驱动 real/mock 切换 + compose 收口"
```

---

## Self-Review

**1. Spec coverage:**
- spec「real 包三 store 调真实后端 HTTP API」→ Task 1/2/3。✅
- spec「配置开关 PROM/LOKI/TEMPO URL（空→memory）」→ Task 5 Step 2。✅
- spec「Alert 即时评估基于真实 metrics」→ Task 4 ListAlerts。✅
- spec「AlertRule store 始终 memory」→ Task 5（rules 固定 memory）。✅
- spec「三 store 独立开关可混用」→ Task 5（三个 if 各自独立）。✅
- spec 验收 1（配 PROM_URL → 返回真实查询）→ Task 1 测试 + Task 5 Step 4。✅
- spec 验收 2（不配三 URL → 行为完全一致）→ Task 5 Step 4（mock 惰性补点）。✅
- spec 验收 3（配 LOKI/TEMPO_URL → 走真实）→ Task 2/3 测试。✅
- spec 验收 4（后端不可用 → 空数据 + 200，不 panic）→ 三 store 降级 + 测试。✅
- spec 验收 5（Alert 基于真实 metrics 触发）→ Task 4。✅
- spec 验收 6（纯 net/http，无新依赖）→ 全部用 net/http + encoding/json。✅
- spec 风险「PromQL/LogQL 维度映射」→ metricNameToPromQL 约定 + LogQL selector 构造。✅
- spec 风险「混用模式」→ 三 reader 独立，compose 不感知组合。✅

**2. Placeholder scan:** 无 TBD；每 store 给出完整 HTTP 构造 + 响应解析 + 降级路径；测试用 httptest 给出确切 mock 响应与断言。Task 4 Step 2 注释说明 ctx 构造的执行时对齐点（非占位，是执行指引）。

**3. Type consistency:** `MetricsReader/LogsReader/TracesReader/RuleStore` 方法签名与 `Repository` 对应方法逐字一致；memory.Store 满足全部四个接口（已有方法）；real.*Store 满足对应单接口；compose.Repo 满足 Repository。`ListAlerts` 评估逻辑与 memory 版一致（Matches + Breached + 同排序）。

**已知决策/限制：**
- Tempo 仅 search 返回 trace 基本信息 + 空 Spans（OTLP detail 解析 YAGNI 留后续）；status 过滤本期客户端空匹配（Tempo 无原生 status 维度）。
- metric/label 命名约定（paas_cpu_usage 等）查询端落地，写入端（应用埋点）归后续埋点切片——本期假设后端已有数据。
- Task 4 测试 ctx 构造：若 `tenant` 包 key 不导出，执行时核对 `memory/store_test.go` 的 `acmeCtx()` 复用其模式（该测试在同包可访问 unexported key；compose 测试跨包需用导出的 `tenant.WithTenant`，确认该函数存在）。
