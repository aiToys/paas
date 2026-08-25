package marketplace

import "context"

// Repository marketplace 持久化接口。平台级公开（无 tenant 过滤——广场天然公开）；
// 发布/下架的所有权校验在 handler 层（PublisherTenant == ctx tenant）。
type Repository interface {
	// List 广场列表（entityType/category/q 空则不过滤；不含 snapshot 语义由 store 保证——列全量返回，handler 按需裁剪）。
	List(ctx context.Context, entityType, category, q string) ([]Item, error)
	// Get 详情（含 snapshot）。
	Get(ctx context.Context, id string) (Item, error)
	// Create/upsert 发布（同 entityType+name+publisherTenant 覆盖）。
	Create(ctx context.Context, i Item) (Item, error)
	// Delete 下架（仅发布者，handler 校验所有权）。
	Delete(ctx context.Context, id string) error
	// IncInstalls 安装计数 +1。
	IncInstalls(ctx context.Context, id string) error
	// ListByPublisher 我的发布（发布者租户视角）。
	ListByPublisher(ctx context.Context, tenantID string) ([]Item, error)
	// ListAll admin 跨租户管理总览。
	ListAll(ctx context.Context) ([]Item, error)
}
