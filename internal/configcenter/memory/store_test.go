package memory

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// mustCreateNs 在指定 ctx 下创建命名空间，返回分配的 ID（测试不再依赖 seed）。
func mustCreateNs(t *testing.T, s *Store, ctx context.Context, name string) string {
	t.Helper()
	n, err := s.CreateNamespace(ctx, configcenter.Namespace{Name: name})
	if err != nil {
		t.Fatalf("创建命名空间 %s 失败: %v", name, err)
	}
	return n.ID
}

// mustUpsertItem 在指定 ns 下 upsert 配置项。
func mustUpsertItem(t *testing.T, s *Store, ctx context.Context, nsID, key, value string) {
	t.Helper()
	if _, err := s.UpsertItem(ctx, configcenter.ConfigItem{
		NamespaceID: nsID, Key: key, Value: value, Type: configcenter.TypeText,
	}); err != nil {
		t.Fatalf("upsert item %s 失败: %v", key, err)
	}
}

// mustPublish 发布一版，返回新发布。
func mustPublish(t *testing.T, s *Store, ctx context.Context, nsID string) configcenter.Publish {
	t.Helper()
	pub, err := s.CreatePublish(ctx, nsID)
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	return pub
}

// TestTenantIsolation 验证命名空间/配置/发布按租户隔离。
func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	acmeID := mustCreateNs(t, s, acmeCtx(), "acme-app")
	mustCreateNs(t, s, globexCtx(), "globex-app")

	acme, _ := s.ListNamespaces(acmeCtx(), "")
	globex, _ := s.ListNamespaces(globexCtx(), "")
	for _, n := range acme {
		if n.TenantID != "t-acme" {
			t.Fatalf("acme 视图泄漏: %s", n.Name)
		}
	}
	if len(globex) == 0 || globex[0].TenantID != "t-globex" {
		t.Fatal("globex 视图异常")
	}
	// 跨租户访问对方命名空间应 not found（不泄漏存在性）
	if _, err := s.GetNamespace(acmeCtx(), acmeID); err != nil {
		t.Fatal("acme 访问自己命名空间不应失败")
	}
	for _, g := range globex {
		if _, err := s.GetNamespace(acmeCtx(), g.ID); err == nil {
			t.Fatal("acme 不应见到 globex 命名空间")
		}
	}
}

// TestPublishAndDiscover 验证发布生成快照 + 客户端发现 + 旧 active 转 rolled-back。
func TestPublishAndDiscover(t *testing.T) {
	s := NewStore()
	nsID := mustCreateNs(t, s, acmeCtx(), "acme-app")
	// 第一版（v1）：seed 一项后发布
	mustUpsertItem(t, s, acmeCtx(), nsID, "feature.newui", "off")
	v1 := mustPublish(t, s, acmeCtx(), nsID)
	if v1.Version != 1 {
		t.Fatalf("首版应为 v1，got v%d", v1.Version)
	}
	// 编辑 draft 后再发布（v2）
	mustUpsertItem(t, s, acmeCtx(), nsID, "rate.limit", "200")
	pub, err := s.CreatePublish(acmeCtx(), nsID)
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if pub.Version != 2 {
		t.Fatalf("版本应递增到 2，got %d", pub.Version)
	}
	if pub.Status != configcenter.StatusActive {
		t.Fatalf("新发布应 active，got %s", pub.Status)
	}
	if pub.Snapshot["rate.limit"] != "200" {
		t.Fatalf("快照应含最新 draft，got %v", pub.Snapshot)
	}
	// 旧 v1 应被标 rolled-back
	hist, _ := s.ListPublishes(acmeCtx(), nsID)
	for _, p := range hist {
		if p.Version == 1 && p.Status != configcenter.StatusRolledBack {
			t.Fatal("v1 应被新版本标 rolled-back")
		}
	}
	// 发现返回 active v2
	active, ok, _ := s.ActivePublish(acmeCtx(), nsID)
	if !ok || active.Version != 2 {
		t.Fatalf("发现应返回 active v2，got ok=%v v=%d", ok, active.Version)
	}
}

