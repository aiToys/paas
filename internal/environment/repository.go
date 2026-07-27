package environment

import "context"

// Repository 是环境持久化抽象。
// 实现必须从 ctx 取租户并强制过滤（缺失即拒，跨租户 not found）。
type Repository interface {
	List(ctx context.Context) ([]Environment, error)
	Get(ctx context.Context, id string) (Environment, error)
	Create(ctx context.Context, e Environment) error
	Delete(ctx context.Context, id string) error
	// EnvType 返回指定环境的生产/测试类型，供生产写权限校验（prod:write）。
	// 环境不存在返回错误。
	EnvType(ctx context.Context, id string) (string, error)
}
