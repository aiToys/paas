package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/pkg/tenant"
)

type Store struct {
	mu     sync.RWMutex
	agents map[string]agent.Agent
}

func NewStore() *Store { return &Store{agents: make(map[string]agent.Agent)} }

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
		return "", agent.ErrAgentNotFound
	}
	return tid, nil
}

func clone(a agent.Agent) agent.Agent {
	t := make([]string, len(a.Tools))
	copy(t, a.Tools)
	k := make([]string, len(a.KnowledgeBases))
	copy(k, a.KnowledgeBases)
	s := make([]string, len(a.Skills))
	copy(s, a.Skills)
	a.Tools, a.KnowledgeBases, a.Skills = t, k, s
	return a
}

func (s *Store) List(ctx context.Context) ([]agent.Agent, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]agent.Agent, 0)
	for _, a := range s.agents {
		if a.TenantID == tid {
			out = append(out, clone(a))
		}
	}
	return out, nil
}

// ListAll admin 跨租户列表（带 TenantID）。
func (s *Store) ListAll(ctx context.Context) ([]agent.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]agent.Agent, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, clone(a))
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (agent.Agent, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok || a.TenantID != tid {
		return agent.Agent{}, agent.ErrAgentNotFound
	}
	return clone(a), nil
}

func (s *Store) Create(ctx context.Context, a agent.Agent) (agent.Agent, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	if err := a.Validate(); err != nil {
		return agent.Agent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.agents {
		if ex.TenantID == tid && ex.Name == a.Name {
			return agent.Agent{}, agent.ErrAgentExists
		}
	}
	if a.ID == "" {
		a.ID = "agent-" + randID()
	}
	if a.MaxSteps == 0 {
		a.MaxSteps = agent.DefaultMaxSteps
	}
	a.Enabled = true // 创建即启用（与平台「创建即可用」惯例一致；用户可 Update 关闭）
	a.TenantID = tid
	now := time.Now()
	a.CreatedAt, a.UpdatedAt = now, now
	s.agents[a.ID] = a
	return clone(a), nil
}

func (s *Store) Update(ctx context.Context, a agent.Agent) (agent.Agent, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	if err := a.Validate(); err != nil {
		return agent.Agent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.agents[a.ID]
	if !ok || ex.TenantID != tid {
		return agent.Agent{}, agent.ErrAgentNotFound
	}
	for id, o := range s.agents {
		if o.TenantID == tid && o.Name == a.Name && id != a.ID {
			return agent.Agent{}, agent.ErrAgentExists
		}
	}
	if a.MaxSteps == 0 {
		a.MaxSteps = agent.DefaultMaxSteps
	}
	a.TenantID = tid
	a.CreatedAt = ex.CreatedAt
	a.UpdatedAt = time.Now()
	s.agents[a.ID] = a
	return clone(a), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok || a.TenantID != tid {
		return agent.ErrAgentNotFound
	}
	delete(s.agents, id)
	return nil
}

func (s *Store) AgentsCount(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.agents), nil
}
