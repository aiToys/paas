// Package pg 提供 environment.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；
// Create 以 ctx 租户为准、忽略请求体 TenantID（防越权写）。
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/environment"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 environment.Repository 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 environment PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// envCols 与 model.Environment 字段顺序对齐（scan 列顺序必须一致）。
const envCols = `id, tenant_id, name, type, cluster, "desc", created_at, promote_order`

// scanEnv 通过 storagepg.RowScanner 抽象 QueryRow 与 Row 两种 Scan 来源。
func scanEnv(r storagepg.RowScanner, e *environment.Environment) error {
	return r.Scan(&e.ID, &e.TenantID, &e.Name, &e.Type, &e.Cluster, &e.Desc, &e.CreatedAt, &e.PromoteOrder)
}

// List 列出当前租户的全部环境，按 id 排序。
func (s *Store) List(ctx context.Context) ([]environment.Environment, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+envCols+` FROM environments WHERE tenant_id=$1 ORDER BY id`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []environment.Environment
	for rows.Next() {
		var e environment.Environment
		if err = scanEnv(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAll 跨租户列出全部环境（admin 平台总览，不过滤 tenant；按 tenant_id, id 排序）。
func (s *Store) ListAll(ctx context.Context) ([]environment.Environment, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+envCols+` FROM environments ORDER BY tenant_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []environment.Environment
	for rows.Next() {
		var e environment.Environment
		if err = scanEnv(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get 取单个环境。跨租户访问返回 not found（不泄漏存在性）。
func (s *Store) Get(ctx context.Context, id string) (environment.Environment, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return environment.Environment{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+envCols+` FROM environments WHERE id=$1 AND tenant_id=$2`, id, tid)
	var e environment.Environment
	if err = scanEnv(row, &e); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return environment.Environment{}, fmt.Errorf("环境不存在: %s", id)
		}
		return environment.Environment{}, err
	}
	return e, nil
}

// Create 写入环境。以 ctx 租户为准、忽略请求体 TenantID。
// 租户内 name 唯一冲突返回「环境已存在」（与内存实现消息一致）。
func (s *Store) Create(ctx context.Context, e environment.Environment) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	e.TenantID = tid
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO environments (`+envCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.ID, e.TenantID, e.Name, e.Type, e.Cluster, e.Desc, e.CreatedAt, e.PromoteOrder)
	if storagepg.IsUniqueViolation(err) {
		return fmt.Errorf("环境已存在: %s", e.ID)
	}
	return err
}

// Delete 删除指定环境。跨租户访问返回 not found（不泄漏）。
func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM environments WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("环境不存在: %s", id)
	}
	return nil
}

// EnvType 返回环境类型（prod|test），供 EnvTypeResolver 实现 prod:write 横切校验。
// 跨租户访问返回 not found（不泄漏）。
func (s *Store) EnvType(ctx context.Context, id string) (string, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return "", err
	}
	var t string
	err = s.db.Pool().QueryRow(ctx,
		`SELECT type FROM environments WHERE id=$1 AND tenant_id=$2`, id, tid).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("环境不存在: %s", id)
	}
	return t, err
}

// NextPromoteTarget 返回同租户内 promote_order 严格大于当前环境的最小阶序环境
// （同 order 取 id 最小，确定性）。当前环境不存在返 not found；已是最高阶序返 ErrNoPromoteTarget。
func (s *Store) NextPromoteTarget(ctx context.Context, envID string) (environment.Environment, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return environment.Environment{}, err
	}
	var curOrder int
	err = s.db.Pool().QueryRow(ctx,
		`SELECT promote_order FROM environments WHERE id=$1 AND tenant_id=$2`, envID, tid).Scan(&curOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return environment.Environment{}, environment.ErrNoPromoteTarget
	}
	if err != nil {
		return environment.Environment{}, err
	}
	var e environment.Environment
	err = s.db.Pool().QueryRow(ctx,
		`SELECT `+envCols+` FROM environments
		 WHERE tenant_id=$1 AND promote_order>$2 AND promote_order>0
		 ORDER BY promote_order ASC, id ASC LIMIT 1`, tid, curOrder).Scan(
		&e.ID, &e.TenantID, &e.Name, &e.Type, &e.Cluster, &e.Desc, &e.CreatedAt, &e.PromoteOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return environment.Environment{}, environment.ErrNoPromoteTarget
	}
	return e, err
}

// EnvsCount 返回全表环境数，供 PG seed 判空（表空才灌，幂等）。
// 注意：不经租户过滤，仅用于启动期 seed 判空，不暴露给业务层。
func (s *Store) EnvsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM environments`).Scan(&n)
	return n, err
}
