// Package pg 变更 + 集成批次的 PostgreSQL 实现。
//
// 与 MemoryStore 同构：所有方法强制按 ctx 租户过滤；Create 以 ctx 租户为准忽略请求体；
// 跨租户访问统一 NotFound 不泄漏存在性。
//
// JSONB 字段（IntegrationBatch.ChangeIDs/ReleaseIDs）用 json.Marshal/Unmarshal，
// 读空返 nil slice（与 memory cloneStrs 语义一致）。
//
// 分支唯一靠唯一索引（idx_changes_tenant_repo_branch / idx_batches_tenant_branch），
// INSERT 冲突按 SQLSTATE 23505 映射 ErrChangeExists/ErrBatchExists。
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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitoys/paas/internal/devops/change"
	"github.com/aitoys/paas/internal/storage/pg"
)

// Store 变更 + 批次仓储的 PostgreSQL 实现。db 必须已完成迁移（含 0027_change_management）。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建变更管理 PG 仓储。db 必须已完成迁移。
func NewStore(db *pg.DB) *Store { return &Store{db: db.Pool()} }

// 列常量与 scan 列序严格对齐（列错位是 pg store 最易踩坑）。
const (
	changeCols = `id, tenant_id, app_id, repo_id, title, type, branch, branch_created, base_branch,
		status, batch_id, conflict_with, created_by, created_at, updated_at`
	batchCols = `id, tenant_id, app_id, repo_id, title, branch, status, change_ids,
		pipeline_id, run_id, release_ids, created_by, created_at, finished_at`
)

// tenantOrErr 从 ctx 取租户，缺失返 ErrNoTenant。
func tenantOrErr(ctx context.Context) (string, error) {
	return pg.TenantOrErr(ctx)
}

// randID 生成带前缀的短 ID（crypto/rand 8 字节 hex，与 memory 实现同源思路）。
func randID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error()) // 无熵优于弱 ID
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// marshalStrs 序列化字符串切片为 JSONB（nil 安全，空写 '[]'）。
func marshalStrs(ss []string) []byte {
	if len(ss) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// unmarshalStrs 反序列化 JSONB 为字符串切片（空返 nil）。
func unmarshalStrs(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// ---------- Change ----------

// ListChanges 按租户 + 可选 appID/status 过滤，created_at 倒序。
func (s *Store) ListChanges(ctx context.Context, appID, status string) ([]change.Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + changeCols + ` FROM changes WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(` AND status=$%d`, len(args))
	}
	q += ` ORDER BY created_at DESC, id`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]change.Change, 0)
	for rows.Next() {
		var c change.Change
		if err = rows.Scan(&c.ID, &c.TenantID, &c.AppID, &c.RepoID, &c.Title, &c.Type, &c.Branch,
			&c.BranchCreated, &c.BaseBranch, &c.Status, &c.BatchID, &c.ConflictWith,
			&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChange 取单个；跨租户访问 NotFound 不泄漏。
func (s *Store) GetChange(ctx context.Context, id string) (change.Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return change.Change{}, err
	}
	var c change.Change
	err = s.db.QueryRow(ctx,
		`SELECT `+changeCols+` FROM changes WHERE id=$1 AND tenant_id=$2`, id, tid).
		Scan(&c.ID, &c.TenantID, &c.AppID, &c.RepoID, &c.Title, &c.Type, &c.Branch,
			&c.BranchCreated, &c.BaseBranch, &c.Status, &c.BatchID, &c.ConflictWith,
			&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return change.Change{}, change.ErrChangeNotFound
		}
		return change.Change{}, err
	}
	return c, nil
}

// CreateChange 创建变更；同 (tenant, repo) 分支唯一（唯一索引冲突 → ErrChangeExists）。
func (s *Store) CreateChange(ctx context.Context, in change.Change) (change.Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return change.Change{}, err
	}
	if err := in.Validate(); err != nil {
		return change.Change{}, err
	}
	in.ID = randID("chg")
	in.TenantID = tid // ctx 为准，忽略请求体
	in.Status = change.ChangeOpen
	now := time.Now().UTC()
	in.CreatedAt, in.UpdatedAt = now, now
	if in.BaseBranch == "" {
		in.BaseBranch = "main"
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO changes (`+changeCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		in.ID, in.TenantID, in.AppID, in.RepoID, in.Title, in.Type, in.Branch,
		in.BranchCreated, in.BaseBranch, in.Status, in.BatchID, in.ConflictWith,
		in.CreatedBy, in.CreatedAt, in.UpdatedAt)
	if err != nil {
		if pg.IsUniqueViolation(err) {
			return change.Change{}, change.ErrChangeExists
		}
		return change.Change{}, err
	}
	return in, nil
}

// UpdateChange 全量覆盖更新；0 行（不存在/跨租户）返 ErrChangeNotFound。CreatedAt 以 DB 现值保留。
func (s *Store) UpdateChange(ctx context.Context, in change.Change) (change.Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return change.Change{}, err
	}
	in.TenantID = tid
	in.UpdatedAt = time.Now().UTC()
	tag, err := s.db.Exec(ctx, `UPDATE changes SET title=$3, type=$4, branch=$5, branch_created=$6,
		base_branch=$7, status=$8, batch_id=$9, conflict_with=$10, created_by=$11, updated_at=$12
		WHERE id=$1 AND tenant_id=$2`,
		in.ID, in.TenantID, in.Title, in.Type, in.Branch, in.BranchCreated,
		in.BaseBranch, in.Status, in.BatchID, in.ConflictWith, in.CreatedBy, in.UpdatedAt)
	if err != nil {
		if pg.IsUniqueViolation(err) {
			return change.Change{}, change.ErrChangeExists
		}
		return change.Change{}, err
	}
	if tag.RowsAffected() == 0 {
		return change.Change{}, change.ErrChangeNotFound
	}
	// 回读保留 DB 的 CreatedAt
	got, err := s.GetChange(ctx, in.ID)
	if err != nil {
		return change.Change{}, err
	}
	return got, nil
}

