// Package pg 提供 application.Repository 的 PostgreSQL 实现。
// applications + application_bindings 两表；ResourceCount 读时由 Bindings Recount 派生。
// 显式 WHERE tenant_id=$1 过滤；Create 以 ctx 租户为准忽略请求体（防越权写）。
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 application.Repository 的 PostgreSQL 实现。
type Store struct {
	db *pg.DB
}

// NewStore 创建 application PG 仓储。db 必须已完成迁移。
func NewStore(db *pg.DB) *Store { return &Store{db: db} }

// List 列出当前租户的全部应用（含绑定项），按 id 排序。
func (s *Store) List(ctx context.Context) ([]application.Application, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx, appSelect+` WHERE tenant_id=$1 ORDER BY id`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	apps := map[string]*application.Application{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps[a.ID] = a
		ids = append(ids, a.ID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = s.attachBindings(ctx, ids, apps); err != nil {
		return nil, err
	}
	out := make([]application.Application, 0, len(ids))
	for _, id := range ids {
		out = append(out, *apps[id])
	}
	return out, nil
}

// Get 取单个应用（含绑定项）。跨租户访问返回 not found（不泄漏存在性）。
func (s *Store) Get(ctx context.Context, id string) (application.Application, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return application.Application{}, err
	}
	row := s.db.Pool().QueryRow(ctx, appSelect+` WHERE id=$1 AND tenant_id=$2`, id, tid)
	a, err := scanApp(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.Application{}, fmt.Errorf("应用不存在: %s", id)
		}
		return application.Application{}, err
	}
	if err = s.attachBindings(ctx, []string{a.ID}, map[string]*application.Application{a.ID: a}); err != nil {
		return application.Application{}, err
	}
	return *a, nil
}

// Create 写入应用（含绑定项）。以 ctx 租户为准、忽略请求体 TenantID。
func (s *Store) Create(ctx context.Context, a application.Application) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	a.TenantID = tid
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx, appInsert, a.ID, a.TenantID, a.Name, a.Initial, a.Env,
		a.Status, a.Gradient, a.Desc, a.Replicas, a.RPS); err != nil {
		if pg.IsUniqueViolation(err) {
			return fmt.Errorf("应用%w", pg.ErrAlreadyExists)
		}
		return err
	}
	for i, b := range a.Bindings {
		if _, err = tx.Exec(ctx, bindingInsert, a.ID, i, b.Type, b.Name, b.Note); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// BindResource 给应用追加一个绑定项（同 type+name 视为重复，拒绝）。
func (s *Store) BindResource(ctx context.Context, id, resourceType, name string) (application.Application, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return application.Application{}, err
	}
	// 先校验应用归属当前租户（跨租户 not found）。
	if _, err = s.Get(ctx, id); err != nil {
		return application.Application{}, err
	}
	// 取下一个 ord（追加到末尾）+ INSERT 同一事务，并用 advisory lock 串行化同 app 的并发绑定，
	// 防止「SELECT MAX(ord) 与 INSERT 间另一并发也读到相同 nextOrd」导致 ord 重复（列表顺序不确定）。
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return application.Application{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, id); err != nil {
		return application.Application{}, err
	}
	var nextOrd int
	if err = tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(ord), -1) + 1 FROM application_bindings WHERE app_id=$1`, id).Scan(&nextOrd); err != nil {
		return application.Application{}, err
	}
	if _, err = tx.Exec(ctx, bindingInsert, id, nextOrd, resourceType, name, ""); err != nil {
		if pg.IsUniqueViolation(err) {
			return application.Application{}, fmt.Errorf("绑定已存在: %s/%s", resourceType, name)
		}
		return application.Application{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return application.Application{}, err
	}
	_ = tid // tid 已通过 Get 的租户过滤保证归属
	return s.Get(ctx, id)
}

// Unbind 移除应用的某个绑定项。
func (s *Store) Unbind(ctx context.Context, id, resourceType, name string) (application.Application, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return application.Application{}, err
	}
	_, err := s.db.Pool().Exec(ctx,
		`DELETE FROM application_bindings WHERE app_id=$1 AND type=$2 AND name=$3`, id, resourceType, name)
	if err != nil {
		return application.Application{}, err
	}
	return s.Get(ctx, id)
}

// AppsCount 返回当前租户应用数，供 seed 判空。
func (s *Store) AppsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM applications`).Scan(&n)
	return n, err
}

// —— 扫描 / 聚合辅助 ——

const appSelect = `SELECT id, tenant_id, name, initial, env, status, gradient, "desc", replicas, rps FROM applications`

const appInsert = `INSERT INTO applications
(id, tenant_id, name, initial, env, status, gradient, "desc", replicas, rps)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

const bindingInsert = `INSERT INTO application_bindings (app_id, ord, type, name, note) VALUES ($1, $2, $3, $4, $5)`

// scanApp 通过 storagepg.RowScanner 抽象 QueryRow 与 Row 两种 Scan 来源。
func scanApp(r pg.RowScanner) (*application.Application, error) {
	a := &application.Application{}
	if err := r.Scan(&a.ID, &a.TenantID, &a.Name, &a.Initial, &a.Env,
		&a.Status, &a.Gradient, &a.Desc, &a.Replicas, &a.RPS); err != nil {
		return nil, err
	}
	return a, nil
}

// attachBindings 批量取绑定项并挂到对应应用，再 Recount 派生计数。
func (s *Store) attachBindings(ctx context.Context, ids []string, apps map[string]*application.Application) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT app_id, type, name, note FROM application_bindings
		 WHERE app_id = ANY($1) ORDER BY app_id, ord`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var appID, bType, bName, bNote string
		if err := rows.Scan(&appID, &bType, &bName, &bNote); err != nil {
			return err
		}
		if a, ok := apps[appID]; ok {
			a.Bindings = append(a.Bindings, application.Binding{Type: bType, Name: bName, Note: bNote})
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, a := range apps {
		a.Recount()
	}
	return nil
}
