# 应用级可观测补齐设计（业务指标埋点 + Trace 完整链路）

- 日期：2026-08-04
- 范围：Core 控制面业务指标埋点（Prometheus）+ OTel span 租户维度 + Trace 按 traceID 查完整链路
- 关联：承接可观测真实后端（Prometheus/Loki/Tempo 已接通）的「应用级业务指标 gap」与「trace span 详情留后续」两块

## 背景与现状

可观测三支柱真实后端已落地（`internal/observability/real` + helm 部署 Prom/Loki/Tempo + Grafana）。深度检测后发现查询侧代码已比早期描述更超前：

- **应用级日志已通**：`real/logs.go` 用 `paas_aitoys_app` label 查 Loki；写入侧 `controller/workload_controller.go` 已给 Pod 打 `paas.aitoys/app`+`tenant`+`workload` label（promtail 转下划线）。CLAUDE.md「应用级 logs 查询 gap」描述已过时。
- **应用级 CPU/内存已通**：`real/metrics.go` `listAppMetrics` 按 `paas_aitoys_app` label 聚合 cAdvisor `container_*` 指标。
- **数据服务指标已通**：`listDataserviceMetrics` 按 Pod 名查 cAdvisor。

真正剩余的 gap：

1. **Core 业务指标全无**：`grep` 确认 core 仅 controller-runtime 进程级 /metrics（:8081），无 `paas_rps`/`paas_latency`/`paas_error_rate`/tokens 等。real/metrics.go 平台级查询（`metricNameToPromQL`）映射到这些名但无人写入 → 查不到。
2. **OTel span 无 tenant 维度**：`tracing.go` resource 仅 `ServiceName`，span 无 tenant attribute → Tempo 按 `tags=tenant=` 查不到平台 trace。
3. **Trace 只有列表占位**：`real/traces.go` 仅调 Tempo `/api/search` 返基本信息 + 空 Spans；span 详情（`/api/traces/{id}` OTLP 解析）明确列为留后续。用户端无法按 traceID 查整条链路。

**核心约束**：业务指标是**平台/租户维度**——PaaS 真实代理的流量是推理 gateway（`/v1/chat/completions`）与平台 API（`/api/*`），这是有意义的 RPS/latency/tokens 数据源。**PaaS 不侵入应用 Pod 自身业务埋点**（应用自己的 HTTP 流量 PaaS 看不到，应用级 RPS/latency 对 app 维度无意义；应用级 CPU/内存走 cAdvisor 已覆盖 infra 维度）。

## 方案选型

- **方案 A（选定）**：HTTP middleware + 自定义 Prometheus Registry + `/metrics`（:8080），gateway 推理路径直接 Record 业务指标；OTel span 注入 tenant attribute；real 适配器对齐查询。
- 方案 B（否决）：复用 `prometheus.DefaultRegisterer` 单端点含 controller-runtime 进程指标——耦合 controller-runtime 配置，业务/进程指标混淆。
- 方案 C（否决）：otel metric SDK 统一 traces+metrics 导出 OTLP——引入新依赖与组装复杂度，YAGNI（本期只需 Prometheus pull）。

依赖：显式引 `github.com/prometheus/client_golang`（Apache 2.0，controller-runtime 已传递依赖，本期显式化）。

## 架构与组件

### 新增包 `internal/metrics/`（平台级业务指标，单一职责）

- `registry.go`：自有 `prometheus.Registry`（隔离 controller-runtime 进程指标）+ 指标定义：
  - `http_requests_total{method,route,status,tenant}` counter
  - `http_request_duration_seconds{method,route,tenant}` histogram（buckets 对齐 SLO）
  - `paas_inference_requests_total{tenant,model,status}` counter
  - `paas_inference_tokens_total{tenant,model,direction}` counter（direction=prompt/completion）
  - `paas_inference_duration_seconds{tenant,model}` histogram
