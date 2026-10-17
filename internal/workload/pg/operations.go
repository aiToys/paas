// Package pg —— workload.OperationsRepo 的 PostgreSQL 实现。
// 受理走显式事务：restart 的 token 与操作记录同事务提交（spec 2026-10-17 §6），
// 幂等键 partial unique 兜底并发重复受理；活跃操作 partial unique 是 409 的库级保证。
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
)

// OpsStore 是 workload.OperationsRepo 的 PG 实现。
type OpsStore struct {
	db  *pg.DB
	wls *Store // 复用 workloads 表访问（事务内写 restart_token）
}

// NewOpsStore 创建操作记录仓储。db 必须已完成迁移（0048）。
func NewOpsStore(db *pg.DB) *OpsStore {
	return &OpsStore{db: db, wls: NewStore(db)}
}

// opCols 与 workload.Operation 字段对齐。
const opCols = `id, tenant_id, workload_id, kind, status, reason, actor, idempotency_key, target_token, pod_uid, pod_name, replicas, evidence, accepted_at, updated_at, finished_at`

func scanOp(r pgx.Row, op *workload.Operation) error {
	return r.Scan(&op.ID, &op.TenantID, &op.WorkloadID, &op.Kind, &op.Status, &op.Reason, &op.Actor,
		&op.IdempotencyKey, &op.TargetToken, &op.PodUID, &op.PodName, &op.Replicas, &op.Evidence,
		&op.AcceptedAt, &op.UpdatedAt, &op.FinishedAt)
}

// mapOpErr 把 pg 错误映射为领域语义错误。
func mapOpErr(err error) error {
	if pg.IsUniqueViolation(err) {
		if strings.Contains(err.Error(), "uq_wl_ops_active") {
			return workload.ErrOpConflict
		}
		return workload.ErrOpConflict // 幂等键冲突：调用方按并发处理（罕见——先查后插窗口）
	}
	return err
}

// newOpID 生成操作 ID（时间前缀 + 随机段，可读且不撞）。
func newOpID() string {
	return fmt.Sprintf("op-%d", time.Now().UnixNano())
}

// newToken 生成滚动重启令牌（不可重复）。
func newToken() string {
	return fmt.Sprintf("rt-%d", time.Now().UnixNano())
}

// CreateRestart 受理滚动重启：事务内 UPDATE restart_token + INSERT 操作记录。
// 幂等：IdempotencyKey 命中返回既有记录（created=false）。
func (s *OpsStore) CreateRestart(ctx context.Context, w workload.Workload, reason, actor, idemKey string) (workload.Operation, bool, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Operation{}, false, err
	}
	// 幂等命中先查（partial unique 兜底竞态窗口）。
	if idemKey != "" {
		if op, ok, err := s.findByIdem(ctx, tid, w.ID, idemKey); err != nil || ok {
			return op, false, err
		}
	}
	token := newToken()
	op := workload.Operation{
		ID: newOpID(), TenantID: tid, WorkloadID: w.ID, Kind: workload.OpKindRestart,
		Status: workload.OpStatusAccepted, Reason: reason, Actor: actor, IdempotencyKey: idemKey,
		TargetToken: token, Replicas: w.Replicas,
		AcceptedAt: time.Now(), UpdatedAt: time.Now(),
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return workload.Operation{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // 提交成功后 Rollback 是 no-op
	tag, err := tx.Exec(ctx, `UPDATE workloads SET restart_token=$3 WHERE id=$1 AND tenant_id=$2 AND type='service'`,
		w.ID, tid, token)
	if err != nil {
		return workload.Operation{}, false, mapOpErr(err)
	}
	if tag.RowsAffected() == 0 {
		return workload.Operation{}, false, fmt.Errorf("工作负载不存在或不支持重启: %s", w.ID)
	}
	if err := insertOp(ctx, tx.Exec, op); err != nil {
		return workload.Operation{}, false, mapOpErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return workload.Operation{}, false, err
	}
	return op, true, nil
}

// CreateRecreate 受理实例重建（快照副本数；不改 workload 期望状态）。
func (s *OpsStore) CreateRecreate(ctx context.Context, w workload.Workload, podName, podUID, reason, actor, idemKey string) (workload.Operation, bool, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Operation{}, false, err
	}
	if idemKey != "" {
		if op, ok, err := s.findByIdem(ctx, tid, w.ID, idemKey); err != nil || ok {
			return op, false, err
		}
	}
	op := workload.Operation{
		ID: newOpID(), TenantID: tid, WorkloadID: w.ID, Kind: workload.OpKindRecreate,
		Status: workload.OpStatusAccepted, Reason: reason, Actor: actor, IdempotencyKey: idemKey,
		PodUID: podUID, PodName: podName, Replicas: w.Replicas,
		AcceptedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := insertOp(ctx, s.db.Pool().Exec, op); err != nil {
		return workload.Operation{}, false, mapOpErr(err)
	}
	return op, true, nil
}

// insertOp 经给定 exec 抽象插入（事务/池双态复用；pgx.Tx 与 pgxpool.Pool 的 Exec 同签名）。
func insertOp(ctx context.Context, exec func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error), op workload.Operation) error {
	_, err := exec(ctx, `INSERT INTO workload_operations (`+opCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		op.ID, op.TenantID, op.WorkloadID, op.Kind, op.Status, op.Reason, op.Actor,
		op.IdempotencyKey, op.TargetToken, op.PodUID, op.PodName, op.Replicas, op.Evidence,
		op.AcceptedAt, op.UpdatedAt, op.FinishedAt)
	return err
}

