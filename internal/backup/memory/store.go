package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/backup"
)

// Store 是 backup.Repository 的内存实现。
type Store struct {
	mu      sync.RWMutex
	backups map[string]backup.Backup
}

func NewStore() *Store {
	s := &Store{backups: map[string]backup.Backup{}}
	now := time.Now()
	// seed 是存量记录模式（mode=record：仅记录无产物，SizeMB 为历史模拟值）。
	s.backups["bk-1"] = backup.Backup{
		ID: "bk-1", TenantID: "t-acme", ResourceID: "ds-db-acme",
		Type: backup.TypeFull, Mode: backup.ModeRecord, Status: backup.StatusCompleted, SizeMB: 128, CreatedAt: now,
	}
	return s
}

func (s *Store) List(_ context.Context, tenantID, resourceID string) ([]backup.Backup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []backup.Backup
	for _, b := range s.backups {
		if b.TenantID == tenantID && (resourceID == "" || b.ResourceID == resourceID) {
			out = append(out, b)
		}
	}
	// 与 PG 实现同序（created_at DESC, id），map 遍历序随机否则前端列表顺序不稳定。
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) Get(_ context.Context, tenantID, id string) (backup.Backup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.backups[id]
	if !ok || b.TenantID != tenantID {
		return backup.Backup{}, fmt.Errorf("备份不存在: %s", id)
	}
	return b, nil
}

func (s *Store) Create(_ context.Context, b backup.Backup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ID == "" {
		return fmt.Errorf("backup id 必填")
	}
	if _, exists := s.backups[b.ID]; exists {
		return fmt.Errorf("备份已存在: %s", b.ID)
	}
	s.backups[b.ID] = b
	return nil
}

func (s *Store) Delete(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.backups[id]
	if !ok || b.TenantID != tenantID {
		return fmt.Errorf("备份不存在: %s", id)
	}
	delete(s.backups, id)
	return nil
}

// UpdateProgress 推进真实作业进度（指针字段非 nil 才更新）。
func (s *Store) UpdateProgress(_ context.Context, tenantID, id string, up backup.ProgressUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.backups[id]
	if !ok || b.TenantID != tenantID {
		return fmt.Errorf("备份不存在: %s", id)
	}
	if up.Status != nil {
		b.Status = *up.Status
	}
	if up.SizeBytes != nil {
		b.SizeBytes = *up.SizeBytes
	}
	if up.Checksum != nil {
		b.Checksum = *up.Checksum
	}
	if up.ArtifactRef != nil {
		b.ArtifactRef = *up.ArtifactRef
	}
	if up.Error != nil {
		b.Error = *up.Error
	}
	if up.VerifiedAt != nil {
		b.VerifiedAt = *up.VerifiedAt
	}
	if up.FinishedAt != nil {
		b.FinishedAt = *up.FinishedAt
	}
	s.backups[id] = b
	return nil
}

// ListAll 跨租户列出全部记录（admin 平台总览，不过滤 tenant），与 List 同序。
func (s *Store) ListAll(_ context.Context) ([]backup.Backup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]backup.Backup, 0, len(s.backups))
	for _, b := range s.backups {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ListActive 列出全部非终态记录（Pacer 恢复扫描，跨租户管理面专用）。
func (s *Store) ListActive(_ context.Context) ([]backup.Backup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []backup.Backup
	for _, b := range s.backups {
		if !b.Terminal() {
			out = append(out, b)
		}
	}
	return out, nil
}
