// Package pg 实现 eval.Repository 的 PostgreSQL 持久化（与 tool/pg 1:1）。
// 显式 WHERE tenant_id=$1 多租户过滤；(tenant_id, agent_id, name) 唯一。
package pg

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/eval"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

const evalCols = `id, tenant_id, agent_id, name, input, expected, match_type, created_at, updated_at`

func (s *Store) newID() string {
	s.seq.Add(1)
	return fmt.Sprintf("eval-%d-%d", time.Now().UnixNano(), s.seq.Load())
}

func scanCase(r storagepg.RowScanner, c *eval.EvalCase) error {
	return r.Scan(&c.ID, &c.TenantID, &c.AgentID, &c.Name, &c.Input, &c.Expected, &c.MatchType, &c.CreatedAt, &c.UpdatedAt)
}

func (s *Store) List(ctx context.Context, agentID string) ([]eval.EvalCase, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	var (
		rows pgx.Rows
		err2 error
	)
	if agentID == "" {
		rows, err2 = s.db.Pool().Query(ctx,
			`SELECT `+evalCols+` FROM ai_eval_cases WHERE tenant_id=$1 ORDER BY created_at DESC`, tid)
	} else {
		rows, err2 = s.db.Pool().Query(ctx,
			`SELECT `+evalCols+` FROM ai_eval_cases WHERE tenant_id=$1 AND agent_id=$2 ORDER BY created_at DESC`, tid, agentID)
	}
	if err2 != nil {
		return nil, err2
	}
	defer rows.Close()
	out := make([]eval.EvalCase, 0)
	for rows.Next() {
		var c eval.EvalCase
		if err = scanCase(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (eval.EvalCase, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return eval.EvalCase{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+evalCols+` FROM ai_eval_cases WHERE id=$1 AND tenant_id=$2`, id, tid)
	var c eval.EvalCase
	if err := scanCase(row, &c); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return eval.EvalCase{}, eval.ErrEvalCaseNotFound
		}
		return eval.EvalCase{}, err
	}
	return c, nil
}

func (s *Store) Create(ctx context.Context, c eval.EvalCase) (eval.EvalCase, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return eval.EvalCase{}, err
	}
	if err := c.Validate(); err != nil {
		return eval.EvalCase{}, err
	}
	if c.ID == "" {
		c.ID = s.newID()
	}
	c.TenantID = tid
	now := time.Now()
	c.CreatedAt, c.UpdatedAt = now, now
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO ai_eval_cases (`+evalCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.ID, c.TenantID, c.AgentID, c.Name, c.Input, c.Expected, c.MatchType, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return eval.EvalCase{}, eval.ErrEvalCaseExists
		}
		return eval.EvalCase{}, err
	}
	return c, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	ct, err := s.db.Pool().Exec(ctx, `DELETE FROM ai_eval_cases WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return eval.ErrEvalCaseNotFound
	}
	return nil
}

func (s *Store) EvalCasesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM ai_eval_cases`).Scan(&n)
	return n, err
}
