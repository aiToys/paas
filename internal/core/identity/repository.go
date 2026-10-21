package identity

import (
	"context"
	"errors"
	"time"
)

// ErrTenantNotEmpty 删除租户时租户下仍有用户（防孤儿数据 + 防误删）。
// handler 映射 409，引导先清用户。
var ErrTenantNotEmpty = errors.New("租户下仍有用户")

// Repository 是 Identity 持久化抽象。
// 实现需在所有查询中强制按 tenant 过滤（Plan 2 的 PG 实现会在 SQL 层注入）。
type Repository interface {
	CreateTenant(ctx context.Context, t Tenant) error
	GetTenant(ctx context.Context, id string) (Tenant, error)
	CreateUser(ctx context.Context, u User) error
	// UsersByTenant 仅返回该租户的用户，防止跨租户泄漏。
	UsersByTenant(ctx context.Context, tenantID string) ([]User, error)
	// GetUserByName 按登录用户名查（密码登录入口；本期全局唯一）。
	GetUserByName(ctx context.Context, name string) (*User, error)
	// GetUser 取单个用户（/auth/users/me 用；租户内隔离）。
	// tenantID 为空时跨租户按用户 ID 定位（平台超管路径，与 DeleteUser 同约定）。
	GetUser(ctx context.Context, tenantID, userID string) (*User, error)

	// API Key 鉴权入口。
	CreateAPIKey(ctx context.Context, k APIKey) error
	// LookupAPIKey 按 bearer key 解析 (租户, 用户, 角色)；找不到返回错误。
	LookupAPIKey(ctx context.Context, key string) (APIKey, error)

	// —— 平台级管理方法（跨租户；handler 强制 tenant:admin）——
	ListTenants(ctx context.Context) ([]Tenant, error)
	DeleteTenant(ctx context.Context, id string) error
	ListUsers(ctx context.Context, tenantID string) ([]User, error) // tenantID 空则全租户
	UpdateUser(ctx context.Context, u User) error                   // 改 roles/status/is_admin（密码见 handler）
	DeleteUser(ctx context.Context, tenantID, userID string) error
	ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error) // tenantID 空则全租户
	DeleteAPIKey(ctx context.Context, id string) error

	// —— 生命周期（D5）——
	// SetTenantStatus 冻结/恢复租户；frozen 时写 FrozenAt=now 与原因，active 时清空。
	SetTenantStatus(ctx context.Context, id, status, reason string) (Tenant, error)
	// GetAPIKey 按 id 取（跨租户 admin 轮换用；鉴权热路径仍走 LookupAPIKey）。
	GetAPIKey(ctx context.Context, id string) (APIKey, error)
	// TouchAPIKeyUsage 回写最近使用时间（网关每分钟至多一次，热路径不写放大）。
	TouchAPIKeyUsage(ctx context.Context, id string, at time.Time) error
	// RevokeAPIKey 软吊销（保留记录与角色供审计；吊销后 LookupAPIKey 拒绝）。
	RevokeAPIKey(ctx context.Context, id string, at time.Time) error
	// SetAPIKeyExpiry 设置到期时间（轮换宽限期用；零值=永不过期）。
	SetAPIKeyExpiry(ctx context.Context, id string, at time.Time) error

	// RevokeUserSessions 撤销该用户已签发的全部会话（session_version+1），返回新版本号。
	// 已签发 token 在下次校验（网关 ≤30s 缓存 / 续期）时失效。租户 ID 为空时跨租户按用户 ID 定位。
	RevokeUserSessions(ctx context.Context, tenantID, userID string) (int, error)
}
