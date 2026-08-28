// Package pg 提供 lane.Repository 的 PostgreSQL 实现。
//
// 显式 WHERE tenant_id 强制多租户过滤；Create 以 ctx 租户为准忽略请求体 TenantID；
// 跨租户访问统一 not found（不泄漏存在性）。
// EnsureByName 用 INSERT ... ON CONFLICT DO NOTHING 幂等（并发竞态由唯一约束兜底），
// 冲突转 SELECT 返回既有行（不覆盖 permanent 等既有属性）。
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/lane"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 实现 lane.Repository。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 lane PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列常量与 scanLane 顺序严格对齐（列序错位是最易踩坑，两处必须同步改）。
const laneCols = `id, tenant_id, env_id, name, mode, status, weight, external_link, description, created_at, updated_at`

func scanLane(row pgx.Row) (lane.Lane, error) {
	var l lane.Lane
	err := row.Scan(&l.ID, &l.TenantID, &l.EnvID, &l.Name, &l.Mode, &l.Status,
		&l.Weight, &l.ExternalLink, &l.Description, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return lane.Lane{}, lane.ErrLaneNotFound
	}
	if err != nil {
		return lane.Lane{}, err
	}
	return l, nil
}

func scanLanes(rows pgx.Rows) ([]lane.Lane, error) {
	defer rows.Close()
	out := make([]lane.Lane, 0)
	for rows.Next() {
		var l lane.Lane
		if err := rows.Scan(&l.ID, &l.TenantID, &l.EnvID, &l.Name, &l.Mode, &l.Status,
			&l.Weight, &l.ExternalLink, &l.Description, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func tenantOrErr(ctx context.Context) (string, error) {
	return storagepg.TenantOrErr(ctx)
}

// List 租户内泳道列表（按创建时间排序）；envID 空不过滤。
func (s *Store) List(ctx context.Context, envID string) ([]lane.Lane, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + laneCols + ` FROM lanes WHERE tenant_id = $1`
	args := []any{tid}
	if envID != "" {
		q += ` AND env_id = $2`
		args = append(args, envID)
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanLanes(rows)
}

// Get 按 ID 取（跨租户 not found 不泄漏）。
func (s *Store) Get(ctx context.Context, id string) (lane.Lane, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	return scanLane(s.db.Pool().QueryRow(
		ctx, `SELECT `+laneCols+` FROM lanes WHERE id = $1 AND tenant_id = $2`, id, tid))
}

// GetByName 按 (envID, name) 取。
func (s *Store) GetByName(ctx context.Context, envID, name string) (lane.Lane, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	return scanLane(s.db.Pool().QueryRow(
		ctx, `SELECT `+laneCols+` FROM lanes WHERE tenant_id = $1 AND env_id = $2 AND name = $3`, tid, envID, name))
}

// newLaneID 生成泳道 ID（与 governance newGovID 同款：纳秒时间戳后缀）。
// EnsureByName 并发双写时两个 ID 只有一个落库，另一个走冲突转读，无碰撞风险。
func newLaneID() string {
	return fmt.Sprintf("lane-%d", time.Now().UnixNano())
}

// Create 创建（唯一冲突映射 ErrLaneExists；租户以 ctx 为准）。
func (s *Store) Create(ctx context.Context, in lane.Lane) (lane.Lane, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	if err := in.Validate(); err != nil {
		return lane.Lane{}, err
	}
	if in.Status == "" {
		in.Status = lane.StatusActive
	}
	now := time.Now()
	in.ID = newLaneID()
	in.TenantID = tid
	in.CreatedAt = now
	in.UpdatedAt = now
	_, err = s.db.Pool().Exec(ctx, `INSERT INTO lanes (`+laneCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		in.ID, in.TenantID, in.EnvID, in.Name, in.Mode, in.Status,
		in.Weight, in.ExternalLink, in.Description, in.CreatedAt, in.UpdatedAt)
	if storagepg.IsUniqueViolation(err) {
		return lane.Lane{}, lane.ErrLaneExists
	}
	if err != nil {
		return lane.Lane{}, err
	}
	return in, nil
}

// Update 更新可变字段（mode/description/externalLink；name/envID 不可改）。
func (s *Store) Update(ctx context.Context, id string, in lane.Lane) (lane.Lane, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	if in.Mode != "" {
		if _, ok := map[string]struct{}{lane.ModeStandard: {}, lane.ModePermanent: {}}[in.Mode]; !ok {
			return lane.Lane{}, errors.New("mode 非法（standard|permanent）")
		}
	}
	// 可选展示字段非空才覆盖（与 mode 同语义，防 PUT Partial body 缺字段误清空——终审 M1）。
	row := s.db.Pool().QueryRow(ctx, `UPDATE lanes
		SET mode = CASE WHEN $3 = '' THEN mode ELSE $3 END,
		    description = CASE WHEN $4 = '' THEN description ELSE $4 END,
		    external_link = CASE WHEN $5 = '' THEN external_link ELSE $5 END,
		    updated_at = now()
		WHERE id = $1 AND tenant_id = $2
		RETURNING `+laneCols, id, tid, in.Mode, in.Description, in.ExternalLink)
	return scanLane(row)
}

// Close 关闭（幂等：WHERE status='active' 更新，0 行说明已 closed/不存在，二次查询返回现状）。
func (s *Store) Close(ctx context.Context, id string) (lane.Lane, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `UPDATE lanes SET status = $3, updated_at = now()
		WHERE id = $1 AND tenant_id = $2 AND status = 'active'
		RETURNING `+laneCols, id, tid, lane.StatusClosed)
	l, err := scanLane(row)
	if errors.Is(err, lane.ErrLaneNotFound) {
		// 幂等：已 closed 时上面 0 行，这里取现状（不存在也会得到 ErrLaneNotFound）
		return s.Get(ctx, id)
	}
	return l, err
}

// EnsureByName 存在返回既有（不覆盖），不存在懒建 standard；
// ON CONFLICT DO NOTHING 幂等——并发双写时后到者 0 行，转 SELECT 返回先建行。
func (s *Store) EnsureByName(ctx context.Context, envID, name string) (lane.Lane, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	if err := lane.ValidateName(name); err != nil {
		return lane.Lane{}, err
	}
	now := time.Now()
	id := newLaneID()
	row := s.db.Pool().QueryRow(ctx, `INSERT INTO lanes (`+laneCols+`)
		VALUES ($1,$2,$3,$4,'standard','active',0,'','',  $5,$6)
		ON CONFLICT (tenant_id, env_id, name) DO NOTHING
		RETURNING `+laneCols, id, tid, envID, name, now, now)
	l, err := scanLane(row)
	if errors.Is(err, lane.ErrLaneNotFound) {
		// 冲突（既有行）转读
		return s.GetByName(ctx, envID, name)
	}
	return l, err
}
