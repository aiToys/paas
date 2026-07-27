// Package memory 提供 identity.Repository 的内存实现，
// 供本 plan 阶段的 Core 启动与测试使用；Plan 2 替换为 PostgreSQL 实现。
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/aitoys/paas/internal/core/identity"
)

// Store 是 identity.Repository 的内存实现。
type Store struct {
	mu      sync.RWMutex
	tenants map[string]identity.Tenant
	users   map[string]identity.User
	apiKeys map[string]identity.APIKey // 按 bearer key 索引
}

// NewStore 创建空仓储。
func NewStore() *Store {
	return &Store{
		tenants: map[string]identity.Tenant{},
		users:   map[string]identity.User{},
		apiKeys: map[string]identity.APIKey{},
	}
}

func (s *Store) CreateTenant(_ context.Context, t identity.Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenants[t.ID]; exists {
		return fmt.Errorf("租户已存在: %s", t.ID)
	}
	s.tenants[t.ID] = t
	return nil
}

func (s *Store) GetTenant(_ context.Context, id string) (identity.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return identity.Tenant{}, fmt.Errorf("租户不存在: %s", id)
	}
	return t, nil
}

func (s *Store) CreateUser(_ context.Context, u identity.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[u.ID]; exists {
		return fmt.Errorf("用户已存在: %s", u.ID)
	}
	s.users[u.ID] = u
	return nil
}

func (s *Store) UsersByTenant(_ context.Context, tenantID string) ([]identity.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []identity.User
	for _, u := range s.users {
		if u.TenantID == tenantID {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *Store) CreateAPIKey(_ context.Context, k identity.APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k.Key == "" {
		return fmt.Errorf("api key 不能为空")
	}
	if _, exists := s.apiKeys[k.Key]; exists {
		return fmt.Errorf("api key 已存在")
	}
	s.apiKeys[k.Key] = k
	return nil
}

func (s *Store) LookupAPIKey(_ context.Context, key string) (identity.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.apiKeys[key]
	if !ok {
		return identity.APIKey{}, fmt.Errorf("api key 不存在")
	}
	return k, nil
}
