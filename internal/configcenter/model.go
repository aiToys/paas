// Package configcenter 是服务治理「配置中心」领域模型（平台能力横切）。
//
// 与 appconfig（工作负载级、静态、重启注入）正交：配置中心是运行时动态配置，
// 跨实例共享，版本/发布/回滚，客户端按版本拉取热更新。
//
// 配置中心独立于物理环境（Namespace 是逻辑隔离单元，不绑定 prod/test），
// 因此权限统一用 governance:read/write，不接入 prod:write。
//
// 本期进程内 mock：发布生成不可变快照；客户端主动拉 version 比对（不做长连接监听）。
package configcenter

import "time"

// 配置项值类型。
const (
	TypeText = "text"
	TypeJSON = "json"
	TypeYAML = "yaml"
)

var validTypes = map[string]struct{}{
	TypeText: {},
	TypeJSON: {},
	TypeYAML: {},
}

// 发布状态。
const (
	StatusActive     = "active"      // 当前生效版本
	StatusRolledBack = "rolled-back" // 已被新版本替代或回滚后非 active
)

// Namespace 是配置的逻辑隔离单元（租户内唯一名，不绑定物理环境）。
type Namespace struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"`   // ctx 写入，请求体忽略
	Name      string    `json:"name"`                 // 租户内唯一
	ServiceID string    `json:"serviceId,omitempty"`  // 关联 governance Service（可选，空=不关联）；双向显示用
	Desc      string    `json:"desc,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate 校验命名空间：name 非空。
func (n Namespace) Validate() error {
	if n.Name == "" {
		return errInvalid("name")
	}
	return nil
}

// ConfigItem 是 namespace 下的一个配置项（draft，可编辑；发布时进入快照）。
type ConfigItem struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	NamespaceID string    `json:"namespaceId"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Type        string    `json:"type"` // text | json | yaml
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Validate 校验配置项：namespaceID/key 非空、type 合法。
func (c ConfigItem) Validate() error {
	if c.NamespaceID == "" {
		return errInvalid("namespaceId")
	}
	if c.Key == "" {
		return errInvalid("key")
	}
	if c.Type != "" {
		if _, ok := validTypes[c.Type]; !ok {
			return errInvalid("type")
		}
	}
	return nil
}

// Publish 是 namespace 配置的一个不可变版本快照。
type Publish struct {
	ID          string            `json:"id"`
	TenantID    string            `json:"tenantId,omitempty"`
	NamespaceID string            `json:"namespaceId"`
	Version     int               `json:"version"`  // namespace 内单调递增
	Snapshot    map[string]string `json:"snapshot"` // key -> value 不可变快照
	Status      string            `json:"status"`   // active | rolled-back
	CreatedAt   time.Time         `json:"createdAt"`
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
