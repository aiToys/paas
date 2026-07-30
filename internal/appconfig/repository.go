package appconfig

import "context"

// Repository 是应用配置持久化抽象。
// 实现必须从 ctx 取租户并强制过滤（缺失即拒，跨租户 not found）。
// List 返回的 Secret 值必须掩码（不泄漏）。
type Repository interface {
	// List 按 (appID, envID) 过滤；空串表示该维度不限。Secret 值掩码返回。
	List(ctx context.Context, appID, envID string) ([]ConfigItem, error)
	// Upsert 新增或更新：同 (tenant, app, env, key) 则更新 value/type，否则插入。
	// 返回掩码后的配置项。
	Upsert(ctx context.Context, item ConfigItem) (ConfigItem, error)
	Delete(ctx context.Context, id string) error
}
