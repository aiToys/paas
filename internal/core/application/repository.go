package application

import "context"

// Repository 是应用持久化抽象。
// Plan 2 起接 PostgreSQL；本期内存实现。
type Repository interface {
	List(ctx context.Context) ([]Application, error)
	Get(ctx context.Context, id string) (Application, error)
	Create(ctx context.Context, a Application) error
	// BindResource 给应用追加一个指定类型的资源绑定（含名称），返回更新后的应用。
	BindResource(ctx context.Context, id, resourceType, name string) (Application, error)
	// Unbind 移除应用的某个绑定项，返回更新后的应用。
	Unbind(ctx context.Context, id, resourceType, name string) (Application, error)
}
