package observability

import "context"

// Repository 是可观测持久化与评估接口。
// 方法名带前缀以避免单 Store 实现时的重名冲突。
// 全方法从 ctx 取租户强制过滤；跨租户访问不泄漏。
type Repository interface {
	// ListMetrics 返回指标时序（惰性补点：查询时按当前时间追加新点，模拟采集）。
	// targetType/targetID/name 任一为空表示该维度不限。
	ListMetrics(ctx context.Context, targetType, targetID, name string) ([]MetricSeries, error)
	// ListAlertRules 告警规则列表。
	ListAlertRules(ctx context.Context) ([]AlertRule, error)
	// CreateAlertRule 创建规则。
	CreateAlertRule(ctx context.Context, rule AlertRule) (AlertRule, error)
	// UpdateAlertRule 更新规则（ID/TenantID 不变；改规则不再「删了重建」断开告警历史关联）。
	UpdateAlertRule(ctx context.Context, id string, rule AlertRule) (AlertRule, error)
	// DeleteAlertRule 删除规则。
	DeleteAlertRule(ctx context.Context, id string) error
	// ListNotificationChannels 通知渠道列表（当前租户）。
	// 渠道 URL/Headers 明文仅出站投递使用，handler 一律经 Masked() 输出。
	ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error)
	// CreateNotificationChannel 创建渠道（ctx 租户为准；同租户重名拒绝）。
	CreateNotificationChannel(ctx context.Context, ch NotificationChannel) (NotificationChannel, error)
	// UpdateNotificationChannel 更新渠道（ID/TenantID 不变；敏感字段掩码占位保留原值）。
	UpdateNotificationChannel(ctx context.Context, id string, ch NotificationChannel) (NotificationChannel, error)
	// DeleteNotificationChannel 删除渠道（不级联规则；引用方在引擎解析时忽略悬空 ID）。
	DeleteNotificationChannel(ctx context.Context, id string) error
	// ListAlerts 即时评估所有 enabled 规则，返回命中（firing）告警列表。
	// targetType/targetId 非空时只返回该维度的告警（前端依赖资源卡按数据服务过滤减传输）；
	// 任一为空表示该维度不限（向后兼容原全量评估）。
	ListAlerts(ctx context.Context, targetType, targetId string) ([]Alert, error)
	// ListAllAlertRules 跨租户列出全部告警规则（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllAlertRules(ctx context.Context) ([]AlertRule, error)
	// ListLogs 应用日志查询（惰性补点：查询时按时间间隔追加 mock 日志）。
	// targetType=dataservice 时按 TargetType/TargetID 维度过滤；否则按 appID 维度（向后兼容）。
	// level 为空表示不限；q 为消息关键字（大小写不敏感）；limit<=0 时用默认上限。按时间倒序返回。
	ListLogs(ctx context.Context, appID, targetType, targetID, level, q, lane, traceID string, limit int) ([]LogEntry, error)
	// ListTraces 链路追踪查询（惰性补点：查询时按时间间隔生成 mock trace）。
	// appID/status 为空表示不限；limit<=0 时用默认上限。按 StartedAt 倒序返回。
	ListTraces(ctx context.Context, appID, status string, limit int) ([]Trace, error)
	// GetTrace 按 traceID 精确查单条（排障直查：日志/告警拿到 traceId 后定位完整链路）。
	GetTrace(ctx context.Context, traceID string) (Trace, error)
	// ProbeMetricsQuality 实体维度采集质量诊断（spec 2026-10-17 专项04 §5）：
	// 逐约定 metric 即时探测，区分 ok/empty/failed（「查询成功无数据」≠「采集源故障」）。
	// 未接真实采集源的实现返回 Source=memory 的「未接入」报告（不伪造探测结果）。
	ProbeMetricsQuality(ctx context.Context, targetType, targetID string) (QualityReport, error)
}
