package memory

import (
	"context"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store prompt.Repository 内存实现。
type Store struct {
	mu      sync.RWMutex
	prompts map[string]prompt.Prompt // id -> Prompt
}

func NewStore() *Store { return &Store{prompts: make(map[string]prompt.Prompt)} }

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return "", prompt.ErrPromptNotFound
	}
	return tid, nil
}

func clone(p prompt.Prompt) prompt.Prompt {
	v := make([]string, len(p.Variables))
	copy(v, p.Variables)
	p.Variables = v
	return p
}

func (s *Store) List(ctx context.Context) ([]prompt.Prompt, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]prompt.Prompt, 0)
	for _, p := range s.prompts {
		if p.TenantID == tid {
			out = append(out, clone(p))
		}
	}
	return out, nil
}

// ListAll admin 跨租户列表（带 TenantID）。
func (s *Store) ListAll(ctx context.Context) ([]prompt.Prompt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]prompt.Prompt, 0, len(s.prompts))
	for _, p := range s.prompts {
		out = append(out, clone(p))
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (prompt.Prompt, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.prompts[id]
	if !ok || p.TenantID != tid {
		return prompt.Prompt{}, prompt.ErrPromptNotFound
	}
	return clone(p), nil
}

// Create 同 name → version = max+1，自动激活（旧版 deactive）。
func (s *Store) Create(ctx context.Context, p prompt.Prompt) (prompt.Prompt, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	if err := p.Validate(); err != nil {
		return prompt.Prompt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maxVer := 0
	for _, ex := range s.prompts {
		if ex.TenantID == tid && ex.Name == p.Name && ex.Version > maxVer {
			maxVer = ex.Version
		}
	}
	p.Version = maxVer + 1
	p.ID = "prompt-" + randID()
	p.TenantID = tid
	p.Active = true // 新版本自动激活
	p.CreatedAt = time.Now()
	// 同 name 旧版 deactive
	for id, ex := range s.prompts {
		if ex.TenantID == tid && ex.Name == p.Name && ex.Active {
			ex.Active = false
			s.prompts[id] = ex
		}
	}
	s.prompts[p.ID] = p
	return clone(p), nil
}

// SetActive 激活某版本（同 name 其他 deactive）。
func (s *Store) SetActive(ctx context.Context, id string) (prompt.Prompt, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.prompts[id]
	if !ok || target.TenantID != tid {
		return prompt.Prompt{}, prompt.ErrPromptNotFound
	}
	for pid, ex := range s.prompts {
		if ex.TenantID == tid && ex.Name == target.Name {
			ex.Active = (pid == id)
			s.prompts[pid] = ex
		}
	}
	return clone(s.prompts[id]), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prompts[id]
	if !ok || p.TenantID != tid {
		return prompt.ErrPromptNotFound
	}
	delete(s.prompts, id)
	// 删的是 active 版本：若有其他版本，激活最新版（max version）
	if p.Active {
		var latestID string
		var latestVer int
		for pid, ex := range s.prompts {
			if ex.TenantID == tid && ex.Name == p.Name && ex.Version > latestVer {
				latestVer = ex.Version
				latestID = pid
			}
		}
		if latestID != "" {
			ex := s.prompts[latestID]
			ex.Active = true
			s.prompts[latestID] = ex
		}
	}
	return nil
}

func (s *Store) GetActive(ctx context.Context, name string) (prompt.Prompt, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return prompt.Prompt{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.prompts {
		if p.TenantID == tid && p.Name == name && p.Active {
			return clone(p), nil
		}
	}
	return prompt.Prompt{}, prompt.ErrNoActivePrompt
}

func (s *Store) PromptsCount(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.prompts), nil
}