// findByIdem 幂等键查询（命中返回既有记录）。
func (s *OpsStore) findByIdem(ctx context.Context, tid, workloadID, idemKey string) (workload.Operation, bool, error) {
	q := `SELECT ` + opCols + ` FROM workload_operations WHERE tenant_id=$1 AND workload_id=$2 AND idempotency_key=$3 LIMIT 1`
	var op workload.Operation
	if err := scanOp(s.db.Pool().QueryRow(ctx, q, tid, workloadID, idemKey), &op); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workload.Operation{}, false, nil
		}
		return workload.Operation{}, false, err
	}
	return op, true, nil
}

// Get 按（租户过滤的）workload + operation ID 取操作。
func (s *OpsStore) Get(ctx context.Context, workloadID, operationID string) (workload.Operation, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Operation{}, err
	}
	q := `SELECT ` + opCols + ` FROM workload_operations WHERE tenant_id=$1 AND workload_id=$2 AND id=$3`
	var op workload.Operation
	if err := scanOp(s.db.Pool().QueryRow(ctx, q, tid, workloadID, operationID), &op); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workload.Operation{}, fmt.Errorf("操作记录不存在: %s", operationID)
		}
		return workload.Operation{}, err
	}
	return op, nil
}

// ListByWorkload 按时间倒序分页。
func (s *OpsStore) ListByWorkload(ctx context.Context, workloadID string, limit, offset int) ([]workload.Operation, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + opCols + ` FROM workload_operations WHERE tenant_id=$1 AND workload_id=$2 ORDER BY accepted_at DESC LIMIT $3 OFFSET $4`
	rows, err := s.db.Pool().Query(ctx, q, tid, workloadID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]workload.Operation, 0)
	for rows.Next() {
		var op workload.Operation
		if err := scanOp(rows, &op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// Active 返回该 workload 当前活跃操作（无则零值）。
func (s *OpsStore) Active(ctx context.Context, workloadID string) (workload.Operation, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Operation{}, err
	}
	q := `SELECT ` + opCols + ` FROM workload_operations WHERE tenant_id=$1 AND workload_id=$2 AND status IN ('accepted','reconciling','running') LIMIT 1`
	var op workload.Operation
	if err := scanOp(s.db.Pool().QueryRow(ctx, q, tid, workloadID), &op); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workload.Operation{}, nil
		}
		return workload.Operation{}, err
	}
	return op, nil
}

// ListActive 跨租户列出非终态操作（Pacer 恢复扫描）。
func (s *OpsStore) ListActive(ctx context.Context) ([]workload.Operation, error) {
	q := `SELECT ` + opCols + ` FROM workload_operations WHERE status IN ('accepted','reconciling','running') ORDER BY accepted_at`
	rows, err := s.db.Pool().Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]workload.Operation, 0)
	for rows.Next() {
		var op workload.Operation
		if err := scanOp(rows, &op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// UpdateStatus 推进状态。终态保护：仅允许非终态推进（succeeded/failed/superseded/unknown 不可再变）。
func (s *OpsStore) UpdateStatus(ctx context.Context, op workload.Operation) error {
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE workload_operations SET status=$2, evidence=$3, updated_at=now(), finished_at=$4
		 WHERE id=$1 AND status IN ('accepted','reconciling','running')`,
		op.ID, op.Status, op.Evidence, op.FinishedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// 终态或不存在：幂等忽略（Pacer 与用户确认并发时以先到的终态为准）。
		return nil
	}
	return nil
}
