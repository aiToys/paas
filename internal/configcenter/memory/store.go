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

// Store 实现 configcenter.Repository（仓储），单 Store 避免重名。
type Store struct {
	mu         sync.RWMutex
	namespaces map[string]configcenter.Namespace
	items      map[string]configcenter.ConfigItem
	publishes  map[string]configcenter.Publish
	overrides  map[string]configcenter.LaneOverride
	refs       map[string]configcenter.NSRef
	nsSeq      int
	itemSeq    int
	pubSeq     int
	ovSeq      int
	refSeq     int
}

// NewStore 创建仓储（空，不 seed mock 配置）。
// 去假数据：用户经控制台配置真实命名空间/配置/发布（原 seed 为 mock 演示配置）。
func NewStore() *Store {
	return &Store{
		namespaces: map[string]configcenter.Namespace{},
		items:      map[string]configcenter.ConfigItem{},
		publishes:  map[string]configcenter.Publish{},
		overrides:  map[string]configcenter.LaneOverride{},
		refs:       map[string]configcenter.NSRef{},
	}
}

// —— Namespace ——

func (s *Store) ListNamespaces(ctx context.Context, serviceID string) ([]configcenter.Namespace, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.Namespace, 0)
	for _, n := range s.namespaces {
		if n.TenantID != tid {
			continue
		}
		if serviceID != "" && n.ServiceID != serviceID {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListAllNamespaces 跨租户列出全部命名空间（admin 平台总览，不过滤 tenant；按 TenantID 升序再 Name 升序）。
func (s *Store) ListAllNamespaces(ctx context.Context) ([]configcenter.Namespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.Namespace, 0, len(s.namespaces))
	for _, n := range s.namespaces {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) GetNamespace(ctx context.Context, id string) (configcenter.Namespace, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.namespaces[id]
	if !ok || n.TenantID != tid {
		return configcenter.Namespace{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, id)
	}
	return n, nil
}

func (s *Store) CreateNamespace(ctx context.Context, n configcenter.Namespace) (configcenter.Namespace, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	if err := n.Validate(); err != nil {
		return configcenter.Namespace{}, err
	}
	// scope 语义：AppID 非空 → 应用派生（Name 强制 app-<appID>，防伪造 shared 占名）；空 → 共享。
	if n.AppID != "" {
		n.Scope = configcenter.ScopeApp
		n.Name = "app-" + n.AppID
	} else {
		n.Scope = configcenter.ScopeShared
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.namespaces {
		if ex.TenantID == tid && ex.Name == n.Name {
			return configcenter.Namespace{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNameTaken, n.Name)
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
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.namespaces[id]
	if !ok || n.TenantID != tid {
		return fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, id)
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

// EnsureByApp 懒建（或返回既有的）应用派生命名空间（env=” 基线，兼容旧签名）。
func (s *Store) EnsureByApp(ctx context.Context, appID string) (configcenter.Namespace, error) {
	return s.EnsureByAppEnv(ctx, appID, "")
}

// FindAppNamespace 查应用派生命名空间（env=” 基线，兼容旧签名）。无返回 false。
func (s *Store) FindAppNamespace(ctx context.Context, appID string) (configcenter.Namespace, bool, error) {
	return s.FindAppNamespaceEnv(ctx, appID, "")
}

// EnsureByAppEnv 懒建（或返回既有的）(app, env) 维度应用派生命名空间。幂等。
// envID 空 = 全环境基线；非空 = 独立 ns（test/prod 配置互不可见）。
// 名字冲突（手工 shared ns 抢占派生名）返回 ErrNamespaceNameTaken，不静默另建。
func (s *Store) EnsureByAppEnv(ctx context.Context, appID, envID string) (configcenter.Namespace, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	name := configcenter.AppNSName(appID, envID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.findAppNSLocked(tid, appID, envID); ok {
		return n, nil
	}
	// 名字冲突：手工共享 ns 占了派生名（handler 映射 409 引导改名，不静默另建同名 ns）。
	for _, n := range s.namespaces {
		if n.TenantID == tid && n.Name == name {
			return configcenter.Namespace{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNameTaken, name)
		}
	}
	s.nsSeq++
	n := configcenter.Namespace{
		ID: fmt.Sprintf("ns-%d", s.nsSeq), TenantID: tid,
		Name: name, Scope: configcenter.ScopeApp, AppID: appID, EnvID: envID,
		UpdatedAt: time.Now(),
	}
	s.namespaces[n.ID] = n
	return n, nil
}

// FindAppNamespaceEnv 查 (app, env) 维度命名空间（不创建）。发现解析语义：
// envID 非空时精确未命中回退 env=” 基线；envID 空仅精确匹配 env=”。
func (s *Store) FindAppNamespaceEnv(ctx context.Context, appID, envID string) (configcenter.Namespace, bool, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n, ok := s.findAppNSLocked(tid, appID, envID); ok {
		return n, true, nil
	}
	// 回退：env 精确未命中 → env='' 基线（envID 空不走此分支，已精确匹配过）。
	if envID != "" {
		if n, ok := s.findAppNSLocked(tid, appID, ""); ok {
			return n, true, nil
		}
	}
	return configcenter.Namespace{}, false, nil
}

// findAppNSLocked 按 (tenant, app, env) 精确查找应用派生 ns；须持锁。
func (s *Store) findAppNSLocked(tid, appID, envID string) (configcenter.Namespace, bool) {
	for _, n := range s.namespaces {
		if n.TenantID == tid && n.Scope == configcenter.ScopeApp && n.AppID == appID && n.EnvID == envID {
			return n, true
		}
	}
	return configcenter.Namespace{}, false
}

// —— Item ——

func (s *Store) ListItems(ctx context.Context, namespaceID string) ([]configcenter.ConfigItem, error) {
	tid, err := tenant.IDOrErr(ctx)
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
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.ConfigItem{}, err
	}
	// namespace 存在性/归属由锁内复校验统一保证（不重复锁外预检）。
	if err := item.Validate(); err != nil {
		return configcenter.ConfigItem{}, err
	}
	if item.Type == "" {
		item.Type = configcenter.TypeText
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 锁内校验 namespace 存在且属本租户（防校验与 Lock 间 DeleteNamespace 级联清产生孤儿 item）
	if n, ok := s.namespaces[item.NamespaceID]; !ok || n.TenantID != tid {
		return configcenter.ConfigItem{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, item.NamespaceID)
	}
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
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok || it.TenantID != tid {
		return fmt.Errorf("%w: %s", configcenter.ErrItemNotFound, id)
	}
	delete(s.items, id)
	return nil
}

// —— Publish ——

// clonePublish 深拷贝发布（Snapshot map 新建独立），防止返回值与 store 内部状态共享 map
// 引发并发读写 panic（与 billing cloneBill 同款防御）。
func clonePublish(p configcenter.Publish) configcenter.Publish {
	if p.Snapshot != nil {
		cp := make(map[string]string, len(p.Snapshot))
		for k, v := range p.Snapshot {
			cp[k] = v
		}
		p.Snapshot = cp
	}
	return p
}

func (s *Store) ListPublishes(ctx context.Context, namespaceID string) ([]configcenter.Publish, error) {
	tid, err := tenant.IDOrErr(ctx)
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
		out = append(out, clonePublish(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// CreatePublish 快照当前 namespace 全部 item 生成新 active 发布。
func (s *Store) CreatePublish(ctx context.Context, namespaceID string) (configcenter.Publish, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 锁内校验 namespace 存在且属本租户（防校验与 Lock 间 DeleteNamespace 级联清产生孤儿 publish）
	if n, ok := s.namespaces[namespaceID]; !ok || n.TenantID != tid {
		return configcenter.Publish{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, namespaceID)
	}
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
	return clonePublish(pub), nil
}

// RollbackPublish 激活历史 rolled-back 发布为 active。
func (s *Store) RollbackPublish(ctx context.Context, publishID string) (configcenter.Publish, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.publishes[publishID]
	if !ok || target.TenantID != tid {
		return configcenter.Publish{}, fmt.Errorf("%w: %s", configcenter.ErrPublishNotFound, publishID)
	}
	if target.Status == configcenter.StatusActive {
		return configcenter.Publish{}, fmt.Errorf("%w: v%d", configcenter.ErrPublishAlreadyActive, target.Version)
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
	return clonePublish(target), nil
}

func (s *Store) ActivePublish(ctx context.Context, namespaceID string) (configcenter.Publish, bool, error) {
	tid, err := tenant.IDOrErr(ctx)
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
	return clonePublish(*found), true, nil
}

// PublishNamespaceID 返回发布所属 namespace（回滚路由校验用）。
func (s *Store) PublishNamespaceID(ctx context.Context, publishID string) (string, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.publishes[publishID]
	if !ok || p.TenantID != tid {
		return "", fmt.Errorf("%w: %s", configcenter.ErrPublishNotFound, publishID)
	}
	return p.NamespaceID, nil
}

// —— LaneOverride ——

// UpsertLaneOverride 同 (tenant, app, env, lane, key) 覆盖更新，否则新增。
func (s *Store) UpsertLaneOverride(ctx context.Context, o configcenter.LaneOverride) (configcenter.LaneOverride, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.LaneOverride{}, err
	}
	if err := o.Validate(); err != nil {
		return configcenter.LaneOverride{}, err
	}
	o.TenantID = tid
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ex := range s.overrides {
		if ex.TenantID == tid && ex.AppID == o.AppID && ex.EnvID == o.EnvID &&
			ex.LaneID == o.LaneID && ex.Key == o.Key {
			ex.Value = o.Value
			ex.UpdatedAt = time.Now()
			s.overrides[id] = ex
			return ex, nil
		}
	}
	s.ovSeq++
	o.ID = fmt.Sprintf("ovr-%d-%d", time.Now().UnixNano(), s.ovSeq)
	o.UpdatedAt = time.Now()
	s.overrides[o.ID] = o
	return o, nil
}

// DeleteLaneOverride 删除覆盖；不存在返回 ErrLaneOverrideNotFound。
func (s *Store) DeleteLaneOverride(ctx context.Context, appID, envID, laneID, key string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ex := range s.overrides {
		if ex.TenantID == tid && ex.AppID == appID && ex.EnvID == envID &&
			ex.LaneID == laneID && ex.Key == key {
			delete(s.overrides, id)
			return nil
		}
	}
	return fmt.Errorf("%w: %s", configcenter.ErrLaneOverrideNotFound, key)
}

// ListLaneOverrides 按 (app, env, lane) 过滤（lane 空=该 env 全部泳道），按 Key 升序。
func (s *Store) ListLaneOverrides(ctx context.Context, appID, envID, laneID string) ([]configcenter.LaneOverride, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.LaneOverride, 0)
	for _, o := range s.overrides {
		if o.TenantID != tid || o.AppID != appID || o.EnvID != envID {
			continue
		}
		if laneID != "" && o.LaneID != laneID {
			continue
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ListLaneOverridesForClean 泳道回收级联清理用：按 (env, lane) 跨 app 列出。
func (s *Store) ListLaneOverridesForClean(ctx context.Context, envID, laneID string) ([]configcenter.LaneOverride, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.LaneOverride, 0)
	for _, o := range s.overrides {
		if o.TenantID != tid || o.EnvID != envID || o.LaneID != laneID {
			continue
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ---- 共享配置引用（shared ns → 应用派生 ns）----

func (s *Store) AddNSRef(ctx context.Context, appNSID, sharedNSID string) (configcenter.NSRef, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.NSRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 前置校验：shared ns 存在 + 本租户 + scope=shared（跨租户/不存在统一 NotFound 不泄漏）+ 非自引。
	shared, ok := s.namespaces[sharedNSID]
	if !ok || shared.TenantID != tid {
		return configcenter.NSRef{}, configcenter.ErrNamespaceNotFound
	}
	if shared.Scope != configcenter.ScopeShared {
		return configcenter.NSRef{}, configcenter.ErrRefNotShared
	}
	if appNSID == sharedNSID {
		return configcenter.NSRef{}, configcenter.ErrRefNotShared
	}
	for _, ex := range s.refs {
		if ex.TenantID == tid && ex.AppNSID == appNSID && ex.SharedNSID == sharedNSID {
			return configcenter.NSRef{}, configcenter.ErrRefExists
		}
	}
	s.refSeq++
	ref := configcenter.NSRef{
		ID:         fmt.Sprintf("ref-%d-%d", time.Now().UnixNano(), s.refSeq),
		TenantID:   tid,
		AppNSID:    appNSID,
		SharedNSID: sharedNSID,
		CreatedAt:  time.Now(),
	}
	s.refs[ref.ID] = ref
	return ref, nil
}

func (s *Store) DeleteNSRef(ctx context.Context, refID string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.refs[refID]
	if !ok || ex.TenantID != tid {
		return configcenter.ErrRefNotFound
	}
	delete(s.refs, refID)
	return nil
}

// ListNSRefs 列 app ns 的引用（创建时间升序 = merge 铺垫顺序）。
func (s *Store) ListNSRefs(ctx context.Context, appNSID string) ([]configcenter.NSRef, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.NSRef, 0)
	for _, r := range s.refs {
		if r.TenantID == tid && r.AppNSID == appNSID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListNSRefUsers 反查 shared ns 的引用方（影响面展示）。
func (s *Store) ListNSRefUsers(ctx context.Context, sharedNSID string) ([]configcenter.NSRef, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]configcenter.NSRef, 0)
	for _, r := range s.refs {
		if r.TenantID == tid && r.SharedNSID == sharedNSID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ResetItemsToSnapshot 持锁把 ns 的 draft items 对齐到快照（回滚同步草稿）。
// 单临界区消除部分重置与并发编辑交错；type 保留 draft 原值（快照不存 type）。
func (s *Store) ResetItemsToSnapshot(ctx context.Context, namespaceID string, snapshot map[string]string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.namespaces[namespaceID]
	if !ok || ns.TenantID != tid {
		return configcenter.ErrNamespaceNotFound
	}
	// 锁内重建：快照 key 补缺/改异（保留原 type），快照外 key 删除。
	keep := make(map[string]configcenter.ConfigItem, len(snapshot))
	for _, it := range s.items {
		if it.NamespaceID != namespaceID {
			continue
		}
		if val, inSnap := snapshot[it.Key]; inSnap {
			it.Value = val
			keep[it.Key] = it
		} else {
			delete(s.items, it.ID)
		}
	}
	for key, val := range snapshot {
		if _, ok := keep[key]; ok {
			continue
		}
		s.itemSeq++
		id := fmt.Sprintf("itm-%d-%d", time.Now().UnixNano(), s.itemSeq)
		s.items[id] = configcenter.ConfigItem{
			ID: id, TenantID: tid, NamespaceID: namespaceID,
			Key: key, Value: val, Type: configcenter.TypeText, UpdatedAt: time.Now(),
		}
	}
	// 写回保留项（值可能已更新）
	for _, it := range keep {
		it.UpdatedAt = time.Now()
		s.items[it.ID] = it
	}
	return nil
}
