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

// Namespace scope：app（应用派生，EnsureByApp 懒建）| shared（跨应用共享，治理方手工建）。
const (
	ScopeApp    = "app"
	ScopeShared = "shared"
)

// Namespace 是配置的逻辑隔离单元（租户内唯一名，不绑定物理环境）。
// EnvID 是应用 scope 的环境维度（scope=app 时按 (appID, envID) 懒建，env 空=全环境基线；
// shared scope 恒为空）。
type Namespace struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"`  // ctx 写入，请求体忽略
	Name      string    `json:"name"`                // 租户内唯一
	Scope     string    `json:"scope"`               // app | shared（存量迁移为 shared）
	AppID     string    `json:"appId,omitempty"`     // scope=app 时归属应用
	EnvID     string    `json:"envId,omitempty"`     // scope=app 时的环境维度（空=基线，兼容存量）
	ServiceID string    `json:"serviceId,omitempty"` // 关联 governance Service（可选，空=不关联）；双向显示用
	Desc      string    `json:"desc,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// LaneOverride 是泳道维度的配置 key 级覆盖（无版本链，即时生效；泳道回收时级联清理）。
// 发现时在 env 基线快照之上做两层 merge（Task 2 的 mergeSnapshot）。
type LaneOverride struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID     string    `json:"appId"`
	EnvID     string    `json:"envId,omitempty"` // 空=全环境基线
	LaneID    string    `json:"laneId"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate 校验泳道覆盖：appID/laneID/key 非空（EnvID 允许空=基线）。
func (o LaneOverride) Validate() error {
	if o.AppID == "" {
		return errInvalid("appId")
	}
	if o.LaneID == "" {
		return errInvalid("laneId")
	}
	if o.Key == "" {
		return errInvalid("key")
	}
	return nil
}

// NSRef 共享配置引用：应用派生 ns（引用方）→ shared ns（被引用方）。
// 发现时 shared 快照作为三层 merge 的基础层（shared → app×env 基线 → lane 覆盖，
// 右者胜——应用自身 key 压制 shared 默认值是逃生门）。
type NSRef struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppNSID    string    `json:"appNsId"`           // 应用派生 ns（引用方；各 env 独立引用）
	SharedNSID string    `json:"sharedNsId"`        // shared ns（被引用方）
	CreatedAt  time.Time `json:"createdAt"`
}

// AppNSName 派生 (app, env) 维度应用命名空间名：env 空 = app-<appID>（兼容存量），
// 非空 = app-<appID>-<envID>。memory/pg 两实现共用（DRY 单一真源）。
func AppNSName(appID, envID string) string {
	if envID == "" {
		return "app-" + appID
	}
	return "app-" + appID + "-" + envID
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
