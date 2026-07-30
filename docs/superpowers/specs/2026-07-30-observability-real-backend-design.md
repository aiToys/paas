# 可观测接真实后端（Prometheus/Loki/Tempo）设计

**日期**：2026-07-30
**状态**：待评审
**关联**：`2026-07-28-observability-slice-design.md`（惰性 mock 已落地）、技术栈表（OTel + Prometheus + Loki + Tempo）

## 背景与动机

observability 模块当前是**惰性 mock**：`MetricSeries`（随机游走 + 查询时补点）、`LogEntry`（模板池随机生成）、`Trace`（链路模板池生成）、`Alert`（基于 mock metrics 即时评估）。dev/echo 路径开箱即用，但生产不可用——真实可观测必须接采集后端。

技术栈（CLAUDE.md 已定）：OpenTelemetry + Prometheus（metrics）+ Loki（logs）+ Tempo（traces）。本切片替换数据源：observability store 从「查 mock」改为「查真实后端 HTTP API」，领域模型与 handler 不动（Repository 接口切换点）。

## 范围

**做**：
- `internal/observability/real/` 新包：metrics/logs/traces 三 store 调真实后端 HTTP API 查询，实现 Repository 接口。
- 配置开关 `PAAS_PROM_URL` / `PAAS_LOKI_URL` / `PAAS_TEMPO_URL`（空 → 保持 memory 惰性 mock，dev 路径零依赖）。
- Alert 即时评估基于真实 metrics 当前值（评估逻辑复用）。

**不做（YAGNI / 归其它切片）**：
- 真实采集管道（应用 OTel 埋点 → OTel Collector → 后端）——属数据面/应用侧，归 K8s 数据面或独立埋点切片。
- Grafana dashboard 导出、告警通知通道（webhook/Slack/PagerDuty）、长期存储降采样。
- Metrics 真实写入（应用侧埋点上报）——本期只读查询。

## 设计

### 适配层（real store）

`internal/observability/real/` 三 store，各实现对应 Repository 接口（与 memory 1:1，handler 透明）：

```go
// real/metrics.go：调 Prometheus HTTP API
type MetricsStore struct{ promURL string; client *http.Client }
// ListMetrics 查 /api/v1/query_range（PromQL：按 target×metric 维度）
// 响应 prometheus.Matrix → []MetricSeries（点序列映射）

// real/logs.go：调 Loki HTTP API
type LogsStore struct{ lokiURL string; client *http.Client }
// ListLogs 查 /loki/api/v1/query_range（LogQL：{app="..."}），stream → []LogEntry

// real/traces.go：调 Tempo HTTP API
type TracesStore struct{ tempoURL string; client *http.Client }
// ListTraces 查 /api/search（TraceQL 或 tag 过滤）→ []Trace，详情 /api/traces/{id} → spans
```

纯 `net/http` + JSON 解析，无新依赖（与 maas `OpenAICompatibleProvider` 同款风格）。后端不可用 → 返回空切片 + 错误日志（降级，不 panic）。

### 配置开关（cmd/core）

与持久化 PG 同款「Repository 接口切换点」：

```
PAAS_PROM_URL 非空 → real.NewMetricsStore(promURL)
为空 → memory.NewStore()（惰性 mock，dev/echo）
Loki / Tempo 同理（各自独立开关，可混用：metrics 接 real、logs 仍 mock）
```

`buildObservabilityStore(env)` 收口，返回 Repository 注入 handler。AlertRule store 始终 memory（规则配置，非时序数据，已落 PG 持久化层不涉及）。

### Alert 即时评估（复用）

`ListAlerts` 遍历 enabled AlertRule，对匹配 series 当前值（现来自 real Prometheus）超阈值生成 `firing`。评估逻辑（`Matches`+`Breached`）与 mock 时代完全复用——只是数据源换了。

### PromQL/LogQL 维度映射

| 领域字段 | 后端标签 |
|---------|---------|
| `targetType`（application/workload）+ `targetID` | `target_type` / `target_id` label |
| `metric.name`（cpu/memory/rps/latency） | PromQL metric（`paas_cpu_usage` / `paas_rps` 等，由应用埋点定义） |
| `appID` | Loki `{app="..."}` / Tempo tag |

> 应用侧埋点暴露的 metric/label 命名约定另定（归埋点切片）；本期假设后端已有数据，real store 按约定查。

## 验收标准

1. 配 `PAAS_PROM_URL=http://prom:9090` → `GET /api/observability/metrics` 返回真实 Prometheus 查询结果（非随机游走）。
2. 不配三 URL → 行为与现状**完全一致**（惰性 mock，dev 零依赖）。
3. 配 `PAAS_LOKI_URL` / `PAAS_TEMPO_URL` → logs/traces 走真实后端。
4. 后端不可用 → 接口返回空数据 + 200（不 5xx/panic），日志记错误。
5. Alert 基于真实 metrics 当前值触发（规则配置不变）。
6. license：纯 net/http + JSON，无新依赖；Prometheus/Loki/Tempo 均 Apache 2.0。

## 风险与对策

- **PromQL/LogQL 复杂度**：target×metric×时间窗口映射易错。对策：real store 内封装查询构造器 + 单测覆盖维度组合；先支持固定时间窗口（如最近 1h），自定义窗口留后续。
- **后端 schema 漂移**：Prometheus/Loki/Tempo API 版本差异。对策：固定 v1 API（稳定）；用结构化响应类型严格解析。
- **查询性能**：大窗口/高基数查询慢。对策：默认窗口 + 点数上限（与 mock 的 `MaxPoints=60` 对齐，后端 step 参数控制）。
- **混用模式复杂度**：metrics real + logs mock 混用。对策：三 store 独立开关，cmd/core 分别构造，handler 不感知组合。
