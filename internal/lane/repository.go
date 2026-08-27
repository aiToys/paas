package lane

import "context"

// Repository 是泳道仓储接口（租户强制过滤，跨租户统一 not found 不泄漏）。
type Repository interface {
	// List 租户内泳道列表；envID 空不过滤（跨环境总览）。
	List(ctx context.Context, envID string) ([]Lane, error)
	// Get 按 ID 取（跨租户返 ErrLaneNotFound）。
	Get(ctx context.Context, id string) (Lane, error)
	// GetByName 按 (envID, name) 取（懒建/解析用）；不存在返 ErrLaneNotFound。
	GetByName(ctx context.Context, envID, name string) (Lane, error)
	// Create 创建（同租户×环境×名唯一，冲突返 ErrLaneExists；租户以 ctx 为准忽略请求体）。
	Create(ctx context.Context, in Lane) (Lane, error)
	// Update 更新 mode/description/externalLink（name/envID 不可改）。
	Update(ctx context.Context, id string, in Lane) (Lane, error)
	// Close 关闭（Status=closed，幂等：已 closed 直接返回）。
	Close(ctx context.Context, id string) (Lane, error)
	// EnsureByName 存在返回既有（不覆盖——permanent 不被懒建降级）；
	// 不存在懒建 Mode=standard（并发竞态由唯一约束兜底，双实现均幂等）。
	EnsureByName(ctx context.Context, envID, name string) (Lane, error)
}
