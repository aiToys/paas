package tool

import "context"

// Repository 工具持久化接口（全方法 ctx + tenant 过滤，跨租户 not found 不泄漏）。
// ToolsCount 全表（不经 tenant，seed 判空用，与 dataservice/KB 同款）。
type Repository interface {
	List(ctx context.Context) ([]Tool, error)
	Get(ctx context.Context, id string) (Tool, error)
	Create(ctx context.Context, t Tool) (Tool, error)
	Update(ctx context.Context, t Tool) (Tool, error)
	Delete(ctx context.Context, id string) error
	ToolsCount(ctx context.Context) (int, error)
}
