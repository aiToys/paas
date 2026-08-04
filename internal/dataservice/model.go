// Package dataservice 是数据服务资源领域模型（资源中心）：用一个通用领域 + Kind
// 区分覆盖 6 种同构数据服务（DB/缓存/MQ/对象存储/向量库/搜索引擎）。
//
// 租户私有；绑定物理环境（EnvID），生产写操作需 prod:write（EnvTypeResolver 依赖倒置）。
// 本期独立资源 CRUD（Create 即 running，KISS）；应用 Add-on 绑定、真实引擎纳管留后续。
package dataservice

import "time"

// 资源 Kind。
const (
	KindDB      = "db"
	KindCache   = "cache"
	KindMQ      = "mq"
	KindStorage = "storage"
	KindVector  = "vector"
	KindSearch  = "search"
)

var validKinds = map[string]struct{}{
	KindDB: {}, KindCache: {}, KindMQ: {}, KindStorage: {}, KindVector: {}, KindSearch: {},
}

// 资源状态。
const (
	StatusCreating = "creating"
	StatusRunning  = "running"
	StatusStopped  = "stopped"
)

var validStatus = map[string]struct{}{
	StatusCreating: {}, StatusRunning: {}, StatusStopped: {},
}

// FieldType 表单字段类型（前端渲染用）。
const (
	FieldText   = "text"
	FieldSelect = "select"
)

