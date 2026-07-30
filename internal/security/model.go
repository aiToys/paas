// Package security 是安全领域模型（平台能力横切）：租户级密钥/证书资产 + 审计日志。
//
// 与 appconfig（应用×环境级、工作负载启动注入的 env/Secret）区分：本包的 Secret 是
// 租户级平台资产（DB 密码 / TLS 证书 / 第三方 token），集中管理供应用引用，不绑定具体应用。
//
// Secret 后端明文存储，API 返回固定掩码（与 appconfig 一致，不泄漏长度/内容）。
// 真实加密存储（KMS/Vault）留后续。审计日志只增不删（合规）。
package security

import "time"

// 资产类型。
const (
	TypeSecret      = "secret"      // 通用密钥（密码 / token）
	TypeCertificate = "certificate" // 证书（TLS / CA）
)

var validTypes = map[string]struct{}{
	TypeSecret:      {},
	TypeCertificate: {},
}

// Secret 作用域。
const (
	ScopeTenant   = "tenant"   // 租户私有（默认，TenantID 必填，按租户隔离）
	ScopePlatform = "platform" // 平台级共享（全租户可用，TenantID 空；如第三方供应商凭证）
)

var validScopes = map[string]struct{}{
	ScopeTenant:   {},
	ScopePlatform: {},
}

// SecretMask 是 Secret 值的固定掩码（不泄漏长度/内容）。
const SecretMask = "••••••" //nolint:gosec // G101 误报：固定掩码占位符，非凭据

// 审计动作。
const (
	ActionCreate = "create"
	ActionDelete = "delete"
	ActionUpdate = "update"
)

// 审计资源类型（当前仅 secret，预留扩展）。
const (
	ResourceSecret = "secret"
)

// Secret 是租户级/平台级密钥/证书资产。
type Secret struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略；平台级为空
	Name      string    `json:"name"`               // 租户内唯一（平台级全平台唯一）
	Type      string    `json:"type"`               // secret | certificate
	Scope     string    `json:"scope"`              // tenant（默认）| platform
	Value     string    `json:"value"`              // 明文存储，List/Get 返回掩码
	Desc      string    `json:"desc,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate 校验：name 非空、type 合法、scope 合法（空默认 tenant）。
func (s Secret) Validate() error {
	if s.Name == "" {
		return errInvalid("name")
	}
	if _, ok := validTypes[s.Type]; !ok {
		return errInvalid("type")
	}
	if s.Scope == "" {
		s.Scope = ScopeTenant
	}
	if _, ok := validScopes[s.Scope]; !ok {
		return errInvalid("scope")
	}
	return nil
}

// Masked 返回掩码后的副本（Value 替换为 SecretMask）。Repository.List/Get 返回用。
func (s Secret) Masked() Secret {
	s.Value = SecretMask
	return s
}

// AuditLog 是安全相关写操作的审计记录（只增不删）。
type AuditLog struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenantId,omitempty"`
	Actor        string    `json:"actor"` // 用户 ID，ctx 取
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Detail       string    `json:"detail,omitempty"`
	At           time.Time `json:"at"`
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
