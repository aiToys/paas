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
	// ListAllAlertRules 跨租户列出全部告警规则（admin 平台总览，不过滤 tenant）。
	ListAllAlertRules(ctx context.Context) ([]AlertRule, error)
}