// SpecField 描述一个 spec 字段的表单元数据。
type SpecField struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`    // text | select
	Options []string `json:"options"` // type=select 时的可选项
	Default string   `json:"default"`
}

// KindMeta 描述某 Kind 的展示与表单元数据（导出供前端对齐）。
type KindMeta struct {
	Kind   string      `json:"kind"`
	Label  string      `json:"label"`
	Icon   string      `json:"icon"` // 前端图标名
	Fields []SpecField `json:"fields"`
}

// KindMetas 是全部 Kind 的元数据（权威定义，前端经 /api/dataservices/meta 拉取）。
var KindMetas = []KindMeta{
	{Kind: KindDB, Label: "数据库", Icon: "database", Fields: []SpecField{
		{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"postgres", "mysql"}, Default: "postgres"},
		{Key: "version", Label: "版本", Type: FieldText, Default: "15"},
		{Key: "size_gb", Label: "容量(GB)", Type: FieldText, Default: "50"},
	}},
	{Kind: KindCache, Label: "缓存", Icon: "zap", Fields: []SpecField{
		{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"redis", "valkey"}, Default: "redis"},
		{Key: "mode", Label: "模式", Type: FieldSelect, Options: []string{"standalone", "cluster"}, Default: "standalone"},
		{Key: "maxmemory_mb", Label: "内存上限(MB)", Type: FieldText, Default: "1024"},
	}},
	{Kind: KindMQ, Label: "消息队列", Icon: "message", Fields: []SpecField{
		{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"nats", "kafka", "rabbitmq", "rocketmq"}, Default: "nats"},
		{Key: "partitions", Label: "分区数", Type: FieldText, Default: "3"},
	}},
	{Kind: KindStorage, Label: "对象存储", Icon: "storage", Fields: []SpecField{
		{Key: "bucket", Label: "Bucket", Type: FieldText, Default: ""},
		{Key: "redundancy", Label: "冗余", Type: FieldSelect, Options: []string{"standard", "ia"}, Default: "standard"},
	}},
	{Kind: KindVector, Label: "向量数据库", Icon: "layers", Fields: []SpecField{
		{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"milvus", "qdrant"}, Default: "milvus"},
		{Key: "dimension", Label: "维度", Type: FieldText, Default: "1536"},
	}},
	{Kind: KindSearch, Label: "搜索引擎", Icon: "search", Fields: []SpecField{
		{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"elasticsearch", "opensearch"}, Default: "elasticsearch"},
		{Key: "shards", Label: "分片数", Type: FieldText, Default: "2"},
	}},
}

// KindLabel 返回 Kind 的中文标签（未知名回退原值）。
func KindLabel(kind string) string {
	for _, m := range KindMetas {
		if m.Kind == kind {
			return m.Label
		}
	}
	return kind
}

// KindFields 返回 Kind 的 spec 字段定义。
func KindFields(kind string) []SpecField {
	for _, m := range KindMetas {
		if m.Kind == kind {
			return m.Fields
		}
	}
	return nil
}

// DefaultSpec 按 KindMeta 的 Default 生成默认 spec。
func DefaultSpec(kind string) map[string]string {
	spec := map[string]string{}
	for _, f := range KindFields(kind) {
		spec[f.Key] = f.Default
	}
	return spec
}

// DataService 是一个数据服务资源实例。
type DataService struct {
	ID       string            `json:"id"`
	TenantID string            `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	Kind     string            `json:"kind"`
	Name     string            `json:"name"` // 租户内唯一
	Spec     map[string]string `json:"spec"`
	Status   string            `json:"status"` // creating | running | stopped
	EnvID    string            `json:"envId"`
	AppID    string            `json:"appId,omitempty"` // 可选预留（Add-on 绑定）
	// Connection 平台生成（host/port/credentials/uri），Create 时由 FillConnection 填充。
	// credentials（password/token/secretKey）持久化（重启不变，Secret 引用）；host/port/uri 是纯函数派生。
	// 所有对外端点（list/detail/create/update/跨租户 admin 总览）一律 MaskConnection 掩码返回；
	// 明文仅内部应用绑定注入用（repo.Get 取原始值），任何 HTTP 响应绝不泄漏明文。
	Connection map[string]string `json:"connection,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// NamespaceResolver 提供 K8s namespace（控制面生成 FQDN 用）。由 cmd/core 注入 PAAS_K8S_NAMESPACE。
// 未注入时 store 兜底用 DefaultNamespace（connection.go）。
type NamespaceResolver interface {
	Namespace() string
}

// EngineOf 返回 ds 的引擎（spec.engine 非空用之，否则按 Kind 默认，与 KindMetas Default 对齐）。
// 供 FillConnection/BuildConnection 按 engine 区分端口/凭证/uri；applier 复用避免逻辑重复（DRY）。
func EngineOf(d DataService) string {
	if e, ok := d.Spec["engine"]; ok && e != "" {
		return e
	}
	switch d.Kind {
	case KindDB:
		return "postgres"
	case KindCache:
		return "redis"
	case KindMQ:
		return "nats"
	case KindStorage:
		return "minio"
	case KindVector:
		return "milvus"
	case KindSearch:
		return "elasticsearch"
	}
	return ""
}

// FillConnection 为 DataService 生成并填充 Connection（凭证 + host/port/uri）。
// host 用 d.ID（与 K8s Service/CRD 名一致：applier 以 d.ID 作 CRD 名 -> reconciler 建 Service<d.ID>，
// 故 FQDN 必须基于 d.ID，应用才能 DNS 解析到 Service）。已有凭证则保留不重生（幂等：
// 避免重启变密码 -> Secret 不一致），仅按当前 ns+engine 重算 host/port/uri。
func (d *DataService) FillConnection(ns string) {
	engine := EngineOf(*d)
	hasCred := d.Connection != nil &&
		(d.Connection["password"] != "" || d.Connection["token"] != "" || d.Connection["secretKey"] != "")
	if !hasCred {
		d.Connection = BuildConnection(d.ID, d.Kind, engine, ns, d.Spec, GenerateCredentials(d.Kind, engine))
		return
	}
	d.Connection = BuildConnection(d.ID, d.Kind, engine, ns, d.Spec, d.Connection)
}

// Validate 校验 Kind/Name/EnvID/Status/Spec 必填字段。
// EnvID 必填：数据服务绑定物理环境，且 prod:write 校验依赖 EnvID（空 EnvID 会绕过生产保护）。
func (d DataService) Validate() error {
	if _, ok := validKinds[d.Kind]; !ok {
		return errInvalid("kind")
	}
	if d.Name == "" {
		return errInvalid("name")
	}
	if d.EnvID == "" {
		return errInvalid("envId")
	}
	// status 为空时由 store 补默认 running；非空则校验合法。
	if d.Status != "" {
		if _, ok := validStatus[d.Status]; !ok {
			return errInvalid("status")
		}
	}
	// 必填字段：Default 为空的 text 字段视为必填（如 storage.bucket）
	for _, f := range KindFields(d.Kind) {
		if f.Default == "" && d.Spec[f.Key] == "" {
			return errInvalid("spec." + f.Key)
		}
	}
	return nil
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
