package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/marketplace"
)

type Store struct {
	mu    sync.RWMutex
	items map[string]marketplace.Item
}

func NewStore() *Store { return &Store{items: make(map[string]marketplace.Item)} }

func (s *Store) List(ctx context.Context, entityType, category, q string) ([]marketplace.Item, error) {
	kw := marketplace.NormalizeQuery(q)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]marketplace.Item, 0)
	for _, it := range s.items {
		if entityType != "" && it.EntityType != entityType {
			continue
		}
		if category != "" && it.Category != category {
			continue
		}
		if kw != "" && !strings.Contains(strings.ToLower(it.Name+" "+it.Description), kw) {
			continue
		}
		out = append(out, it)
	}
	// 安装量降序 + 发布时间降序（热门优先，确定性输出便于测试）
	sortItems(out)
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (marketplace.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.items[id]
	if !ok {
		return marketplace.Item{}, marketplace.ErrItemNotFound
	}
	return it, nil
}

func (s *Store) Create(ctx context.Context, in marketplace.Item) (marketplace.Item, error) {
	if err := in.Validate(); err != nil {
		return marketplace.Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// upsert：同 entityType+name+publisher 覆盖（重发布）
	for id, it := range s.items {
		if it.EntityType == in.EntityType && it.Name == in.Name && it.PublisherTenant == in.PublisherTenant {
			delete(s.items, id)
		}
	}
	if in.ID == "" {
		in.ID = fmt.Sprintf("mk-%d", time.Now().UnixNano())
	}
	in.Installs = 0
	in.CreatedAt = time.Now()
	s.items[in.ID] = in
	return in, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return marketplace.ErrItemNotFound
	}
	delete(s.items, id)
	return nil
}

func (s *Store) IncInstalls(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return marketplace.ErrItemNotFound
	}
	it.Installs++
	s.items[id] = it
	return nil
}

func (s *Store) ListByPublisher(ctx context.Context, tenantID string) ([]marketplace.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]marketplace.Item, 0)
	for _, it := range s.items {
		if it.PublisherTenant == tenantID {
			out = append(out, it)
		}
	}
	sortItems(out)
	return out, nil
}

func (s *Store) ListAll(ctx context.Context) ([]marketplace.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]marketplace.Item, 0, len(s.items))
	for _, it := range s.items {
		out = append(out, it)
	}
	sortItems(out)
	return out, nil
}

func sortItems(items []marketplace.Item) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j-1], items[j]
			if a.Installs > b.Installs || (a.Installs == b.Installs && a.CreatedAt.After(b.CreatedAt)) {
				break
			}
			items[j-1], items[j] = b, a
		}
	}
}
