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
	// ListAll 跨租户列出全部环境（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAll(ctx context.Context) ([]Environment, error)
}

// EnvTypeResolver 是 EnvType 的最小依赖倒置接口（供跨模块生产写权限校验）。
// environment.Repository 天然实现该接口；workload/appconfig/devops/governance/dataservice/backup
// 等模块注入此接口，避免各自复制粘贴同名定义（单一真源，DRY）。
type EnvTypeResolver interface {
	EnvType(ctx context.Context, envID string) (string, error)
}
