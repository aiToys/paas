// Package identity 是 Platform Core 的身份与租户领域模型。
// 多租户隔离的最小元数据在此定义；所有业务表通过 tenant_id 关联。
package identity

import "time"

// Tenant 表示一个租户（组织）。
type Tenant struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// User 表示租户内的用户。
type User struct {
	ID       string
	TenantID string // 所属租户；多租户隔离键
	Name     string
	IsAdmin  bool
	Roles    []string // 角色名，关联 BuiltinRoles()
}

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
type APIKey struct {
	ID        string
	TenantID  string
	UserID    string
	Roles     []string
	Key       string // 明文 bearer，内存态
	CreatedAt time.Time
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
			"model:infer", "model:read",
			// 无 prod:write：developer 在生产环境只读，防误操作
		}},
		"viewer": {Name: "viewer", Permissions: []Permission{
			"application:read", "workload:read", "environment:read", "model:read",
		}},
	}
}

// PermProdWrite 是生产环境写操作所需的额外权限。
// 生产环境的写操作（创建/更新/删除）除基础权限外需持有此权限，防止误操作生产。
const PermProdWrite = "prod:write"
