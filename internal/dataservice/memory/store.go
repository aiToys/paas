// Package memory 提供 dataservice.Repository 的内存实现，seed 跨两租户多种数据服务。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 dataservice.Repository。
type Store struct {
	mu         sync.RWMutex
	services   map[string]dataservice.DataService
	nsResolver dataservice.NamespaceResolver
	seq        int
}

// Option 配置 Store。
type Option func(*Store)

// WithNamespaceResolver 注入 K8s namespace 解析器，Create 时用于生成 Connection FQDN。
// 未注入时兜底 dataservice.DefaultNamespace。
func WithNamespaceResolver(r dataservice.NamespaceResolver) Option {
	return func(s *Store) { s.nsResolver = r }
}

func NewStore(opts ...Option) *Store {
	s := &Store{services: map[string]dataservice.DataService{}}
	for _, o := range opts {
		o(s)
	}
	// 不 seed mock 数据服务实例（去假数据）：用户经控制台创建真实数据服务（已有真实引擎 mysql/redis/nats/minio）。
	return s
}

// namespace 返回当前 namespace（注入优先，否则兜底 DefaultNamespace）。
func (s *Store) namespace() string {
	if s.nsResolver != nil {
		if ns := s.nsResolver.Namespace(); ns != "" {
			return ns
		}
	}
	return dataservice.DefaultNamespace
}

// cloneStrMap 深拷 map[string]string，隔离返回值/写入值与对端，避免并发 map 读写 panic（与 billing cloneIntMap 同款）。
func cloneStrMap(m map[string]string) map[string]string {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	return tid, nil
}

func (s *Store) List(ctx context.Context, kind string) ([]dataservice.DataService, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]dataservice.DataService, 0)
	for _, d := range s.services {
		if d.TenantID != tid {
			continue
		}
		if kind != "" && d.Kind != kind {
			continue
		}
		d.Spec = cloneStrMap(d.Spec)
		d.Connection = cloneStrMap(d.Connection)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListAll 跨租户返回全部数据服务（admin 后台用），按 tenant_id 升序、created_at 倒序。
// 不读取 tenant ctx；返回对象带 TenantID 供归属展示。
func (s *Store) ListAll(ctx context.Context) ([]dataservice.DataService, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]dataservice.DataService, 0, len(s.services))
	for _, d := range s.services {
		d.Spec = cloneStrMap(d.Spec)
		d.Connection = cloneStrMap(d.Connection)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (dataservice.DataService, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return dataservice.DataService{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.services[id]
	if !ok || d.TenantID != tid {
		return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", id)
	}
	d.Spec = cloneStrMap(d.Spec)
	d.Connection = cloneStrMap(d.Connection)
	return d, nil
}

// Create 校验后存入；status 空时补 running。
func (s *Store) Create(ctx context.Context, d dataservice.DataService) (dataservice.DataService, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return dataservice.DataService{}, err
	}
	if err := d.Validate(); err != nil {
		return dataservice.DataService{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.services {
		if ex.TenantID == tid && ex.Name == d.Name {
			return dataservice.DataService{}, fmt.Errorf("数据服务名已存在: %s", d.Name)
		}
	}
	if d.ID == "" {
		s.seq++
		d.ID = fmt.Sprintf("ds-%d-%d", time.Now().UnixNano(), s.seq)
	}
	d.TenantID = tid
	if d.Status == "" {
		d.Status = dataservice.StatusRunning
	}
	// 生成并填充 Connection（凭证 + FQDN + uri）；凭证持久化（重启不变，Secret 引用）。
	d.FillConnection(s.namespace())
	d.Spec = cloneStrMap(d.Spec) // 隔离 caller 传入的 map
	now := time.Now()
	d.CreatedAt = now
	d.UpdatedAt = now
	s.services[d.ID] = d
	return d, nil
}

func (s *Store) Update(ctx context.Context, d dataservice.DataService) (dataservice.DataService, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return dataservice.DataService{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.services[d.ID]
	if !ok || ex.TenantID != tid {
		return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", d.ID)
	}
	// 仅允许改 spec/status/updatedAt；kind/name/tenantId/envId 不变（env 改属迁移，留后续）
	if d.Status != "" {
		if _, ok := map[string]struct{}{
			dataservice.StatusCreating: {}, dataservice.StatusRunning: {}, dataservice.StatusStopped: {},
		}[d.Status]; !ok {
			return dataservice.DataService{}, fmt.Errorf("非法状态: %s", d.Status)
		}
		ex.Status = d.Status
	}
	if d.Spec != nil {
		ex.Spec = cloneStrMap(d.Spec) // 隔离 caller 传入，避免外部改影响 store 内部
	}
	ex.UpdatedAt = time.Now()
	// spec 改动后重算连接 uri/host（凭证保留，namespace 可能变）；spec 字段（如 db_name）影响 uri。
	if ex.Connection != nil {
		ex.FillConnection(s.namespace())
	}
	// 合并后复校验，防止 PUT 用空 spec 清空 Create 时强制的必填字段。
	if err := ex.Validate(); err != nil {
		return dataservice.DataService{}, err
	}
	ex.Spec = cloneStrMap(ex.Spec)
	ex.Connection = cloneStrMap(ex.Connection)
	s.services[d.ID] = ex
	return ex, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.services[id]
	if !ok || d.TenantID != tid {
		return fmt.Errorf("数据服务不存在: %s", id)
	}
	delete(s.services, id)
	return nil
}

// SeedDataServices 返回跨两租户的 seed 数据服务列表，供内存与 PG 路径共用同一真源（DRY）。
// 时间锚点由调用方决定（内存路径用 time.Now() +/- ago；PG 路径可在 seed 时同样锚定）。
// 返回的 DataService 已带 ID/TenantID/Kind/Name/Spec/Status/EnvID 与相对时间 CreatedAt/UpdatedAt。
func SeedDataServices() []dataservice.DataService {
	t := time.Now()
	mk := func(id, tid, kind, name, env, status string, spec map[string]string, ago time.Duration) dataservice.DataService {
		return dataservice.DataService{
			ID: id, TenantID: tid, Kind: kind, Name: name, Spec: spec,
			Status: status, EnvID: env, CreatedAt: t.Add(ago), UpdatedAt: t.Add(ago),
		}
	}
	return []dataservice.DataService{
		mk("ds-acme-db", "t-acme", dataservice.KindDB, "acme-orders-db", "env-acme-test", dataservice.StatusRunning,
			map[string]string{"engine": "postgres", "version": "15", "size_gb": "100"}, -72*time.Hour),
		mk("ds-acme-cache", "t-acme", dataservice.KindCache, "acme-session-cache", "env-acme-test", dataservice.StatusRunning,
			map[string]string{"engine": "redis", "mode": "cluster", "maxmemory_mb": "2048"}, -48*time.Hour),
		mk("ds-acme-mq", "t-acme", dataservice.KindMQ, "acme-events-mq", "env-acme-prod-bj", dataservice.StatusRunning,
			map[string]string{"engine": "nats", "partitions": "6"}, -24*time.Hour),
		mk("ds-globex-db", "t-globex", dataservice.KindDB, "globex-main-db", "env-globex-prod", dataservice.StatusRunning,
			map[string]string{"engine": "mysql", "version": "8", "size_gb": "200"}, -36*time.Hour),
		mk("ds-globex-vector", "t-globex", dataservice.KindVector, "globex-embedding", "env-globex-test", dataservice.StatusStopped,
			map[string]string{"engine": "milvus", "dimension": "1536"}, -12*time.Hour),
	}
}
