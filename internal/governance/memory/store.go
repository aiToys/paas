// Package memory 提供 governance.Repository 的内存实现，seed 跨两租户示例服务与实例。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 governance.Repository（ServiceStore + InstanceStore + RouteStore + BreakerStore），单 Store 避免重名。
type Store struct {
	mu         sync.RWMutex
	services   map[string]governance.Service
	instances  map[string]governance.Instance
	routes     map[string]governance.Route
	breakers   map[string]governance.CircuitBreaker
	svcSeq     int
	instSeq    int
	routeSeq   int
	breakerSeq int
}

// NewStore 创建仓储（空，不 seed mock 演示数据）。
// 去假数据：服务/实例/路由/熔断由用户配置产生。实例真源为 K8s Endpoints（/dp/ 已提供数据面
// 发现）；governance /api/instances 切 K8s Endpoints 留后续（需 service->workload 映射设计）。
func NewStore() *Store {
	return &Store{
		services:  map[string]governance.Service{},
		instances: map[string]governance.Instance{},
		routes:    map[string]governance.Route{},
		breakers:  map[string]governance.CircuitBreaker{},
	}
}

// cloneInstance 深拷贝 Instance（Meta map），隔离返回值与 store 内部状态，防外部修改 + 并发 map race。
func cloneInstance(in governance.Instance) governance.Instance {
	if in.Meta != nil {
		in.Meta = cloneStringMap(in.Meta)
	}
	return in
}

// cloneRoute 深拷贝 Route（Methods 切片），隔离返回值与 store 内部状态。
func cloneRoute(r governance.Route) governance.Route {
	if r.Methods != nil {
		r.Methods = append([]string(nil), r.Methods...)
	}
	return r
}

func cloneStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// —— Service ——

