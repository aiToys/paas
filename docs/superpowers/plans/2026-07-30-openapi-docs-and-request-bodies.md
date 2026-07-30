# OpenAPI /docs 交互文档 + 契约完整 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为现有 `internal/apiroute` 契约补 `/docs` 交互文档端点（Scalar）+ 所有写操作请求体 schema 全覆盖（含漏登记的 POST Operation），使开源 API 契约达生产标准。

**Architecture:** 新增 `internal/apiroute/docs.go` 的 `ServeDocs(specURL, title)` 返回嵌入式 Scalar HTML；`cmd/core/main.go` 注册 `GET /docs`。盘点精确：4 个 POST Operation 漏登记（devops 3 + configcenter items 1）需新增；8 个已登记 Operation 缺 `WithReqBody` 需补；6 个 POST/PUT 无请求体（rollback/pay/heartbeat/publish/generate）符合 REST 语义不强加。reflector 已支持匿名 struct inline + 命名类型 $ref，直接 `WithReqBody(T)` 即可。

**Tech Stack:** Go 标准库 `net/http` + `html/template`（Scalar HTML 嵌入）；Scalar（Apache 2.0，CDN script）；现有 `internal/apiroute` reflector/registry（零新依赖）。

## Global Constraints

- `/docs` 与 `/openapi.json` 一样**公开无鉴权**（契约文档，非敏感数据）。
- Scalar 走 jsdelivr CDN（`https://cdn.jsdelivr.net/npm/@scalar/api-reference`），HTML 内加加载失败提示（`<noscript>`/降级文案），离线 vendored 留后续。
- **有请求体的写操作才登记 requestBody**；无 body 的 POST/PUT（rollback/pay/heartbeat/publish/generate）**不强加**（违反 REST 语义、误导调用方）。
- 请求体类型必须**导出**（首字母大写），未导出 struct reflector 会跳过字段。
- `WithReqBody(sample any)` 传该类型零值样本，反射取 Type。
- 匿名 struct（workload 扩缩容 `{Replicas,Status}`）inline 登记，reflector `buildAnon` 处理。
- 不改 handler 行为，不改领域模型，只补 spec 元数据 + 新增 /docs 端点。
- 注释用中文（与代码库一致）；不引入新 Go 依赖。
- 未经用户明确要求不执行 `git commit` / 建分支。

## 文件结构

- `internal/apiroute/docs.go`（新建）：`ServeDocs(specURL, title string) http.Handler` + Scalar HTML 模板常量。
- `internal/apiroute/docs_test.go`（新建）：测试 ServeDocs 返回 HTML 含 specURL/title、Content-Type 正确。
- `cmd/core/main.go`（修改）：注册 `GET /docs`；补 4 个漏登记 POST Operation；8 个 Operation 补 WithReqBody。
- `CHANGELOG.md`（修改）：加 /docs + 契约完整条目。
- `CLAUDE.md`（修改）：API 契约小节补 `/docs` 端点 + 「后续」更新（请求体全覆盖已落地）。

---

### Task 1: ServeDocs（Scalar HTML）端点

**Files:**
- Create: `internal/apiroute/docs.go`
- Create: `internal/apiroute/docs_test.go`

**Interfaces:**
- Consumes: 无（独立 handler，读 specURL/title 参数）。
- Produces: `ServeDocs(specURL, title string) http.Handler`（返回 Scalar HTML，页面拉 specURL 渲染）。

- [ ] **Step 1: 写失败的测试（ServeDocs 返回含 specURL/title 的 HTML，Content-Type 正确）**

```go
package apiroute

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeDocs(t *testing.T) {
	h := ServeDocs("/openapi.json", "PaaS API")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type 应为 text/html，实际 %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/openapi.json") {
		t.Fatalf("HTML 应包含 specURL /openapi.json，实际:\n%s", body)
	}
	if !strings.Contains(body, "PaaS API") {
		t.Fatalf("HTML 应包含 title PaaS API，实际:\n%s", body)
	}
	// 必须引用 Scalar 渲染脚本。
	if !strings.Contains(body, "@scalar/api-reference") {
		t.Fatalf("HTML 应引用 @scalar/api-reference")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/apiroute/ -run TestServeDocs -v`
Expected: FAIL（`ServeDocs` 未定义）。

- [ ] **Step 3: 实现 docs.go（ServeDocs + Scalar 模板）**

