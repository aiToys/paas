//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 governance 4 表）避免残留。
//
// 测试覆盖：
//   - 4 实体 CRUD（Service / Instance / Route / CircuitBreaker）
//   - Instance.Meta（map[string]string）JSONB nil 安全往返
//   - Route.Methods（[]string）JSONB nil/空/多值往返
//   - Breaker 读出无 State/Stats（运行时由 EvaluateBreaker 即时填）
//   - DeleteService 级联清 instances/routes/breakers（事务原子）
//   - Heartbeat 仅刷新 UpdatedAt
//   - 租户内 Name 唯一冲突 → 领域错误
//   - 多租户隔离（缺失拒、跨租户 not found 不泄漏）

package pg

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/governance"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表。
func newTestDB(t *testing.T) *storagepg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	db, err := storagepg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := storagepg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

// resetSchema 清空所有业务表 + 迁移版本表，避免跨包测试残留污染。
// 覆盖全部已迁模块表（governance + devops + workload + environment + appconfig + dataservice + identity + application）。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS gov_breakers, gov_routes, gov_instances, gov_services,
			 releases, images, build_runs, code_repos,
			 workloads, data_services, appconfigs, environments,
			 application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

// sampleService 构造一条合法 Service（不含 TenantID，由 ctx 写入）。
func sampleService(id, name string) governance.Service {
	return governance.Service{
		ID: id, Name: name, AppID: "app-cs", EnvID: "env-acme-test",
		Protocol: governance.ProtocolHTTP, Port: 8080, Desc: "测试服务",
	}
}

// sampleInstance 构造一条合法 Instance（不含 TenantID/Status/LaneID，由 store 补）。
func sampleInstance(id, svcID, addr string) governance.Instance {
	return governance.Instance{ID: id, ServiceID: svcID, Addr: addr}
}

// sampleRoute 构造一条合法 Route。
func sampleRoute(id, name, svcID string, methods []string) governance.Route {
	return governance.Route{
		ID: id, Name: name, Path: "/api/v1/x", ServiceID: svcID,
		Methods: methods, StripPath: true, Enabled: true,
	}
}

// sampleBreaker 构造一条合法 CircuitBreaker。
func sampleBreaker(id, name, svcID string) governance.CircuitBreaker {
	return governance.CircuitBreaker{
		ID: id, Name: name, ServiceID: svcID,
		Strategy: governance.StrategyErrorRate, Threshold: 50,
		MinRequests: 20, WindowSecs: 60, Enabled: true,
	}
}

// createService helper：保证前置 Service 存在，简化后续实例/路由/熔断器测试。
func createService(t *testing.T, s *Store, ctx context.Context, id, name string) governance.Service {
	t.Helper()
	sv, err := s.CreateService(ctx, sampleService(id, name))
	if err != nil {
		t.Fatalf("CreateService(%s): %v", name, err)
	}
	return sv
}

// ---------- Service CRUD ----------

func TestServiceCreateGetList(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	sv, err := s.CreateService(ctx, sampleService("svc-1", "orders-api"))
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	if sv.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", sv.TenantID)
	}
	if sv.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt 应由 store 填充")
	}

	// Get 往返。
	g, err := s.GetService(ctx, "svc-1")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if g.Name != "orders-api" || g.Port != 8080 || g.Protocol != governance.ProtocolHTTP {
		t.Fatalf("GetService 往返不一致: %+v", g)
	}

	// List 多条 + envID/appID 过滤。
	createService(t, s, ctx, "svc-2", "cart-api")
	list, err := s.ListServices(ctx, "env-acme-test", "")
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListServices 应有 2 条, got %d", len(list))
	}
	// 按 name 升序：cart-api < orders-api。
	if list[0].Name != "cart-api" {
		t.Fatalf("ListServices 应按 name 升序, first=%s", list[0].Name)
	}
	// appID 过滤。
	list, _ = s.ListServices(ctx, "", "app-cs")
	if len(list) != 2 {
		t.Fatalf("appID=app-cs 应有 2 条, got %d", len(list))
	}
	list, _ = s.ListServices(ctx, "", "app-other")
	if len(list) != 0 {
		t.Fatalf("appID=app-other 应 0 条, got %d", len(list))
	}

	// Delete。
	if err := s.DeleteService(ctx, "svc-1"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if _, err := s.GetService(ctx, "svc-1"); err == nil {
		t.Fatalf("Delete 后 GetService 应报错")
	}
}

