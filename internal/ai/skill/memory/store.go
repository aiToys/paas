package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/pkg/tenant"
)

type Store struct {
	mu     sync.RWMutex
	skills map[string]skill.Skill
}

func NewStore() *Store { return &Store{skills: make(map[string]skill.Skill)} }

func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return "", skill.ErrSkillNotFound
	}
	return tid, nil
}

func (s *Store) List(ctx context.Context) ([]skill.Skill, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]skill.Skill, 0)
	for _, sk := range s.skills {
		if sk.TenantID == tid {
			out = append(out, sk)
		}
	}
	return out, nil
}

func (s *Store) ListAll(ctx context.Context) ([]skill.Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]skill.Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		out = append(out, sk)
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (skill.Skill, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return skill.Skill{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[id]
	if !ok || sk.TenantID != tid {
		return skill.Skill{}, skill.ErrSkillNotFound
	}
	return sk, nil
}

func (s *Store) Create(ctx context.Context, in skill.Skill) (skill.Skill, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return skill.Skill{}, err
	}
	if err := in.Validate(); err != nil {
		return skill.Skill{}, err
	}
	if in.ID == "" {
		in.ID = "skill-" + randID()
	}
	in.TenantID = tid
	now := time.Now()
	in.CreatedAt, in.UpdatedAt = now, now
	in.Enabled = true
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sk := range s.skills {
		if sk.TenantID == tid && sk.Name == in.Name {
			return skill.Skill{}, skill.ErrSkillExists
		}
	}
	s.skills[in.ID] = in
	return in, nil
}

func (s *Store) Update(ctx context.Context, in skill.Skill) (skill.Skill, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return skill.Skill{}, err
	}
	if err := in.Validate(); err != nil {
		return skill.Skill{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.skills[in.ID]
	if !ok || old.TenantID != tid {
		return skill.Skill{}, skill.ErrSkillNotFound
	}
	for id, sk := range s.skills {
		if id != in.ID && sk.TenantID == tid && sk.Name == in.Name {
			return skill.Skill{}, skill.ErrSkillExists
		}
	}
	in.TenantID = tid
	in.CreatedAt = old.CreatedAt
	in.UpdatedAt = time.Now()
	s.skills[in.ID] = in
	return in, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.skills[id]
	if !ok || sk.TenantID != tid {
		return skill.ErrSkillNotFound
	}
	delete(s.skills, id)
	return nil
}

func (s *Store) SkillsCount(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.skills), nil
}
