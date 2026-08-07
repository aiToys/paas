// Package pg 实现 tool.Repository 的 PostgreSQL 持久化（与 KB pg 1:1）。
// 显式 WHERE tenant_id=$1 多租户过滤；config JSONB；name 租户内唯一（UNIQUE 约束）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/tool"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列顺序与 scan 对齐（列错位 panic 警示）。
const toolCols = `id, tenant_id, name, description, type, config, enabled, created_at, updated_at`

func (s *Store) newID() string {
	s.seq.Add(1)
	return fmt.Sprintf("tool-%d-%d", time.Now().UnixNano(), s.seq.Load())
}

func marshalConfig(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func scanTool(r storagepg.RowScanner, t *tool.Tool) error {
	var cfgRaw []byte
	if err := r.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.Type, &cfgRaw, &t.Enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}
	t.Config = map[string]string{}
	if len(cfgRaw) > 0 && string(cfgRaw) != "null" {
		_ = json.Unmarshal(cfgRaw, &t.Config)
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]tool.Tool, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+toolCols+` FROM ai_tools WHERE tenant_id=$1 ORDER BY created_at DESC`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]tool.Tool, 0)
	for rows.Next() {
		var t tool.Tool
		if err = scanTool(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (tool.Tool, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return tool.Tool{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+toolCols+` FROM ai_tools WHERE id=$1 AND tenant_id=$2`, id, tid)
	var t tool.Tool
	if err := scanTool(row, &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tool.Tool{}, tool.ErrToolNotFound
		}
		return tool.Tool{}, err
	}
	return t, nil
}

func (s *Store) Create(ctx context.Context, t tool.Tool) (tool.Tool, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return tool.Tool{}, err
	}
	if err := t.Validate(); err != nil {
		return tool.Tool{}, err
	}
	if t.ID == "" {
		t.ID = s.newID()
	}
	t.TenantID = tid
	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	cfg, _ := marshalConfig(t.Config)
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO ai_tools (`+toolCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		t.ID, t.TenantID, t.Name, t.Description, t.Type, cfg, t.Enabled, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return tool.Tool{}, tool.ErrToolExists
		}
		return tool.Tool{}, err
	}
	return t, nil
}

func (s *Store) Update(ctx context.Context, t tool.Tool) (tool.Tool, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return tool.Tool{}, err
	}
	if err := t.Validate(); err != nil {
		return tool.Tool{}, err
	}
	t.TenantID = tid
	t.UpdatedAt = time.Now()
	cfg, _ := marshalConfig(t.Config)
	ct, err := s.db.Pool().Exec(ctx,
		`UPDATE ai_tools SET name=$1, description=$2, type=$3, config=$4, enabled=$5, updated_at=$6
		 WHERE id=$7 AND tenant_id=$8`,
		t.Name, t.Description, t.Type, cfg, t.Enabled, t.UpdatedAt, t.ID, t.TenantID)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return tool.Tool{}, tool.ErrToolExists
		}
		return tool.Tool{}, err
	}
	if ct.RowsAffected() == 0 {
		return tool.Tool{}, tool.ErrToolNotFound
	}
	return s.Get(ctx, t.ID) // 回读 created_at
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	ct, err := s.db.Pool().Exec(ctx, `DELETE FROM ai_tools WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return tool.ErrToolNotFound
	}
	return nil
}

func (s *Store) ToolsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM ai_tools`).Scan(&n)
	return n, err
}