// TestRollback 验证回滚激活旧版本。
func TestRollback(t *testing.T) {
	s := NewStore()
	nsID := mustCreateNs(t, s, acmeCtx(), "acme-app")
	mustUpsertItem(t, s, acmeCtx(), nsID, "feature.newui", "off")
	v1 := mustPublish(t, s, acmeCtx(), nsID)
	// 再发布一版（v2，v1 转 rolled-back）
	mustPublish(t, s, acmeCtx(), nsID)
	// 回滚到 v1
	rb, err := s.RollbackPublish(acmeCtx(), v1.ID)
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if rb.Status != configcenter.StatusActive {
		t.Fatal("回滚后目标版本应 active")
	}
	active, _, _ := s.ActivePublish(acmeCtx(), nsID)
	if active.Version != 1 {
		t.Fatalf("回滚后 active 应为 v1，got v%d", active.Version)
	}
}

// TestRollbackActiveRejected 验证回滚当前 active 版本被拒。
func TestRollbackActiveRejected(t *testing.T) {
	s := NewStore()
	nsID := mustCreateNs(t, s, acmeCtx(), "acme-app")
	mustUpsertItem(t, s, acmeCtx(), nsID, "feature.newui", "off")
	v1 := mustPublish(t, s, acmeCtx(), nsID)
	// v1 当前即 active，回滚应被拒
	if _, err := s.RollbackPublish(acmeCtx(), v1.ID); err == nil {
		t.Fatal("回滚当前 active 版本应报错")
	}
}

