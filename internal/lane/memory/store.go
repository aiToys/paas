// Package memory 提供 lane.Repository 的内存实现（无 seed——泳道由用户创建或懒建）。
package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/lane"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 lane.Repository。
type Store struct {
	mu    sync.RWMutex
	lanes map[string]lane.Lane
	seq   int
}

// NewStore 创建仓储。
func NewStore() *Store {
	return &Store{lanes: map[string]lane.Lane{}}
}

// List 租户内泳道列表（按创建时间稳定排序）。
func (s *Store) List(ctx context.Context, envID string) ([]lane.Lane, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]lane.Lane, 0, len(s.lanes))
	for _, l := range s.lanes {
		if l.TenantID != tid {
			continue
		}
		if envID != "" && l.EnvID != envID {
			continue
		}
		out = append(out, l)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].CreatedAt.After(out[j].CreatedAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

// Get 按 ID 取（跨租户不泄漏）。
func (s *Store) Get(ctx context.Context, id string) (lane.Lane, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.lanes[id]
	if !ok || l.TenantID != tid {
		return lane.Lane{}, lane.ErrLaneNotFound
	}
	return l, nil
}

// GetByName 按 (envID, name) 取。
func (s *Store) GetByName(ctx context.Context, envID, name string) (lane.Lane, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.lanes {
		if l.TenantID == tid && l.EnvID == envID && l.Name == name {
			return l, nil
		}
	}
	return lane.Lane{}, lane.ErrLaneNotFound
}

// Create 创建（租户以 ctx 为准；唯一性查重与写入同临界区，无 TOCTOU）。
func (s *Store) Create(ctx context.Context, in lane.Lane) (lane.Lane, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	if err := in.Validate(); err != nil {
		return lane.Lane{}, err
	}
	if in.Status == "" {
		in.Status = lane.StatusActive
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lanes {
		if l.TenantID == tid && l.EnvID == in.EnvID && l.Name == in.Name {
			return lane.Lane{}, lane.ErrLaneExists
		}
	}
	s.seq++
	now := time.Now()
	in.ID = fmt.Sprintf("lane-%d", s.seq)
	in.TenantID = tid
	in.CreatedAt = now
	in.UpdatedAt = now
	s.lanes[in.ID] = in
	return in, nil
}

// Update 更新可变字段（mode/description/externalLink；name/envID 不可改）。
func (s *Store) Update(ctx context.Context, id string, in lane.Lane) (lane.Lane, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.getLocked(ctx, id)
	if err != nil {
		return lane.Lane{}, err
	}
	next := cur
	if in.Mode != "" {
		if _, ok := map[string]struct{}{lane.ModeStandard: {}, lane.ModePermanent: {}}[in.Mode]; !ok {
			return lane.Lane{}, errors.New("mode 非法（standard|permanent）")
		}
		next.Mode = in.Mode
	}
	next.Description = in.Description
	next.ExternalLink = in.ExternalLink
	next.UpdatedAt = time.Now()
	s.lanes[id] = next
	return next, nil
}

// getLocked 读（调用方已持锁；跨租户返 ErrLaneNotFound 不泄漏）。
func (s *Store) getLocked(ctx context.Context, id string) (lane.Lane, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	l, ok := s.lanes[id]
	if !ok || l.TenantID != tid {
		return lane.Lane{}, lane.ErrLaneNotFound
	}
	return l, nil
}

// Close 关闭（幂等）。
func (s *Store) Close(ctx context.Context, id string) (lane.Lane, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.getLocked(ctx, id)
	if err != nil {
		return lane.Lane{}, err
	}
	if cur.Status == lane.StatusClosed {
		return cur, nil // 幂等
	}
	cur.Status = lane.StatusClosed
	cur.UpdatedAt = time.Now()
	s.lanes[id] = cur
	return cur, nil
}

// EnsureByName 存在返回既有（不覆盖 permanent），不存在懒建 standard（锁内查建原子）。
func (s *Store) EnsureByName(ctx context.Context, envID, name string) (lane.Lane, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return lane.Lane{}, err
	}
	if err := lane.ValidateName(name); err != nil {
		return lane.Lane{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lanes {
		if l.TenantID == tid && l.EnvID == envID && l.Name == name {
			return l, nil
		}
	}
	s.seq++
	now := time.Now()
	in := lane.Lane{
		ID: fmt.Sprintf("lane-%d", s.seq), TenantID: tid, EnvID: envID, Name: name,
		Mode: lane.ModeStandard, Status: lane.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	s.lanes[in.ID] = in
	return in, nil
}