func TestServiceCreateUniqueName(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	createService(t, s, ctx, "svc-a", "dup")
	_, err := s.CreateService(ctx, sampleService("svc-b", "dup"))
	if err == nil || !strings.Contains(err.Error(), "服务名已存在") {
		t.Fatalf("期望「服务名已存在」, got %v", err)
	}
	// 跨租户同名应允许（UNIQUE 含 tenant_id）。
	if _, err := s.CreateService(globexCtx(), sampleService("svc-g", "dup")); err != nil {
		t.Fatalf("跨租户同名应允许, got %v", err)
	}
}

// ---------- Instance + Meta JSONB ----------

func TestInstanceMetaJSONBRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-m", "meta-svc")

	// nil Meta → 写入 '{}'，从 DB 读出空 map 非 nil。
	// 注意：RegisterInstance 返回的是经 store 补默认字段的输入 struct（与内存版一致），
	// Meta 字段保留输入 nil；DB 端 NORMALIZATION 后存 '{}'，验证须读 DB。
	inNil := sampleInstance("inst-nil", sv.ID, "10.0.0.1:8080")
	got, err := s.RegisterInstance(ctx, inNil)
	if err != nil {
		t.Fatalf("RegisterInstance nil meta: %v", err)
	}
	// 默认 Status/Lane 应被补全（返回 struct 上即可验证）。
	if got.Status != governance.StatusHealthy {
		t.Fatalf("Status 空应补 healthy, got %s", got.Status)
	}
	if got.LaneID != governance.LaneDefault {
		t.Fatalf("LaneID 空应补 default, got %s", got.LaneID)
	}
	// 从 DB 读回验证 Meta nil 安全（list[0] = inst-nil，addr 升序首位）。
	dbNil, _ := s.ListInstances(ctx, sv.ID)
	var dbInstNil governance.Instance
	for _, x := range dbNil {
		if x.ID == "inst-nil" {
			dbInstNil = x
		}
	}
	if dbInstNil.Meta == nil {
		t.Fatalf("nil Meta 读出应为空 map 非 nil")
	}
	if len(dbInstNil.Meta) != 0 {
		t.Fatalf("nil Meta 读出应为空 map, got %v", dbInstNil.Meta)
	}

	// 多键 Meta 往返。
	inRich := sampleInstance("inst-rich", sv.ID, "10.0.0.2:8080")
	inRich.Meta = map[string]string{"version": "v2", "weight": "100", "az": "us-east-1a"}
	got2, err := s.RegisterInstance(ctx, inRich)
	if err != nil {
		t.Fatalf("RegisterInstance rich meta: %v", err)
	}
	if got2.Meta["version"] != "v2" || got2.Meta["weight"] != "100" || got2.Meta["az"] != "us-east-1a" {
		t.Fatalf("Meta 往返丢失: %v", got2.Meta)
	}

	// ListInstances + serviceID 过滤。
	list, err := s.ListInstances(ctx, sv.ID)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应有 2 实例, got %d", len(list))
	}
	// 按 addr 升序。
	if list[0].ID != "inst-nil" {
		t.Fatalf("ListInstances 应按 addr 升序, first ID=%s", list[0].ID)
	}
	// 读出 meta 仍非 nil。
	if list[1].Meta["version"] != "v2" {
		t.Fatalf("ListInstances 读出 meta 丢失: %v", list[1].Meta)
	}

	// 全租户实例（serviceID 空）。
	all, _ := s.ListInstances(ctx, "")
	if len(all) != 2 {
		t.Fatalf("全租户实例应 2 条, got %d", len(all))
	}
}

func TestInstanceRegisterToMissingService(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	_, err := s.RegisterInstance(ctx, sampleInstance("inst-x", "no-such-svc", "10.0.0.1:8080"))
	if err == nil || !strings.Contains(err.Error(), "服务不存在") {
		t.Fatalf("注册到不存在服务应报「服务不存在」, got %v", err)
	}
}

