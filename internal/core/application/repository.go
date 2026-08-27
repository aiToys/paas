package application

import "context"

// Repository 是应用持久化抽象。
// Plan 2 起接 PostgreSQL；本期内存实现。
type Repository interface {
	List(ctx context.Context) ([]Application, error)
	// ListAll 跨租户列出全部应用（admin 平台总览，不过滤 tenant；返回对象带 TenantID）。
	ListAll(ctx context.Context) ([]Application, error)
	Get(ctx context.Context, id string) (Application, error)
	Create(ctx context.Context, a Application) error
	// Delete 删除应用记录（含内嵌 bindings）。跨 store 关联资源（工作负载/配置/DevOps）
	// 由 handler 层 CascadeDelete 钩子在调本方法前清理，store 不直接依赖其他 store。
	Delete(ctx context.Context, id string) error
	// BindResource 给应用追加一个指定类型的资源绑定（含名称），返回更新后的应用。
	BindResource(ctx context.Context, id, resourceType, name string) (Application, error)
	// Unbind 移除应用的某个绑定项，返回更新后的应用。
	Unbind(ctx context.Context, id, resourceType, name string) (Application, error)
	// SetRestricted 切换应用级权限受限模式（true=写操作需成员角色匹配）。
	SetRestricted(ctx context.Context, id string, restricted bool) error
	// SetResourceTemplate 覆盖应用级资源规格默认值（deploy 未显式指定时继承；空=清除继承）。
	SetResourceTemplate(ctx context.Context, id string, t ResourceTemplate) error
}
