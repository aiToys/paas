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
	// 先建一个 customer-svc
	if _, err := s.CreateService(acmeCtx(), governance.Service{
		Name: "customer-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	}); err != nil {
		t.Fatalf("建服务失败: %v", err)
	}
	// 同名再建应冲突
	if _, err := s.CreateService(acmeCtx(), governance.Service{
		Name: "customer-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	}); err == nil {
		t.Fatal("同名服务应冲突")
	}
}

// TestInstanceLifecycle 验证实例注册/发现/心跳/注销。
func TestInstanceLifecycle(t *testing.T) {
	s := NewStore()
	// 先建服务，拿到 svcID（实例必须挂靠真实存在的服务）
	svc, err := s.CreateService(acmeCtx(), governance.Service{
		Name: "customer-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	if err != nil {
		t.Fatalf("建服务失败: %v", err)
	}
	in, err := s.RegisterInstance(acmeCtx(), governance.Instance{
		ServiceID: svc.ID, Addr: "10.0.1.99:8080",
	})
	if err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	if in.Status != governance.StatusHealthy {
		t.Fatalf("新实例默认 healthy，got %s", in.Status)
	}
	// 发现
	list, _ := s.ListInstances(acmeCtx(), svc.ID)
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
	// 先建服务 + 实例
	svc, err := s.CreateService(acmeCtx(), governance.Service{
		Name: "customer-svc", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	if err != nil {
		t.Fatalf("建服务失败: %v", err)
	}
	if _, err := s.RegisterInstance(acmeCtx(), governance.Instance{
		ServiceID: svc.ID, Addr: "10.0.1.99:8080",
	}); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	// 注销服务应级联清实例
	if err := s.DeleteService(acmeCtx(), svc.ID); err != nil {
		t.Fatalf("注销服务失败: %v", err)
	}
	list, _ := s.ListInstances(acmeCtx(), svc.ID)
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
	// 自建 2 条 acme 路由，绑不同服务（Route 不校验 ServiceID 存在，纯逻辑配置）
	if _, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "chat-api", Path: "/api/v1/chat/*", ServiceID: "svc-acme-cs",
		Methods: []string{governance.MethodPost}, Enabled: true,
	}); err != nil {
		t.Fatalf("建路由 chat-api 失败: %v", err)
	}
	if _, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "recommend-grpc", Path: "/grpc.recommend/*", ServiceID: "svc-acme-rec",
		Methods: []string{governance.MethodPost}, Enabled: true,
	}); err != nil {
		t.Fatalf("建路由 recommend-grpc 失败: %v", err)
	}
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
	// 自建路由，拿到 ID 后更新
	r, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "chat-api", Path: "/api/v1/chat/*", ServiceID: "svc-acme-cs",
		Methods: []string{governance.MethodPost}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("建路由失败: %v", err)
	}
	updated, err := s.UpdateRoute(acmeCtx(), governance.Route{ID: r.ID, Enabled: false})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.Enabled {
		t.Fatal("应已禁用")
	}
}

// TestRouteTenantIsolation 验证路由跨租户隔离。
func TestRouteTenantIsolation(t *testing.T) {
	s := NewStore()
	// acme 1 条 + globex 1 条
	acmeR, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "chat-api", Path: "/api/v1/chat/*", ServiceID: "svc-acme-cs",
		Methods: []string{governance.MethodPost}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("建 acme 路由失败: %v", err)
	}
	if _, err := s.CreateRoute(globexCtx(), governance.Route{
		Name: "agent-api", Path: "/api/agent/*", ServiceID: "svc-globex-agent",
		Methods: []string{governance.MethodAny}, Enabled: true,
	}); err != nil {
		t.Fatalf("建 globex 路由失败: %v", err)
	}
	acme, _ := s.ListRoutes(acmeCtx(), "")
	globex, _ := s.ListRoutes(globexCtx(), "")
	if len(globex) != 1 {
		t.Fatalf("globex 应 1 条路由，got %d", len(globex))
	}
	// globex 跨租户 Get acme 路由应 not found
	if _, err := s.GetRoute(globexCtx(), acmeR.ID); err == nil {
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
	// 自建 2 条 acme 路由
	r1, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "chat-api", Path: "/api/v1/chat/*", ServiceID: "svc-acme-cs",
		Methods: []string{governance.MethodPost}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("建路由 r1 失败: %v", err)
	}
	if _, err := s.CreateRoute(acmeCtx(), governance.Route{
		Name: "recommend-grpc", Path: "/grpc.recommend/*", ServiceID: "svc-acme-rec",
		Methods: []string{governance.MethodPost}, Enabled: true,
	}); err != nil {
		t.Fatalf("建路由 r2 失败: %v", err)
	}
	// 跨租户删除应拒绝
	if err := s.DeleteRoute(globexCtx(), r1.ID); err == nil {
		t.Fatal("跨租户删除应拒绝")
	}
	// 同租户删除
	if err := s.DeleteRoute(acmeCtx(), r1.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	all, _ := s.ListRoutes(acmeCtx(), "")
	if len(all) != 1 {
		t.Fatalf("删除后应 1 条，got %d", len(all))
	}
}

