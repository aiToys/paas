// Package pg 提供 service.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；
// Create/Update 以 ctx 租户为准、忽略请求体 TenantID（防越权写）；
// 跨租户访问统一返回 not found（不泄漏存在性）。
package pg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/service"
	"github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 service.Repository 的 PostgreSQL 实现。
type Store struct {
	db *pg.DB
}

// NewStore 创建 service PG 仓储。db 必须已完成迁移。
func NewStore(db *pg.DB) *Store { return &Store{db: db} }

// svcCols 与 model.Service 字段顺序对齐（scan 列顺序必须一致）。
const svcCols = `id, tenant_id, app_id, name, type, repo_id, repo_path, port, replicas, build_args, env, model_ref, tools, schedule, created_at`

// randID 生成带前缀的短 ID（crypto/rand 8 字节 hex，与 memory 实现同源思路）。
func randID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error()) // 无熵优于弱 ID
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// marshalStrMap 把 map[string]string 序列化为 JSONB 字节；nil/空 -> '{}'（与列语义一致）。
func marshalStrMap(m map[string]string) []byte {
	if len(m) == 0 {
		return []byte(`{}`)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// unmarshalStrMap 反序列化 JSONB 为 map[string]string；nil/空/null/无效 -> 空 map（nil 安全）。
func unmarshalStrMap(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// marshalTools 序列化 []string 为 JSONB 数组；nil/空 -> '[]'。
func marshalTools(t []string) []byte {
	if len(t) == 0 {
		return []byte(`[]`)
	}
	b, err := json.Marshal(t)
	if err != nil {
		return []byte(`[]`)
	}
	return b
}

// unmarshalTools 反序列化 JSONB 数组为 []string；nil/空/null/无效 -> nil（omitempty 友好）。
func unmarshalTools(raw []byte) []string {
	var out []string
	if len(raw) == 0 {
		return nil
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// scanSvc 通过 pg.RowScanner 抽象 QueryRow 与 Row 两种 Scan 来源。
func scanSvc(r pg.RowScanner, s *service.Service) error {
	var buildArgs, env, tools []byte
	if err := r.Scan(
		&s.ID, &s.TenantID, &s.AppID, &s.Name, &s.Type, &s.RepoID, &s.RepoPath,
		&s.Port, &s.Replicas, &buildArgs, &env, &s.ModelRef, &tools, &s.Schedule, &s.CreatedAt,
	); err != nil {
		return err
	}
	s.BuildArgs = unmarshalStrMap(buildArgs)
	s.Env = unmarshalStrMap(env)
	s.Tools = unmarshalTools(tools)
	return nil
}

// List 列出当前租户指定应用的全部服务，按 id 排序。
func (s *Store) List(ctx context.Context, appID string) ([]service.Service, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+svcCols+` FROM services WHERE tenant_id=$1 AND app_id=$2 ORDER BY id`, tid, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.Service, 0)
	for rows.Next() {
		var it service.Service
		if err = scanSvc(rows, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Get 取单个服务。跨租户访问返回 not found（不泄漏存在性）。
func (s *Store) Get(ctx context.Context, appID, id string) (service.Service, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return service.Service{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+svcCols+` FROM services WHERE id=$1 AND tenant_id=$2 AND app_id=$3`, id, tid, appID)
	var out service.Service
	if err = scanSvc(row, &out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return service.Service{}, service.ErrNotFound
		}
		return service.Service{}, err
	}
	return out, nil
}

// Create 写入服务。以 ctx 租户为准、忽略请求体 TenantID；ID 空则生成。
// (tenant, app, name) 唯一冲突返回 ErrExists。
func (s *Store) Create(ctx context.Context, in service.Service) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if in.ID == "" {
		in.ID = randID("svc")
	}
	in.TenantID = tid // 以 ctx 为准
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO services (`+svcCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		in.ID, in.TenantID, in.AppID, in.Name, in.Type, in.RepoID, in.RepoPath,
		in.Port, in.Replicas, marshalStrMap(in.BuildArgs), marshalStrMap(in.Env),
		in.ModelRef, marshalTools(in.Tools), in.Schedule, in.CreatedAt)
	if pg.IsUniqueViolation(err) {
		return service.ErrExists
	}
	return err
}

// Update 更新服务（全字段覆盖）。创建时间不可变；name 撞他人返回 ErrExists；
// 不存在返回 ErrNotFound（跨租户不泄漏）。
func (s *Store) Update(ctx context.Context, in service.Service) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE services SET name=$3, type=$4, repo_id=$5, repo_path=$6, port=$7, replicas=$8,
		 build_args=$9, env=$10, model_ref=$11, tools=$12, schedule=$13
		 WHERE id=$1 AND tenant_id=$2 AND app_id=$14`,
		in.ID, tid, in.Name, in.Type, in.RepoID, in.RepoPath,
		in.Port, in.Replicas, marshalStrMap(in.BuildArgs), marshalStrMap(in.Env),
		in.ModelRef, marshalTools(in.Tools), in.Schedule, in.AppID)
	if err != nil {
		if pg.IsUniqueViolation(err) {
			return service.ErrExists
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return service.ErrNotFound
	}
	return nil
}

// Delete 删除服务。跨租户访问返回 not found（不泄漏）。
func (s *Store) Delete(ctx context.Context, appID, id string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM services WHERE id=$1 AND tenant_id=$2 AND app_id=$3`, id, tid, appID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return service.ErrNotFound
	}
	return nil
}

// GetOrCreateByName 按 (app, name) 取，无则建（幂等，供存量回填）。fill 可为 nil。
// 并发下唯一索引兜底：冲突（他人先建）时重查返回已有记录。
func (s *Store) GetOrCreateByName(ctx context.Context, appID, name, typ string, fill func(*service.Service)) (service.Service, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return service.Service{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+svcCols+` FROM services WHERE tenant_id=$1 AND app_id=$2 AND name=$3`, tid, appID, name)
	var out service.Service
	if err = scanSvc(row, &out); err == nil {
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return service.Service{}, err
	}
	ns := service.Service{ID: randID("svc"), AppID: appID, Name: name, Type: typ}
	if fill != nil {
		fill(&ns)
	}
	if err := ns.Validate(); err != nil {
		return service.Service{}, err
	}
	ns.TenantID = tid
	ns.CreatedAt = time.Now()
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO services (`+svcCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		ns.ID, ns.TenantID, ns.AppID, ns.Name, ns.Type, ns.RepoID, ns.RepoPath,
		ns.Port, ns.Replicas, marshalStrMap(ns.BuildArgs), marshalStrMap(ns.Env),
		ns.ModelRef, marshalTools(ns.Tools), ns.Schedule, ns.CreatedAt)
	if err != nil {
		if pg.IsUniqueViolation(err) {
			// 并发兜底：他人已建同名，重查返回
			return s.getByName(ctx, appID, name)
		}
		return service.Service{}, err
	}
	return ns, nil
}

// getByName 按 (tenant, app, name) 查（GetOrCreateByName 并发兜底用）。
func (s *Store) getByName(ctx context.Context, appID, name string) (service.Service, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return service.Service{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+svcCols+` FROM services WHERE tenant_id=$1 AND app_id=$2 AND name=$3`, tid, appID, name)
	var out service.Service
	if err = scanSvc(row, &out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return service.Service{}, service.ErrNotFound
		}
		return service.Service{}, err
	}
	return out, nil
}

// ServicesCount 返回全表服务数，供 PG seed 判空（表空才灌，幂等）。
// 注意：不经租户过滤，仅用于启动期 seed 判空，不暴露给业务层。
func (s *Store) ServicesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM services`).Scan(&n)
	return n, err
}

var _ = fmt.Sprintf // 保留 fmt 引用占位（错误消息构造如需可扩展）