func (s *Store) ListServices(ctx context.Context, envID, appID string) ([]governance.Service, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]governance.Service, 0)
	for _, sv := range s.services {
		if sv.TenantID != tid {
			continue
		}
		if envID != "" && sv.EnvID != envID {
			continue
		}
		if appID != "" && sv.AppID != appID {
			continue
		}
		out = append(out, sv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListAllServices 跨租户列出全部服务（admin 平台总览，不过滤 tenant；按 TenantID 升序再 Name 升序）。
func (s *Store) ListAllServices(ctx context.Context) ([]governance.Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]governance.Service, 0, len(s.services))
	for _, sv := range s.services {
		out = append(out, sv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) GetService(ctx context.Context, id string) (governance.Service, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.Service{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sv, ok := s.services[id]
	if !ok || sv.TenantID != tid {
		return governance.Service{}, fmt.Errorf("服务不存在: %s", id)
	}
	return sv, nil
}

func (s *Store) CreateService(ctx context.Context, svc governance.Service) (governance.Service, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.Service{}, err
	}
	if err := svc.Validate(); err != nil {
		return governance.Service{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 租户内服务名唯一
	for _, ex := range s.services {
		if ex.TenantID == tid && ex.Name == svc.Name {
			return governance.Service{}, fmt.Errorf("服务名已存在: %s", svc.Name)
		}
	}
	s.svcSeq++
	svc.ID = fmt.Sprintf("svc-%d-%d", time.Now().UnixNano(), s.svcSeq)
	svc.TenantID = tid
	svc.UpdatedAt = time.Now()
	s.services[svc.ID] = svc
	return svc, nil
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sv, ok := s.services[id]
	if !ok || sv.TenantID != tid {
		return fmt.Errorf("服务不存在: %s", id)
	}
	delete(s.services, id)
	// 级联删除该服务下所有实例 + 路由 + 熔断器（与 PG 版单事务级联三表同语义，
	// 防悬挂 Route/Breaker 指向已删服务）。
	for iid, in := range s.instances {
		if in.ServiceID == id {
			delete(s.instances, iid)
		}
	}
	for rid, rt := range s.routes {
		if rt.ServiceID == id {
			delete(s.routes, rid)
		}
	}
	for bid, b := range s.breakers {
		if b.ServiceID == id {
			delete(s.breakers, bid)
		}
	}
	return nil
}

// —— Instance ——

func (s *Store) ListInstances(ctx context.Context, serviceID string) ([]governance.Instance, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]governance.Instance, 0)
	for _, in := range s.instances {
		if in.TenantID != tid {
			continue
		}
		if serviceID != "" && in.ServiceID != serviceID {
			continue
		}
		out = append(out, cloneInstance(in))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out, nil
}

func (s *Store) RegisterInstance(ctx context.Context, in governance.Instance) (governance.Instance, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.Instance{}, err
	}
	if err := in.Validate(); err != nil {
		return governance.Instance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 锁内校验服务存在且属同租户，避免与 DeleteService 级联清理竞态导致注册到不存在的服务。
	sv, ok := s.services[in.ServiceID]
	if !ok || sv.TenantID != tid {
		return governance.Instance{}, fmt.Errorf("服务不存在: %s", in.ServiceID)
	}
	if in.Status == "" {
		in.Status = governance.StatusHealthy
	}
	if in.LaneID == "" {
		in.LaneID = governance.LaneDefault
	}
	s.instSeq++
	in.ID = fmt.Sprintf("inst-%d-%d", time.Now().UnixNano(), s.instSeq)
	in.TenantID = tid
	in.UpdatedAt = time.Now()
	in = cloneInstance(in) // 深拷贝 Meta，隔离入参与 store 内部状态
	s.instances[in.ID] = in
	return cloneInstance(in), nil
}

func (s *Store) DeregisterInstance(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok || in.TenantID != tid {
		return fmt.Errorf("实例不存在: %s", id)
	}
	delete(s.instances, id)
	return nil
}

func (s *Store) Heartbeat(ctx context.Context, id string) (governance.Instance, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.Instance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok || in.TenantID != tid {
		return governance.Instance{}, fmt.Errorf("实例不存在: %s", id)
	}
	in.UpdatedAt = time.Now()
	in.Status = governance.StatusHealthy
	s.instances[id] = in
	return cloneInstance(in), nil
}

// InstanceServiceID 返回实例所属服务 ID（handler 注销时校验生产权限用）。
func (s *Store) InstanceServiceID(ctx context.Context, id string) (string, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	in, ok := s.instances[id]
	if !ok || in.TenantID != tid {
		return "", fmt.Errorf("实例不存在: %s", id)
	}
	return in.ServiceID, nil
}

// SeedServices 导出 seed 服务列表，供 PG seed 复用（同一真源，DRY）。
func SeedServices() []governance.Service { return seedServices() }

// SeedInstances 导出 seed 实例列表，供 PG seed 复用（同一真源，DRY）。
func SeedInstances() []governance.Instance { return seedInstances() }

// SeedRoutes 导出 seed 路由列表，供 PG seed 复用（同一真源，DRY）。
func SeedRoutes() []governance.Route { return seedRoutes() }

// SeedBreakers 导出 seed 熔断器列表，供 PG seed 复用（同一真源，DRY）。
func SeedBreakers() []governance.CircuitBreaker { return seedBreakers() }

func seedServices() []governance.Service {
	t := time.Now()
	return []governance.Service{
		{ID: "svc-acme-cs", TenantID: "t-acme", Name: "customer-svc", AppID: "app-cs", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080, Desc: "客户中心服务", UpdatedAt: t},
		{ID: "svc-acme-rec", TenantID: "t-acme", Name: "recommend-svc", AppID: "app-rec", EnvID: "env-acme-prod-bj", Protocol: governance.ProtocolGRPC, Port: 9090, Desc: "推荐服务", UpdatedAt: t},
		{ID: "svc-globex-agent", TenantID: "t-globex", Name: "agent-svc", AppID: "app-agent", EnvID: "env-globex-prod", Protocol: governance.ProtocolHTTP, Port: 8000, Desc: "智能体服务", UpdatedAt: t},
	}
}

func seedInstances() []governance.Instance {
	t := time.Now()
	return []governance.Instance{
		{ID: "inst-acme-cs-1", TenantID: "t-acme", ServiceID: "svc-acme-cs", Addr: "10.0.1.11:8080", Status: governance.StatusHealthy, LaneID: governance.LaneDefault, UpdatedAt: t},
		{ID: "inst-acme-cs-2", TenantID: "t-acme", ServiceID: "svc-acme-cs", Addr: "10.0.1.12:8080", Status: governance.StatusHealthy, LaneID: governance.LaneDefault, UpdatedAt: t},
		{ID: "inst-acme-rec-1", TenantID: "t-acme", ServiceID: "svc-acme-rec", Addr: "10.1.0.21:9090", Status: governance.StatusHealthy, LaneID: governance.LaneDefault, UpdatedAt: t},
		{ID: "inst-globex-agent-1", TenantID: "t-globex", ServiceID: "svc-globex-agent", Addr: "10.2.0.31:8000", Status: governance.StatusHealthy, LaneID: governance.LaneDefault, UpdatedAt: t},
	}
}

// —— Route ——

func (s *Store) ListRoutes(ctx context.Context, serviceID string) ([]governance.Route, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]governance.Route, 0)
	for _, r := range s.routes {
		if r.TenantID != tid {
			continue
		}
		if serviceID != "" && r.ServiceID != serviceID {
			continue
		}
		out = append(out, cloneRoute(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *Store) GetRoute(ctx context.Context, id string) (governance.Route, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.Route{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.routes[id]
	if !ok || r.TenantID != tid {
		return governance.Route{}, fmt.Errorf("路由不存在: %s", id)
	}
	return cloneRoute(r), nil
}

func (s *Store) CreateRoute(ctx context.Context, r governance.Route) (governance.Route, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.Route{}, err
	}
	if err := r.Validate(); err != nil {
		return governance.Route{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.routes {
		if ex.TenantID == tid && ex.Name == r.Name {
			return governance.Route{}, fmt.Errorf("路由名已存在: %s", r.Name)
		}
	}
	s.routeSeq++
	r.ID = fmt.Sprintf("route-%d-%d", time.Now().UnixNano(), s.routeSeq)
	r.TenantID = tid
	r.UpdatedAt = time.Now()
	r.Methods = append([]string(nil), r.Methods...) // 深拷贝，隔离调用方切片与 store 内部状态
	s.routes[r.ID] = r
	return cloneRoute(r), nil
}

// UpdateRoute 混合更新语义（PUT 全量替换的变体）：
//   - 必填字段（Path/ServiceID/Methods）：非空才覆盖（部分更新，防 PUT 漏传误清）
//   - 可清空字段（StripPath/Enabled bool / Host 字符串）：直接覆盖（允许从有值改回默认/空）
//
// bool 字段无法区分"未设"与"false"，故直接覆盖是唯一选择；Host 直接覆盖允许从有域名改回不限 Host。
// 合并后复 Validate，防 PUT 用空 methods 绕过 Create 时的非空不变量。
func (s *Store) UpdateRoute(ctx context.Context, r governance.Route) (governance.Route, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.Route{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.routes[r.ID]
	if !ok || ex.TenantID != tid {
		return governance.Route{}, fmt.Errorf("路由不存在: %s", r.ID)
	}
	if r.Path != "" {
		ex.Path = r.Path
	}
	if r.ServiceID != "" {
		ex.ServiceID = r.ServiceID
	}
	if r.Methods != nil {
		ex.Methods = append([]string(nil), r.Methods...) // 深拷贝，隔离调用方切片与 store 内部状态
	}
	ex.StripPath = r.StripPath
	ex.Enabled = r.Enabled
	ex.Host = r.Host // 直接覆盖语义，允许清空（从有域名改回不限 Host）
	ex.UpdatedAt = time.Now()
	// 合并后复校验，防止 PUT 用空 methods 绕过 Create 时的非空不变量。
	if err := ex.Validate(); err != nil {
		return governance.Route{}, err
	}
	s.routes[r.ID] = ex
	return cloneRoute(ex), nil
}

func (s *Store) DeleteRoute(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.routes[id]
	if !ok || r.TenantID != tid {
		return fmt.Errorf("路由不存在: %s", id)
	}
	delete(s.routes, id)
	return nil
}

func seedRoutes() []governance.Route {
	t := time.Now()
	return []governance.Route{
		{ID: "route-acme-chat", TenantID: "t-acme", Name: "chat-api", Path: "/api/v1/chat/*", ServiceID: "svc-acme-cs", Methods: []string{governance.MethodPost, governance.MethodAny}, StripPath: true, Enabled: true, UpdatedAt: t},
		{ID: "route-acme-rec", TenantID: "t-acme", Name: "recommend-grpc", Path: "/grpc.recommend/*", ServiceID: "svc-acme-rec", Methods: []string{governance.MethodPost}, StripPath: false, Enabled: true, UpdatedAt: t},
		{ID: "route-globex-agent", TenantID: "t-globex", Name: "agent-api", Path: "/api/agent/*", ServiceID: "svc-globex-agent", Methods: []string{governance.MethodAny}, StripPath: true, Enabled: true, UpdatedAt: t},
	}
}

// —— CircuitBreaker ——

func (s *Store) ListBreakers(ctx context.Context, serviceID string) ([]governance.CircuitBreaker, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]governance.CircuitBreaker, 0)
	for _, b := range s.breakers {
		if b.TenantID != tid {
			continue
		}
		if serviceID != "" && b.ServiceID != serviceID {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *Store) GetBreaker(ctx context.Context, id string) (governance.CircuitBreaker, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.breakers[id]
	if !ok || b.TenantID != tid {
		return governance.CircuitBreaker{}, fmt.Errorf("熔断器不存在: %s", id)
	}
	return b, nil
}

func (s *Store) CreateBreaker(ctx context.Context, b governance.CircuitBreaker) (governance.CircuitBreaker, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	if err := b.Validate(); err != nil {
		return governance.CircuitBreaker{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.breakers {
		if ex.TenantID == tid && ex.Name == b.Name {
			return governance.CircuitBreaker{}, fmt.Errorf("熔断器名已存在: %s", b.Name)
		}
	}
	s.breakerSeq++
	b.ID = fmt.Sprintf("cb-%d-%d", time.Now().UnixNano(), s.breakerSeq)
	b.TenantID = tid
	b.UpdatedAt = time.Now()
	s.breakers[b.ID] = b
	return b, nil
}

func (s *Store) UpdateBreaker(ctx context.Context, b governance.CircuitBreaker) (governance.CircuitBreaker, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.breakers[b.ID]
	if !ok || ex.TenantID != tid {
		return governance.CircuitBreaker{}, fmt.Errorf("熔断器不存在: %s", b.ID)
	}
	if b.Strategy != "" {
		ex.Strategy = b.Strategy
	}
	if b.Threshold > 0 {
		ex.Threshold = b.Threshold
	}
	if b.MinRequests > 0 {
		ex.MinRequests = b.MinRequests
	}
	if b.WindowSecs > 0 {
		ex.WindowSecs = b.WindowSecs
	}
	if b.ServiceID != "" {
		ex.ServiceID = b.ServiceID
	}
	ex.Enabled = b.Enabled
	ex.UpdatedAt = time.Now()
	if err := ex.Validate(); err != nil {
		return governance.CircuitBreaker{}, err
	}
	s.breakers[b.ID] = ex
	return ex, nil
}

func (s *Store) DeleteBreaker(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.breakers[id]
	if !ok || b.TenantID != tid {
		return fmt.Errorf("熔断器不存在: %s", id)
	}
	delete(s.breakers, id)
	return nil
}

func seedBreakers() []governance.CircuitBreaker {
	t := time.Now()
	return []governance.CircuitBreaker{
		{ID: "cb-acme-cs-err", TenantID: "t-acme", Name: "cs-error-breaker", ServiceID: "svc-acme-cs", Strategy: governance.StrategyErrorRate, Threshold: 50, MinRequests: 20, WindowSecs: 60, Enabled: true, UpdatedAt: t},
		{ID: "cb-acme-rec-slow", TenantID: "t-acme", Name: "rec-slow-breaker", ServiceID: "svc-acme-rec", Strategy: governance.StrategySlowCall, Threshold: 60, MinRequests: 15, WindowSecs: 120, Enabled: true, UpdatedAt: t},
		{ID: "cb-globex-agent-err", TenantID: "t-globex", Name: "agent-error-breaker", ServiceID: "svc-globex-agent", Strategy: governance.StrategyErrorRate, Threshold: 40, MinRequests: 10, WindowSecs: 60, Enabled: true, UpdatedAt: t},
	}
}
