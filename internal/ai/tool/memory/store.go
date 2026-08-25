package memory

import (
	"context"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store tool.Repository 内存实现（seed + 集成测试用）。
// 全方法租户强制过滤（ctx 无 tenant 返 ErrNoTenant），跨租户 not found 不泄漏。
// 读返深拷贝（Config map 防外部修改污染）。
type Store struct {
	mu    sync.RWMutex
	tools map[string]tool.Tool // id -> Tool
}

func NewStore() *Store { return &Store{tools: make(map[string]tool.Tool)} }

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return "", tool.ErrToolNotFound // 跨租户 not found 不泄漏：无 tenant 统一 not found
	}
	return tid, nil
}

func clone(t tool.Tool) tool.Tool {
	cfg := make(map[string]string, len(t.Config))
	for k, v := range t.Config {
		cfg[k] = v
	}
	t.Config = cfg
	return t
}

func (s *Store) List(ctx context.Context) ([]tool.Tool, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]tool.Tool, 0)
	for _, t := range s.tools {
		if t.TenantID == tid {
			out = append(out, clone(t))
		}
	}
	return out, nil
}

// ListAll admin 跨租户列表（带 TenantID）。
func (s *Store) ListAll(ctx context.Context) ([]tool.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]tool.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, clone(t))
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (tool.Tool, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return tool.Tool{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tools[id]
	if !ok || t.TenantID != tid {
		return tool.Tool{}, tool.ErrToolNotFound
	}
	return clone(t), nil
}

func (s *Store) Create(ctx context.Context, t tool.Tool) (tool.Tool, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return tool.Tool{}, err
	}
	if err := t.Validate(); err != nil {
		return tool.Tool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.tools {
		if ex.TenantID == tid && ex.Name == t.Name {
			return tool.Tool{}, tool.ErrToolExists
		}
	}
	if t.ID == "" {
		t.ID = "tool-" + randID()
	}
	t.TenantID = tid
	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	s.tools[t.ID] = t
	return clone(t), nil
}

func (s *Store) Update(ctx context.Context, t tool.Tool) (tool.Tool, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return tool.Tool{}, err
	}
	if err := t.Validate(); err != nil {
		return tool.Tool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.tools[t.ID]
	if !ok || ex.TenantID != tid {
		return tool.Tool{}, tool.ErrToolNotFound
	}
	// name 唯一性（排除自身）
	for _, o := range s.tools {
		if o.TenantID == tid && o.Name == t.Name && o.ID != t.ID {
			return tool.Tool{}, tool.ErrToolExists
		}
	}
	t.TenantID = tid
	t.CreatedAt = ex.CreatedAt
	t.UpdatedAt = time.Now()
	s.tools[t.ID] = t
	return clone(t), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tools[id]
	if !ok || t.TenantID != tid {
		return tool.ErrToolNotFound
	}
	delete(s.tools, id)
	return nil
}

// ToolsCount 全表（不经 tenant，seed 判空用）。
func (s *Store) ToolsCount(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tools), nil
}
