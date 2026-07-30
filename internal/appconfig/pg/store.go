// Package pg 提供 appconfig.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；
// Upsert 以 ctx 租户为准、忽略请求体 TenantID（防越权写）；
// secret 值后端明文存储，List/Upsert 返回掩码副本（不泄漏长度/内容）。
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/appconfig"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 appconfig.Repository 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 appconfig PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// cfgCols 与 model.ConfigItem 字段顺序对齐（scan 列顺序必须一致）。
const cfgCols = `id, tenant_id, app_id, env_id, key, value, type, updated_at`

// scanCfg 通过 storagepg.RowScanner 抽象 QueryRow 与 Row 两种 Scan 来源。
func scanCfg(r storagepg.RowScanner, c *appconfig.ConfigItem) error {
	return r.Scan(&c.ID, &c.TenantID, &c.AppID, &c.EnvID, &c.Key, &c.Value, &c.Type, &c.UpdatedAt)
}

// List 按 (appID, envID) 过滤当前租户的配置项；空串表示该维度不限。
// Secret 值掩码返回（与内存一致，不泄漏）。按 key 升序。
func (s *Store) List(ctx context.Context, appID, envID string) ([]appconfig.ConfigItem, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + cfgCols + ` FROM app_configs WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(" AND app_id=$%d", len(args))
	}
	if envID != "" {
		args = append(args, envID)
		q += fmt.Sprintf(" AND env_id=$%d", len(args))
	}
	q += " ORDER BY key"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]appconfig.ConfigItem, 0)
	for rows.Next() {
		var c appconfig.ConfigItem
		if err = scanCfg(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c.Masked()) // Secret 掩码
	}
	return out, rows.Err()
}

// Upsert 新增或更新：同 (tenant, app, env, key) 则更新 value/type/updated_at，否则插入。
// 以 ctx 租户写 tenant_id（覆盖请求体）。返回掩码副本（secret 不泄漏）。
func (s *Store) Upsert(ctx context.Context, item appconfig.ConfigItem) (appconfig.ConfigItem, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return appconfig.ConfigItem{}, err
	}
	if err := item.Validate(); err != nil {
		return appconfig.ConfigItem{}, err
	}
	item.TenantID = tid
	// ON CONFLICT 主路径：命中唯一键 (tenant_id, app_id, env_id, key) 则更新 value/type/updated_at，
	// RETURNING 取实际落库行（含生成的 id 与 updated_at）。
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO app_configs (id, tenant_id, app_id, env_id, key, value, type, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (tenant_id, app_id, env_id, key) DO UPDATE
    SET value = EXCLUDED.value,
        type = EXCLUDED.type,
        updated_at = EXCLUDED.updated_at
RETURNING `+cfgCols,
		item.ID, item.TenantID, item.AppID, item.EnvID, item.Key, item.Value, item.Type, item.UpdatedAt,
	)
	var saved appconfig.ConfigItem
	if err = scanCfg(row, &saved); err != nil {
		return appconfig.ConfigItem{}, err
	}
	return saved.Masked(), nil
}

// Delete 删除指定配置项。跨租户访问 RowsAffected==0 → not found（不泄漏存在性）。
func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM app_configs WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("配置不存在: %s", id)
	}
	return nil
}

// ConfigsCount 返回全表配置项数，供 PG seed 判空（表空才灌，幂等）。
// 注意：不经租户过滤，仅用于启动期 seed 判空，不暴露给业务层。
func (s *Store) ConfigsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM app_configs`).Scan(&n)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	return n, nil
}
