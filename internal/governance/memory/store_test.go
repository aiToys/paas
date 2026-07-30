package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// TestTenantIsolation 验证服务/实例按租户隔离。
func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, _ := s.ListServices(acmeCtx(), "", "")
	globex, _ := s.ListServices(globexCtx(), "", "")
	for _, sv := range acme {
		if sv.TenantID != "t-acme" {
			t.Fatalf("acme 视图泄漏其他租户服务: %s", sv.Name)
		}
	}
	for _, sv := range globex {
		if sv.TenantID != "t-globex" {
			t.Fatalf("globex 视图泄漏其他租户服务: %s", sv.Name)
		}
	}
	// 跨租户 Get 不泄漏存在性
	if _, err := s.GetService(acmeCtx(), "svc-globex-agent"); err == nil {
		t.Fatal("acme 不应见到 globex 服务")
	}
}

// TestCreateServiceDedup 验证租户内服务名唯一。
func TestCreateServiceDedup(t *testing.T) {
	s := NewStore()
	_, err := s.CreateService(acmeCtx(), governance.Service{
		Name: "customer-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	if err == nil {
		t.Fatal("同名服务应冲突")
	}
}

// TestInstanceLifecycle 验证实例注册/发现/心跳/注销。
func TestInstanceLifecycle(t *testing.T) {
	s := NewStore()
	in, err := s.RegisterInstance(acmeCtx(), governance.Instance{
		ServiceID: "svc-acme-cs", Addr: "10.0.1.99:8080",
	})
	if err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	if in.Status != governance.StatusHealthy {
		t.Fatalf("新实例默认 healthy，got %s", in.Status)
	}
	// 发现
	list, _ := s.ListInstances(acmeCtx(), "svc-acme-cs")
	found := false
	for _, x := range list {
		if x.ID == in.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("注册后实例应可被发现")
	}
	// 心跳
	hb, err := s.Heartbeat(acmeCtx(), in.ID)
	if err != nil || hb.Status != governance.StatusHealthy {
		t.Fatalf("心跳失败: %v", err)
	}
	// 注销
	if err := s.DeregisterInstance(acmeCtx(), in.ID); err != nil {
		t.Fatalf("注销失败: %v", err)
	}
	if _, err := s.Heartbeat(acmeCtx(), in.ID); err == nil {
		t.Fatal("注销后心跳应失败")
	}
}

// TestDeleteServiceCascade 验证注销服务级联清除实例。
func TestDeleteServiceCascade(t *testing.T) {
	s := NewStore()
	if err := s.DeleteService(acmeCtx(), "svc-acme-cs"); err != nil {
		t.Fatalf("注销服务失败: %v", err)
	}
	list, _ := s.ListInstances(acmeCtx(), "svc-acme-cs")
	if len(list) != 0 {
		t.Fatalf("服务注销应级联清实例，剩余 %d", len(list))
	}
}

// TestMissingTenant 验证缺失租户上下文即拒。
func TestMissingTenant(t *testing.T) {
	s := NewStore()
	if _, err := s.ListServices(context.Background(), "", ""); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}

// —— Route 测试 ——

// TestRouteListAndFilter 验证路由列表 + 按 serviceID 过滤。
func TestRouteListAndFilter(t *testing.T) {
	s := NewStore()
	all, _ := s.ListRoutes(acmeCtx(), "")
	if len(all) != 2 {
		t.Fatalf("acme 应 2 条路由，got %d", len(all))
	}
	bySvc, _ := s.ListRoutes(acmeCtx(), "svc-acme-cs")
	if len(bySvc) != 1 || bySvc[0].Name != "chat-api" {
		t.Fatalf("按服务过滤应 1 条 chat-api，got %+v", bySvc)
	}
}

// TestRouteCreate 验证创建 + 租户内名唯一。
func TestRouteCreate(t *testing.T) {
	s := NewStore()
	r, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "new-route", Path: "/api/x/*", ServiceID: "svc-acme-cs",
		Methods: []string{governance.MethodGet}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if r.TenantID != "t-acme" || !r.Enabled {
		t.Fatalf("ctx 注入/默认值异常: %+v", r)
	}
	// 重名冲突
	if _, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "new-route", Path: "/y", ServiceID: "svc-acme-cs", Methods: []string{governance.MethodGet},
	}); err == nil {
		t.Fatal("重名应冲突")
	}
}

// TestRouteValidate 验证路由校验。
func TestRouteValidate(t *testing.T) {
	s := NewStore()
	// 缺 methods
	if _, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "bad", Path: "/x", ServiceID: "svc-acme-cs",
	}); err == nil {
		t.Fatal("缺 methods 应校验失败")
	}
	// 非法 method
	if _, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "bad2", Path: "/x", ServiceID: "svc-acme-cs", Methods: []string{"PATCH"},
	}); err == nil {
		t.Fatal("非法 method 应校验失败")
	}
}

