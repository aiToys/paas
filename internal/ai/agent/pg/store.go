// Package pg 实现 agent.Repository 的 PostgreSQL 持久化（与 tool/prompt pg 1:1）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/agent"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列顺序（tools/knowledge_bases JSONB）。
const agentCols = `id, tenant_id, name, description, model, system_prompt, prompt_ref, tools, knowledge_bases, skills, category, installed_from, max_steps, enabled, created_at, updated_at`

func (s *Store) newID() string {
	s.seq.Add(1)
	return fmt.Sprintf("agent-%d-%d", time.Now().UnixNano(), s.seq.Load())
}

func marshalStrList(v []string) ([]byte, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(v)
}

func scanAgent(r storagepg.RowScanner, a *agent.Agent) error {
	var toolsRaw, kbRaw, skillsRaw []byte
	if err := r.Scan(&a.ID, &a.TenantID, &a.Name, &a.Description, &a.Model, &a.SystemPrompt, &a.PromptRef,
		&toolsRaw, &kbRaw, &skillsRaw, &a.Category, &a.InstalledFrom, &a.MaxSteps, &a.Enabled, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return err
	}
	a.Tools = []string{}
	a.KnowledgeBases = []string{}
	if len(toolsRaw) > 0 && string(toolsRaw) != "null" {
		_ = json.Unmarshal(toolsRaw, &a.Tools)
	}
	if len(kbRaw) > 0 && string(kbRaw) != "null" {
		_ = json.Unmarshal(kbRaw, &a.KnowledgeBases)
	}
	a.Skills = []string{}
	if len(skillsRaw) > 0 && string(skillsRaw) != "null" {
		_ = json.Unmarshal(skillsRaw, &a.Skills)
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]agent.Agent, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+agentCols+` FROM ai_agents WHERE tenant_id=$1 ORDER BY created_at DESC`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]agent.Agent, 0)
	for rows.Next() {
		var a agent.Agent
		if err = scanAgent(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAll admin 跨租户列表（不过滤 tenant，LIMIT 防大表）。
func (s *Store) ListAll(ctx context.Context) ([]agent.Agent, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+agentCols+` FROM ai_agents ORDER BY created_at DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]agent.Agent, 0)
	for rows.Next() {
		var a agent.Agent
		if err = scanAgent(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (agent.Agent, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+agentCols+` FROM ai_agents WHERE id=$1 AND tenant_id=$2`, id, tid)
	var a agent.Agent
	if err := scanAgent(row, &a); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Agent{}, agent.ErrAgentNotFound
		}
		return agent.Agent{}, err
	}
	return a, nil
}

func (s *Store) Create(ctx context.Context, a agent.Agent) (agent.Agent, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	if err := a.Validate(); err != nil {
		return agent.Agent{}, err
	}
	if a.ID == "" {
		a.ID = s.newID()
	}
	if a.MaxSteps == 0 {
		a.MaxSteps = agent.DefaultMaxSteps
	}
	a.Enabled = true // 创建即启用（与平台「创建即可用」惯例一致；用户可 Update 关闭）
	a.TenantID = tid
	now := time.Now()
	a.CreatedAt, a.UpdatedAt = now, now
	tools, _ := marshalStrList(a.Tools)
	kbs, _ := marshalStrList(a.KnowledgeBases)
	skills, _ := marshalStrList(a.Skills)
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO ai_agents (`+agentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		a.ID, a.TenantID, a.Name, a.Description, a.Model, a.SystemPrompt, a.PromptRef, tools, kbs, skills, a.Category, a.InstalledFrom, a.MaxSteps, a.Enabled, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return agent.Agent{}, agent.ErrAgentExists
		}
		return agent.Agent{}, err
	}
	return a, nil
}

func (s *Store) Update(ctx context.Context, a agent.Agent) (agent.Agent, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	if err := a.Validate(); err != nil {
		return agent.Agent{}, err
	}
	if a.MaxSteps == 0 {
		a.MaxSteps = agent.DefaultMaxSteps
	}
	a.TenantID = tid
	a.UpdatedAt = time.Now()
	tools, _ := marshalStrList(a.Tools)
	kbs, _ := marshalStrList(a.KnowledgeBases)
	skills, _ := marshalStrList(a.Skills)
	ct, err := s.db.Pool().Exec(ctx,
		`UPDATE ai_agents SET name=$1, description=$2, model=$3, system_prompt=$4, prompt_ref=$5,
		 tools=$6, knowledge_bases=$7, skills=$8, max_steps=$9, enabled=$10, updated_at=$11
		 WHERE id=$12 AND tenant_id=$13`,
		a.Name, a.Description, a.Model, a.SystemPrompt, a.PromptRef, tools, kbs, skills, a.MaxSteps, a.Enabled, a.UpdatedAt, a.ID, a.TenantID)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return agent.Agent{}, agent.ErrAgentExists
		}
		return agent.Agent{}, err
	}
	if ct.RowsAffected() == 0 {
		return agent.Agent{}, agent.ErrAgentNotFound
	}
	return s.Get(ctx, a.ID)
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	ct, err := s.db.Pool().Exec(ctx, `DELETE FROM ai_agents WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return agent.ErrAgentNotFound
	}
	return nil
}

func (s *Store) AgentsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM ai_agents`).Scan(&n)
	return n, err
}
