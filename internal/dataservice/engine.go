package dataservice

import "context"

// Engine 模式（决定实例形态与连接来源）。
const (
	EngineModeManaged           = "managed"            // 平台拉起独占实例（轻量引擎）
	EngineModeExternalShared    = "external-shared"    // admin 配共享集群连接，用户复用（多租户共享集群）
	EngineModeExternalDedicated = "external-dedicated" // 用户自填连接（独占外部实例）
)

var validEngineModes = map[string]struct{}{
	EngineModeManaged: {}, EngineModeExternalShared: {}, EngineModeExternalDedicated: {},
}

// Engine 是平台级「引擎目录」实体：admin 在后台配置，决定用户能创建哪些数据服务实例、
// 以何种模式（平台托管 / 共享集群 / 用户自填）。平台级（全租户共享），仅 super_admin 可写。
//
// 用户创建 DataService 时从 enabled Engine 列表选一个（不再自由组合 kind+engine），
// kind/engine/mode/connection 由 Engine 决定：
//   - managed：平台拉起，平台生成凭证（DataService.Create 调 FillConnection）。
//   - external-shared：DataService.Connection 复制 Engine.Connection（共享集群）+ 用户 logical name。
//   - external-dedicated：用户填自己的连接。
type Engine struct {
	ID          string            `json:"id"`     // milvus-shared / pg-managed / qdrant-managed
	Kind        string            `json:"kind"`   // db|cache|mq|storage|vector|search
	Engine      string            `json:"engine"` // postgres|milvus|qdrant|...
	Label       string            `json:"label"`  // 展示名「共享 Milvus 集群（生产）」
	Description string            `json:"description,omitempty"`
	Mode        string            `json:"mode"`                 // managed | external-shared | external-dedicated
	Enabled     bool              `json:"enabled"`              // 对用户可见开关（false=仅 admin 可见）
	Connection  map[string]string `json:"connection,omitempty"` // external-shared：共享集群连接（admin 配）
	Order       int               `json:"order,omitempty"`      // 展示排序（小→大）
}

// EngineRepository 是 Engine 持久化接口（平台级，无租户隔离）。
type EngineRepository interface {
	ListEngines(ctx context.Context) ([]Engine, error)
	GetEngine(ctx context.Context, id string) (Engine, error)
	CreateEngine(ctx context.Context, e Engine) (Engine, error)
	UpdateEngine(ctx context.Context, e Engine) (Engine, error)
	DeleteEngine(ctx context.Context, id string) error
	EnginesCount(ctx context.Context) (int, error)
}

// Validate 校验 Engine 字段：kind/engine/mode 合法；managed 模式 engine 必须在白名单；
// external-shared 模式必须配 Connection（host 非空）。
func (e Engine) Validate() error {
	if _, ok := validKinds[e.Kind]; !ok {
		return errInvalid("kind")
	}
	if e.Engine == "" {
		return errInvalid("engine")
	}
	if e.Label == "" {
		return errInvalid("label")
	}
	if _, ok := validEngineModes[e.Mode]; !ok {
		return errInvalid("mode")
	}
	// managed 模式：engine 必须可拉起（轻量白名单）。
	if e.Mode == EngineModeManaged && !IsManagedEngine(e.Kind, e.Engine) {
		return fieldErr{field: "engine", msg: "managed 模式仅支持轻量引擎，重型请用 external-shared"}
	}
	// external-shared + 已启用：必须配共享集群连接（host 非空）。
	// disabled 占位允许空连接（admin 配好连接再启用；DefaultEngines 的重型引擎占位即此场景）。
	if e.Mode == EngineModeExternalShared && e.Enabled && (e.Connection == nil || e.Connection["host"] == "") {
		return fieldErr{field: "connection", msg: "external-shared 启用前必须配置共享集群 host"}
	}
	return nil
}

// DefaultEngines 是 seed 默认引擎目录（admin 可改）：6 类 managed 轻量引擎（enabled）+
// 重型引擎 external-shared 占位（disabled，admin 配连接后启用）。导出供 memory/pg seed 共用（DRY）。
func DefaultEngines() []Engine {
	mk := func(id, kind, eng, label, mode string, enabled bool, order int) Engine {
		return Engine{ID: id, Kind: kind, Engine: eng, Label: label, Mode: mode, Enabled: enabled, Order: order}
	}
	return []Engine{
		// managed 轻量引擎（默认开放，平台拉起独占实例）
		mk("pg-managed", KindDB, "postgres", "PostgreSQL（平台托管）", EngineModeManaged, true, 10),
		mk("mysql-managed", KindDB, "mysql", "MySQL（平台托管）", EngineModeManaged, true, 11),
		mk("redis-managed", KindCache, "redis", "Redis（平台托管）", EngineModeManaged, true, 20),
		mk("valkey-managed", KindCache, "valkey", "Valkey（平台托管）", EngineModeManaged, true, 21),
		mk("nats-managed", KindMQ, "nats", "NATS（平台托管）", EngineModeManaged, true, 30),
		mk("minio-managed", KindStorage, "minio", "MinIO（平台托管）", EngineModeManaged, true, 40),
		mk("qdrant-managed", KindVector, "qdrant", "Qdrant（平台托管）", EngineModeManaged, true, 50),
		mk("meilisearch-managed", KindSearch, "meilisearch", "Meilisearch（平台托管）", EngineModeManaged, true, 60),
		// 重型引擎 external-shared 占位（disabled，admin 配共享集群连接后启用）
		mk("milvus-shared", KindVector, "milvus", "Milvus 共享集群", EngineModeExternalShared, false, 51),
		mk("es-shared", KindSearch, "elasticsearch", "Elasticsearch 共享集群", EngineModeExternalShared, false, 61),
		mk("kafka-shared", KindMQ, "kafka", "Kafka 共享集群", EngineModeExternalShared, false, 31),
	}
}