// —— CircuitBreaker ——

// TestBreakerListAndFilter 验证列表 + serviceID 过滤。
func TestBreakerListAndFilter(t *testing.T) {
	s := NewStore()
	// 自建 2 条 acme 熔断器，绑不同服务
	if _, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "cs-error-breaker", ServiceID: "svc-acme-cs",
		Strategy: governance.StrategyErrorRate, Threshold: 50,
		MinRequests: 20, WindowSecs: 60, Enabled: true,
	}); err != nil {
		t.Fatalf("建熔断器 cs-error 失败: %v", err)
	}
	if _, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "rec-slow-breaker", ServiceID: "svc-acme-rec",
		Strategy: governance.StrategySlowCall, Threshold: 60,
		MinRequests: 15, WindowSecs: 120, Enabled: true,
	}); err != nil {
		t.Fatalf("建熔断器 rec-slow 失败: %v", err)
	}
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
	// 自建熔断器，拿到 ID 后更新
	b, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "cs-error-breaker", ServiceID: "svc-acme-cs",
		Strategy: governance.StrategyErrorRate, Threshold: 50,
		MinRequests: 20, WindowSecs: 60, Enabled: true,
	})
	if err != nil {
		t.Fatalf("建熔断器失败: %v", err)
	}
	updated, err := s.UpdateBreaker(acmeCtx(), governance.CircuitBreaker{
		ID: b.ID, Threshold: 75, Enabled: false,
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.Threshold != 75 || updated.Enabled {
		t.Fatalf("更新未生效: %+v", updated)
	}
}

// TestBreakerTenantIsolation 验证跨租户不可见。
func TestBreakerTenantIsolation(t *testing.T) {
	s := NewStore()
	// acme 1 条 + globex 1 条
	acmeB, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "cs-error-breaker", ServiceID: "svc-acme-cs",
		Strategy: governance.StrategyErrorRate, Threshold: 50,
		MinRequests: 20, WindowSecs: 60, Enabled: true,
	})
	if err != nil {
		t.Fatalf("建 acme 熔断器失败: %v", err)
	}
	if _, err := s.CreateBreaker(globexCtx(), governance.CircuitBreaker{
		Name: "agent-error-breaker", ServiceID: "svc-globex-agent",
		Strategy: governance.StrategyErrorRate, Threshold: 40,
		MinRequests: 10, WindowSecs: 60, Enabled: true,
	}); err != nil {
		t.Fatalf("建 globex 熔断器失败: %v", err)
	}
	all, _ := s.ListBreakers(globexCtx(), "")
	if len(all) != 1 {
		t.Fatalf("globex 应 1 条熔断器，got %d", len(all))
	}
	if _, err := s.GetBreaker(globexCtx(), acmeB.ID); err == nil {
		t.Fatal("跨租户 GetBreaker 应拒绝")
	}
	if err := s.DeleteBreaker(globexCtx(), acmeB.ID); err == nil {
		t.Fatal("跨租户删除应拒绝")
	}
}

// TestBreakerDelete 验证删除。
func TestBreakerDelete(t *testing.T) {
	s := NewStore()
	// 自建 2 条 acme 熔断器
	if _, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "cs-error-breaker", ServiceID: "svc-acme-cs",
		Strategy: governance.StrategyErrorRate, Threshold: 50,
		MinRequests: 20, WindowSecs: 60, Enabled: true,
	}); err != nil {
		t.Fatalf("建熔断器 cs-error 失败: %v", err)
	}
	rec, err := s.CreateBreaker(acmeCtx(), governance.CircuitBreaker{
		Name: "rec-slow-breaker", ServiceID: "svc-acme-rec",
		Strategy: governance.StrategySlowCall, Threshold: 60,
		MinRequests: 15, WindowSecs: 120, Enabled: true,
	})
	if err != nil {
		t.Fatalf("建熔断器 rec-slow 失败: %v", err)
	}
	if err := s.DeleteBreaker(acmeCtx(), rec.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	all, _ := s.ListBreakers(acmeCtx(), "")
	if len(all) != 1 {
		t.Fatalf("删除后应 1 条，got %d", len(all))
	}
}
