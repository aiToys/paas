package memory

import (
	"context"
	"fmt"
	"sort"

	"github.com/aitoys/paas/internal/dataservice"
)

// Engine memory 实现（平台级，无租户）。复用 Store.mu 防并发；engines 在 NewStore 时 seed。
// 读返深拷 Connection map（隔离）；写深拷入参。

// ListEngines 按 Order 升序返回全部引擎（含 disabled，admin 看全量）。
func (s *Store) ListEngines(_ context.Context) ([]dataservice.Engine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]dataservice.Engine, 0, len(s.engines))
	for _, e := range s.engines {
		e.Connection = cloneStrMap(e.Connection)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) GetEngine(_ context.Context, id string) (dataservice.Engine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.engines[id]
	if !ok {
		return dataservice.Engine{}, fmt.Errorf("引擎不存在: %s", id)
	}
	e.Connection = cloneStrMap(e.Connection)
	return e, nil
}

func (s *Store) CreateEngine(_ context.Context, e dataservice.Engine) (dataservice.Engine, error) {
	if err := e.Validate(); err != nil {
		return dataservice.Engine{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.engines[e.ID]; ok {
		return dataservice.Engine{}, fmt.Errorf("引擎已存在: %s", e.ID)
	}
	e.Connection = cloneStrMap(e.Connection)
	s.engines[e.ID] = e
	return e, nil
}

func (s *Store) UpdateEngine(_ context.Context, e dataservice.Engine) (dataservice.Engine, error) {
	if err := e.Validate(); err != nil {
		return dataservice.Engine{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.engines[e.ID]; !ok {
		return dataservice.Engine{}, fmt.Errorf("引擎不存在: %s", e.ID)
	}
	e.Connection = cloneStrMap(e.Connection)
	s.engines[e.ID] = e
	return e, nil
}

func (s *Store) DeleteEngine(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.engines[id]; !ok {
		return fmt.Errorf("引擎不存在: %s", id)
	}
	delete(s.engines, id)
	return nil
}

func (s *Store) EnginesCount(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.engines), nil
}
