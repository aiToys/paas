package configcenter

import "context"

// Repository 是配置中心持久化接口（命名空间 + 配置项 + 发布版本）。
// 方法名带实体前缀以避免单 Store 实现时的重名冲突。
// 全方法从 ctx 取租户强制过滤；跨租户访问统一 not found（不泄漏存在性）。
type Repository interface {
	NamespaceStore
	ItemStore
	PublishStore
}

// NamespaceStore 命名空间仓储。
type NamespaceStore interface {
	ListNamespaces(ctx context.Context, serviceID string) ([]Namespace, error)
	GetNamespace(ctx context.Context, id string) (Namespace, error)
	CreateNamespace(ctx context.Context, n Namespace) (Namespace, error)
	DeleteNamespace(ctx context.Context, id string) error
	// ListAllNamespaces 跨租户列出全部命名空间（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllNamespaces(ctx context.Context) ([]Namespace, error)
}

// ItemStore 配置项仓储（draft）。
type ItemStore interface {
	ListItems(ctx context.Context, namespaceID string) ([]ConfigItem, error)
	UpsertItem(ctx context.Context, item ConfigItem) (ConfigItem, error)
	DeleteItem(ctx context.Context, id string) error
}

// PublishStore 发布版本仓储。
type PublishStore interface {
	// ListPublishes 发布历史（按版本降序）。
	ListPublishes(ctx context.Context, namespaceID string) ([]Publish, error)
	// CreatePublish 快照当前 namespace 全部 item 生成新 active 发布，
	// 该 namespace 其他 active 改 rolled-back。返回新发布。
	CreatePublish(ctx context.Context, namespaceID string) (Publish, error)
	// RollbackPublish 激活历史 rolled-back 发布为 active，当前 active 改 rolled-back。
	RollbackPublish(ctx context.Context, publishID string) (Publish, error)
	// ActivePublish 返回 namespace 当前 active 发布（客户端发现用）。无发布返回零值+false。
	ActivePublish(ctx context.Context, namespaceID string) (Publish, bool, error)
	// PublishNamespaceID 返回发布所属 namespace（回滚路由校验用）。
	PublishNamespaceID(ctx context.Context, publishID string) (string, error)
}

// ServiceLookup 校验 governance Service 是否存在（依赖倒置，避免 configcenter→governance import）。
// Namespace.ServiceID 是弱关联（双向显示用），CreateNamespace 时校验非空 ServiceID 存在 + 属本租户，
// 防悬挂引用（typo/已删/跨租户 serviceID 产生脏数据）。实现按 ctx tenant 过滤，跨租户返 false。
// nil 时跳过校验（兼容旧装配/测试）。
type ServiceLookup interface {
	ServiceExists(ctx context.Context, serviceID string) (bool, error)
}
