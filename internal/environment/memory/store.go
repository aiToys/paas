package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 是 environment.Repository 的内存实现。
type Store struct {
	mu   sync.RWMutex
	envs map[string]environment.Environment
}

// NewStore 创建仓储并 seed 三环境（acme/globex 各见自己的，seed 按租户分）。
func NewStore() *Store {
	s := &Store{envs: map[string]environment.Environment{}}
	for _, e := range seed() {
		s.envs[e.ID] = e
	}
	return s
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	return tid, nil
}

func (s *Store) List(ctx context.Context) ([]environment.Environment, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]environment.Environment, 0)
	for _, e := range s.envs {
		if e.TenantID == tid {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (environment.Environment, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return environment.Environment{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, hit := s.envs[id]
	if !hit || e.TenantID != tid {
		return environment.Environment{}, fmt.Errorf("环境不存在: %s", id)
	}
	return e, nil
}

func (s *Store) Create(ctx context.Context, e environment.Environment) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" {
		return fmt.Errorf("环境 ID 不能为空")
	}
	if _, exists := s.envs[e.ID]; exists {
		return fmt.Errorf("环境已存在: %s", e.ID)
	}
	e.TenantID = tid
	s.envs[e.ID] = e
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, hit := s.envs[id]
	if !hit || e.TenantID != tid {
		return fmt.Errorf("环境不存在: %s", id)
	}
	delete(s.envs, id)
	return nil
}

// EnvType 返回环境类型（prod|test），跨租户访问同样返回错误（不泄漏）。
func (s *Store) EnvType(ctx context.Context, id string) (string, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return e.Type, nil
}

// SeedEnvs 返回预置环境数据（PG seed 复用，DRY：内存/PG 同一真源）。
// acme: env-acme-prod-bj / env-acme-prod-sh / env-acme-test
// globex: env-globex-prod / env-globex-test
func SeedEnvs() []environment.Environment {
	return seed()
}

// seed 生成跨两租户的环境。
func seed() []environment.Environment {
	t := time.Now()
	return []environment.Environment{
		{ID: "env-acme-prod-bj", TenantID: "t-acme", Name: "生产-北京", Type: environment.TypeProd, Cluster: "prod-bj", CreatedAt: t},
		{ID: "env-acme-prod-sh", TenantID: "t-acme", Name: "生产-上海", Type: environment.TypeProd, Cluster: "prod-sh", CreatedAt: t},
		{ID: "env-acme-test", TenantID: "t-acme", Name: "测试", Type: environment.TypeTest, CreatedAt: t},
		{ID: "env-globex-prod", TenantID: "t-globex", Name: "生产", Type: environment.TypeProd, Cluster: "prod-aws", CreatedAt: t},
		{ID: "env-globex-test", TenantID: "t-globex", Name: "测试", Type: environment.TypeTest, CreatedAt: t},
	}
}
