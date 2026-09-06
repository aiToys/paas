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

// cloneItem 深拷贝广场条目（Snapshot 字节切片独立）。快照 payload（AgentSnapshot/WorkflowSnapshot
// 的内嵌 skills/tools 等）以 JSON 字节形态存于 Snapshot，拷字节即完整隔离，防并发读写 race。
func cloneItem(it marketplace.Item) marketplace.Item {
	it.Snapshot = append([]byte(nil), it.Snapshot...)
	return it
}

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
		out = append(out, cloneItem(it))
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
	return cloneItem(it), nil
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
	s.items[in.ID] = cloneItem(in) // 存与返回双向深拷贝（与 pipeline cloneRun 同款）
	return cloneItem(in), nil
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
			out = append(out, cloneItem(it))
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
		out = append(out, cloneItem(it))
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
