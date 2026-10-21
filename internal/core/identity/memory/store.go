// Package memory 提供 identity.Repository 的内存实现，
// 供本 plan 阶段的 Core 启动与测试使用；Plan 2 替换为 PostgreSQL 实现。
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

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

// cloneUser 深拷贝用户（Roles 切片底层数组独立），防止返回值与 store 内部共享引发并发读写 race。
func cloneUser(u identity.User) identity.User {
	u.Roles = append([]string(nil), u.Roles...)
	return u
}

// cloneAPIKey 深拷贝 API Key（Roles 同 cloneUser）。LookupAPIKey 是每请求热路径，仅拷一层。
func cloneAPIKey(k identity.APIKey) identity.APIKey {
	k.Roles = append([]string(nil), k.Roles...)
	return k
}

func (s *Store) CreateTenant(_ context.Context, t identity.Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenants[t.ID]; exists {
		return fmt.Errorf("租户已存在: %s", t.ID)
	}
	if t.Status == "" {
		t.Status = identity.TenantStatusActive
	}
	s.tenants[t.ID] = t
	return nil
}

func (s *Store) GetTenant(_ context.Context, id string) (identity.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return identity.Tenant{}, fmt.Errorf("%w: %s", identity.ErrTenantNotFound, id)
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
			out = append(out, cloneUser(u))
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
			uu := cloneUser(u)
			return &uu, nil
		}
	}
	return nil, fmt.Errorf("用户不存在: %s", name)
}

// GetUser 取单个用户（租户内隔离）；tenantID 为空时跨租户按用户 ID 定位
// （与 DeleteUser / RevokeUserSessions 同约定，供平台超管路径使用）。
func (s *Store) GetUser(_ context.Context, tenantID, userID string) (*identity.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[userID]
	if !ok || (tenantID != "" && u.TenantID != tenantID) {
		return nil, fmt.Errorf("用户不存在: %s", userID)
	}
	uu := cloneUser(u)
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
	// 非空保护：有用户拒绝，引导先清用户（防孤儿 + 防误删）
	for _, u := range s.users {
		if u.TenantID == id {
			return fmt.Errorf("%w: %s", identity.ErrTenantNotEmpty, id)
		}
	}
	delete(s.tenants, id)
	// 级联清 API Key（用户已无；Key 防御性清孤儿）
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
			out = append(out, cloneUser(u))
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
	// 保留原 CreatedAt；PasswordHash 空则保留原值（非空=handler 传入新 hash，更新密码）。
	// SessionVersion 只由 RevokeUserSessions 变更（与 PG 实现一致：UPDATE 不触该列），
	// 若此处跟随入参归零会让已撤销的会话重新获得「版本 0」的合法身份。
	cur := s.users[u.ID]
	if u.PasswordHash == "" {
		u.PasswordHash = cur.PasswordHash
	}
	u.CreatedAt = cur.CreatedAt
	u.SessionVersion = cur.SessionVersion
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
	// 级联吊销该用户的 API Key：LookupAPIKey 不校验用户存在性，
	// 不删则已删用户的凭证永久有效（与 DeleteTenant 级联语义对称）。
	for key, k := range s.apiKeys {
		if k.UserID == userID {
			delete(s.apiKeys, key)
		}
	}
	return nil
}

func (s *Store) ListAPIKeys(_ context.Context, tenantID string) ([]identity.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []identity.APIKey
	for _, k := range s.apiKeys {
		if tenantID == "" || k.TenantID == tenantID {
			out = append(out, cloneAPIKey(k))
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
	if err := k.Usable(time.Now()); err != nil {
		return identity.APIKey{}, err
	}
	// 冻结租户的密钥一律拒绝：控制面准入关闭，数据面已部署业务不受影响。
	// 孤儿密钥（租户行已删）与冻结同判：fail-closed，与 PG 实现行为一致（R5 审计 P2）。
	if t, ok := s.tenants[k.TenantID]; !ok || t.IsFrozen() {
		return identity.APIKey{}, identity.ErrTenantFrozen
	}
	return cloneAPIKey(k), nil
}

// —— 生命周期（D5）——

func (s *Store) SetTenantStatus(_ context.Context, id, status, reason string) (identity.Tenant, error) {
	if status != identity.TenantStatusActive && status != identity.TenantStatusFrozen {
		return identity.Tenant{}, fmt.Errorf("非法租户状态: %s", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[id]
	if !ok {
		return identity.Tenant{}, identity.ErrTenantNotFound
	}
	if status == identity.TenantStatusFrozen {
		now := time.Now()
		t.FrozenAt = &now
		t.FrozenReason = reason
	} else {
		t.FrozenAt = nil
		t.FrozenReason = ""
	}
	t.Status = status
	s.tenants[id] = t
	return t, nil
}

func (s *Store) GetAPIKey(_ context.Context, id string) (identity.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.apiKeys {
		if k.ID == id {
			return cloneAPIKey(k), nil
		}
	}
	return identity.APIKey{}, fmt.Errorf("API Key 不存在: %s", id)
}

func (s *Store) TouchAPIKeyUsage(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, k := range s.apiKeys {
		if k.ID == id {
			k.LastUsedAt = &at
			s.apiKeys[key] = k
			return nil
		}
	}
	return nil // 已删除的 Key 不再回写（不视为错误）
}

func (s *Store) RevokeAPIKey(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, k := range s.apiKeys {
		if k.ID == id {
			k.RevokedAt = &at
			s.apiKeys[key] = k
			return nil
		}
	}
	return fmt.Errorf("API Key 不存在: %s", id)
}

func (s *Store) SetAPIKeyExpiry(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, k := range s.apiKeys {
		if k.ID == id {
			if at.IsZero() {
				k.ExpiresAt = nil
			} else {
				k.ExpiresAt = &at
			}
			s.apiKeys[key] = k
			return nil
		}
	}
	return fmt.Errorf("API Key 不存在: %s", id)
}

// RevokeUserSessions 撤销用户已签发的全部会话：session_version+1 并返回新值。
// 已签发 token 携带旧代次，下次校验即失配（网关缓存窗口 ≤30s，续期立即拒绝）。
func (s *Store) RevokeUserSessions(_ context.Context, tenantID, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok || (tenantID != "" && u.TenantID != tenantID) {
		return 0, fmt.Errorf("用户不存在: %s", userID)
	}
	u.SessionVersion++
	s.users[userID] = u
	return u.SessionVersion, nil
}
