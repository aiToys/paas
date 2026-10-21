// Package backup 是数据服务备份管理领域。备份归属某个数据服务资源（dataservice）。
//
// 两种模式（spec 2026-10-17 专项05 §4 两阶段）：
//   - record 记录模式：仅持久化备份意图记录（无产物、无恢复证明），Create 即 completed；
//   - snapshot 快照模式：引擎级真实备份（本期仅 postgres）——K8s Job 执行 pg_dump 落
//     PVC 产物 + sha256 校验和，Pacer 观察推进 accepted→running→completed|failed；
//   - restore 恢复作业：从快照恢复到新建实例（type=restore 语义独立成 Mode）。
package backup

import (
	"context"
	"time"

	"github.com/aitoys/paas/internal/environment"
)

// CreateBackupRequest 只包含用户可写字段；产物与校验证据由执行器生成。
type CreateBackupRequest struct {
	ResourceID string `json:"resourceId"`
	Type       string `json:"type,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

// Backup 是一次备份/恢复记录。
type Backup struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	ResourceID string    `json:"resourceId"` // 所属数据服务 ID
	Type       string    `json:"type"`       // full|incremental（snapshot 细分，本期恒 full）
	Status     string    `json:"status"`     // accepted|running|verifying|completed|failed
	Mode       string    `json:"mode"`       // record|snapshot|restore（''=存量记录，按 record 解释）
	SizeMB     int       `json:"sizeMB"`     // 兼容字段：存量模拟值；snapshot/restore 恒 0（真实值看 sizeBytes）
	CreatedAt  time.Time `json:"createdAt,omitempty"`

	// —— 真实备份（snapshot/restore）专有字段（record 模式全空）——
	Engine      string    `json:"engine,omitempty"`      // postgres（本期唯一支持）
	Provider    string    `json:"provider,omitempty"`    // file（PVC 产物）；''=无产物
	ArtifactRef string    `json:"artifactRef,omitempty"` // file://<pvc名>/<文件名>；restore 时指向源快照
	SizeBytes   int64     `json:"sizeBytes,omitempty"`   // 产物真实字节数
	Checksum    string    `json:"checksum,omitempty"`    // sha256 hex
	VerifiedAt  time.Time `json:"verifiedAt,omitempty"`  // 校验和记录时间（≠可恢复验证通过）
	Error       string    `json:"error,omitempty"`       // 失败原因（截断脱敏）
	// restore 作业关联（Mode=restore）：源快照 + 目标新实例。
	SourceBackupID     string    `json:"sourceBackupId,omitempty"`
	RestoredResourceID string    `json:"restoredResourceId,omitempty"`
	RestoreName        string    `json:"restoreName,omitempty"`
	FinishedAt         time.Time `json:"finishedAt,omitempty"`
}

// 备份类型 / 模式 / 状态。
const (
	TypeFull        = "full"
	TypeIncremental = "incremental"

	ModeRecord   = "record"   // 仅记录（无产物无恢复证明）
	ModeSnapshot = "snapshot" // 引擎级真实快照
	ModeRestore  = "restore"  // 从快照恢复到新实例

	StatusAccepted  = "accepted"  // 已受理（Job 未确认创建）
	StatusRunning   = "running"   // Job 执行中
	StatusVerifying = "verifying" // 产物校验中（保留状态值，当前实现 Job 成功即校验完成）
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Terminal 判断是否终态（Pacer 停止观察）。
func (b Backup) Terminal() bool {
	return b.Status == StatusCompleted || b.Status == StatusFailed
}

// EffectiveMode 返回有效模式：存量记录 mode 为空，按 record（记录语义）解释。
func (b Backup) EffectiveMode() string {
	if b.Mode == "" {
		return ModeRecord
	}
	return b.Mode
}

// Restorable 判断该记录能否作为恢复源（与 restore 端点同一判定，单一真源）：
// 仅「真实快照 + completed + 产物定位匹配 + 校验和合法」可恢复。
// 记录模式（record）无产物无恢复证明；snapshot 的 VerifiedAt 只是校验和记录时间，
// 不等于「恢复演练通过」——本方法只回答「平台是否受理恢复」，不证明恢复成功。
func (b Backup) Restorable() bool {
	return b.EffectiveMode() == ModeSnapshot && b.Status == StatusCompleted &&
		b.ArtifactRef == "file://"+backupsPVCName+"/"+dumpFile(b) && ValidChecksum(b.Checksum)
}

// Repository 是 backup 持久化抽象（租户强制过滤）。
type Repository interface {
	List(ctx context.Context, tenantID, resourceID string) ([]Backup, error)
	Get(ctx context.Context, tenantID, id string) (Backup, error)
	Create(ctx context.Context, b Backup) error
	Delete(ctx context.Context, tenantID, id string) error
	// UpdateProgress 推进真实备份/恢复作业进度（Pacer 调用；status/size/checksum/error/finishedAt）。
	UpdateProgress(ctx context.Context, tenantID, id string, up ProgressUpdate) error
	// ListActive 跨租户列出全部非终态记录（Pacer 恢复扫描；裸查管理面专用）。
	ListActive(ctx context.Context) ([]Backup, error)
	// ListAll 跨租户列出全部备份/恢复记录（admin 平台总览；不过滤 tenant，
	// 返回对象带 TenantID，按创建时间倒序）。
	ListAll(ctx context.Context) ([]Backup, error)
}

// ProgressUpdate 是 Pacer 推进的字段集（零值不更新，指针语义）。
type ProgressUpdate struct {
	Status      *string
	SizeBytes   *int64
	Checksum    *string
	ArtifactRef *string
	Error       *string
	FinishedAt  *time.Time
	VerifiedAt  *time.Time
}

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验（依赖倒置，
// 由 environment.Repository 实现）。
type EnvTypeResolver = environment.EnvTypeResolver

// ResourceEnvResolver 解析数据服务资源所属环境 ID（依赖倒置，由 dataservice store 实现）。
// backup 经 ResourceID → envID → EnvType 链路判定生产写权限。
type ResourceEnvResolver interface {
	ResourceEnv(ctx context.Context, resourceID string) (string, error)
}

// PermProdWrite 生产环境写操作额外权限；developer 无此权限 -> 生产只读。
const PermProdWrite = "prod:write"
