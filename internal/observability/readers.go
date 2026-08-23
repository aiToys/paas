package observability

import (
	"context"
	"errors"
)

// ErrTraceNotFound trace 不存在（GetTrace 精确查询）。
var ErrTraceNotFound = errors.New("trace not found")

// MetricsReader 是指标读取能力（memory 与 real 均实现，compose 聚合用）。
type MetricsReader interface {
	ListMetrics(ctx context.Context, targetType, targetID, name string) ([]MetricSeries, error)
}

// LogsReader 是日志读取能力。
type LogsReader interface {
	// ListLogs 应用日志查询。targetType=dataservice 时按 TargetType/TargetID 维度过滤；
	// 否则按 appID 维度（向后兼容历史 app 日志）。level/q 为空表示不限；limit<=0 用默认上限。
	ListLogs(ctx context.Context, appID, targetType, targetID, level, q, lane string, limit int) ([]LogEntry, error)
}

// TracesReader 是链路读取能力。
type TracesReader interface {
	ListTraces(ctx context.Context, appID, status string, limit int) ([]Trace, error)
	// GetTrace 按 traceID 精确查单条（排障直查入口：从日志/告警/响应头拿到 traceId 后直接定位）。
	// 不存在返 ErrTraceNotFound。
	GetTrace(ctx context.Context, traceID string) (Trace, error)
}

// RuleStore 是告警规则配置存取（始终 memory，compose 委托）。
type RuleStore interface {
	ListAlertRules(ctx context.Context) ([]AlertRule, error)
	CreateAlertRule(ctx context.Context, rule AlertRule) (AlertRule, error)
	DeleteAlertRule(ctx context.Context, id string) error
	// ListAllAlertRules 跨租户列出全部告警规则（admin 平台总览，不过滤 tenant）。
	ListAllAlertRules(ctx context.Context) ([]AlertRule, error)
}

// AppWorkloadLister 解析某应用下全部工作负载 ID（用于应用级指标/日志按 pod 名正则查询）。
// 依赖倒置：real 适配器不直接依赖 workload 包，由 cmd/core 桥接 workload.Repository。
// 工作负载的 K8s Deployment 名 = 工作负载 ID（wl-<id>），Pod 名 = <id>-<rsHash>-<podHash>，
// 故按 `pod=~"wl-<id>-.*"` 聚合即可得应用级 cAdvisor 指标 / Loki 日志，无需 kube-state-metrics。
type AppWorkloadLister interface {
	AppWorkloadIDs(ctx context.Context, appID string) ([]string, error)
	// AppWorkloadNames 返回某应用下全部工作负载名（K8s Deployment/Service 名）。
	// 用于应用级 trace 查询：应用 span 的 OTel service.name = 工作负载名（如 paas-shop-bff），
	// 故按 service.name 匹配工作负载名即可定位该应用的 trace（非工作负载 ID wl-xxx）。
	AppWorkloadNames(ctx context.Context, appID string) ([]string, error)
}

// TenantEntityLister 列出租户内全部实体（应用 + 数据服务 ID），用于全局指标聚合
// （可观测大屏「全部」视图健康矩阵：targetType 空 = 租户全部 series，与 memory 契约对齐）。
// 依赖倒置：real 适配器不依赖 application/dataservice 包，由 cmd/core 桥接各自 Repository。
type TenantEntityLister interface {
	TenantAppIDs(ctx context.Context) ([]string, error)
	TenantDataServiceIDs(ctx context.Context) ([]string, error)
	// TenantServiceNames 列出租户内全部 service 工作负载名（跨应用去重）。
	// 用于 trace 平台级查询与 GetTrace 归属校验：租户可见的 service.name 集合
	// 以此为白名单，禁止 Jaeger /api/services 全量枚举（跨租户泄漏）。
	TenantServiceNames(ctx context.Context) ([]string, error)
}
