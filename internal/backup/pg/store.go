// Package pg 提供 backup.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；Create 以 ctx 租户为准。
// 真实快照/恢复作业由 Pacer 推进（UpdateProgress），本存储只持久化记录元数据。
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/backup"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 backup.Repository 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 backup PG 仓储。db 必须已完成迁移（0044/0053）。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

const bkCols = `id, tenant_id, resource_id, type, status, mode, size_mb, created_at,
	engine, provider, artifact_ref, size_bytes, checksum, verified_at, error,
	source_backup_id, restored_resource_id, finished_at, restore_name`

func scanBackup(r storagepg.RowScanner, b *backup.Backup) error {
	// verified_at/finished_at 可空指针接收：存量行（0053 前）为 NULL，scan 进
	// time.Time 会「cannot scan NULL」（governance applied_at 同款坑，2026-10-19）。
	var verifiedAt, finishedAt *time.Time
	if err := r.Scan(&b.ID, &b.TenantID, &b.ResourceID, &b.Type, &b.Status, &b.Mode, &b.SizeMB, &b.CreatedAt,
		&b.Engine, &b.Provider, &b.ArtifactRef, &b.SizeBytes, &b.Checksum, &verifiedAt, &b.Error,
		&b.SourceBackupID, &b.RestoredResourceID, &finishedAt, &b.RestoreName); err != nil {
		return err
	}
	if verifiedAt != nil {
		b.VerifiedAt = *verifiedAt
	}
	if finishedAt != nil {
		b.FinishedAt = *finishedAt
	}
	return nil
}

// List 列出租户备份（resourceID 空 = 全部资源），按创建时间倒序（最新在前）。
func (s *Store) List(ctx context.Context, tenantID, resourceID string) ([]backup.Backup, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+bkCols+` FROM backups WHERE tenant_id=$1 AND ($2='' OR resource_id=$2)
		 ORDER BY created_at DESC, id`, tenantID, resourceID)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close() //nolint:staticcheck // SA3000 defer rows.Close 在 err 判断前已有值
	var out []backup.Backup
	for rows.Next() {
		var b backup.Backup
		if err := scanBackup(rows, &b); err != nil {
			return nil, fmt.Errorf("scan backup: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Get 取单条。不存在/跨租户统一「备份不存在」（不泄漏存在性）。
func (s *Store) Get(ctx context.Context, tenantID, id string) (backup.Backup, error) {
	var b backup.Backup
	var verifiedAt, finishedAt *time.Time
	err := s.db.Pool().QueryRow(ctx,
		`SELECT `+bkCols+` FROM backups WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(
		&b.ID, &b.TenantID, &b.ResourceID, &b.Type, &b.Status, &b.Mode, &b.SizeMB, &b.CreatedAt,
		&b.Engine, &b.Provider, &b.ArtifactRef, &b.SizeBytes, &b.Checksum, &verifiedAt, &b.Error,
		&b.SourceBackupID, &b.RestoredResourceID, &finishedAt, &b.RestoreName)
	if errors.Is(err, pgx.ErrNoRows) {
		return backup.Backup{}, fmt.Errorf("备份不存在: %s", id)
	}
	if err != nil {
		return backup.Backup{}, fmt.Errorf("get backup: %w", err)
	}
	if verifiedAt != nil {
		b.VerifiedAt = *verifiedAt
	}
	if finishedAt != nil {
		b.FinishedAt = *finishedAt
	}
	return b, nil
}

// Create 写入备份记录；同 ID 已存在返「备份已存在」（与内存版同语义，handler 映射 409）。
// ON CONFLICT DO NOTHING 拦截原生 23505 错误（WriteServiceError 会脱敏成 500），
// 再以 RowsAffected 区分（R8 审计 B2：原实现静默返回 nil，重复创建假成功且双实现语义分叉）。
func (s *Store) Create(ctx context.Context, b backup.Backup) error {
	tag, err := s.db.Pool().Exec(ctx,
		`INSERT INTO backups (id, tenant_id, resource_id, type, status, mode, size_mb, created_at,
			engine, provider, artifact_ref, size_bytes, checksum, source_backup_id, restored_resource_id, restore_name)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		 ON CONFLICT (id) DO NOTHING`,
		b.ID, b.TenantID, b.ResourceID, b.Type, b.Status, b.Mode, b.SizeMB, b.CreatedAt,
		b.Engine, b.Provider, b.ArtifactRef, b.SizeBytes, b.Checksum, b.SourceBackupID, b.RestoredResourceID, b.RestoreName)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("备份已存在: %s", b.ID)
	}
	return nil
}

// Delete 删除备份记录。不存在/跨租户统一「备份不存在」。
func (s *Store) Delete(ctx context.Context, tenantID, id string) error {
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM backups WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("备份不存在: %s", id)
	}
	return nil
}

// UpdateProgress 推进真实作业进度（Pacer 调用）。指针字段非 nil 才更新（零值不覆盖）。
func (s *Store) UpdateProgress(ctx context.Context, tenantID, id string, up backup.ProgressUpdate) error {
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE backups SET
		   status = COALESCE($3, status),
		   size_bytes = COALESCE($4, size_bytes),
		   checksum = COALESCE($5, checksum),
		   artifact_ref = COALESCE($6, artifact_ref),
		   verified_at = COALESCE($7, verified_at),
		   error = COALESCE($8, error),
		   finished_at = COALESCE($9, finished_at)
		 WHERE tenant_id=$1 AND id=$2`,
		tenantID, id, up.Status, up.SizeBytes, up.Checksum, up.ArtifactRef, up.VerifiedAt, up.Error, up.FinishedAt)
	if err != nil {
		return fmt.Errorf("update backup progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("备份不存在: %s", id)
	}
	return nil
}

// ListActive 跨租户列出全部非终态记录（Pacer 恢复扫描；管理面专用不带租户过滤）。
func (s *Store) ListActive(ctx context.Context) ([]backup.Backup, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+bkCols+` FROM backups WHERE status IN ('accepted','running','verifying')`)
	if err != nil {
		return nil, fmt.Errorf("list active backups: %w", err)
	}
	defer rows.Close() //nolint:staticcheck // SA3000 defer rows.Close 在 err 判断前已有值
	var out []backup.Backup
	for rows.Next() {
		var b backup.Backup
		if err := scanBackup(rows, &b); err != nil {
			return nil, fmt.Errorf("scan backup: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