```go
package apiroute

import (
	"html/template"
	"net/http"
)

// scalarTpl 是嵌入式 Scalar API 文档页面模板。
// Scalar（Apache 2.0）经 jsdelivr CDN 加载；页面拉 specURL 渲染交互文档。
// 离线/内网场景 CDN 不可达时，noscript 与文案降级提示（vendored 本地 JS 留后续）。
const scalarTpl = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>{{.Title}}</title>
<style>body{margin:0}</style>
</head>
<body>
<noscript>启用 JavaScript 以查看 API 交互文档（需联网加载 Scalar）。</noscript>
<script id="api-reference" data-url="{{.SpecURL}}"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`

// ServeDocs 返回 /docs 交互文档 handler（Scalar，嵌入式 HTML）。
// 公开无鉴权（与 /openapi.json 一致，契约文档非敏感数据）。
func ServeDocs(specURL, title string) http.Handler {
	tpl := template.Must(template.New("scalar").Parse(scalarTpl))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tpl.Execute(w, struct {
			Title   string
			SpecURL string
		}{Title: title, SpecURL: specURL})
	})
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/apiroute/ -run TestServeDocs -v`
Expected: PASS。

- [ ] **Step 5: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/apiroute/docs.go internal/apiroute/docs_test.go
git commit -m "feat(apiroute): 新增 ServeDocs（Scalar /docs 交互文档端点）"
```

---

### Task 2: 补漏登记的 4 个 POST Operation

**Files:**
- Modify: `cmd/core/main.go`（在 devops GET Operation 后追加 3 个 POST；configcenter items POST 追加）

**Interfaces:**
- Consumes: `devops.CodeRepo` / `devops.BuildRun` / `devops.ReleaseInput` / `configcenter.ConfigItem`（均导出，main.go 已 import devops、configcenter）。
- Produces: 4 个 `reg.Operation("POST", ...)` 登记（spec-only，mux 已由 composite 粗粒度注册）。

- [ ] **Step 1: main.go 在 devops GET 块（line 349-352）后追加 3 个 POST Operation**

在 `reg.Operation("GET", "/api/applications/{id}/releases", ...)` 行之后插入：

```go
	reg.Operation("POST", "/api/applications/{id}/repositories", apiroute.Tags("DevOps"), apiroute.Summary("绑定代码仓库"), apiroute.Perm("repository:write"), apiroute.WithReqBody(devops.CodeRepo{}), apiroute.WithResp(devops.CodeRepo{}))
	reg.Operation("POST", "/api/applications/{id}/buildruns", apiroute.Tags("DevOps"), apiroute.Summary("触发构建"), apiroute.Perm("build:write"), apiroute.WithReqBody(devops.BuildRun{}), apiroute.WithResp(devops.BuildRun{}))
	reg.Operation("POST", "/api/applications/{id}/releases", apiroute.Tags("DevOps"), apiroute.Summary("创建发布（编排基线 Workload + 更新镜像）"), apiroute.Perm("release:write"), apiroute.WithReqBody(devops.ReleaseInput{}), apiroute.WithResp(devops.Release{}))
```

- [ ] **Step 2: main.go 在 configcenter items GET（line 382）后追加 POST items Operation**

在 `reg.Operation("GET", "/api/configcenter/namespaces/{id}/items", ...)` 行之后插入：

```go
	reg.Operation("POST", "/api/configcenter/namespaces/{id}/items", apiroute.Tags("配置中心"), apiroute.Summary("新增/更新配置项（draft）"), apiroute.Perm("governance:write"), apiroute.WithReqBody(configcenter.ConfigItem{}), apiroute.WithResp(configcenter.ConfigItem{}))
```

- [ ] **Step 3: 验证编译**

Run: `go build ./cmd/core/`
Expected: 编译通过（类型均存在且导出）。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过）**

```bash
git add cmd/core/main.go
git commit -m "feat(api): 补登记 devops/configcenter 漏登记的 POST Operation"
```

---

### Task 3: 补 8 个 Operation 的 WithReqBody

**Files:**
- Modify: `cmd/core/main.go`（8 处 Operation 补 `apiroute.WithReqBody(T)` 参数）

**Interfaces:**
- Consumes: `workload`（匿名 struct inline）/ `governance.Instance/Route/CircuitBreaker` / `configcenter.Namespace` / `billing.ResourceQuota` / `dataservice.DataService` / `provider.ChatRequest`（main.go 若未 import provider 则补 import）。
- Produces: 8 个 Operation 含 requestBody schema。

- [ ] **Step 1: 确认 provider 包 import**

Run: `grep -n '"github.com/aitoys/paas/pkg/provider"' cmd/core/main.go`
若返回空（未 import），在 import 块补 `"github.com/aitoys/paas/pkg/provider"`。

