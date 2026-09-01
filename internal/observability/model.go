// Package observability 是可观测领域模型（平台能力横切）。
//
// 本切片实现指标监控（MetricSeries 惰性时序）+ 告警规则（AlertRule 即时评估）。
// 可观测三支柱（Metrics/Logs/Traces）中的 Metrics + Alerts 闭环；Logs/Traces 后续。
//
// 不接真实采集（Prometheus/Loki/Jaeger）：ListMetrics 惰性补点模拟采集，ListAlerts 即时评估。
// 全部进程内 mock，接口为未来接入真实采集铺路。
package observability

import "time"

// 指标 target 类型。
const (
	TargetApp         = "app"
	TargetWorkload    = "workload"
	TargetEnv         = "env"
	TargetDataservice = "dataservice" // 数据服务实例（targetId = 数据服务 K8s 资源名，即领域 ID）
)

// 指标名（常用）。
const (
	MetricCPU       = "cpu"       // CPU 利用率 %
	MetricMem       = "mem"       // 内存利用率 %
	MetricRPS       = "rps"       // 每秒请求数
	MetricLatency   = "latency"   // P95 延迟 ms
	MetricDiskIO    = "disk_io"   // 磁盘 IO 速率 bytes/s（数据服务排障：IO 高=慢查询/全表扫描）
	MetricNetIO     = "net_io"    // 网络 IO 速率 bytes/s（数据服务排障：流量异常=连接风暴）
	MetricErrorRate = "errorRate" // 错误率 %
)

// 引擎业务指标（数据服务排障：需引擎 exporter sidecar 提供，见 controller.exporterSidecar）。
const (
	MetricConnections = "connections" // 数据库/缓存/MQ 连接数
	MetricQPS         = "qps"         // 查询/事务每秒数（db/cache/vector/search）
	MetricHitRate     = "hit_rate"    // 缓存命中率 %（redis）
	MetricLag         = "lag"         // 消息堆积（MQ pending）
	MetricVectors     = "vectors"     // 向量数（qdrant collection）
	MetricDiskUsage   = "disk_usage"  // PVC 磁盘使用率 %（kubelet_volume_stats，无需 exporter）
)

// 比较运算符。
const (
	OpGT  = ">"  // 大于
	OpGTE = ">=" // 大于等于
	OpLT  = "<"  // 小于
	OpLTE = "<=" // 小于等于
)

var validOps = map[string]struct{}{
	OpGT: {}, OpGTE: {}, OpLT: {}, OpLTE: {},
}

// 告警严重级别。
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

var validSeverities = map[string]struct{}{
	SeverityCritical: {}, SeverityWarning: {},
}

// MaxPoints 是单条 series 保留的最近点数（环形截断）。
const MaxPoints = 60

// MetricPoint 是一个时序数据点。
type MetricPoint struct {
	TS    time.Time `json:"ts"`
	Value float64   `json:"value"`
}

// MetricSeries 是按 (target, metric) 维度的时序。Current 随机游走，Points 惰性追加。
type MetricSeries struct {
	ID         string        `json:"id"`
	TenantID   string        `json:"tenantId,omitempty"`
	TargetType string        `json:"targetType"` // app | workload | env | dataservice
	TargetID   string        `json:"targetId"`
	Name       string        `json:"name"` // cpu | mem | rps | latency | errorRate
	Unit       string        `json:"unit"` // % / ms / req/s
	Current    float64       `json:"current"`
	Points     []MetricPoint `json:"points"`
}

// AlertRule 是告警规则：对某 metric（可选 target）超阈值即告警。
type AlertRule struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	Name       string    `json:"name"`
	MetricName string    `json:"metricName"`         // cpu | rps | ...
	TargetType string    `json:"targetType"`         // app | workload | env | dataservice
	TargetID   string    `json:"targetId,omitempty"` // 空 = 该类型全部 target
	Operator   string    `json:"operator"`           // > >= < <=
	Threshold  float64   `json:"threshold"`
	Severity   string    `json:"severity"` // critical | warning
	Enabled    bool      `json:"enabled"`
	WebhookURL string    `json:"webhookUrl,omitempty"` // 告警 firing 时 POST 通知出站（空=不出站）
	UpdatedAt  time.Time `json:"updatedAt"`
}

// 告警状态（评估引擎状态机）。
const (
	AlertPending  = "pending"  // 首次命中，未达持续窗口（防瞬时毛刺）
	AlertFiring   = "firing"   // 持续命中（正式告警 + 出站通知）
	AlertResolved = "resolved" // 命中后恢复（保留展示，评估继续）
)

// Validate 校验规则字段。
func (r AlertRule) Validate() error {
	if r.Name == "" {
		return errInvalid("name")
	}
	if r.MetricName == "" {
		return errInvalid("metricName")
	}
	if r.TargetType == "" {
		return errInvalid("targetType")
	}
	if _, ok := validOps[r.Operator]; !ok {
		return errInvalid("operator")
	}
	if _, ok := validSeverities[r.Severity]; !ok {
		return errInvalid("severity")
	}
	return nil
}

// Matches 判断 series 是否命中规则的目标范围。
func (r AlertRule) Matches(s MetricSeries) bool {
	return s.TargetType == r.TargetType && r.MetricName == s.Name &&
		(r.TargetID == "" || r.TargetID == s.TargetID)
}

// Breached 判断值是否超过阈值（命中规则条件）。
func (r AlertRule) Breached(value float64) bool {
	switch r.Operator {
	case OpGT:
		return value > r.Threshold
	case OpGTE:
		return value >= r.Threshold
	case OpLT:
		return value < r.Threshold
	case OpLTE:
		return value <= r.Threshold
	}
	return false
}