// TestDeleteNamespaceCascade 验证级联清理 item + publish。
func TestDeleteNamespaceCascade(t *testing.T) {
	s := NewStore()
	nsID := mustCreateNs(t, s, acmeCtx(), "acme-app")
	mustUpsertItem(t, s, acmeCtx(), nsID, "feature.newui", "on")
	mustPublish(t, s, acmeCtx(), nsID)

	if err := s.DeleteNamespace(acmeCtx(), nsID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if items, _ := s.ListItems(acmeCtx(), nsID); len(items) != 0 {
		t.Fatalf("删除应级联清 item，剩余 %d", len(items))
	}
	if pubs, _ := s.ListPublishes(acmeCtx(), nsID); len(pubs) != 0 {
		t.Fatalf("删除应级联清 publish，剩余 %d", len(pubs))
	}
}

// TestMissingTenant 验证缺失租户上下文即拒。
func TestMissingTenant(t *testing.T) {
	s := NewStore()
	if _, err := s.ListNamespaces(context.Background(), ""); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}

func TestEnsureByAppIdempotent(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	n1, err := s.EnsureByApp(ctx, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if n1.Scope != configcenter.ScopeApp || n1.AppID != "app-1" || n1.Name != "app-app-1" {
		t.Fatalf("scope/appID/name 错误: %+v", n1)
	}
	n2, err := s.EnsureByApp(ctx, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if n2.ID != n1.ID {
		t.Fatalf("幂等失败: %s vs %s", n1.ID, n2.ID)
	}
	// 跨租户隔离：t-globex 看不到 t-acme 的 ns，各自独立
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	n3, err := s.EnsureByApp(ctxB, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if n3.ID == n1.ID {
		t.Fatal("跨租户泄漏：两租户拿到同一 ns")
	}
}

func TestFindAppNamespace(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if _, ok, _ := s.FindAppNamespace(ctx, "app-1"); ok {
		t.Fatal("未创建时不应找到")
	}
	s.EnsureByApp(ctx, "app-1")
	ns, ok, err := s.FindAppNamespace(ctx, "app-1")
	if err != nil || !ok || ns.AppID != "app-1" {
		t.Fatalf("创建后应找到: ok=%v err=%v", ok, err)
	}
	// 手工 shared ns 不被 FindAppNamespace 命中
	s.CreateNamespace(ctx, configcenter.Namespace{Name: "manual-ns"})
	if _, ok, _ := s.FindAppNamespace(ctx, "app-1"); !ok {
		t.Fatal("app ns 仍应找到（shared 不干扰）")
	}
}

// TestCreatePublishConcurrent 锁住 R5-I1/R7-F3：10 goroutine 并发对同一 ns 发布，
// version 无重复、active 恰好 1 个（内存锁内原子；PG 由 partial unique index + tx 兜底）。
func TestCreatePublishConcurrent(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	nsID := mustCreateNs(t, s, ctx, "concurrent-ns")
	mustUpsertItem(t, s, ctx, nsID, "k", "v")

	const n = 10
	var wg sync.WaitGroup
	versions := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pub, err := s.CreatePublish(ctx, nsID)
			if err != nil {
				t.Errorf("并发发布失败: %v", err)
				return
			}
			versions <- pub.Version
		}()
	}
	wg.Wait()
	close(versions)

	seen := map[int]bool{}
	for v := range versions {
		if seen[v] {
			t.Fatalf("version 重复: %d", v)
		}
		seen[v] = true
	}
	if len(seen) != n {
		t.Fatalf("应 %d 个不同 version, got %d", n, len(seen))
	}
	// active 恰 1 个
	pubs, _ := s.ListPublishes(ctx, nsID)
	activeCount := 0
	for _, p := range pubs {
		if p.Status == configcenter.StatusActive {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("active 应恰 1 个, got %d", activeCount)
	}
}

// TestListAllNamespaces 锁住 R7-F4：跨租户列出全部命名空间（admin 总览），带 TenantID。
func TestListAllNamespaces(t *testing.T) {
	s := NewStore()
	mustCreateNs(t, s, acmeCtx(), "acme-all")
	mustCreateNs(t, s, globexCtx(), "globex-all")
	list, err := s.ListAllNamespaces(context.Background())
	if err != nil {
		t.Fatalf("ListAllNamespaces: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应 2 条（两租户各一）, got %d", len(list))
	}
	tids := map[string]bool{}
	for _, n := range list {
		if n.TenantID == "" {
			t.Fatalf("ListAll 返回应带 TenantID: %+v", n)
		}
		tids[n.TenantID] = true
	}
	if !tids["t-acme"] || !tids["t-globex"] {
		t.Fatalf("应覆盖两租户: %v", tids)
	}
}

// TestEnsureByAppEnvCreatesPerEnv 锁住环境维度隔离：同一应用 test/prod 各懒建独立 ns，
// envID 空 → app-<appID>（兼容旧名），非空 → app-<appID>-<envID>；幂等。
func TestEnsureByAppEnvCreatesPerEnv(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	def, err := s.EnsureByAppEnv(ctx, "app-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "app-app-1" || def.EnvID != "" {
		t.Fatalf("env 空 ns 名应 app-<appID> 且 EnvID 空: %+v", def)
	}
	testNS, err := s.EnsureByAppEnv(ctx, "app-1", "env-acme-test")
	if err != nil {
		t.Fatal(err)
	}
	prodNS, err := s.EnsureByAppEnv(ctx, "app-1", "env-acme-prod")
	if err != nil {
		t.Fatal(err)
	}
	if testNS.ID == prodNS.ID || testNS.ID == def.ID {
		t.Fatal("不同 env 应各自独立 ns")
	}
	if testNS.Name != "app-app-1-env-acme-test" || testNS.EnvID != "env-acme-test" {
		t.Fatalf("env ns 名/EnvID 错误: %+v", testNS)
	}
	// 幂等
	again, err := s.EnsureByAppEnv(ctx, "app-1", "env-acme-test")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != testNS.ID {
		t.Fatal("EnsureByAppEnv 幂等失败")
	}
}

// TestFindAppNamespaceEnvFallback 锁住发现回退语义：env 精确命中优先；
// 无 (app,env) ns 时回退 env='' 的存量 ns；envID 空仅精确匹配 env=''。
func TestFindAppNamespaceEnvFallback(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 仅存在 env='' 存量 ns。
	base, err := s.EnsureByAppEnv(ctx, "app-1", "")
	if err != nil {
		t.Fatal(err)
	}
	// 精确未命中 → 回退 env=''。
	got, ok, err := s.FindAppNamespaceEnv(ctx, "app-1", "env-x")
	if err != nil || !ok || got.ID != base.ID {
		t.Fatalf("应回退到 env='' ns: ok=%v err=%v got=%+v", ok, err, got)
	}
	// 建 env 精确 ns 后，精确优先。
	envNS, _ := s.EnsureByAppEnv(ctx, "app-1", "env-x")
	got, ok, _ = s.FindAppNamespaceEnv(ctx, "app-1", "env-x")
	if !ok || got.ID != envNS.ID {
		t.Fatalf("env 精确应优先: got %+v", got)
	}
	// envID 空：仅精确 env=''。
	got, ok, _ = s.FindAppNamespaceEnv(ctx, "app-1", "")
	if !ok || got.ID != base.ID {
		t.Fatalf("env 空应精确命中 env='' ns: got %+v", got)
	}
	// 跨租户不泄漏。
	if _, ok, _ := s.FindAppNamespaceEnv(globexCtx(), "app-1", ""); ok {
		t.Fatal("跨租户不应找到")
	}
}

// TestLaneOverrideUpsertDeleteList 锁住泳道覆盖 upsert/delete/list 语义：
// 同 (app,env,lane,key) 覆盖更新；list 按 (app,env,lane) 过滤且跨租户隔离。
func TestLaneOverrideUpsertDeleteList(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	o1, err := s.UpsertLaneOverride(ctx, configcenter.LaneOverride{
		AppID: "app-1", EnvID: "env-t", LaneID: "feat-x", Key: "rate.limit", Value: "100",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o1.TenantID != "t-acme" || o1.UpdatedAt.IsZero() {
		t.Fatalf("TenantID/UpdatedAt 应由 store 填充: %+v", o1)
	}
	// 同 key 覆盖更新。
	o2, err := s.UpsertLaneOverride(ctx, configcenter.LaneOverride{
		AppID: "app-1", EnvID: "env-t", LaneID: "feat-x", Key: "rate.limit", Value: "200",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o2.ID != o1.ID || o2.Value != "200" {
		t.Fatalf("upsert 应覆盖原行: %+v vs %+v", o2, o1)
	}
	// 另一 key + 另一 lane + 另一 env。
	s.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "app-1", EnvID: "env-t", LaneID: "feat-x", Key: "k2", Value: "v"})
	s.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "app-1", EnvID: "env-t", LaneID: "feat-y", Key: "k3", Value: "v"})
	s.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "app-1", EnvID: "", LaneID: "feat-x", Key: "k4", Value: "v"})

	list, err := s.ListLaneOverrides(ctx, "app-1", "env-t", "feat-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("(app-1,env-t,feat-x) 应 2 条, got %d", len(list))
	}
	// lane 空 = 全部泳道。
	all, _ := s.ListLaneOverrides(ctx, "app-1", "env-t", "")
	if len(all) != 3 {
		t.Fatalf("(app-1,env-t) 全泳道应 3 条, got %d", len(all))
	}
	// 跨租户不泄漏。
	if l, _ := s.ListLaneOverrides(globexCtx(), "app-1", "env-t", ""); len(l) != 0 {
		t.Fatal("跨租户 list 应 0 条")
	}
	// delete。
	if err := s.DeleteLaneOverride(ctx, "app-1", "env-t", "feat-x", "rate.limit"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListLaneOverrides(ctx, "app-1", "env-t", "feat-x"); len(l) != 1 {
		t.Fatalf("删除后应剩 1 条, got %d", len(l))
	}
	// 删不存在 → ErrLaneOverrideNotFound。
	if err := s.DeleteLaneOverride(ctx, "app-1", "env-t", "feat-x", "nope"); !errors.Is(err, configcenter.ErrLaneOverrideNotFound) {
		t.Fatalf("期望 ErrLaneOverrideNotFound, got %v", err)
	}
	// 校验：必填字段缺失拒绝。
	if _, err := s.UpsertLaneOverride(ctx, configcenter.LaneOverride{EnvID: "e", LaneID: "l", Key: "k"}); err == nil {
		t.Fatal("appID 缺失应拒绝")
	}
}
