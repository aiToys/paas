package governance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	envmemory "github.com/aitoys/paas/internal/environment/memory"
	"github.com/aitoys/paas/internal/governance"
	govmemory "github.com/aitoys/paas/internal/governance/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newHandler 构造集成 handler：真实 governance/env 内存仓储 + stub 鉴权。
// prodWrite=true 模拟 admin，false 模拟 developer（生产只读）。
// 可选传入共享 governance store（如需跨 handler 复用预置数据）；未传则各自独立 NewStore。
func newHandler(prodWrite bool, stores ...*govmemory.Store) *governance.Handler {
	var store *govmemory.Store
	if len(stores) > 0 && stores[0] != nil {
		store = stores[0]
	} else {
		store = govmemory.NewStore()
	}
	h := governance.NewHandler(store, governance.WithEnvResolver(envmemory.NewStore()))
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == governance.PermProdWrite {
			return prodWrite
		}
		return true
	}
	return h
}

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

// decodeData 解包 {data:T} 信封后反序列化到 v（单资源响应，handler 统一 WriteData 契约）。
func decodeData(t *testing.T, body []byte, v interface{}) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解码信封失败: %v (body=%s)", err, string(body))
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("解码 data 失败: %v (data=%s)", err, string(env.Data))
	}
}

func req(ctx context.Context, method, path string, body interface{}) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	return rq.WithContext(ctx)
}