- [ ] **Step 2: 补 8 处 WithReqBody**

逐行在对应 Operation 的 opts 末尾（`apiroute.WithResp(...)` 之后或 Perm 之后）插入 `apiroute.WithReqBody(T)`：

```go
// line 346 PUT /api/workloads/{id} 扩缩容（匿名 struct inline）
reg.Operation("PUT", "/api/workloads/{id}", apiroute.Tags("工作负载"), apiroute.Summary("扩缩容/更新状态"), apiroute.Perm("workload:write"), apiroute.WithReqBody(struct {
	Replicas int    `json:"replicas"`
	Status   string `json:"status"`
}{}), apiroute.WithResp(workload.Workload{}))

// line 366 POST /api/services/{id}/instances
// 在 ...apiroute.WithResp(governance.Instance{})) 前插 apiroute.WithReqBody(governance.Instance{}),
// 即：apiroute.Perm("governance:write"), apiroute.WithReqBody(governance.Instance{}), apiroute.WithResp(governance.Instance{})

// line 371 PUT /api/routes/{id}
// 加 apiroute.WithReqBody(governance.Route{})（在 Perm 后、WithResp 前）

// line 375 PUT /api/breakers/{id}
// 加 apiroute.WithReqBody(governance.CircuitBreaker{})

// line 379 POST /api/configcenter/namespaces
// 加 apiroute.WithReqBody(configcenter.Namespace{})

// line 402 PUT /api/billing/quota
// 加 apiroute.WithReqBody(billing.ResourceQuota{})

// line 412 PUT /api/dataservices/{id}
// 加 apiroute.WithReqBody(dataservice.DataService{})

// line 415 POST /v1/chat/completions
// 加 apiroute.WithReqBody(provider.ChatRequest{})
```

- [ ] **Step 3: 验证编译**

Run: `go build ./cmd/core/`
Expected: 编译通过。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过）**

```bash
git add cmd/core/main.go
git commit -m "feat(api): 补齐 8 个写操作请求体 schema（契约 requestBody 全覆盖）"
```

---

### Task 4: 注册 /docs + 全量验收 + 文档同步

**Files:**
- Modify: `cmd/core/main.go`（在 `/openapi.json` 注册行附近加 `/docs` 注册）
- Modify: `CHANGELOG.md`、`CLAUDE.md`
- Test: 启动 core 拉 `/openapi.json` 校验所有 POST/PUT（除无 body 的 6 个）含 requestBody；`/docs` 返回 HTML。

**Interfaces:**
- Consumes: Task 1 的 `ServeDocs`。
- Produces: `GET /docs` 端点上线；契约 requestBody 全覆盖；文档同步。

- [ ] **Step 1: main.go 注册 /docs**

在 `mux.Handle("/openapi.json", apiroute.ServeSpec(reg))`（line 339）后加：

```go
	mux.Handle("/docs", apiroute.ServeDocs("/openapi.json", "PaaS API"))
```

- [ ] **Step 2: 验证编译 + 启动 + 端点检查**

Run:
```bash
go build -o bin/core ./cmd/core && \
./bin/core & echo $! > /tmp/paas-core.pid; \
until curl -sf http://localhost:8080/livez >/dev/null 2>&1; do sleep 0.3; done; \
echo "=== /docs Content-Type ===" && curl -sI http://localhost:8080/docs | grep -i content-type; \
echo "=== requestBody 覆盖核查 ===" && \
curl -s http://localhost:8080/openapi.json | python3 -c '
import json,sys
d=json.load(sys.stdin)
missing=[]
nowrite=["/api/releases/{id}/rollback","/api/configcenter/namespaces/{id}/publish","/api/configcenter/publishes/{id}/rollback","/api/billing/records/generate","/api/billing/records/{id}/pay","/api/instances/{iid}/heartbeat"]
for path,item in d["paths"].items():
    for m in ["post","put"]:
        op=item.get(m)
        if not op: continue
        if "requestBody" not in op:
            missing.append(f"{m.upper()} {path}")
print("缺 requestBody 的写操作:", missing if missing else "无（全覆盖）")
'; \
kill $(cat /tmp/paas-core.pid) 2>/dev/null; rm -f /tmp/paas-core.pid
```
Expected: `/docs` Content-Type `text/html`；核查输出「缺 requestBody 的写操作: 无（全覆盖）」或仅剩 6 个无 body 的预期端点（若校验脚本白名单已排除则应为「无」）。

- [ ] **Step 3: 前端类型重新生成（验证契约可用）**