// ---------- IntegrationBatch ----------

// ListBatches 按租户 + 可选 appID/status 过滤，created_at 倒序。
func (s *Store) ListBatches(ctx context.Context, appID, status string) ([]change.IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + batchCols + ` FROM integration_batches WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(` AND status=$%d`, len(args))
	}
	q += ` ORDER BY created_at DESC, id`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]change.IntegrationBatch, 0)
	for rows.Next() {
		var b change.IntegrationBatch
		var changeIDsB, releaseIDsB []byte
		if err = rows.Scan(&b.ID, &b.TenantID, &b.AppID, &b.RepoID, &b.Title, &b.Branch, &b.Status,
			&changeIDsB, &b.PipelineID, &b.RunID, &releaseIDsB, &b.CreatedBy, &b.CreatedAt, &b.FinishedAt); err != nil {
			return nil, err
		}
		b.ChangeIDs = unmarshalStrs(changeIDsB)
		b.ReleaseIDs = unmarshalStrs(releaseIDsB)
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBatch 取单个；跨租户访问 NotFound 不泄漏。
func (s *Store) GetBatch(ctx context.Context, id string) (change.IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return change.IntegrationBatch{}, err
	}
	var b change.IntegrationBatch
	var changeIDsB, releaseIDsB []byte
	err = s.db.QueryRow(ctx,
		`SELECT `+batchCols+` FROM integration_batches WHERE id=$1 AND tenant_id=$2`, id, tid).
		Scan(&b.ID, &b.TenantID, &b.AppID, &b.RepoID, &b.Title, &b.Branch, &b.Status,
			&changeIDsB, &b.PipelineID, &b.RunID, &releaseIDsB, &b.CreatedBy, &b.CreatedAt, &b.FinishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return change.IntegrationBatch{}, change.ErrBatchNotFound
		}
		return change.IntegrationBatch{}, err
	}
	b.ChangeIDs = unmarshalStrs(changeIDsB)
	b.ReleaseIDs = unmarshalStrs(releaseIDsB)
	return b, nil
}

// CreateBatch 创建批次；同租户集成分支唯一（唯一索引冲突 → ErrBatchExists）。
func (s *Store) CreateBatch(ctx context.Context, in change.IntegrationBatch) (change.IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return change.IntegrationBatch{}, err
	}
	if err := in.ValidateBatch(); err != nil {
		return change.IntegrationBatch{}, err
	}
	in.ID = randID("batch")
	in.TenantID = tid // ctx 为准，忽略请求体
	in.Status = change.BatchCollecting
	in.CreatedAt = time.Now().UTC()
	_, err = s.db.Exec(ctx,
		`INSERT INTO integration_batches (`+batchCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		in.ID, in.TenantID, in.AppID, in.RepoID, in.Title, in.Branch, in.Status,
		marshalStrs(in.ChangeIDs), in.PipelineID, in.RunID, marshalStrs(in.ReleaseIDs),
		in.CreatedBy, in.CreatedAt, in.FinishedAt)
	if err != nil {
		if pg.IsUniqueViolation(err) {
			return change.IntegrationBatch{}, change.ErrBatchExists
		}
		return change.IntegrationBatch{}, err
	}
	return in, nil
}

// UpdateBatch 全量覆盖更新；0 行返 ErrBatchNotFound。CreatedAt 以 DB 现值保留。
func (s *Store) UpdateBatch(ctx context.Context, in change.IntegrationBatch) (change.IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return change.IntegrationBatch{}, err
	}
	in.TenantID = tid
	tag, err := s.db.Exec(ctx, `UPDATE integration_batches SET title=$3, branch=$4, status=$5,
		change_ids=$6, pipeline_id=$7, run_id=$8, release_ids=$9, created_by=$10, finished_at=$11
		WHERE id=$1 AND tenant_id=$2`,
		in.ID, in.TenantID, in.Title, in.Branch, in.Status,
		marshalStrs(in.ChangeIDs), in.PipelineID, in.RunID, marshalStrs(in.ReleaseIDs),
		in.CreatedBy, in.FinishedAt)
	if err != nil {
		if pg.IsUniqueViolation(err) {
			return change.IntegrationBatch{}, change.ErrBatchExists
		}
		return change.IntegrationBatch{}, err
	}
	if tag.RowsAffected() == 0 {
		return change.IntegrationBatch{}, change.ErrBatchNotFound
	}
	got, err := s.GetBatch(ctx, in.ID)
	if err != nil {
		return change.IntegrationBatch{}, err
	}
	return got, nil
}

// ChangesCount 返回 changes 总行数（平台级，seed 判空用）。
func (s *Store) ChangesCount(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM changes`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// 编译期断言 Store 实现 change.Repository。
var _ change.Repository = (*Store)(nil)