// TestHandlerList 验证服务列表（租户隔离）。
func TestHandlerList(t *testing.T) {
	h := newHandler(true)
	r := req(acmeCtx(), "GET", "/api/services?envId=env-acme-test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", w.Code)
	}
	var out struct {
		Data []governance.Service `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, s := range out.Data {
		if s.TenantID != "t-acme" {
			t.Fatalf("泄漏其他租户服务: %s", s.Name)
		}
	}
}

// TestHandlerProdGuard 验证生产注册/注销权限守卫。
func TestHandlerProdGuard(t *testing.T) {
	// 共享 store：admin 先建生产服务，dev/admin 注销同一服务（验证权限差异）
	store := govmemory.NewStore()
	hAdmin := newHandler(true, store)
	// admin 先建一个生产服务，供后续注销测试
	r := req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "prod-svc", EnvID: "env-acme-prod-bj", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w := httptest.NewRecorder()
	hAdmin.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin 建生产服务应 201，got %d: %s", w.Code, w.Body.String())
	}
	var prodSvc governance.Service
	decodeData(t, w.Body.Bytes(), &prodSvc)

	hDev := newHandler(false, store)
	// dev 注册生产服务 -> 403
	r = req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "prod-new", EnvID: "env-acme-prod-bj", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 注册生产服务应 403，got %d", w.Code)
	}
	// dev 注册测试服务 -> 201
	r = req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "test-new", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("dev 注册测试服务应 201，got %d", w.Code)
	}
	// dev 注销生产服务 -> 403 / admin 注销 -> 200
	r = req(acmeCtx(), "DELETE", "/api/services/"+prodSvc.ID, nil)
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 注销生产服务应 403，got %d", w.Code)
	}
	r = req(acmeCtx(), "DELETE", "/api/services/"+prodSvc.ID, nil)
	w = httptest.NewRecorder()
	hAdmin.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 注销生产服务应 200，got %d", w.Code)
	}
}

// TestHandlerInstanceOps 验证实例注册/发现/心跳。
func TestHandlerInstanceOps(t *testing.T) {
	h := newHandler(true)
	// 先建测试服务（属 env-acme-test），拿到 svcID 后注册实例
	r := req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "test-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("建测试服务应 201，got %d: %s", w.Code, w.Body.String())
	}
	var svc governance.Service
	decodeData(t, w.Body.Bytes(), &svc)
	// 注册实例
	r = req(acmeCtx(), "POST", "/api/services/"+svc.ID+"/instances", governance.Instance{
		Addr: "10.0.1.200:8080",
	})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("注册实例应 201，got %d: %s", w.Code, w.Body.String())
	}
	var created governance.Instance
	decodeData(t, w.Body.Bytes(), &created)
	// 服务详情应包含新实例
	r = req(acmeCtx(), "GET", "/api/services/"+svc.ID, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var detail governance.ServiceDetail
	decodeData(t, w.Body.Bytes(), &detail)
	found := false
	for _, x := range detail.Instances {
		if x.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("详情应包含新注册实例")
	}
	// 心跳
	r = req(acmeCtx(), "PUT", "/api/instances/"+created.ID+"/heartbeat", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("心跳应 200，got %d", w.Code)
	}
}

// TestHandlerInstanceDeleteCrossService 验证实例归属校验。
func TestHandlerInstanceDeleteCrossService(t *testing.T) {
	h := newHandler(true)
	// 用 svc-acme-rec 的实例去 svc-acme-cs 下注销 -> not found
	r := req(acmeCtx(), "DELETE", "/api/services/svc-acme-cs/instances/inst-acme-rec-1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨服务注销应 404，got %d", w.Code)
	}
}

// TestHandlerInstanceLaneFilter 验证服务详情按 lane 过滤（L2 启用）。
// 注册 default + feature-x 两条实例，?lane=feature-x 只返 feature；空返全部。
func TestHandlerInstanceLaneFilter(t *testing.T) {
	h := newHandler(true)
	r := req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "lane-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var svc governance.Service
	decodeData(t, w.Body.Bytes(), &svc)

	// 注册 default 基线 + feature-x 泳道两条实例
	for _, lane := range []string{"", "feature-x"} {
		r = req(acmeCtx(), "POST", "/api/services/"+svc.ID+"/instances", governance.Instance{
			Addr: "10.0.2.1:8080", LaneID: lane,
		})
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("注册实例应 201，got %d: %s", w.Code, w.Body.String())
		}
	}

	// 无 lane：返全部（2 条）
	r = req(acmeCtx(), "GET", "/api/services/"+svc.ID, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var all governance.ServiceDetail
	decodeData(t, w.Body.Bytes(), &all)
	if len(all.Instances) != 2 {
		t.Fatalf("无 lane 应返 2 条实例，got %d", len(all.Instances))
	}

	// ?lane=feature-x：只返 feature 泳道（1 条）
	r = req(acmeCtx(), "GET", "/api/services/"+svc.ID+"?lane=feature-x", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var feat governance.ServiceDetail
	decodeData(t, w.Body.Bytes(), &feat)
	if len(feat.Instances) != 1 || feat.Instances[0].LaneID != "feature-x" {
		t.Fatalf("?lane=feature-x 应只返 feature 实例，got %+v", feat.Instances)
	}

	// ?lane=default：只返基线
	r = req(acmeCtx(), "GET", "/api/services/"+svc.ID+"?lane=default", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var base governance.ServiceDetail
	decodeData(t, w.Body.Bytes(), &base)
	if len(base.Instances) != 1 || base.Instances[0].LaneID != "default" {
		t.Fatalf("?lane=default 应只返基线实例，got %+v", base.Instances)
	}
}

// fakeDiscoverer stub governance.InstanceDiscoverer（数据面真源）。
type fakeDiscoverer struct {
	instances []governance.Instance
	err       error
	gotLane   string
}

func (f *fakeDiscoverer) DiscoverInstances(ctx context.Context, namespace, serviceName, lane string) ([]governance.Instance, error) {
	f.gotLane = lane
	if f.err != nil {
		return nil, f.err
	}
	return f.instances, nil
}

// TestHandlerServiceDetailDiscovered 验证注入 discoverer 后服务详情返数据面 ready 实例（覆盖手动注册表）。
func TestHandlerServiceDetailDiscovered(t *testing.T) {
	store := govmemory.NewStore()
	h := governance.NewHandler(store,
		governance.WithEnvResolver(envmemory.NewStore()),
		governance.WithInstanceDiscoverer(&fakeDiscoverer{instances: []governance.Instance{
			{ID: "ep-1", ServiceID: "paas-shop-bff", Addr: "192.168.1.10:8080", Status: governance.StatusHealthy, LaneID: governance.LaneDefault},
		}}))
	h.Authorize = func(r *http.Request, perm string) bool { return true }

	// 建一个服务（手动注册表为空）
	r := req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "paas-shop-bff", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("建服务应 201，got %d: %s", w.Code, w.Body.String())
	}
	var svc governance.Service
	decodeData(t, w.Body.Bytes(), &svc)

	// 详情应返 discoverer 的 ready 实例（手动表为空，被数据面真源覆盖）
	r = req(acmeCtx(), "GET", "/api/services/"+svc.ID, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var detail governance.ServiceDetail
	decodeData(t, w.Body.Bytes(), &detail)
	if len(detail.Instances) != 1 || detail.Instances[0].Addr != "192.168.1.10:8080" {
		t.Fatalf("应返 discoverer 的 ready 实例，got %+v", detail.Instances)
	}
}

// TestHandlerServiceDetailDiscovererEmptyFallback 验证 discoverer 返空（未部署）时回退手动注册表。
func TestHandlerServiceDetailDiscovererEmptyFallback(t *testing.T) {
	store := govmemory.NewStore()
	h := governance.NewHandler(store,
		governance.WithEnvResolver(envmemory.NewStore()),
		governance.WithInstanceDiscoverer(&fakeDiscoverer{instances: nil}))
	h.Authorize = func(r *http.Request, perm string) bool { return true }

	r := req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "manual-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var svc governance.Service
	decodeData(t, w.Body.Bytes(), &svc)

	// 手动注册一条实例
	r = req(acmeCtx(), "POST", "/api/services/"+svc.ID+"/instances", governance.Instance{Addr: "10.0.0.1:8080"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("注册实例应 201，got %d", w.Code)
	}

	// discoverer 返空 -> 回退手动表（1 条）
	r = req(acmeCtx(), "GET", "/api/services/"+svc.ID, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var detail governance.ServiceDetail
	decodeData(t, w.Body.Bytes(), &detail)
	if len(detail.Instances) != 1 || detail.Instances[0].Addr != "10.0.0.1:8080" {
		t.Fatalf("discoverer 空应回退手动表，got %+v", detail.Instances)
	}
}

// TestCreateRouteRejectsDanglingServiceID：Route/Breaker 创建校验 ServiceID 归属
// （防悬挂引用——指向跨租户/不存在服务的脏数据，applier 解析失败静默跳过 path 用户不知）。
func TestCreateRouteRejectsDanglingServiceID(t *testing.T) {
	h := governance.NewHandler(govmemory.NewStore())
	h.Authorize = func(*http.Request, string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// 不存在的 serviceID
	body, _ := json.Marshal(governance.Route{Name: "r1", Path: "/x", ServiceID: "svc-nope", Methods: []string{governance.MethodGet}})
	r := httptest.NewRequest(http.MethodPost, "/api/routes", bytes.NewReader(body))
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("悬挂 serviceID 应 400，got %d: %s", w.Code, w.Body.String())
	}

	// Breaker 同款
	bbody, _ := json.Marshal(governance.CircuitBreaker{Name: "b1", ServiceID: "svc-nope", Strategy: governance.StrategyErrorRate, Threshold: 50, MinRequests: 5, WindowSecs: 60})
	r2 := httptest.NewRequest(http.MethodPost, "/api/breakers", bytes.NewReader(bbody))
	r2 = r2.WithContext(ctx)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("breaker 悬挂 serviceID 应 400，got %d", w2.Code)
	}
}

// TestRouteValidatePath：Path 必须 / 开头且不含 ..，Host 逗号分段非空（下发 Ingress 前拦截）。
func TestRouteValidatePath(t *testing.T) {
	if err := (governance.Route{Name: "r", Path: "api/x", ServiceID: "s", Methods: []string{governance.MethodGet}}).Validate(); err == nil {
		t.Fatal("非 / 开头 path 应拒绝")
	}
	if err := (governance.Route{Name: "r", Path: "/a/../b", ServiceID: "s", Methods: []string{governance.MethodGet}}).Validate(); err == nil {
		t.Fatal("含 .. path 应拒绝")
	}
	if err := (governance.Route{Name: "r", Path: "/a", Host: "a.com,,b.com", ServiceID: "s", Methods: []string{governance.MethodGet}}).Validate(); err == nil {
		t.Fatal("Host 空段应拒绝")
	}
	if err := (governance.Route{Name: "r", Path: "/a", Host: "a.com,b.com", ServiceID: "s", Methods: []string{governance.MethodGet}}).Validate(); err != nil {
		t.Fatalf("合法多 Host 应通过: %v", err)
	}
}

// TestAuditOnGovernanceWrites：治理写操作记审计（服务拓扑变更高敏感，action 前缀校验）。
func TestAuditOnGovernanceWrites(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	h := governance.NewHandler(govmemory.NewStore(), governance.WithAudit(auditFunc(
		func(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error {
			mu.Lock()
			defer mu.Unlock()
			actions = append(actions, action)
			return nil
		})))
	h.Authorize = func(*http.Request, string) bool { return true }
	h.CallerUserID = func(context.Context) string { return "u-1" }

	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 建服务（审计 service_create）
	sbody, _ := json.Marshal(governance.Service{Name: "svc-a", EnvID: "env-1", Protocol: governance.ProtocolHTTP, Port: 8080})
	r := httptest.NewRequest(http.MethodPost, "/api/services", bytes.NewReader(sbody))
	r = r.WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), r)

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 1 || actions[0] != "service_create" {
		t.Fatalf("service_create 应记审计，got %v", actions)
	}
}

// auditFunc 适配 AuditRecorder 接口的测试闭包（返 error 版）。
type auditFunc func(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error

func (f auditFunc) Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error {
	return f(ctx, tenantID, actor, action, resourceType, resourceID, detail)
}
