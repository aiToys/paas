package configcenter

import "context"

// Repository 是配置中心持久化接口（命名空间 + 配置项 + 发布版本）。
// 方法名带实体前缀以避免单 Store 实现时的重名冲突。
// 全方法从 ctx 取租户强制过滤；跨租户访问统一 not found（不泄漏存在性）。
type Repository interface {
	NamespaceStore
	ItemStore
	PublishStore
	LaneOverrideStore
	NSRefStore
}

// NSRefStore 共享配置引用仓储（shared ns → 应用派生 ns）。
type NSRefStore interface {
	// AddNSRef 建引用；前置校验 shared ns 存在 + scope=shared + 本租户 + 非自引，
	// 违反返回 ErrNamespaceNotFound/ErrRefNotShared；已引用返回 ErrRefExists。
	AddNSRef(ctx context.Context, appNSID, sharedNSID string) (NSRef, error)
	// DeleteNSRef 解除引用；不存在返回 ErrRefNotFound。
	DeleteNSRef(ctx context.Context, refID string) error
	// ListNSRefs 列出 app ns 的引用（按创建时间升序——merge 铺垫顺序，
	// 多 shared 引用时后者覆盖前者，与 UI 展示顺序一致可预期）。
	ListNSRefs(ctx context.Context, appNSID string) ([]NSRef, error)
	// ListNSRefUsers 反查 shared ns 的引用方（影响面展示：shared 发布时
	// 告知发布者会被哪些应用消费）。返回引用列表（含 app_ns_id 供前端解析归属）。
	ListNSRefUsers(ctx context.Context, sharedNSID string) ([]NSRef, error)
}

// NamespaceStore 命名空间仓储。
type NamespaceStore interface {
	ListNamespaces(ctx context.Context, serviceID string) ([]Namespace, error)
	GetNamespace(ctx context.Context, id string) (Namespace, error)
	CreateNamespace(ctx context.Context, n Namespace) (Namespace, error)
	DeleteNamespace(ctx context.Context, id string) error
	// ListAllNamespaces 跨租户列出全部命名空间（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllNamespaces(ctx context.Context) ([]Namespace, error)
	// EnsureByApp 懒建（或返回既有的）应用派生命名空间（scope=app，name=app-<appID>）。幂等。
	EnsureByApp(ctx context.Context, appID string) (Namespace, error)
	// FindAppNamespace 查应用派生命名空间（不创建）。无返回 false。
	FindAppNamespace(ctx context.Context, appID string) (Namespace, bool, error)
	// EnsureByAppEnv 懒建（或返回既有的）(app, env) 维度应用派生命名空间。幂等。
	// envID 空 = 全环境基线（name=app-<appID>，兼容存量）；非空 = name=app-<appID>-<envID>。
	EnsureByAppEnv(ctx context.Context, appID, envID string) (Namespace, error)
	// FindAppNamespaceEnv 查 (app, env) 维度命名空间（不创建）。发现解析语义：
	// envID 非空时精确 (app,env) 未命中回退 env='' 基线 ns；envID 空仅精确匹配 env=''。
	FindAppNamespaceEnv(ctx context.Context, appID, envID string) (Namespace, bool, error)
}

// LaneOverrideStore 泳道配置覆盖仓储（无版本链，upsert 即生效）。
type LaneOverrideStore interface {
	// UpsertLaneOverride 同 (tenant, app, env, lane, key) 覆盖更新，否则新增。
	UpsertLaneOverride(ctx context.Context, o LaneOverride) (LaneOverride, error)
	// DeleteLaneOverride 删除覆盖；不存在返回 ErrLaneOverrideNotFound。
	DeleteLaneOverride(ctx context.Context, appID, envID, laneID, key string) error
	// ListLaneOverrides 按 (app, env, lane) 过滤（lane 空=该 env 全部泳道），按 Key 升序。
	ListLaneOverrides(ctx context.Context, appID, envID, laneID string) ([]LaneOverride, error)
	// ListLaneOverridesForClean 泳道回收级联清理用：按 (env, lane) 跨 app 列出（tenant 从 ctx）。
	ListLaneOverridesForClean(ctx context.Context, envID, laneID string) ([]LaneOverride, error)
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

// LaneOverrideCleaner 泳道回收级联清理（依赖倒置：workload/lane 回收路径消费，
// configcenter 侧实现，避免下游模块 import configcenter）。
// 按 (tenant, env, lane) 跨 app 全量清理——LaneGC 按泳道维度回收（多 app），清理同维度。
type LaneOverrideCleaner interface {
	// CleanLane 物理删除该 (tenant, env, lane) 的全部覆盖（泳道已消失，覆盖无保留价值）。
	CleanLane(ctx context.Context, tenantID, envID, laneID string) error
}
