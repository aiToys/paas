// Package pg 实现 skill.Repository 的 PostgreSQL 持久化（与 tool pg 1:1）。
// 显式 WHERE tenant_id=$1 多租户过滤；name 租户内唯一（UNIQUE 约束）。
package pg

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/skill"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列顺序与 scan 对齐（列错位 panic 警示）。
const skillCols = `id, tenant_id, name, description, instructions, category, use_cases, examples, installed_from, enabled, created_at, updated_at`

func (s *Store) newID() string {
	s.seq.Add(1)
	return fmt.Sprintf("skill-%d-%d", time.Now().UnixNano(), s.seq.Load())
}

func (s *Store) List(ctx context.Context) ([]skill.Skill, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+skillCols+` FROM ai_skills WHERE tenant_id=$1 ORDER BY created_at DESC`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]skill.Skill, 0)
	for rows.Next() {
		var sk skill.Skill
		if err = rows.Scan(&sk.ID, &sk.TenantID, &sk.Name, &sk.Description, &sk.Instructions, &sk.Category, &sk.UseCases, &sk.Examples, &sk.InstalledFrom, &sk.Enabled, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

func (s *Store) ListAll(ctx context.Context) ([]skill.Skill, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+skillCols+` FROM ai_skills ORDER BY created_at DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]skill.Skill, 0)
	for rows.Next() {
		var sk skill.Skill
		if err = rows.Scan(&sk.ID, &sk.TenantID, &sk.Name, &sk.Description, &sk.Instructions, &sk.Category, &sk.UseCases, &sk.Examples, &sk.InstalledFrom, &sk.Enabled, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (skill.Skill, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return skill.Skill{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+skillCols+` FROM ai_skills WHERE id=$1 AND tenant_id=$2`, id, tid)
	var sk skill.Skill
	if err := row.Scan(&sk.ID, &sk.TenantID, &sk.Name, &sk.Description, &sk.Instructions, &sk.Category, &sk.UseCases, &sk.Examples, &sk.InstalledFrom, &sk.Enabled, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return skill.Skill{}, skill.ErrSkillNotFound
		}
		return skill.Skill{}, err
	}
	return sk, nil
}

func (s *Store) Create(ctx context.Context, sk skill.Skill) (skill.Skill, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return skill.Skill{}, err
	}
	if err := sk.Validate(); err != nil {
		return skill.Skill{}, err
	}
	if sk.ID == "" {
		sk.ID = s.newID()
	}
	sk.TenantID = tid
	now := time.Now()
	sk.CreatedAt, sk.UpdatedAt = now, now
	sk.Enabled = true // 创建即启用（与 agent/tool 一致；用户可 Update 关闭）
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO ai_skills (`+skillCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		sk.ID, sk.TenantID, sk.Name, sk.Description, sk.Instructions, sk.Category, sk.UseCases, sk.Examples, sk.InstalledFrom, sk.Enabled, sk.CreatedAt, sk.UpdatedAt)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return skill.Skill{}, skill.ErrSkillExists
		}
		return skill.Skill{}, err
	}
	return sk, nil
}

func (s *Store) Update(ctx context.Context, sk skill.Skill) (skill.Skill, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return skill.Skill{}, err
	}
	if err := sk.Validate(); err != nil {
		return skill.Skill{}, err
	}
	sk.TenantID = tid
	sk.UpdatedAt = time.Now()
	ct, err := s.db.Pool().Exec(ctx,
		`UPDATE ai_skills SET name=$1, description=$2, instructions=$3, category=$4, use_cases=$5, examples=$6, installed_from=$7, enabled=$8, updated_at=$9
		 WHERE id=$10 AND tenant_id=$11`,
		sk.Name, sk.Description, sk.Instructions, sk.Category, sk.UseCases, sk.Examples, sk.InstalledFrom, sk.Enabled, sk.UpdatedAt, sk.ID, sk.TenantID)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return skill.Skill{}, skill.ErrSkillExists
		}
		return skill.Skill{}, err
	}
	if ct.RowsAffected() == 0 {
		return skill.Skill{}, skill.ErrSkillNotFound
	}
	return s.Get(ctx, sk.ID) // 回读 created_at
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	ct, err := s.db.Pool().Exec(ctx, `DELETE FROM ai_skills WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return skill.ErrSkillNotFound
	}
	return nil
}

func (s *Store) SkillsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM ai_skills`).Scan(&n)
	return n, err
}
