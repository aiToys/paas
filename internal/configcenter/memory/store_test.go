package memory

import (
	"context"
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
