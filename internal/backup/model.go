// Package backup 是数据服务备份管理领域。备份归属某个数据服务资源（dataservice）。
// 本期进程内 mock（Create 即 completed，不接真实快照/对象存储）。
package backup

import (
	"context"
	"time"

	"github.com/aitoys/paas/internal/environment"
)

// Backup 是一次备份记录。
type Backup struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	ResourceID string    `json:"resourceId"` // 所属数据服务 ID
	Type       string    `json:"type"`       // full|incremental
	Status     string    `json:"status"`     // completed|failed|running
	SizeMB     int       `json:"sizeMB"`
	CreatedAt  time.Time `json:"createdAt,omitempty"`
}

// 备份类型 / 状态。
const (
	TypeFull        = "full"
	TypeIncremental = "incremental"
	StatusCompleted = "completed"
	StatusRunning   = "running"
	StatusFailed    = "failed"
)

// Repository 是 backup 持久化抽象（租户强制过滤）。
type Repository interface {
	List(ctx context.Context, tenantID, resourceID string) ([]Backup, error)
	Get(ctx context.Context, tenantID, id string) (Backup, error)
	Create(ctx context.Context, b Backup) error
	Delete(ctx context.Context, tenantID, id string) error
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
