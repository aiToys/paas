package workload

import "context"

// Repository 是工作负载持久化抽象。
// 实现必须在所有查询中从 ctx 取租户并强制过滤（缺失即拒，跨租户 not found）。
type Repository interface {
	// List 按租户过滤；appID 空串表示跨应用；wtype 空串表示所有类型。
	List(ctx context.Context, appID, wtype string) ([]Workload, error)
	Get(ctx context.Context, id string) (Workload, error)
	Create(ctx context.Context, w Workload) error
	// Update 调整期望副本与状态（扩缩容/暂停/恢复）。返回更新后的工作负载。
	Update(ctx context.Context, id string, replicas int, status string) (Workload, error)
	Delete(ctx context.Context, id string) error
}
