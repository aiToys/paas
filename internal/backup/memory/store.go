package memory

import (
	"context"
	"fmt"
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
	s.backups["bk-1"] = backup.Backup{
		ID: "bk-1", TenantID: "t-acme", ResourceID: "ds-db-acme",
		Type: backup.TypeFull, Status: backup.StatusCompleted, SizeMB: 128, CreatedAt: now,
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
