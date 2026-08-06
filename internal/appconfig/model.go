// Package appconfig 是应用配置领域模型：应用在某环境的 env/Secret 键值（工作负载级静态配置）。
//
// 与服务治理的「配置中心」严格区分：
//   - 应用配置（本包）：工作负载级、静态、改了重启注入；env/Secret 键值
//   - 配置中心（服务治理）：运行时动态、跨实例、版本灰度/回滚
//
// Secret 值后端明文存储，API 返回掩码（不泄漏长度/内容）。mock 期不真注入工作负载，
// 接口为未来接 K8s ConfigMap/Secret 铺路。
package appconfig

import "time"

// 配置项类型。
const (
	TypeEnv    = "env"    // 明文环境变量
	TypeSecret = "secret" // 敏感值，API 掩码返回
)

// DefaultEnv 是跨环境共享配置的桶名（平台级资源凭证注入用，如模型推理 LLM Key）。
// WorkloadReconciler 读 appconfig 时聚合 {工作负载 EnvID} + DefaultEnv。
const DefaultEnv = "default"

// SecretMask 是 Secret 值的固定掩码（不泄漏长度/内容）。
const SecretMask = "••••••" //nolint:gosec // G101 误报：这是固定掩码占位符，非凭据

// ConfigItem 是应用在某环境的单个配置项。
type ConfigItem struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID     string    `json:"appId"`
	EnvID     string    `json:"envId"`
	Key       string    `json:"key"`
	Value     string    `json:"value"` // env 明文；secret 存储 明文，API 返回掩码
	Type      string    `json:"type"`  // env | secret
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate 校验配置项：key 非空、type 合法。
func (c ConfigItem) Validate() error {
	if c.Key == "" {
		return errInvalid("key")
	}
	if c.Type != TypeEnv && c.Type != TypeSecret {
		return errInvalid("type")
	}
	return nil
}

// Masked 返回掩码后的副本（secret 值替换为 SecretMask）。Repository.List/返回用。
func (c ConfigItem) Masked() ConfigItem {
	if c.Type == TypeSecret {
		c.Value = SecretMask
	}
	return c
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
