package identity

import "context"

// Repository 是 Identity 持久化抽象。
// 实现需在所有查询中强制按 tenant 过滤（Plan 2 的 PG 实现会在 SQL 层注入）。
type Repository interface {
	CreateTenant(ctx context.Context, t Tenant) error
	GetTenant(ctx context.Context, id string) (Tenant, error)
	CreateUser(ctx context.Context, u User) error
	// UsersByTenant 仅返回该租户的用户，防止跨租户泄漏。
	UsersByTenant(ctx context.Context, tenantID string) ([]User, error)

	// API Key 鉴权入口。
	CreateAPIKey(ctx context.Context, k APIKey) error
	// LookupAPIKey 按 bearer key 解析 (租户, 用户, 角色)；找不到返回错误。
	LookupAPIKey(ctx context.Context, key string) (APIKey, error)
}
