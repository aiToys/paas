// Package pg 实现 workflow.Repository 的 PostgreSQL 持久化（与 skill pg 1:1 模式）。
// 显式 WHERE tenant_id=$1 多租户过滤；name 租户内唯一（UNIQUE 约束）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/workflow"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列顺序与 scan 对齐（列错位 panic 警示）。
const defCols = `id, tenant_id, name, desc, nodes, enabled, created_at, updated_at`
const runCols = `id, tenant_id, workflow_id, status, inputs, node_runs, created_at, finished_at`

func (s *Store) newID(prefix string) string {
	s.seq.Add(1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), s.seq.Load())
}

func marshalNodes(ns []workflow.NodeDef) ([]byte, error) { return json.Marshal(ns) }
func unmarshalNodes(b []byte) ([]workflow.NodeDef, error) {
	var ns []workflow.NodeDef
	if len(b) == 0 {
		return ns, nil
	}
	return ns, json.Unmarshal(b, &ns)
}

func (s *Store) List(ctx context.Context) ([]workflow.WorkflowDef, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+defCols+` FROM ai_workflows WHERE tenant_id=$1 ORDER BY created_at DESC`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]workflow.WorkflowDef, 0)
	for rows.Next() {
		d, err := scanDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanDef(row pgx.Row) (workflow.WorkflowDef, error) {
	var d workflow.WorkflowDef
	var nodes []byte
	if err := row.Scan(&d.ID, &d.TenantID, &d.Name, &d.Desc, &nodes, &d.Enabled, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return workflow.WorkflowDef{}, err
	}
	ns, err := unmarshalNodes(nodes)
	if err != nil {
		return workflow.WorkflowDef{}, fmt.Errorf("nodes 反序列化: %w", err)
	}
	d.Nodes = ns
	return d, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func (s *Store) Get(ctx context.Context, id string) (workflow.WorkflowDef, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	d, err := scanDef(s.db.Pool().QueryRow(ctx,
		`SELECT `+defCols+` FROM ai_workflows WHERE tenant_id=$1 AND id=$2`, tid, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.WorkflowDef{}, workflow.ErrWorkflowNotFound
	}
	return d, err
}

func (s *Store) Create(ctx context.Context, in workflow.WorkflowDef) (workflow.WorkflowDef, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	nodes, err := marshalNodes(in.Nodes)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	now := time.Now()
	in.ID = s.newID("wf")
	in.TenantID = tid
	in.CreatedAt, in.UpdatedAt = now, now
	err = s.db.Pool().QueryRow(ctx,
		`INSERT INTO ai_workflows (`+defCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		in.ID, in.TenantID, in.Name, in.Desc, nodes, in.Enabled, in.CreatedAt, in.UpdatedAt).
		Scan()
	if storagepg.IsUniqueViolation(err) {
		return workflow.WorkflowDef{}, workflow.ErrWorkflowExists
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return workflow.WorkflowDef{}, err
	}
	// Scan() 对无 RETURNING 的 QueryRow 返回 ErrNoRows；插入成功时忽略。
	return in, nil
}

func (s *Store) Update(ctx context.Context, in workflow.WorkflowDef) (workflow.WorkflowDef, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	nodes, err := marshalNodes(in.Nodes)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	in.TenantID = tid
	in.UpdatedAt = time.Now()
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE ai_workflows SET name=$3, desc=$4, nodes=$5, enabled=$6, updated_at=$7
		 WHERE tenant_id=$1 AND id=$2`,
		tid, in.ID, in.Name, in.Desc, nodes, in.Enabled, in.UpdatedAt)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return workflow.WorkflowDef{}, workflow.ErrWorkflowExists
		}
		return workflow.WorkflowDef{}, err
	}
	if tag.RowsAffected() == 0 {
		return workflow.WorkflowDef{}, workflow.ErrWorkflowNotFound
	}
	// 回读 created_at（Update 不改写）
	d, err := s.Get(ctx, in.ID)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	return d, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM ai_workflows WHERE tenant_id=$1 AND id=$2`, tid, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return workflow.ErrWorkflowNotFound
	}
	return nil
}

func (s *Store) WorkflowsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM ai_workflows`).Scan(&n)
	return n, err
}

func (s *Store) CreateRun(ctx context.Context, in workflow.WorkflowRun) (workflow.WorkflowRun, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowRun{}, err
	}
	inputs, _ := json.Marshal(mapOrDefault(in.Inputs))
	nrs, _ := json.Marshal(in.NodeRuns)
	in.ID = s.newID("wfr")
	in.TenantID = tid
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	err = s.db.Pool().QueryRow(ctx,
		`INSERT INTO ai_workflow_runs (`+runCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		in.ID, in.TenantID, in.WorkflowID, in.Status, inputs, nrs, in.CreatedAt, nilFinishAt(in.FinishedAt)).
		Scan()
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return workflow.WorkflowRun{}, err
	}
	return in, nil
}

func mapOrDefault(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nilFinishAt(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *Store) GetRun(ctx context.Context, id string) (workflow.WorkflowRun, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowRun{}, err
	}
	r, err := scanRun(s.db.Pool().QueryRow(ctx,
		`SELECT `+runCols+` FROM ai_workflow_runs WHERE tenant_id=$1 AND id=$2`, tid, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.WorkflowRun{}, workflow.ErrRunNotFound
	}
	return r, err
}

func scanRun(row rowScanner) (workflow.WorkflowRun, error) {
	var r workflow.WorkflowRun
	var inputs, nrs []byte
	var finished *time.Time
	if err := row.Scan(&r.ID, &r.TenantID, &r.WorkflowID, &r.Status, &inputs, &nrs, &r.CreatedAt, &finished); err != nil {
		return workflow.WorkflowRun{}, err
	}
	if len(inputs) > 0 {
		if err := json.Unmarshal(inputs, &r.Inputs); err != nil {
			return workflow.WorkflowRun{}, fmt.Errorf("inputs 反序列化: %w", err)
		}
	}
	if len(nrs) > 0 {
		if err := json.Unmarshal(nrs, &r.NodeRuns); err != nil {
			return workflow.WorkflowRun{}, fmt.Errorf("node_runs 反序列化: %w", err)
		}
	}
	if finished != nil {
		r.FinishedAt = *finished
	}
	return r, nil
}

func (s *Store) UpdateRun(ctx context.Context, in workflow.WorkflowRun) (workflow.WorkflowRun, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowRun{}, err
	}
	inputs, _ := json.Marshal(mapOrDefault(in.Inputs))
	nrs, _ := json.Marshal(in.NodeRuns)
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE ai_workflow_runs SET status=$3, inputs=$4, node_runs=$5, finished_at=$6
		 WHERE tenant_id=$1 AND id=$2`,
		tid, in.ID, in.Status, inputs, nrs, nilFinishAt(in.FinishedAt))
	if err != nil {
		return workflow.WorkflowRun{}, err
	}
	if tag.RowsAffected() == 0 {
		return workflow.WorkflowRun{}, workflow.ErrRunNotFound
	}
	return in, nil
}

func (s *Store) ListRuns(ctx context.Context, workflowID string) ([]workflow.WorkflowRun, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	// 同审计日志防御性上界：运行历史量大后防全量撑爆
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+runCols+` FROM ai_workflow_runs WHERE tenant_id=$1 AND ($2='' OR workflow_id=$2)
		 ORDER BY created_at DESC LIMIT 200`, tid, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]workflow.WorkflowRun, 0)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
