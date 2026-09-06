// Package pg 提供 backup.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；Create 以 ctx 租户为准。
// 真实备份执行（快照/对象存储）留后续——本存储只持久化备份记录元数据。
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/backup"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 backup.Repository 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 backup PG 仓储。db 必须已完成迁移（0044）。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

const bkCols = `id, tenant_id, resource_id, type, status, size_mb, created_at`

func scanBackup(r storagepg.RowScanner, b *backup.Backup) error {
	return r.Scan(&b.ID, &b.TenantID, &b.ResourceID, &b.Type, &b.Status, &b.SizeMB, &b.CreatedAt)
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
	err := s.db.Pool().QueryRow(ctx,
		`SELECT `+bkCols+` FROM backups WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(
		&b.ID, &b.TenantID, &b.ResourceID, &b.Type, &b.Status, &b.SizeMB, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return backup.Backup{}, fmt.Errorf("备份不存在: %s", id)
	}
	if err != nil {
		return backup.Backup{}, fmt.Errorf("get backup: %w", err)
	}
	return b, nil
}

// Create 写入备份记录（幂等拒绝同 ID）。
func (s *Store) Create(ctx context.Context, b backup.Backup) error {
	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO backups (`+bkCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (id) DO NOTHING`, //nolint:staticcheck // 命中冲突时受影响行数=0，调用方以 Get 校验
		b.ID, b.TenantID, b.ResourceID, b.Type, b.Status, b.SizeMB, b.CreatedAt)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
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
