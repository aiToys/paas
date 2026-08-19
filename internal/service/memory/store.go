package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/service"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 是 service.Repository 的内存实现。
type Store struct {
	mu     sync.RWMutex
	items  map[string]service.Service // key=tenant|app|id
	byName map[string]string          // key=tenant|app|name → id 唯一索引
}

// NewStore 创建空仓储（无 seed，服务由用户自建或存量回填）。
func NewStore() *Store {
	return &Store{
		items:  map[string]service.Service{},
		byName: map[string]string{},
	}
}

func key(tid, appID, id string) string       { return tid + "|" + appID + "|" + id }
func nameKey(tid, appID, name string) string { return tid + "|" + appID + "|" + name }

// randID 生成带前缀的短 ID（crypto/rand 8 字节 hex，与各模块 memory 实现同源思路）。
func randID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error()) // 无熵优于弱 ID
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// clone 深拷贝（map/slice 复制，防调用方改写污染存储）。
func clone(s service.Service) service.Service {
	if s.BuildArgs != nil {
		m := make(map[string]string, len(s.BuildArgs))
		for k, v := range s.BuildArgs {
			m[k] = v
		}
		s.BuildArgs = m
	}
	if s.Env != nil {
		m := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			m[k] = v
		}
		s.Env = m
	}
	if s.Tools != nil {
		s.Tools = append([]string(nil), s.Tools...)
	}
	return s
}

func (s *Store) List(ctx context.Context, appID string) ([]service.Service, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]service.Service, 0)
	for _, it := range s.items {
		if it.TenantID == tid && it.AppID == appID {
			out = append(out, clone(it))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) Get(ctx context.Context, appID, id string) (service.Service, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return service.Service{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, hit := s.items[key(tid, appID, id)]
	if !hit {
		return service.Service{}, service.ErrNotFound
	}
	return clone(it), nil
}

func (s *Store) Create(ctx context.Context, in service.Service) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if in.ID == "" {
		in.ID = randID("svc")
	}
	in.TenantID = tid // 以 ctx 为准，忽略请求体
	in.CreatedAt = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tid, in.AppID, in.ID)
	if _, dup := s.items[k]; dup {
		return service.ErrExists
	}
	nk := nameKey(tid, in.AppID, in.Name)
	if _, dup := s.byName[nk]; dup {
		return service.ErrExists
	}
	s.items[k] = clone(in)
	s.byName[nk] = in.ID
	return nil
}

func (s *Store) Update(ctx context.Context, in service.Service) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tid, in.AppID, in.ID)
	old, hit := s.items[k]
	if !hit {
		return service.ErrNotFound
	}
	nk := nameKey(tid, in.AppID, in.Name)
	if owner, dup := s.byName[nk]; dup && owner != in.ID {
		return service.ErrExists
	}
	in.TenantID = tid
	in.CreatedAt = old.CreatedAt // 创建时间不可变
	delete(s.byName, nameKey(tid, in.AppID, old.Name))
	s.items[k] = clone(in)
	s.byName[nk] = in.ID
	return nil
}

func (s *Store) Delete(ctx context.Context, appID, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tid, appID, id)
	it, hit := s.items[k]
	if !hit {
		return service.ErrNotFound
	}
	delete(s.items, k)
	delete(s.byName, nameKey(tid, appID, it.Name))
	return nil
}

// GetOrCreateByName 按 (app, name) 取，无则建（幂等）。fill 可为 nil。
func (s *Store) GetOrCreateByName(ctx context.Context, appID, name, typ string, fill func(*service.Service)) (service.Service, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return service.Service{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nk := nameKey(tid, appID, name)
	if id, hit := s.byName[nk]; hit {
		return clone(s.items[key(tid, appID, id)]), nil
	}
	ns := service.Service{ID: randID("svc"), AppID: appID, Name: name, Type: typ}
	if fill != nil {
		fill(&ns)
	}
	if err := ns.Validate(); err != nil {
		return service.Service{}, err
	}
	ns.TenantID = tid
	ns.CreatedAt = time.Now()
	s.items[key(tid, appID, ns.ID)] = clone(ns)
	s.byName[nk] = ns.ID
	return clone(ns), nil
}
