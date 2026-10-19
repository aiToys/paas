package backup

import "context"

// Target 是真实备份执行的数据面定位（由 cmd/core 从 dataservice 实体派生，
// 不含凭证明文——Job 经 secretKeyRef 引用 Secret，凭证不落 Job spec）。
type Target struct {
	Namespace   string // paas-<tenant>（Job 与数据服务同 ns，Service 直连短名）
	ServiceName string // K8s Service 名 = ds.ID
	SecretName  string // 凭证 Secret 名 = <ds.ID>-secret（db: user/password/database 三 key）
	Image       string // 与目标数据库使用相同 PostgreSQL 镜像版本
	Engine      string // postgres（本期唯一支持真实快照）
}

// ProbeResult 是 Executor.Probe 的观察结果。
type ProbeResult struct {
	Phase     string // pending（Job 未建/未出结果）| running | succeeded | failed
	SizeBytes int64  // succeeded 时产物真实字节数（从 Job 日志 BK_META 行解析）
	Checksum  string // succeeded 时 sha256 hex
	ErrMsg    string // failed 时原因（截断脱敏）
}

// Executor 启动并观察真实备份/恢复作业（K8s Job）。nil 时 handler 降级拒绝 snapshot 模式。
type Executor interface {
	// Start 创建快照/恢复 Job（幂等：同名 Job 已存在直接成功——Pacer 恢复扫描重入安全）。
	Start(ctx context.Context, b Backup, t Target) error
	// Probe 观察 Job 状态与产物元数据。
	Probe(ctx context.Context, b Backup, t Target) (ProbeResult, error)
}

// TargetResolver 由 resourceID（ds ID）解析数据面定位（cmd/core 桥接 dataservice repo；
// 备份模块不 import dataservice，避免领域耦合）。
type TargetResolver interface {
	ResolveTarget(ctx context.Context, tenantID, resourceID string) (Target, error)
}

// EngineSupported 判定引擎是否支持真实快照（spec 专项05 §4B：不一次承诺六类引擎，
// 首期仅 postgres——pg_dump/pg_restore 自定义格式）。
func EngineSupported(engine string) bool { return engine == "postgres" }