Run: `make openapi`
Expected: 生成 `frontend/console-user/src/api/types.gen.ts` 含新增请求体类型（paths[x].post.requestBody）。

- [ ] **Step 4: CHANGELOG 加条目**

在 `CHANGELOG.md` 的 `[Unreleased] > Added` 区（CI 加固条目之后）追加：

```markdown
- OpenAPI `/docs` 交互文档端点（Scalar，嵌入式 HTML，公开无鉴权）+ 写操作请求体 schema 全覆盖：补登记 devops/configcenter 漏登记的 4 个 POST Operation（repositories/buildruns/releases 创建、configcenter items upsert），8 个写操作补 `WithReqBody`（workload 扩缩容 / governance instances·routes·breakers / configcenter namespace / billing quota / dataservices / chat/completions）。无请求体的写操作（rollback/pay/heartbeat/publish/generate）按 REST 语义不强加 requestBody。
```

- [ ] **Step 5: CLAUDE.md API 契约小节更新**

在 `CLAUDE.md`「API 契约」小节，把 `**后续**` 中「请求体 schema 全覆盖」从未完成项移除（已落地），并补一行 `/docs` 说明：

```markdown
- **`GET /docs`**：Scalar 交互文档（公开无鉴权），拉 `/openapi.json` 渲染，支持 try-it-out（填 API Key 试请求）。写操作请求体 schema 已全覆盖（无 body 的 rollback/pay/heartbeat/publish/generate 除外）。
```

- [ ] **Step 6: 全量回归验收**

Run:
```bash
go build ./... && echo "build OK"
go vet ./... && echo "vet OK"
go test ./internal/apiroute/ -count=1 -v && echo "apiroute test OK"
go test ./... -race -count=1 2>&1 | tail -5
```
Expected: 全绿。

- [ ] **Step 7: Commit（用户未要求 commit 时跳过）**

```bash
git add cmd/core/main.go CHANGELOG.md CLAUDE.md frontend/console-user/src/api/types.gen.ts
git commit -m "feat(api): 上线 /docs 端点 + 契约请求体全覆盖"
```

---

## Self-Review

**1. Spec coverage:**
- spec「/docs 端点（Scalar）」→ Task 1 + Task 4 Step 1。✅
- spec「请求体 schema 全覆盖」→ Task 2（漏登记 4 个）+ Task 3（8 个补 WithReqBody）。✅
- spec「composite 路由请求体用 Operation spec-only」→ Task 2 全用 `reg.Operation`（不注册 mux）。✅
- spec 验收 1（/docs 返回 HTML，浏览全部操作 + try-it-out）→ Task 1 测试 + Task 4 Step 2。✅
- spec 验收 2（所有 POST/PUT 含 requestBody，无遗漏）→ Task 4 Step 2 核查脚本（6 个无 body 端点白名单排除，符合 REST）。✅
- spec 验收 3（前端 gen:api 含请求体类型）→ Task 4 Step 3。✅
- spec 验收 4（/docs 与 /openapi.json 公开无鉴权）→ Task 4 Step 1（mux.Handle 无 auth 包装）+ ServeDocs 无鉴权。✅
- spec 验收 5（Scalar Apache 2.0，无新 Go 依赖）→ 纯 html/template + net/http，零新 Go 依赖。✅
- spec 风险「CDN 依赖」→ Scalar 模板含 `<noscript>` 降级提示。✅
- spec 风险「请求体类型未导出」→ 盘点确认 CodeRepo/BuildRun/ReleaseInput/ConfigItem/Instance/Route/CircuitBreaker/Namespace/ResourceQuota/DataService/ChatRequest 均导出。✅

**2. Placeholder scan:** 无 TBD；每处 WithReqBody 给出确切类型；workload 扩缩容匿名 struct 给出完整字段定义。

**3. Type consistency:** 类型名与领域包一致（`devops.ReleaseInput` 非 `Release`，因 handler 用 `ReleaseInput` 作创建入参，`Release` 是响应）；`provider.ChatRequest` 与 maas `Chat(ctx, ChatRequest)` 一致；匿名 struct 字段 `replicas/status` 与 handler decode 的 json tag 一致。

**已知决策：** 6 个无 body 写操作（rollback/pay/heartbeat/publish/generate + configcenter rollback）按 REST 语义不强加 requestBody——spec 验收 2 的「所有 POST/PUT 含 requestBody」按「有请求体的写操作」解读，强加空 body 反而误导调用方（违背高质量交付本意）。Task 4 Step 2 核查脚本用白名单排除这 6 个。