func TestHeartbeat(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-h", "hb-svc")
	in, _ := s.RegisterInstance(ctx, sampleInstance("inst-h", sv.ID, "10.0.0.1:8080"))

	// 等一秒后心跳，UpdatedAt 必须推进（>0 差即可，避免平台时钟精度差异）。
	time.Sleep(time.Second + 50*time.Millisecond)
	hb, err := s.Heartbeat(ctx, "inst-h")
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !hb.UpdatedAt.After(in.UpdatedAt) {
		t.Fatalf("Heartbeat 后 UpdatedAt 应推进: prev=%v now=%v", in.UpdatedAt, hb.UpdatedAt)
	}

	// 跨租户心跳 not found。
	if _, err := s.Heartbeat(globexCtx(), "inst-h"); err == nil || !strings.Contains(err.Error(), "实例不存在") {
		t.Fatalf("跨租户 Heartbeat 应报「实例不存在」, got %v", err)
	}
}

func TestInstanceServiceID(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-sid", "sid-svc")
	s.RegisterInstance(ctx, sampleInstance("inst-sid", sv.ID, "10.0.0.1:8080"))
	svcID, err := s.InstanceServiceID(ctx, "inst-sid")
	if err != nil {
		t.Fatalf("InstanceServiceID: %v", err)
	}
	if svcID != sv.ID {
		t.Fatalf("InstanceServiceID 不一致: got %s want %s", svcID, sv.ID)
	}
	// 跨租户 not found。
	if _, err := s.InstanceServiceID(globexCtx(), "inst-sid"); err == nil ||
		!strings.Contains(err.Error(), "实例不存在") {
		t.Fatalf("跨租户 InstanceServiceID 应报「实例不存在」, got %v", err)
	}
}

// ---------- Route + Methods JSONB ----------

func TestRouteMethodsJSONBRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-r", "route-svc")

	// 多值 Methods 往返。
	r, err := s.CreateRoute(ctx, sampleRoute("route-1", "chat-api", sv.ID,
		[]string{governance.MethodGet, governance.MethodPost}))
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}
	if r.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", r.TenantID)
	}
	g, _ := s.GetRoute(ctx, "route-1")
	if len(g.Methods) != 2 || g.Methods[0] != governance.MethodGet || g.Methods[1] != governance.MethodPost {
		t.Fatalf("Methods 往返失败: %v", g.Methods)
	}
	if !g.StripPath || !g.Enabled {
		t.Fatalf("StripPath/Enabled 往返失败: %+v", g)
	}

	// 单值 ANY。
	r2, _ := s.CreateRoute(ctx, sampleRoute("route-2", "any-api", sv.ID, []string{governance.MethodAny}))
	g2, _ := s.GetRoute(ctx, "route-2")
	if len(g2.Methods) != 1 || g2.Methods[0] != governance.MethodAny {
		t.Fatalf("Methods 单值 ANY 往返失败: %v", g2.Methods)
	}
	_ = r2

	// ListRoutes 按 updated_at 倒序 + serviceID 过滤。
	list, _ := s.ListRoutes(ctx, sv.ID)
	if len(list) != 2 {
		t.Fatalf("ListRoutes 应 2 条, got %d", len(list))
	}
	if !list[0].UpdatedAt.After(list[1].UpdatedAt) && !list[0].UpdatedAt.Equal(list[1].UpdatedAt) {
		t.Fatalf("ListRoutes 应倒序, first=%v second=%v", list[0].UpdatedAt, list[1].UpdatedAt)
	}

	// UpdateRoute：改 methods + stripPath + enabled。
	up, err := s.UpdateRoute(ctx, governance.Route{
		ID: "route-1", Methods: []string{governance.MethodPut}, StripPath: false, Enabled: false,
	})
	if err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}
	if len(up.Methods) != 1 || up.Methods[0] != governance.MethodPut || up.StripPath || up.Enabled {
		t.Fatalf("UpdateRoute 后字段不一致: %+v", up)
	}

	// Methods=nil 表示不改：再次 Update 应保留 [PUT]。
	up2, _ := s.UpdateRoute(ctx, governance.Route{ID: "route-1", StripPath: true, Enabled: true})
	if len(up2.Methods) != 1 || up2.Methods[0] != governance.MethodPut {
		t.Fatalf("Methods=nil 应保留原值, got %v", up2.Methods)
	}

	// Delete。
	if err := s.DeleteRoute(ctx, "route-1"); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	if _, err := s.GetRoute(ctx, "route-1"); err == nil {
		t.Fatalf("Delete 后 GetRoute 应报错")
	}
}

func TestRouteCreateUniqueName(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-ru", "ru-svc")
	s.CreateRoute(ctx, sampleRoute("ra", "dup-route", sv.ID, []string{governance.MethodGet}))
	_, err := s.CreateRoute(ctx, sampleRoute("rb", "dup-route", sv.ID, []string{governance.MethodPost}))
	if err == nil || !strings.Contains(err.Error(), "路由名已存在") {
		t.Fatalf("期望「路由名已存在」, got %v", err)
	}
}

