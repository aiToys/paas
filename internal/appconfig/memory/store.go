// Package memory 提供 appconfig.Repository 的内存实现，seed 跨两租户示例配置。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/pkg/tenant"
)

type Store struct {
	mu    sync.RWMutex
	items map[string]appconfig.ConfigItem
	idSeq int
}

// NewStore 创建仓储（空，不 seed mock 配置）。
// 去假数据：用户经控制台配置真实 env/Secret（原 seed 含假 secret sk-real-secret-value）。
func NewStore() *Store {
	return &Store{items: map[string]appconfig.ConfigItem{}}
}

func (s *Store) List(ctx context.Context, appID, envID string) ([]appconfig.ConfigItem, error) {
	return s.list(ctx, appID, envID, true)
}

// ListPlain 同 List 但 Secret 返明文（reconciler 注入工作负载 env 用）。
func (s *Store) ListPlain(ctx context.Context, appID, envID string) ([]appconfig.ConfigItem, error) {
	return s.list(ctx, appID, envID, false)
}

func (s *Store) list(ctx context.Context, appID, envID string, mask bool) ([]appconfig.ConfigItem, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]appconfig.ConfigItem, 0)
	for _, c := range s.items {
		if c.TenantID != tid {
			continue
		}
		if appID != "" && c.AppID != appID {
			continue
		}
		if envID != "" && c.EnvID != envID {
			continue
		}
		if mask {
			out = append(out, c.Masked()) // Secret 掩码
		} else {
			out = append(out, c) // 明文（reconciler 注入用）
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Upsert 新增或更新（同 tenant+app+env+key 视为同一项）。存储明文，返回掩码。
func (s *Store) Upsert(ctx context.Context, item appconfig.ConfigItem) (appconfig.ConfigItem, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return appconfig.ConfigItem{}, err
	}
	if err := item.Validate(); err != nil {
		return appconfig.ConfigItem{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 查找同 (tenant, app, env, key)
	for id, existing := range s.items {
		if existing.TenantID == tid && existing.AppID == item.AppID &&
			existing.EnvID == item.EnvID && existing.Key == item.Key {
			existing.Value = item.Value
			existing.Type = item.Type
			existing.UpdatedAt = time.Now()
			s.items[id] = existing
			return existing.Masked(), nil
		}
	}

	// 新增
	s.idSeq++
	item.ID = fmt.Sprintf("cfg-%d-%d", time.Now().UnixNano(), s.idSeq)
	item.TenantID = tid
	item.UpdatedAt = time.Now()
	s.items[item.ID] = item
	return item.Masked(), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.items[id]
	if !ok || c.TenantID != tid {
		return fmt.Errorf("配置不存在: %s", id)
	}
	delete(s.items, id)
	return nil
}

// SeedConfigs 返回预置配置项（PG/内存同一真源，DRY）。
// 租户/应用/环境字段已填好，调用方按 TenantID 自建 ctx 写入。
func SeedConfigs() []appconfig.ConfigItem {
	t := time.Now()
	return []appconfig.ConfigItem{
		{ID: "cfg-acme-1", TenantID: "t-acme", AppID: "app-cs", EnvID: "env-acme-test", Key: "LOG_LEVEL", Value: "info", Type: appconfig.TypeEnv, UpdatedAt: t},
		{ID: "cfg-acme-2", TenantID: "t-acme", AppID: "app-cs", EnvID: "env-acme-test", Key: "API_KEY", Value: "sk-real-secret-value", Type: appconfig.TypeSecret, UpdatedAt: t},
		{ID: "cfg-globex-1", TenantID: "t-globex", AppID: "app-agent", EnvID: "env-globex-prod", Key: "MODEL_TIMEOUT", Value: "30", Type: appconfig.TypeEnv, UpdatedAt: t},
	}
}
