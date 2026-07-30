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

// GetUserByName 按登录用户名查；本期全局唯一。
func (s *Store) GetUserByName(_ context.Context, name string) (*identity.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Name == name {
			uu := u
			return &uu, nil
		}
	}
	return nil, fmt.Errorf("用户不存在: %s", name)
}

// GetUser 取单个用户（租户内隔离）。
func (s *Store) GetUser(_ context.Context, tenantID, userID string) (*identity.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[userID]
	if !ok || u.TenantID != tenantID {
		return nil, fmt.Errorf("用户不存在: %s", userID)
	}
	uu := u
	return &uu, nil
}

// —— 平台级管理方法（跨租户；handler 强制 tenant:admin）——

func (s *Store) ListTenants(_ context.Context) ([]identity.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]identity.Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) DeleteTenant(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[id]; !ok {
		return fmt.Errorf("租户不存在: %s", id)
	}
	delete(s.tenants, id)
	// 级联清用户/API Key
	for uid, u := range s.users {
		if u.TenantID == id {
			delete(s.users, uid)
		}
	}
	for key, k := range s.apiKeys {
		if k.TenantID == id {
			delete(s.apiKeys, key)
		}
	}
	return nil
}

func (s *Store) ListUsers(_ context.Context, tenantID string) ([]identity.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []identity.User
	for _, u := range s.users {
		if tenantID == "" || u.TenantID == tenantID {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *Store) UpdateUser(_ context.Context, u identity.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.ID]; !ok {
		return fmt.Errorf("用户不存在: %s", u.ID)
	}
	// 保留原 CreatedAt；PasswordHash 空则保留原值（非空=handler 传入新 hash，更新密码）
	cur := s.users[u.ID]
	if u.PasswordHash == "" {
		u.PasswordHash = cur.PasswordHash
	}
	u.CreatedAt = cur.CreatedAt
	if u.TenantID == "" {
		u.TenantID = cur.TenantID
	}
	if u.Name == "" {
		u.Name = cur.Name
	}
	s.users[u.ID] = u
	return nil
}

func (s *Store) DeleteUser(_ context.Context, tenantID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok || (tenantID != "" && u.TenantID != tenantID) {
		return fmt.Errorf("用户不存在: %s", userID)
	}
	delete(s.users, userID)
	return nil
}

func (s *Store) ListAPIKeys(_ context.Context, tenantID string) ([]identity.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []identity.APIKey
	for _, k := range s.apiKeys {
		if tenantID == "" || k.TenantID == tenantID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *Store) DeleteAPIKey(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// id 是 APIKey.ID（非 bearer key）
	found := false
	for key, k := range s.apiKeys {
		if k.ID == id {
			delete(s.apiKeys, key)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("API Key 不存在: %s", id)
	}
	return nil
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
