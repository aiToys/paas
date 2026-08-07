// Package pg 实现 prompt.Repository 的 PostgreSQL 持久化（版本化，同 name 多版本行）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/prompt"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

const promptCols = `id, tenant_id, name, template, variables, version, active, created_at`

func (s *Store) newID() string {
	s.seq.Add(1)
	return fmt.Sprintf("prompt-%d-%d", time.Now().UnixNano(), s.seq.Load())
}

func marshalVars(v []string) ([]byte, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(v)
}

func scanPrompt(r storagepg.RowScanner, p *prompt.Prompt) error {
	var vRaw []byte
	if err := r.Scan(&p.ID, &p.TenantID, &p.Name, &p.Template, &vRaw, &p.Version, &p.Active, &p.CreatedAt); err != nil {
		return err
	}
	p.Variables = []string{}
	if len(vRaw) > 0 && string(vRaw) != "null" {
		_ = json.Unmarshal(vRaw, &p.Variables)
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]prompt.Prompt, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+promptCols+` FROM ai_prompts WHERE tenant_id=$1 ORDER BY name, version DESC`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]prompt.Prompt, 0)
	for rows.Next() {
		var p prompt.Prompt
		if err = scanPrompt(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (prompt.Prompt, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+promptCols+` FROM ai_prompts WHERE id=$1 AND tenant_id=$2`, id, tid)
	var p prompt.Prompt
	if err := scanPrompt(row, &p); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return prompt.Prompt{}, prompt.ErrPromptNotFound
		}
		return prompt.Prompt{}, err
	}
	return p, nil
}

// Create 同 name → version=max+1（事务内 SELECT FOR UPDATE 串行化 + 旧版 deactive + 新版 active）。
func (s *Store) Create(ctx context.Context, p prompt.Prompt) (prompt.Prompt, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	if err := p.Validate(); err != nil {
		return prompt.Prompt{}, err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 锁同 name 全部行 + 取 max version（FOR UPDATE 防并发版本竞争）
	rows, err := tx.Query(ctx, `SELECT version FROM ai_prompts WHERE tenant_id=$1 AND name=$2 FOR UPDATE`, tid, p.Name)
	if err != nil {
		return prompt.Prompt{}, err
	}
	maxVer := 0
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return prompt.Prompt{}, err
		}
		if v > maxVer {
			maxVer = v
		}
	}
	rows.Close()

	p.ID = s.newID()
	p.TenantID = tid
	p.Version = maxVer + 1
	p.Active = true
	p.CreatedAt = time.Now()
	vars, _ := marshalVars(p.Variables)

	// 旧版 deactive
	if _, err := tx.Exec(ctx, `UPDATE ai_prompts SET active=false WHERE tenant_id=$1 AND name=$2 AND active`, tid, p.Name); err != nil {
		return prompt.Prompt{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ai_prompts (`+promptCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.ID, p.TenantID, p.Name, p.Template, vars, p.Version, p.Active, p.CreatedAt); err != nil {
		return prompt.Prompt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return prompt.Prompt{}, err
	}
	return p, nil
}

func (s *Store) SetActive(ctx context.Context, id string) (prompt.Prompt, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM ai_prompts WHERE id=$1 AND tenant_id=$2`, id, tid).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return prompt.Prompt{}, prompt.ErrPromptNotFound
		}
		return prompt.Prompt{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_prompts SET active=false WHERE tenant_id=$1 AND name=$2`, tid, name); err != nil {
		return prompt.Prompt{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_prompts SET active=true WHERE id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return prompt.Prompt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return prompt.Prompt{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var name string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT name, active FROM ai_prompts WHERE id=$1 AND tenant_id=$2`, id, tid).Scan(&name, &active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return prompt.ErrPromptNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ai_prompts WHERE id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return err
	}
	// 删 active 版本：激活剩余最新版
	if active {
		if _, err := tx.Exec(ctx, `UPDATE ai_prompts SET active=true
			WHERE id=(SELECT id FROM ai_prompts WHERE tenant_id=$1 AND name=$2 ORDER BY version DESC LIMIT 1)`, tid, name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) GetActive(ctx context.Context, name string) (prompt.Prompt, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+promptCols+` FROM ai_prompts WHERE tenant_id=$1 AND name=$2 AND active`, tid, name)
	var p prompt.Prompt
	if err := scanPrompt(row, &p); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return prompt.Prompt{}, prompt.ErrNoActivePrompt
		}
		return prompt.Prompt{}, err
	}
	return p, nil
}

func (s *Store) PromptsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM ai_prompts`).Scan(&n)
	return n, err
}
