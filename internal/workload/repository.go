package workload

import "context"

// Repository 是工作负载持久化抽象。
// 实现必须在所有查询中从 ctx 取租户并强制过滤（缺失即拒，跨租户 not found）。
type Repository interface {
	// List 按租户过滤；envID 空串表示所有环境；appID 空串表示跨应用；
	// laneID 空串表示所有泳道（含 default 基线）；wtype 空串表示所有类型；
	// service 空串表示所有服务（含单服务场景的空 Service 字段）。
	List(ctx context.Context, envID, appID, laneID, wtype, service string) ([]Workload, error)
	// ListAll 跨租户列出全部工作负载（admin 平台总览，不过滤 tenant；返回对象带 TenantID）。
	ListAll(ctx context.Context) ([]Workload, error)
	Get(ctx context.Context, id string) (Workload, error)
	Create(ctx context.Context, w Workload) error
	// Update 调整期望副本与状态（扩缩容/暂停/恢复）。返回更新后的工作负载。
	Update(ctx context.Context, id string, replicas int, status string) (Workload, error)
	// UpdateSchedule 修改 cronjob 的 cron 表达式（仅 cronjob 类型有效；schedule 空对 cronjob 拒绝）。
	// 返回更新后的工作负载；跨租户访问返回 not found（不泄漏）。
	UpdateSchedule(ctx context.Context, id, schedule string) (Workload, error)
	// UpdateImage 更新工作负载镜像（display 字符串 + 不可变 digest）。供 Release 编排调用。
	UpdateImage(ctx context.Context, id, image, imageRef string) (Workload, error)
	Delete(ctx context.Context, id string) error
}