// ---------- Breaker（不持久化 State/Stats） ----------

func TestBreakerNoStatePersisted(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-b", "breaker-svc")

	b, err := s.CreateBreaker(ctx, sampleBreaker("cb-1", "err-brk", sv.ID))
	if err != nil {
		t.Fatalf("CreateBreaker: %v", err)
	}
	if b.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", b.TenantID)
	}

	// 读出：State/Stats 必须为零值（store 不持久化、不填充）。
	g, _ := s.GetBreaker(ctx, "cb-1")
	if g.State != "" {
		t.Fatalf("Breaker.State 读出必须为空（运行时由 EvaluateBreaker 填充）, got %q", g.State)
	}
	if g.Stats != (governance.WindowStats{}) {
		t.Fatalf("Breaker.Stats 读出必须为零值, got %+v", g.Stats)
	}
	// 配置列往返。
	if g.Strategy != governance.StrategyErrorRate || g.Threshold != 50 ||
		g.MinRequests != 20 || g.WindowSecs != 60 || !g.Enabled {
		t.Fatalf("Breaker 配置列往返不一致: %+v", g)
	}

	// 模拟 handler 行为：调 EvaluateBreaker 即时填充。
	stats, state := governance.EvaluateBreaker(g, time.Now())
	g.Stats = stats
	g.State = state
	if g.State != governance.StateClosed && g.State != governance.StateOpen && g.State != governance.StateHalfOpen {
		t.Fatalf("EvaluateBreaker 后 State 应为合法三态之一, got %q", g.State)
	}

	// ListBreakers + serviceID 过滤。
	s.CreateBreaker(ctx, sampleBreaker("cb-2", "slow-brk", sv.ID))
	list, _ := s.ListBreakers(ctx, sv.ID)
	if len(list) != 2 {
		t.Fatalf("ListBreakers 应 2 条, got %d", len(list))
	}
	// 所有读出的 State 仍为空（确认未持久化）。
	for _, lb := range list {
		if lb.State != "" {
			t.Fatalf("ListBreakers 读出的 State 必须为空, got %q", lb.State)
		}
	}

	// UpdateBreaker：改 threshold/enabled/serviceId。
	up, err := s.UpdateBreaker(ctx, governance.CircuitBreaker{
		ID: "cb-1", Threshold: 80, Enabled: false, ServiceID: sv.ID,
	})
	if err != nil {
		t.Fatalf("UpdateBreaker: %v", err)
	}
	if up.Threshold != 80 || up.Enabled {
		t.Fatalf("UpdateBreaker 后字段不一致: %+v", up)
	}

	// Delete。
	if err := s.DeleteBreaker(ctx, "cb-1"); err != nil {
		t.Fatalf("DeleteBreaker: %v", err)
	}
}

func TestBreakerCreateUniqueName(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-bu", "bu-svc")
	s.CreateBreaker(ctx, sampleBreaker("cba", "dup-brk", sv.ID))
	_, err := s.CreateBreaker(ctx, sampleBreaker("cbb", "dup-brk", sv.ID))
	if err == nil || !strings.Contains(err.Error(), "熔断器名已存在") {
		t.Fatalf("期望「熔断器名已存在」, got %v", err)
	}
}

// ---------- DeleteService 级联 ----------

func TestDeleteServiceCascade(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-casc", "cascade-svc")

	// 灌子表：2 实例 + 1 路由 + 1 熔断器。
	s.RegisterInstance(ctx, sampleInstance("ic-1", sv.ID, "10.0.0.1:8080"))
	s.RegisterInstance(ctx, sampleInstance("ic-2", sv.ID, "10.0.0.2:8080"))
	s.CreateRoute(ctx, sampleRoute("rc-1", "casc-route", sv.ID, []string{governance.MethodGet}))
	s.CreateBreaker(ctx, sampleBreaker("cbc-1", "casc-brk", sv.ID))

	// 删另一服务（无子表）确认不影响。
	other := createService(t, s, ctx, "svc-other", "other-svc")
	s.RegisterInstance(ctx, sampleInstance("io-1", other.ID, "10.1.0.1:8080"))

	// 删除目标服务。
	if err := s.DeleteService(ctx, sv.ID); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}

	// 级联：实例/路由/熔断器全清。
	if list, _ := s.ListInstances(ctx, sv.ID); len(list) != 0 {
		t.Fatalf("级联清 instances 失败: 仍剩 %d", len(list))
	}
	if list, _ := s.ListRoutes(ctx, sv.ID); len(list) != 0 {
		t.Fatalf("级联清 routes 失败: 仍剩 %d", len(list))
	}
	if list, _ := s.ListBreakers(ctx, sv.ID); len(list) != 0 {
		t.Fatalf("级联清 breakers 失败: 仍剩 %d", len(list))
	}

	// 另一服务的实例保留。
	if list, _ := s.ListInstances(ctx, other.ID); len(list) != 1 {
		t.Fatalf("不应误删其他服务的实例, got %d", len(list))
	}
}

