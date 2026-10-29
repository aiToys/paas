// Package identity 是 Platform Core 的身份与租户领域模型。
// 多租户隔离的最小元数据在此定义；所有业务表通过 tenant_id 关联。
package identity

import (
	"errors"
	"time"
)

// Tenant 表示一个租户（组织）。
// Status 为 active|frozen：冻结只阻断**控制面准入**（登录与 API Key 鉴权），
// 数据面已部署业务继续运行（控制面与数据面解耦，冻结不是停机）。
type Tenant struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"` // active | frozen
	FrozenAt     *time.Time `json:"frozenAt,omitempty"`
	FrozenReason string     `json:"frozenReason,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// 租户状态常量。
const (
	TenantStatusActive = "active"
	TenantStatusFrozen = "frozen"
)

// IsFrozen 判断租户是否被冻结（空状态按 active 兼容存量数据）。
func (t Tenant) IsFrozen() bool { return t.Status == TenantStatusFrozen }

// ErrTenantFrozen 租户已冻结：控制面准入被拒（登录 / API Key 鉴权）。
var ErrTenantFrozen = errors.New("租户已冻结")

// ErrTenantNotFound 租户不存在（跨租户 admin 路径复用标准 404 语义）。
var ErrTenantNotFound = errors.New("租户不存在")

// User 表示租户内的用户。
type User struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenantId"`        // 所属租户；多租户隔离键
	Name         string   `json:"name"`            // 登录用户名（本期全局唯一）
	Email        string   `json:"email,omitempty"` // 可选
	PasswordHash string   `json:"-"`               // 永不序列化（handler 显式清空 + json:"-" 双保险）
	IsAdmin      bool     `json:"isAdmin"`
	Roles        []string `json:"roles"`            // 角色名，关联 BuiltinRoles()
	Status       string   `json:"status,omitempty"` // active|disabled；仅 active 可密码登录
	// SessionVersion 会话代次（D5-3）：签发 token 时固化进 claims，校验时比对。
	// 改密/停用/改角色/显式撤销均 +1，已签发 token 随即失效（网关 ≤30s 缓存窗口）。
	SessionVersion int       `json:"sessionVersion"`
	CreatedAt      time.Time `json:"createdAt,omitempty"`
}

// 用户状态常量。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Permission 是粗粒度权限标识，形如 "application:read"。
type Permission string

// Role 是一组权限的命名集合。
type Role struct {
	Name        string
	Permissions []Permission
}

// Grants 判断角色是否持有某权限。
// tenant-admin（含 tenant:admin）通行所有权限。
func (r Role) Grants(p Permission) bool {
	for _, own := range r.Permissions {
		if own == p || own == "tenant:admin" {
			return true
		}
	}
	return false
}

// APIKey 是 (租户, 用户, 角色) 三元组的凭证，鉴权与计量的统一锚点。
// AppID 非空 = 应用级 Key（模型推理用量归因到具体应用）；空 = 租户级 Key（管理员/通用）。
//
// 生命周期（D5）：ExpiresAt 到期即失效（空=永不过期）；RevokedAt 吊销（保留记录供审计核对）；
// LastUsedAt 最近使用（网关每分钟至多回写一次，热路径不写放大）；RotatedFrom 指向轮换前的旧 Key。
type APIKey struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId"`
	UserID      string     `json:"userId"`
	AppID       string     `json:"appId,omitempty"` // 可选：绑定应用则用量归因到应用
	Roles       []string   `json:"roles"`
	Key         string     `json:"key"` // 明文 bearer，内存态；handler 在列表时掩码
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	RotatedFrom string     `json:"rotatedFrom,omitempty"`
	CreatedAt   time.Time  `json:"createdAt,omitempty"`
	// State 是派生状态（active|expiring|expired|revoked），不落库；仅响应回填，
	// 由 KeyState(now) 计算，避免前端各自复制「7 天内到期」的判定规则。
	State string `json:"state,omitempty"`
}

// 密钥失效原因（LookupAPIKey 返回，供调用方区分「无效」与「到期/吊销」）。
var (
	ErrAPIKeyExpired = errors.New("API Key 已过期")
	ErrAPIKeyRevoked = errors.New("API Key 已吊销")
)

// Usable 判断密钥在 now 时刻是否可用；不可用返具体原因。
func (k APIKey) Usable(now time.Time) error {
	if k.RevokedAt != nil {
		return ErrAPIKeyRevoked
	}
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return ErrAPIKeyExpired
	}
	return nil
}

// KeyState 面向管理页的派生状态（不落库）：active | expiring | expired | revoked。
// expiring = 7 天内到期（运维提前轮换的观察窗口）。
func (k APIKey) KeyState(now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return "revoked"
	case k.ExpiresAt == nil:
		return "active"
	case !now.Before(*k.ExpiresAt):
		return "expired"
	case k.ExpiresAt.Sub(now) <= 7*24*time.Hour:
		return "expiring"
	default:
		return "active"
	}
}

// BuiltinRoles 返回内置角色定义（起步期固定）。
// tenant-admin 通行（含生产写）；developer 覆盖应用/工作负载/环境读写与推理，但**生产只读**（无 prod:write）；
// viewer 全只读。
func BuiltinRoles() map[string]Role {
	return map[string]Role{
		"tenant-admin": {Name: "tenant-admin", Permissions: []Permission{"tenant:admin", "prod:write"}},
		"developer": {Name: "developer", Permissions: []Permission{
			"application:read", "application:write", "binding:write",
			"service:read", "service:write",
			"workload:read", "workload:write",
			"environment:read", "environment:write",
			"repository:read", "repository:write",
			"build:read", "build:write",
			"image:read",
			"release:read", "release:write",
			"pipeline:read", "pipeline:write",
			"config:read", "config:write",
			"governance:read", "governance:write",
			"observability:read", "observability:write",
			"security:read", "security:write",
			"billing:read", "billing:write",
			"dataservice:read", "dataservice:write",
			"agentrun:read", "agentrun:write", // Agent 编排（agentrun 观测代理 + agent stage 编排）
			"kb:read", "kb:write",
			"tool:read", "tool:write",
			"prompt:read", "prompt:write",
			"agent:read", "agent:write",
			"model:infer", "model:read",
			"diagnosis:read", "diagnosis:run", // 控制台 AI 助手（资源作用域只读诊断）
			// 无 prod:write：developer 在生产环境只读，防误操作
		}},
		"viewer": {Name: "viewer", Permissions: []Permission{
			"application:read", "service:read", "workload:read", "environment:read", "model:read",
			"repository:read", "build:read", "image:read", "release:read", "pipeline:read", "config:read", "governance:read", "observability:read", "security:read", "billing:read", "dataservice:read", "agentrun:read", "kb:read", "tool:read", "prompt:read", "agent:read", "diagnosis:read",
		}},
		// app-llm 是应用级 API Key 的最小角色（绑模型时自动生成 Key 用）：
		// 仅含推理权限，用量归因到应用；无任何管理/写权限（最小权限原则）。
		"app-llm": {Name: "app-llm", Permissions: []Permission{"model:infer", "model:read"}},
	}
}

// PermProdWrite 是生产环境写操作所需的额外权限。
// 生产环境的写操作（创建/更新/删除）除基础权限外需持有此权限，防止误操作生产。
const PermProdWrite = "prod:write"
