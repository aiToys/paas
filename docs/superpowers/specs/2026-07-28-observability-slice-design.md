# 可观测切片设计：指标监控 + 告警

> 平台能力（横切）。可观测三支柱（Metrics / Logs / Traces）+ 告警。本切片聚焦**指标监控 + 告警规则**的闭环，Logs/Traces 各为后续子切片。
>
> 开源起步期不接真实采集（Prometheus/Loki/Tempo），用**惰性时序生成**模拟数据面采集，接口为未来接入铺路。

## 定位

可观测属「平台能力（横切）」，租户私有，侧栏「平台能力 → 可观测」。本期不按物理环境隔离规则（规则租户级；target 可带 env 维度但规则本身不绑环境），不接 `prod:write`，权限统一 `observability:read/write`。

## 范围

### 实体

```
MetricSeries（指标时序，按 target × metric 维度）
  ID, TenantID, TargetType（app|workload|env）, TargetID, Name（cpu|mem|rps|latency|errorRate）, Unit,
  Current float64,            // 当前值（随机游走）
  Points []MetricPoint        // 最近 N 点（惰性追加，环形截断）

MetricPoint { TS, Value }

AlertRule（告警规则）
  ID, TenantID, Name, MetricName, TargetType, TargetID（空=该类型全部 target）,
  Operator（>|>=|<|<=）, Threshold float64,
  Severity（critical|warning）, Enabled, UpdatedAt

Alert（告警实例 = 规则评估命中，即时生成不持久化）
  RuleID, RuleName, TargetType, TargetID, MetricName, Value, Threshold, Severity,
  Status（firing）, FiredAt
```

### mock 数据采集（惰性，无后台 goroutine）

- `ListMetrics` 时：若 `now - lastPoint > interval`，按当前时间补点，`Current` 随机游走（有界）。
- 好处：无 goroutine 生命周期管理，测试可控，查询时"看起来实时"。

### 告警评估（即时，不持久化）

- `ListAlerts`：遍历 enabled 规则，对匹配 target 的最新指标点评估，超阈值即 firing。无历史告警持久化 / resolved 状态机（留后续）。

### Repository（单 Store，带前缀方法）

- metrics：`ListMetrics(targetType, targetID, name)（惰性补点） / ListSeriesSummary(targetType, targetID)`
- alert-rules：`ListAlertRules / CreateAlertRule / DeleteAlertRule`
- alerts：`ListAlerts（即时评估）`
- 全方法租户强制过滤。

### REST API

```
GET    /api/observability/metrics?targetType=&targetId=&name=  指标时序（惰性补点）
GET    /api/observability/alert-rules                          规则列表
POST   /api/observability/alert-rules                          创建规则
DELETE /api/observability/alert-rules/{id}                     删除规则
GET    /api/observability/alerts                               当前告警（即时评估）
```

### 权限

- `observability:read` / `observability:write`（admin/dev 读写，viewer 只读）。规则写用 `observability:write`，不接 prod:write。

## 不做（YAGNI / 后续）

- 日志聚合（Loki）/ 链路追踪（Tempo）—— 后续子切片。
- 告警通知通道（webhook/钉钉/邮件）+ 告警状态机（firing/resolved 持久化）—— 后续。
- 真实采集接入（Prometheus remote / OTel）—— 数据面接入期。
- 指标聚合 / PromQL / 自定义大盘 —— 后续。
- 长期存储 / 降采样。
