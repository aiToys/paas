// Package identity 是 Platform Core 的身份与租户领域模型。
// 多租户隔离的最小元数据在此定义；所有业务表通过 tenant_id 关联。
package identity

import "time"

// Tenant 表示一个租户（组织）。
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// User 表示租户内的用户。
type User struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenantId"`        // 所属租户；多租户隔离键
	Name         string    `json:"name"`            // 登录用户名（本期全局唯一）
	Email        string    `json:"email,omitempty"` // 可选
	PasswordHash string    `json:"-"`               // 永不序列化（handler 显式清空 + json:"-" 双保险）
	IsAdmin      bool      `json:"isAdmin"`
	Roles        []string  `json:"roles"`            // 角色名，关联 BuiltinRoles()
	Status       string    `json:"status,omitempty"` // active|disabled；仅 active 可密码登录
	CreatedAt    time.Time `json:"createdAt,omitempty"`
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
type APIKey struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	UserID    string    `json:"userId"`
	AppID     string    `json:"appId,omitempty"` // 可选：绑定应用则用量归因到应用
	Roles     []string  `json:"roles"`
	Key       string    `json:"key"` // 明文 bearer，内存态；handler 在列表时掩码
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// BuiltinRoles 返回内置角色定义（起步期固定）。
// tenant-admin 通行（含生产写）；developer 覆盖应用/工作负载/环境读写与推理，但**生产只读**（无 prod:write）；
// viewer 全只读。
func BuiltinRoles() map[string]Role {
	return map[string]Role{
		"tenant-admin": {Name: "tenant-admin", Permissions: []Permission{"tenant:admin", "prod:write"}},
		"developer": {Name: "developer", Permissions: []Permission{
			"application:read", "application:write", "binding:write",
			"workload:read", "workload:write",
			"environment:read", "environment:write",
			"repository:read", "repository:write",
			"build:read", "build:write",
			"image:read",
			"release:read", "release:write",
			"config:read", "config:write",
			"governance:read", "governance:write",
			"observability:read", "observability:write",
			"security:read", "security:write",
			"billing:read", "billing:write",
			"dataservice:read", "dataservice:write",
			"kb:read", "kb:write",
			"model:infer", "model:read",
			// 无 prod:write：developer 在生产环境只读，防误操作
		}},
		"viewer": {Name: "viewer", Permissions: []Permission{
			"application:read", "workload:read", "environment:read", "model:read",
			"repository:read", "build:read", "image:read", "release:read", "config:read", "governance:read", "observability:read", "security:read", "billing:read", "dataservice:read", "kb:read",
		}},
		// app-llm 是应用级 API Key 的最小角色（绑模型时自动生成 Key 用）：
		// 仅含推理权限，用量归因到应用；无任何管理/写权限（最小权限原则）。
		"app-llm": {Name: "app-llm", Permissions: []Permission{"model:infer", "model:read"}},
	}
}

// PermProdWrite 是生产环境写操作所需的额外权限。
// 生产环境的写操作（创建/更新/删除）除基础权限外需持有此权限，防止误操作生产。
const PermProdWrite = "prod:write"
