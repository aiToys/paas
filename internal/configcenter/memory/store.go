// Package memory 提供 configcenter.Repository 的内存实现，seed 跨两租户示例。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 configcenter.Repository（三仓储），单 Store 避免重名。
type Store struct {
	mu         sync.RWMutex
	namespaces map[string]configcenter.Namespace
	items      map[string]configcenter.ConfigItem
	publishes  map[string]configcenter.Publish
	nsSeq      int
	itemSeq    int
	pubSeq     int
}

func NewStore() *Store {
	s := &Store{
		namespaces: map[string]configcenter.Namespace{},
		items:      map[string]configcenter.ConfigItem{},
		publishes:  map[string]configcenter.Publish{},
	}
	s.seed()
	return s
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	return tid, nil
}

// —— Namespace ——

func (s *Store) ListNamespaces(ctx context.Context) ([]configcenter.Namespace, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.Namespace, 0)
	for _, n := range s.namespaces {
		if n.TenantID == tid {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) GetNamespace(ctx context.Context, id string) (configcenter.Namespace, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.namespaces[id]
	if !ok || n.TenantID != tid {
		return configcenter.Namespace{}, fmt.Errorf("命名空间不存在: %s", id)
	}
	return n, nil
}

func (s *Store) CreateNamespace(ctx context.Context, n configcenter.Namespace) (configcenter.Namespace, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	if err := n.Validate(); err != nil {
		return configcenter.Namespace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.namespaces {
		if ex.TenantID == tid && ex.Name == n.Name {
			return configcenter.Namespace{}, fmt.Errorf("命名空间已存在: %s", n.Name)
		}
	}
	s.nsSeq++
	n.ID = fmt.Sprintf("ns-%d-%d", time.Now().UnixNano(), s.nsSeq)
	n.TenantID = tid
	n.UpdatedAt = time.Now()
	s.namespaces[n.ID] = n
	return n, nil
}

func (s *Store) DeleteNamespace(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.namespaces[id]
	if !ok || n.TenantID != tid {
		return fmt.Errorf("命名空间不存在: %s", id)
	}
	delete(s.namespaces, id)
	// 级联清 item + publish
	for iid, it := range s.items {
		if it.NamespaceID == id {
			delete(s.items, iid)
		}
	}
	for pid, p := range s.publishes {
		if p.NamespaceID == id {
			delete(s.publishes, pid)
		}
	}
	return nil
}

// —— Item ——

func (s *Store) ListItems(ctx context.Context, namespaceID string) ([]configcenter.ConfigItem, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.ConfigItem, 0)
	for _, it := range s.items {
		if it.TenantID != tid {
			continue
		}
		if namespaceID != "" && it.NamespaceID != namespaceID {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// UpsertItem 同 (tenant, namespace, key) 视为同一项更新，否则新增。
func (s *Store) UpsertItem(ctx context.Context, item configcenter.ConfigItem) (configcenter.ConfigItem, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return configcenter.ConfigItem{}, err
	}
	// 校验 namespace 存在且属同租户
	if _, err := s.GetNamespace(ctx, item.NamespaceID); err != nil {
		return configcenter.ConfigItem{}, err
	}
	if err := item.Validate(); err != nil {
		return configcenter.ConfigItem{}, err
	}
	if item.Type == "" {
		item.Type = configcenter.TypeText
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ex := range s.items {
		if ex.TenantID == tid && ex.NamespaceID == item.NamespaceID && ex.Key == item.Key {
			ex.Value = item.Value
			ex.Type = item.Type
			ex.UpdatedAt = time.Now()
			s.items[id] = ex
			return ex, nil
		}
	}
	s.itemSeq++
	item.ID = fmt.Sprintf("item-%d-%d", time.Now().UnixNano(), s.itemSeq)
	item.TenantID = tid
	item.UpdatedAt = time.Now()
	s.items[item.ID] = item
	return item, nil
}

func (s *Store) DeleteItem(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok || it.TenantID != tid {
		return fmt.Errorf("配置项不存在: %s", id)
	}
	delete(s.items, id)
	return nil
}

// —— Publish ——

func (s *Store) ListPublishes(ctx context.Context, namespaceID string) ([]configcenter.Publish, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.Publish, 0)
	for _, p := range s.publishes {
		if p.TenantID != tid {
			continue
		}
		if namespaceID != "" && p.NamespaceID != namespaceID {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// CreatePublish 快照当前 namespace 全部 item 生成新 active 发布。
func (s *Store) CreatePublish(ctx context.Context, namespaceID string) (configcenter.Publish, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	if _, err := s.GetNamespace(ctx, namespaceID); err != nil {
		return configcenter.Publish{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 计算下一个版本号（namespace 内最大 version + 1）
	maxVersion := 0
	snapshot := map[string]string{}
	for _, it := range s.items {
		if it.TenantID == tid && it.NamespaceID == namespaceID {
			snapshot[it.Key] = it.Value
		}
	}
	for _, p := range s.publishes {
		if p.TenantID == tid && p.NamespaceID == namespaceID && p.Version > maxVersion {
			maxVersion = p.Version
		}
	}
	// 旧 active -> rolled-back
	for id, p := range s.publishes {
		if p.TenantID == tid && p.NamespaceID == namespaceID && p.Status == configcenter.StatusActive {
			p.Status = configcenter.StatusRolledBack
			s.publishes[id] = p
		}
	}
	s.pubSeq++
	pub := configcenter.Publish{
		ID:          fmt.Sprintf("pub-%d-%d", time.Now().UnixNano(), s.pubSeq),
		TenantID:    tid,
		NamespaceID: namespaceID,
		Version:     maxVersion + 1,
		Snapshot:    snapshot,
		Status:      configcenter.StatusActive,
		CreatedAt:   time.Now(),
	}
	s.publishes[pub.ID] = pub
	return pub, nil
}

// RollbackPublish 激活历史 rolled-back 发布为 active。
func (s *Store) RollbackPublish(ctx context.Context, publishID string) (configcenter.Publish, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.publishes[publishID]
	if !ok || target.TenantID != tid {
		return configcenter.Publish{}, fmt.Errorf("发布不存在: %s", publishID)
	}
	if target.Status == configcenter.StatusActive {
		return configcenter.Publish{}, fmt.Errorf("发布已是当前生效版本: v%d", target.Version)
	}
	// 当前 active -> rolled-back；目标 -> active
	for id, p := range s.publishes {
		if p.TenantID == tid && p.NamespaceID == target.NamespaceID && p.Status == configcenter.StatusActive {
			p.Status = configcenter.StatusRolledBack
			s.publishes[id] = p
		}
	}
	target.Status = configcenter.StatusActive
	s.publishes[publishID] = target
	return target, nil
}

func (s *Store) ActivePublish(ctx context.Context, namespaceID string) (configcenter.Publish, bool, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found *configcenter.Publish
	for _, p := range s.publishes {
		if p.TenantID != tid || p.NamespaceID != namespaceID {
			continue
		}
		if p.Status == configcenter.StatusActive {
			cp := p
			found = &cp
			break
		}
	}
	if found == nil {
		return configcenter.Publish{}, false, nil
	}
	return *found, true, nil
}

// PublishNamespaceID 返回发布所属 namespace（回滚路由校验用）。
func (s *Store) PublishNamespaceID(ctx context.Context, publishID string) (string, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.publishes[publishID]
	if !ok || p.TenantID != tid {
		return "", fmt.Errorf("发布不存在: %s", publishID)
	}
	return p.NamespaceID, nil
}

// SeedNamespaces 返回预置命名空间（PG/内存同一真源，DRY）。
// 调用方按每条记录的 TenantID 自建 ctx 写入（PG Create 以 ctx 租户为准）。
func SeedNamespaces() []configcenter.Namespace {
	t := time.Now()
	return []configcenter.Namespace{
		{ID: "ns-acme-app", TenantID: "t-acme", Name: "acme-app", Desc: "Acme 应用公共配置", UpdatedAt: t},
		{ID: "ns-globex-app", TenantID: "t-globex", Name: "globex-app", Desc: "Globex 应用公共配置", UpdatedAt: t},
	}
}

// SeedItems 返回预置配置项（draft）。
func SeedItems() []configcenter.ConfigItem {
	t := time.Now()
	return []configcenter.ConfigItem{
		{ID: "item-acme-1", TenantID: "t-acme", NamespaceID: "ns-acme-app", Key: "feature.newui", Value: "on", Type: configcenter.TypeText, UpdatedAt: t},
		{ID: "item-acme-2", TenantID: "t-acme", NamespaceID: "ns-acme-app", Key: "rate.limit", Value: "100", Type: configcenter.TypeText, UpdatedAt: t},
		{ID: "item-globex-1", TenantID: "t-globex", NamespaceID: "ns-globex-app", Key: "model.temperature", Value: "0.7", Type: configcenter.TypeText, UpdatedAt: t},
	}
}

// SeedPublishes 返回预置发布版本（active/rolled-back 状态保持，PG 路径由 CreatePublish 保证唯一 active）。
func SeedPublishes() []configcenter.Publish {
	t := time.Now()
	return []configcenter.Publish{
		{
			ID: "pub-acme-1", TenantID: "t-acme", NamespaceID: "ns-acme-app", Version: 1,
			Snapshot: map[string]string{"feature.newui": "off", "rate.limit": "50"},
			Status:   configcenter.StatusActive, CreatedAt: t,
		},
	}
}

// seed 灌入内存版预置数据（PG 路径直接调对应的 Seed* 函数共用同一真源）。
func (s *Store) seed() {
	for _, n := range SeedNamespaces() {
		s.namespaces[n.ID] = n
	}
	for _, it := range SeedItems() {
		s.items[it.ID] = it
	}
	for _, p := range SeedPublishes() {
		s.publishes[p.ID] = p
	}
}