- `middleware.go`：`HTTPMiddleware(reg)` 包 mux 外层，从 ctx 取 tenantID（`gateway.TenantFrom`），`r.URL.Path` 归一化为 route 模板。**模板来源**：借力 apiroute registry（route 单一真源）已登记的 path 列表，按最长前缀匹配把实际 path 映射回模板（如 `/api/applications/app-1` → `/api/applications/{id}`）；匹配不到归 `"unmatched"`，避免 path 片段当 label 撑爆 cardinality。
- `MetricsHandler(reg)`：返 `promhttp.HandlerFor(reg, ...)`，挂 core :8080 `/metrics`。
- `mustRecord` helper：固定 label 顺序封装，防 `WithLabelValues` 维度错配 panic。

### Trace 完整链路

- `internal/observability/real/traces.go` 加 `GetTrace(ctx, traceID)`：调 Tempo `GET /api/traces/{traceID}`，解析 OTLP JSON（`batches[].resource.attributes` + `batches[].scopeSpans[].spans[]`，旧版 `instrumentationLibrarySpans` 兼容）→ 构建 span 父子树 + service/duration/tags。
- `observability.Span` 补 `SpanID` 字段（用于父子关联，现可能缺）。
- `observability.TracesReader` 接口加 `GetTrace(ctx, traceID) (*Trace, error)`；memory 实现 mock 一条带 span 树的 trace 供前端联调。
- REST `GET /api/observability/traces/{id}`（`observability:read`）+ OpenAPI Operation 登记 + `WithResp(observability.Trace{})`。

### 改造点

- `cmd/core/main.go`：mux 加 `/metrics`（公开无鉴权，Prometheus scrape 约定）+ metrics middleware 包外层。
- `internal/core/gateway/openai.go`：推理路径成功/失败/超时分别 Record；`serveStream` 已累计 tokens，加 `paas_inference_tokens_total`（prompt/completion 拆分）。
- `internal/observability/real/metrics.go`：平台级查询对齐新指标名——`MetricRPS`→`rate(paas_inference_requests_total[5m])`、`MetricLatency`→`histogram_quantile(0.95, rate(paas_inference_duration_seconds_bucket[5m]))`、`MetricErrorRate`→错误 status 占比。
- `internal/observability/tracing/` + middleware：span attribute `tenant` 从 ctx 注入（resource 保持静态 ServiceName）。
- console-user `Observability.vue`：trace 列表点行 → 详情抽屉，`GET /api/observability/traces/{id}` 渲染 span 瀑布图。

## 数据流

**指标流（Prometheus pull）**：

```
请求 → metrics.HTTPMiddleware（包 mux 外层，otelhttp/recovery 之上）
  ├─ ctx 取 tenantID（gateway.TenantFrom，鉴权后才有；未鉴权端点 tenant=""）
  ├─ route 归一化（path 模板，{id} 占位）
  └─ defer Record: http_requests_total{method,route,status,tenant} + request_duration observe
请求 → handler
推理专用：/v1/chat/completions → gateway.ChatCompletions
  ├─ serveStream 流式累计 tokens
  └─ defer Record: paas_inference_requests_total + paas_inference_tokens_total + paas_inference_duration_seconds
Prometheus scrape → core:8080/metrics → promhttp.HandlerFor(自定义 registry)
real/metrics.go ListMetrics(platform) → PromQL rate()/histogram_quantile() 查回
```

**Trace 流（Tempo push + pull）**：

```
core 处理请求 → otelhttp 建 span → middleware 注入 span attr tenant=<tid>（ctx 取）
  → OTLP/HTTP push Tempo（PAAS_OTEL_ENDPOINT）
real/traces.go ListTraces → Tempo /api/search?tags=tenant=<tid>   （列表，已有）
real/traces.go GetTrace    → Tempo /api/traces/{id} → 解析 OTLP → tenant 归属校验 → 返 span 树
```

