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
}
