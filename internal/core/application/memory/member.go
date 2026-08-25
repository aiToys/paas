// 应用成员（应用级权限）的内存实现。key: appID+"\x00"+userID。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/pkg/tenant"
)

// MemberStore 是 application.MemberRepository 的内存实现。
type MemberStore struct {
	mu      sync.RWMutex
	members map[string]application.Member
}

// NewMemberStore 创建成员仓储。
func NewMemberStore() *MemberStore {
	return &MemberStore{members: map[string]application.Member{}}
}

func memberKey(appID, userID string) string { return appID + "\x00" + userID }

func (s *MemberStore) ListMembers(ctx context.Context, appID string) ([]application.Member, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("missing tenant context")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]application.Member, 0, 4)
	for _, m := range s.members {
		if m.AppID == appID && m.TenantID == tid {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func (s *MemberStore) ListAllMembers(ctx context.Context) ([]application.Member, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]application.Member, 0, len(s.members))
	for _, m := range s.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].AppID < out[j].AppID
	})
	return out, nil
}

func (s *MemberStore) GetMember(ctx context.Context, appID, userID string) (application.Member, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return application.Member{}, fmt.Errorf("missing tenant context")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, hit := s.members[memberKey(appID, userID)]
	if !hit || m.TenantID != tid {
		return application.Member{}, application.ErrMemberNotFound
	}
	return m, nil
}

func (s *MemberStore) AddMember(ctx context.Context, m application.Member) error {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return fmt.Errorf("missing tenant context")
	}
	if !application.ValidAppRole(m.Role) {
		return application.ErrInvalidRole
	}
	if m.ID == "" {
		m.ID = fmt.Sprintf("mb-%d", time.Now().UnixNano())
	}
	m.TenantID = tid
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[memberKey(m.AppID, m.UserID)] = m
	return nil
}

func (s *MemberStore) RemoveMember(ctx context.Context, appID, userID string) error {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return fmt.Errorf("missing tenant context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, hit := s.members[memberKey(appID, userID)]
	if !hit || m.TenantID != tid {
		return application.ErrMemberNotFound
	}
	delete(s.members, memberKey(appID, userID))
	return nil
}

func (s *MemberStore) RemoveAppMembers(ctx context.Context, appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, m := range s.members {
		if m.AppID == appID {
			delete(s.members, k)
		}
	}
	return nil
}

func (s *MemberStore) MemberRole(ctx context.Context, appID, userID string) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, hit := s.members[memberKey(appID, userID)]
	if !hit || m.TenantID != tid {
		return "", nil
	}
	return m.Role, nil
}