// Alert 是规则评估命中的告警实例（即时生成，不持久化）。
type Alert struct {
	RuleID     string    `json:"ruleId"`
	RuleName   string    `json:"ruleName"`
	TenantID   string    `json:"tenantId,omitempty"` // 评估引擎填充（即时评估路径为空，按 ctx 租户隐含）
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	MetricName string    `json:"metricName"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	Operator   string    `json:"operator"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"` // pending | firing | resolved
	FiredAt    time.Time `json:"firedAt"`
	LastSeen   time.Time `json:"lastSeen,omitempty"` // 最近一次评估命中时间（引擎路径填充）
}

// AlertEvent 告警历史事件（状态转变时落库，只增不删；PG alert_events 表）。
// ID 由持久层生成；OccurredAt = 转变发生时间。
type AlertEvent struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	RuleID     string    `json:"ruleId"`
	RuleName   string    `json:"ruleName"`
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	MetricName string    `json:"metricName"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	Operator   string    `json:"operator"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"`     // firing | resolved（转变事件，pending 不落）
	FiredAt    time.Time `json:"firedAt"`    // 告警首次 firing 时间
	OccurredAt time.Time `json:"occurredAt"` // 本转变发生时间
}

// AlertEventFromAlert 由告警实例构造历史事件（ID 留给持久层生成）。
func AlertEventFromAlert(a Alert, status string, occurredAt time.Time) AlertEvent {
	return AlertEvent{
		TenantID: a.TenantID, RuleID: a.RuleID, RuleName: a.RuleName,
		TargetType: a.TargetType, TargetID: a.TargetID,
		MetricName: a.MetricName, Value: a.Value, Threshold: a.Threshold,
		Operator: a.Operator, Severity: a.Severity,
		Status: status, FiredAt: a.FiredAt, OccurredAt: occurredAt,
	}
}

// 日志级别。
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

var validLevels = map[string]struct{}{
	LevelInfo: {}, LevelWarn: {}, LevelError: {},
}

// MaxLogs 日志查询返回上限（最近 N 条）。memory 环形缓冲 + real Loki 查询共用，
// 防一次查询拉爆内存/PVC。统一与 MaxTraces 对齐为 100。
const MaxLogs = 100

// ValidLevel 校验级别合法。
func ValidLevel(l string) bool {
	_, ok := validLevels[l]
	return ok
}

// LogEntry 是一条应用日志（可观测三支柱之 Logs）。
// 惰性补点（无 goroutine）：查询时按时间间隔追加 mock 日志，环形截断。
type LogEntry struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"`
	AppID      string    `json:"appId"`
	TargetType string    `json:"targetType,omitempty"` // app | dataservice（日志归属维度，空=历史 app 日志）
	TargetID   string    `json:"targetId,omitempty"`   // app 时=appID；dataservice 时=数据服务 ID
	Level      string    `json:"level"`                // info | warn | error
	Message    string    `json:"message"`
	TraceID    string    `json:"traceId,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// 链路状态。
const (
	TraceSuccess = "success"
	TraceError   = "error"
)

var validTraceStatus = map[string]struct{}{
	TraceSuccess: {}, TraceError: {},
}

// MaxTraces 每租户 trace 缓冲的环形截断上限。
const MaxTraces = 100

// ValidTraceStatus 校验 trace 状态合法。
func ValidTraceStatus(s string) bool {
	_, ok := validTraceStatus[s]
	return ok
}

// Span 是 trace 内的一个调用片段（flat，无强制树渲染）。
//
// Tags 是 OTel span attributes 的全量透传（http.* / client.address / exception.stacktrace /
// 应用自定义任意属性），前端按 key-value 表展示，便于排障定位。
// IsError 标识错误 span（OTLP status=ERROR 或 http.response.status_code>=500），
// ErrorType/ErrorMessage 取自 exception.type / exception.message
// （由 tracing.ErrorTraceMiddleware 在 4xx/5xx 响应/panic 时写入；otelhttp 只设 status 不记原因）。
type Span struct {
	ID           string            `json:"id"`
	ParentID     string            `json:"parentId,omitempty"`
	Operation    string            `json:"operation"`
	Service      string            `json:"service"`
	Kind         string            `json:"kind,omitempty"` // OTel span.kind：server（入口）/ client（出站调用）/ producer / consumer / internal
	Peer         string            `json:"peer,omitempty"` // client span 的真实对端（peer.service > db.system > server.address），如 bff client span 显示「bff → redis」
	StartMs      int64             `json:"startMs"`        // 相对 trace 起点的偏移（ms）
	DurationMs   int64             `json:"durationMs"`     // 本 span 时长（ms）
	IsError      bool              `json:"isError,omitempty"`
	ErrorType    string            `json:"errorType,omitempty"`
	ErrorMessage string            `json:"errorMessage,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// Trace 是一次分布式调用的链路（可观测三支柱之 Traces）。
// 惰性补点：查询时按时间间隔生成 mock trace（含若干 span 的服务调用链）。
type Trace struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"`
	AppID      string    `json:"appId"`
	Operation  string    `json:"operation"`         // 入口操作，如 POST /v1/chat
	Service    string    `json:"service,omitempty"` // OTel service.name（工作负载名），多服务应用区分来源
	Status     string    `json:"status"`            // success | error
	DurationMs int64     `json:"durationMs"`
	StartedAt  time.Time `json:"startedAt"`
	Spans      []Span    `json:"spans"`
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
