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

// 资源来源（部署模式）。
const (
	// SourceManaged 平台托管：平台拉起 Pod（轻量引擎），生成凭证，reconciler 建 STS。
	// 仅支持轻量引擎（IsManagedEngine=true）；重型引擎选 managed 在 Validate 期拒绝。
	SourceManaged = "managed"
	// SourceExternalShared 共享集群：admin 在 Engine 配置共享集群连接，用户创建实例时复用
	// 该连接（+ 自填逻辑单元名如 collection/database），平台不部署。典型：一个 milvus 集群多团队共享。
	SourceExternalShared = "external-shared"
	// SourceExternalDedicated 独占外部实例：用户自填连接信息（host/port/credentials），平台不部署。
	SourceExternalDedicated = "external-dedicated"
)

var validSources = map[string]struct{}{
	SourceManaged: {}, SourceExternalShared: {}, SourceExternalDedicated: {},
}

// IsExternal 判断 source 是否为外部模式（不部署，仅控制面记录连接 + 应用绑定注入）。
// external-shared（共享集群）与 external-dedicated（用户自填）都不走 reconciler 拉起。
func IsExternal(source string) bool {
	return source == SourceExternalShared || source == SourceExternalDedicated
}

// managedEngines 是平台托管模式支持的引擎白名单（轻量、单容器可拉起）。
// 重型引擎（milvus/es/opensearch/kafka/rabbitmq/rocketmq）不在内 -> 必须用 external 对接已有实例。
var managedEngines = map[string]map[string]bool{
	KindDB:      {"postgres": true, "mysql": true},
	KindCache:   {"redis": true, "valkey": true},
	KindMQ:      {"nats": true},
	KindStorage: {"minio": true},
	KindVector:  {"qdrant": true},
	KindSearch:  {"meilisearch": true},
}

// IsManagedEngine 判断 Kind+Engine 是否支持平台托管（轻量可拉起）。
// external 模式无此限制（任意引擎对接外部实例）。
func IsManagedEngine(kind, engine string) bool {
	return managedEngines[kind][engine]
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
		// 默认 Qdrant（平台托管，轻量单容器）；milvus 重型仅 external 模式对接已有集群。
		{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"qdrant", "milvus"}, Default: "qdrant"},
		{Key: "dimension", Label: "维度", Type: FieldText, Default: "1536"},
	}},
	{Kind: KindSearch, Label: "搜索引擎", Icon: "search", Fields: []SpecField{
		// 默认 Meilisearch（平台托管，轻量）；elasticsearch/opensearch 重型仅 external 模式对接。
		{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"meilisearch", "elasticsearch", "opensearch"}, Default: "meilisearch"},
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
	Source   string            `json:"source"` // managed（平台托管）| external（接入外部实例）；空=managed
	EngineID string            `json:"engineId,omitempty"` // 关联 Engine 目录（kind/engine/mode/connection 由其决定）
	EnvID    string            `json:"envId"`
	AppID    string            `json:"appId,omitempty"` // 可选预留（Add-on 绑定）
	// Connection 平台生成（host/port/credentials/uri），Create 时由 FillConnection 填充。
	// credentials（password/token/secretKey）持久化（重启不变，Secret 引用）；host/port/uri 是纯函数派生。
	// 所有对外端点（list/detail/create/update/跨租户 admin 总览）一律 MaskConnection 掩码返回；
	// 明文仅内部应用绑定注入用（repo.Get 取原始值），任何 HTTP 响应绝不泄漏明文。
	Connection map[string]string `json:"connection,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
	// 实例浅管理字段（与 CRD DataServiceSpec 对齐，applier 投影到 K8s STS）：
	//   - Replicas：nil/0=默认 1 副本；显式 0 = 停（scale 0）。
	//   - CPU/Memory：覆盖默认容器 resources request（K8s 资源量字符串，如 "250m"/"512Mi"）。
	//   - StorageGB：PVC 容量 GiB（0 = 默认 10）；仅扩容。
	//   - Image：覆盖默认镜像（含 tag，版本升级）。
	Replicas  *int   `json:"replicas,omitempty"`
	CPU       string `json:"cpu,omitempty"`
	Memory    string `json:"memory,omitempty"`
	StorageGB int    `json:"storageGb,omitempty"`
	Image     string `json:"image,omitempty"`
}

// NamespaceResolver 提供 K8s namespace（控制面生成 FQDN 用）。按租户派生（paas-<tenant>）。
// 由 cmd/core 注入（包装 tenant.Namespace）；未注入时 store 兜底直接用 tenant.Namespace。
type NamespaceResolver interface {
	Namespace(tid string) string
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
		return "qdrant"
	case KindSearch:
		return "meilisearch"
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
		(d.Connection["password"] != "" || d.Connection["token"] != "" ||
			d.Connection["secretKey"] != "" || d.Connection["api_key"] != "" ||
			d.Connection["master_key"] != "")
	if !hasCred {
		d.Connection = BuildConnection(d.ID, d.Kind, engine, ns, d.Spec, GenerateCredentials(d.Kind, engine))
		return
	}
	d.Connection = BuildConnection(d.ID, d.Kind, engine, ns, d.Spec, d.Connection)
}

// Validate 校验 Kind/Name/EnvID/Status/Source/Spec 必填字段。
// EnvID 必填：数据服务绑定物理环境，且 prod:write 校验依赖 EnvID（空 EnvID 会绕过生产保护）。
// managed 模式：engine 必须在白名单（轻量可拉起），重型引擎（milvus/es/kafka...）拒绝并引导用 external。
// external 模式：engine 任意，跳过 spec 表单必填（用户填 connection，spec 字段如 dimension 非必需）。
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
	// source 空默认 managed；非空校验合法。
	src := d.Source
	if src == "" {
		src = SourceManaged
	}
	if _, ok := validSources[src]; !ok {
		return errInvalid("source")
	}
	// managed 模式：engine 必须可拉起（白名单）；重型 -> 明确错误引导 external。
	if src == SourceManaged && !IsManagedEngine(d.Kind, EngineOf(d)) {
		return fieldErr{field: "engine", msg: "该引擎不支持平台托管（过重），请改用 external 接入已有实例"}
	}
	// spec 必填校验：Default 为空的 text 字段视为必填（如 storage.bucket）。external 模式跳过（用户填 connection）。
	if src == SourceManaged {
		for _, f := range KindFields(d.Kind) {
			if f.Default == "" && d.Spec[f.Key] == "" {
				return errInvalid("spec." + f.Key)
			}
		}
	}
	return nil
}

type fieldErr struct {
	field string
	msg   string
}

func (e fieldErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return "字段非法或缺失: " + e.field
}

func errInvalid(field string) error { return fieldErr{field: field} }
