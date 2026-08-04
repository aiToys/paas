package security

import "context"

// Repository 是安全持久化接口（密钥资产 + 审计日志）。
// 方法名带前缀以避免单 Store 实现时的重名冲突。
// Secret 全方法从 ctx 取租户强制过滤；跨租户访问统一 not found（不泄漏存在性）。
type Repository interface {
	SecretStore
	AuditStore
}

// SecretStore 密钥/证书资产仓储。
type SecretStore interface {
	ListSecrets(ctx context.Context) ([]Secret, error) // 返回掩码
	GetSecret(ctx context.Context, id string) (Secret, error)
	CreateSecret(ctx context.Context, s Secret) (Secret, error) // 返回掩码
	DeleteSecret(ctx context.Context, id string) error
	// Resolve 按 ID 取**平台级** Secret 明文（供第三方供应商通道运行时解析凭证）。
	// 仅平台级可经此路径读明文；租户级 Secret 返回 not found（防绕过掩码）。
	Resolve(ctx context.Context, id string) (Secret, error)
	// ListAllSecrets 跨租户列出全部密钥（admin 平台总览，掩码返回；含平台级+各租户级）。
	ListAllSecrets(ctx context.Context) ([]Secret, error)
}

// AuditStore 审计日志仓储（只增不删）。
type AuditStore interface {
	// ListAuditLogs 审计查询，resourceType/action 为空表示不限；按时间倒序。
	ListAuditLogs(ctx context.Context, resourceType, action string) ([]AuditLog, error)
	// RecordAudit 记录一条审计（由 handler 在写操作后调用，actor 由调用方注入）。
	RecordAudit(ctx context.Context, log AuditLog) error
	// ListAllAuditLogs 跨租户列出全部审计日志（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllAuditLogs(ctx context.Context) ([]AuditLog, error)
}