**中间件顺序关键**：metrics middleware 与 tenant span 注入必须在**鉴权中间件之后**（才能从 ctx 取到 tenant）。未鉴权端点（`/livez` `/openapi.json` `/docs` `/metrics`）span/指标 tenant=""，可接受（平台探针/契约端点）。

## 错误处理与降级

沿用平台既定横切模式（与 observability real 三支柱同构）：

- **Prometheus/Loki/Tempo 不可达**：fetch 失败 → 日志 + 返空切片/空 trace，不 5xx、不 panic（降级）。前端指标卡/trace 列表空态。
- **/metrics 端点**：`promhttp.HandlerFor` 自带错误处理；registry 空也正常返。
- **指标 Record panic 防护**：`mustRecord` 固定 label 顺序 + 单测覆盖所有调用点，防 `WithLabelValues` 维度错配 panic。
- **Trace tenant 归属校验**：fetch 后遍历 span 找 `tenant` 标签，与 `tenant.TenantFrom(ctx)` 比对；不匹配或无 tenant attribute → 返 `ErrTraceNotFound`，handler 映射 404（不泄漏存在性，与 ListTraces 同款）。Tempo `/api/traces/{id}` 不支持 tag 过滤，只能 fetch 后校验。
- **OTLP 解析容错**：旧版/新版字段名、空 resource、空 attributes → 防御性解码，缺字段降级空，不阻断返回（best-effort，与 logs inferLevel 同款）。
- **高基数 label 防护**：route 归一化白名单，未识别 path 归 `"unmatched"`，避免 path 片段当 label 撑爆 cardinality。
- **多副本**：metrics 自定义 registry 进程内（Prometheus 按 Pod instance 区分）；trace tenant 校验 per-request 无状态。多副本无需 Redis（指标天然可分片聚合，与限流不同）。

## 测试

**单元测试（内存 + fake，零依赖，`make test`）**：

- `internal/metrics/`：middleware 记录正确（method/route/status/tenant + route 归一化 `/api/applications/app-1`→`/api/applications/{id}`）；未鉴权 tenant="" 正常；`mustRecord` 维度匹配防 panic；`/metrics` 返非空。
- gateway `openai.go`：推理成功/失败/超时三路径 Record 正确 + tokens prompt/completion 拆分（mock provider 返已知 tokens 断言 counter）。
- `real/metrics.go`：平台级查询生成正确 PromQL（`rate`/`histogram_quantile`）；`httptest` mock Prometheus 返预置响应断言聚合。
- `real/traces.go GetTrace`：OTLP JSON 解析（旧版 `instrumentationLibrarySpans` + 新版 `scopeSpans` 两 fixture）→ span 树正确；tenant 归属校验匹配/不匹配/无 attribute 三分支；Tempo 不可达返空不 panic。
- `observability` memory：`GetTrace` mock span 树供前端联调。
- handler：`GET /api/observability/traces/{id}` 映射 200/404；OpenAPI Operation 登记。

**集群 e2e（手动，部署后）**：

- port-forward core /metrics → curl 见 `paas_inference_*` + `http_requests_total`。
- 触发推理 → Grafana/Prom 验证指标曲线。
- 触发 API 请求 → Tempo `/api/search?tags=tenant=t-acme` 见 trace → `/api/traces/{id}` 见完整 span 树 + tenant attribute。
- console-user 可观测页 trace 列表点行 → 详情瀑布图渲染。

**不测**（YAGNI）：应用 Pod 自身埋点（不在范围）；多副本 Redis 共享（架构级留后续）。

## 留后续

- 应用 Pod 自身业务埋点（OTel SDK / Prometheus业务指标）——应用自己的事，PaaS 不侵入；如需可后续提供 SDK 引导（paas-registry 同模式）。
- metrics 多副本 Redis 共享（当前进程内分片 + Prom 按 instance 聚合已够）。
- Tempo span 详情的 span event / link 解析（当前取 attributes 子集）。
- 指标维度细分（按 status code 桶、按 error type 分类）。
