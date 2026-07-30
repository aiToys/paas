// Package observability 是可观测领域模型（平台能力横切）。
//
// 本切片实现指标监控（MetricSeries 惰性时序）+ 告警规则（AlertRule 即时评估）。
// 可观测三支柱（Metrics/Logs/Traces）中的 Metrics + Alerts 闭环；Logs/Traces 后续。
//
// 不接真实采集（Prometheus/Loki/Tempo）：ListMetrics 惰性补点模拟采集，ListAlerts 即时评估。
// 全部进程内 mock，接口为未来接入真实采集铺路。
package observability

import "time"

// 指标 target 类型。
const (
	TargetApp      = "app"
	TargetWorkload = "workload"
	TargetEnv      = "env"
)

// 指标名（常用）。
const (
	MetricCPU       = "cpu"       // CPU 利用率 %
	MetricMem       = "mem"       // 内存利用率 %
	MetricRPS       = "rps"       // 每秒请求数
	MetricLatency   = "latency"   // P95 延迟 ms
	MetricErrorRate = "errorRate" // 错误率 %
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
	TargetType string        `json:"targetType"` // app | workload | env
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
	TargetType string    `json:"targetType"`         // app | workload | env
	TargetID   string    `json:"targetId,omitempty"` // 空 = 该类型全部 target
	Operator   string    `json:"operator"`           // > >= < <=
	Threshold  float64   `json:"threshold"`
	Severity   string    `json:"severity"` // critical | warning
	Enabled    bool      `json:"enabled"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

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
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	MetricName string    `json:"metricName"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	Operator   string    `json:"operator"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"` // firing
	FiredAt    time.Time `json:"firedAt"`
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

// MaxLogs 每租户日志缓冲的环形截断上限（最新 N 条）。
const MaxLogs = 200

// ValidLevel 校验级别合法。
func ValidLevel(l string) bool {
	_, ok := validLevels[l]
	return ok
}

// LogEntry 是一条应用日志（可观测三支柱之 Logs）。
// 惰性补点（无 goroutine）：查询时按时间间隔追加 mock 日志，环形截断。
type LogEntry struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"`
	AppID     string    `json:"appId"`
	Level     string    `json:"level"` // info | warn | error
	Message   string    `json:"message"`
	TraceID   string    `json:"traceId,omitempty"`
	Timestamp time.Time `json:"timestamp"`
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
type Span struct {
	ID         string            `json:"id"`
	ParentID   string            `json:"parentId,omitempty"`
	Operation  string            `json:"operation"`
	Service    string            `json:"service"`
	StartMs    int64             `json:"startMs"`    // 相对 trace 起点的偏移（ms）
	DurationMs int64             `json:"durationMs"` // 本 span 时长（ms）
	Tags       map[string]string `json:"tags,omitempty"`
}

// Trace 是一次分布式调用的链路（可观测三支柱之 Traces）。
// 惰性补点：查询时按时间间隔生成 mock trace（含若干 span 的服务调用链）。
type Trace struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"`
	AppID      string    `json:"appId"`
	Operation  string    `json:"operation"` // 入口操作，如 POST /v1/chat
	Status     string    `json:"status"`    // success | error
	DurationMs int64     `json:"durationMs"`
	StartedAt  time.Time `json:"startedAt"`
	Spans      []Span    `json:"spans"`
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