// ---------- 多租户隔离 ----------

func TestTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	acme := acmeCtx()
	globex := globexCtx()

	// Acme 创建服务 + 实例 + 路由 + 熔断器。
	sv := createService(t, s, acme, "svc-acme", "acme-only")
	s.RegisterInstance(acme, sampleInstance("ia", sv.ID, "10.0.0.1:8080"))
	s.CreateRoute(acme, sampleRoute("ra", "acme-route", sv.ID, []string{governance.MethodGet}))
	s.CreateBreaker(acme, sampleBreaker("cba", "acme-brk", sv.ID))

	// Globex 看不到 Acme 的资源（跨租户 not found / 0 条）。
	if list, _ := s.ListServices(globex, "", ""); len(list) != 0 {
		t.Fatalf("跨租户 ListServices 应 0 条, got %d", len(list))
	}
	if _, err := s.GetService(globex, sv.ID); err == nil ||
		!strings.Contains(err.Error(), "服务不存在") {
		t.Fatalf("跨租户 GetService 应 not found, got %v", err)
	}
	if list, _ := s.ListInstances(globex, sv.ID); len(list) != 0 {
		t.Fatalf("跨租户 ListInstances 应 0 条, got %d", len(list))
	}
	if _, err := s.GetRoute(globex, "ra"); err == nil ||
		!strings.Contains(err.Error(), "路由不存在") {
		t.Fatalf("跨租户 GetRoute 应 not found, got %v", err)
	}
	if _, err := s.GetBreaker(globex, "cba"); err == nil ||
		!strings.Contains(err.Error(), "熔断器不存在") {
		t.Fatalf("跨租户 GetBreaker 应 not found, got %v", err)
	}

	// Globex 删 Acme 的资源：RowsAffected==0 → not found，不泄漏存在性。
	if err := s.DeleteService(globex, sv.ID); err == nil ||
		!strings.Contains(err.Error(), "服务不存在") {
		t.Fatalf("跨租户 DeleteService 应 not found, got %v", err)
	}
	if err := s.DeleteRoute(globex, "ra"); err == nil ||
		!strings.Contains(err.Error(), "路由不存在") {
		t.Fatalf("跨租户 DeleteRoute 应 not found, got %v", err)
	}
	if err := s.DeleteBreaker(globex, "cba"); err == nil ||
		!strings.Contains(err.Error(), "熔断器不存在") {
		t.Fatalf("跨租户 DeleteBreaker 应 not found, got %v", err)
	}
	if err := s.DeregisterInstance(globex, "ia"); err == nil ||
		!strings.Contains(err.Error(), "实例不存在") {
		t.Fatalf("跨租户 DeregisterInstance 应 not found, got %v", err)
	}

	// Globex 同名服务应允许创建（UNIQUE(tenant_id, name)）。
	if _, err := s.CreateService(globex, sampleService("svc-globex", "acme-only")); err != nil {
		t.Fatalf("跨租户同名应允许, got %v", err)
	}
}

func TestMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := noTenantCtx()

	// 全部方法：缺失租户即拒（fail-closed）。
	if _, err := s.ListServices(ctx, "", ""); err == nil {
		t.Fatalf("ListServices 缺失租户应拒")
	}
	if _, err := s.GetService(ctx, "x"); err == nil {
		t.Fatalf("GetService 缺失租户应拒")
	}
	if _, err := s.CreateService(ctx, sampleService("x", "x")); err == nil {
		t.Fatalf("CreateService 缺失租户应拒")
	}
	if err := s.DeleteService(ctx, "x"); err == nil {
		t.Fatalf("DeleteService 缺失租户应拒")
	}
	if _, err := s.RegisterInstance(ctx, sampleInstance("x", "x", "x")); err == nil {
		t.Fatalf("RegisterInstance 缺失租户应拒")
	}
	if _, err := s.ListInstances(ctx, ""); err == nil {
		t.Fatalf("ListInstances 缺失租户应拒")
	}
	if err := s.DeregisterInstance(ctx, "x"); err == nil {
		t.Fatalf("DeregisterInstance 缺失租户应拒")
	}
	if _, err := s.Heartbeat(ctx, "x"); err == nil {
		t.Fatalf("Heartbeat 缺失租户应拒")
	}
	if _, err := s.InstanceServiceID(ctx, "x"); err == nil {
		t.Fatalf("InstanceServiceID 缺失租户应拒")
	}
	if _, err := s.CreateRoute(ctx, sampleRoute("x", "x", "x", []string{governance.MethodGet})); err == nil {
		t.Fatalf("CreateRoute 缺失租户应拒")
	}
	if _, err := s.ListBreakers(ctx, ""); err == nil {
		t.Fatalf("ListBreakers 缺失租户应拒")
	}
	if _, err := s.CreateBreaker(ctx, sampleBreaker("x", "x", "x")); err == nil {
		t.Fatalf("CreateBreaker 缺失租户应拒")
	}
}

// ---------- Count 方法（seed 判空用） ----------

func TestCountMethods(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 空表。
	n, _ := s.ServicesCount(ctx)
	if n != 0 {
		t.Fatalf("空表 ServicesCount 应 0, got %d", n)
	}

	// 灌数据。
	sv := createService(t, s, ctx, "svc-c1", "count-svc")
	s.RegisterInstance(ctx, sampleInstance("ic", sv.ID, "10.0.0.1:8080"))
	s.CreateRoute(ctx, sampleRoute("rc", "count-route", sv.ID, []string{governance.MethodGet}))
	s.CreateBreaker(ctx, sampleBreaker("cbc", "count-brk", sv.ID))

	if n, _ := s.ServicesCount(ctx); n != 1 {
		t.Fatalf("ServicesCount 应 1, got %d", n)
	}
	if n, _ := s.InstancesCount(ctx); n != 1 {
		t.Fatalf("InstancesCount 应 1, got %d", n)
	}
	if n, _ := s.RoutesCount(ctx); n != 1 {
		t.Fatalf("RoutesCount 应 1, got %d", n)
	}
	if n, _ := s.BreakersCount(ctx); n != 1 {
		t.Fatalf("BreakersCount 应 1, got %d", n)
	}
}

// TestRouteHostRoundTrip 验证 Route.Host 字段持久化 + 更新 + 清空（对外域名配置）。
func TestRouteHostRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	sv := createService(t, s, ctx, "svc-h", "host-svc")

	// Create 带 Host。
	created, err := s.CreateRoute(ctx, governance.Route{
		ID: "route-h", Name: "host-api", Host: "api.acme.com",
		Path: "/api/v1/*", ServiceID: sv.ID, Methods: []string{governance.MethodAny},
		StripPath: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}
	if created.Host != "api.acme.com" {
		t.Fatalf("CreateRoute Host 往返失败: %s", created.Host)
	}
	g, _ := s.GetRoute(ctx, "route-h")
	if g.Host != "api.acme.com" {
		t.Fatalf("GetRoute Host 持久化失败: %s", g.Host)
	}

	// UpdateRoute 改 Host（传完整值，避免 bool 直接覆盖语义误清原值）。
	up, err := s.UpdateRoute(ctx, governance.Route{
		ID: "route-h", Host: "beta.acme.com", Path: "/api/v1/*",
		Methods: []string{governance.MethodAny}, StripPath: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpdateRoute Host: %v", err)
	}
	if up.Host != "beta.acme.com" {
		t.Fatalf("UpdateRoute Host 失败: %s", up.Host)
	}

	// Host 清空（直接覆盖语义，允许从有域名改回不限 Host）。
	up2, _ := s.UpdateRoute(ctx, governance.Route{
		ID: "route-h", Host: "", Path: "/api/v1/*",
		Methods: []string{governance.MethodAny}, StripPath: true, Enabled: true,
	})
	if up2.Host != "" {
		t.Fatalf("Host 应可清空, got %s", up2.Host)
	}
}

// 编译期断言：pgx.ErrNoRows 用于错误映射（避免误删 import）。
var _ = errors.Is
var _ = pgx.ErrNoRows