// TestRouteUpdate 验证更新（启停）。
func TestRouteUpdate(t *testing.T) {
	s := NewStore()
	r, err := s.UpdateRoute(acmeCtx(), governance.Route{ID: "route-acme-chat", Enabled: false})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if r.Enabled {
		t.Fatal("应已禁用")
	}
}

// TestRouteTenantIsolation 验证路由跨租户隔离。
func TestRouteTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, _ := s.ListRoutes(acmeCtx(), "")
	globex, _ := s.ListRoutes(globexCtx(), "")
	if len(globex) != 1 {
		t.Fatalf("globex 应 1 条路由，got %d", len(globex))
	}
	// globex 跨租户 Get acme 路由应 not found
	if _, err := s.GetRoute(globexCtx(), "route-acme-chat"); err == nil {
		t.Fatal("globex 不应见到 acme 路由")
	}
	for _, r := range acme {
		if r.TenantID != "t-acme" {
			t.Fatalf("acme 路由泄漏其它租户: %+v", r)
		}
	}
}

// TestRouteDelete 验证删除 + 跨租户拒绝。
func TestRouteDelete(t *testing.T) {
	s := NewStore()
	if err := s.DeleteRoute(globexCtx(), "route-acme-chat"); err == nil {
		t.Fatal("跨租户删除应拒绝")
	}
	if err := s.DeleteRoute(acmeCtx(), "route-acme-chat"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	all, _ := s.ListRoutes(acmeCtx(), "")
	if len(all) != 1 {
		t.Fatalf("删除后应 1 条，got %d", len(all))
	}
}

// —— CircuitBreaker ——

// TestBreakerListAndFilter 验证 seed 加载 + serviceID 过滤。
func TestBreakerListAndFilter(t *testing.T) {
	s := NewStore()
	all, err := s.ListBreakers(acmeCtx(), "")
	if err != nil {
		t.Fatalf("ListBreakers: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("acme 应 2 条熔断器，got %d", len(all))
	}
	forCS, _ := s.ListBreakers(acmeCtx(), "svc-acme-cs")
	if len(forCS) != 1 || forCS[0].ServiceID != "svc-acme-cs" {
		t.Fatalf("svc-acme-cs 过滤应 1 条，got %+v", forCS)
	}
}

// TestBreakerCreate 验证创建 + 重名拒绝。
func TestBreakerCreate(t *testing.T) {
	s := NewStore()
	created, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "new-cb", ServiceID: "svc-acme-cs",
		Strategy: governance.StrategyErrorRate, Threshold: 30,
		MinRequests: 5, WindowSecs: 60, Enabled: true,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if created.ID == "" || created.TenantID != "t-acme" {
		t.Fatalf("创建回填异常: %+v", created)
	}
	if _, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "new-cb", ServiceID: "svc-acme-cs",
		Strategy: governance.StrategyErrorRate, Threshold: 30, MinRequests: 5, WindowSecs: 60,
	}); err == nil {
		t.Fatal("重名应拒绝")
	}
}

// TestBreakerUpdate 验证更新字段。
func TestBreakerUpdate(t *testing.T) {
	s := NewStore()
	b, err := s.UpdateBreaker(acmeCtx(), governance.CircuitBreaker{
		ID: "cb-acme-cs-err", Threshold: 75, Enabled: false,
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if b.Threshold != 75 || b.Enabled {
		t.Fatalf("更新未生效: %+v", b)
	}
}

// TestBreakerTenantIsolation 验证跨租户不可见。
func TestBreakerTenantIsolation(t *testing.T) {
	s := NewStore()
	all, _ := s.ListBreakers(globexCtx(), "")
	if len(all) != 1 {
		t.Fatalf("globex 应 1 条熔断器，got %d", len(all))
	}
	if _, err := s.GetBreaker(globexCtx(), "cb-acme-cs-err"); err == nil {
		t.Fatal("跨租户 GetBreaker 应拒绝")
	}
	if err := s.DeleteBreaker(globexCtx(), "cb-acme-cs-err"); err == nil {
		t.Fatal("跨租户删除应拒绝")
	}
}

// TestBreakerDelete 验证删除。
func TestBreakerDelete(t *testing.T) {
	s := NewStore()
	if err := s.DeleteBreaker(acmeCtx(), "cb-acme-rec-slow"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	all, _ := s.ListBreakers(acmeCtx(), "")
	if len(all) != 1 {
		t.Fatalf("删除后应 1 条，got %d", len(all))
	}
}
