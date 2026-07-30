package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// TestTenantIsolation 验证命名空间/配置/发布按租户隔离。
func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, _ := s.ListNamespaces(acmeCtx())
	globex, _ := s.ListNamespaces(globexCtx())
	for _, n := range acme {
		if n.TenantID != "t-acme" {
			t.Fatalf("acme 视图泄漏: %s", n.Name)
		}
	}
	if len(globex) == 0 || globex[0].TenantID != "t-globex" {
		t.Fatal("globex 视图异常")
	}
	if _, err := s.GetNamespace(acmeCtx(), "ns-globex-app"); err == nil {
		t.Fatal("acme 不应见到 globex 命名空间")
	}
}

// TestPublishAndDiscover 验证发布生成快照 + 客户端发现。
func TestPublishAndDiscover(t *testing.T) {
	s := NewStore()
	// 编辑 draft（改 feature.newui on 已是 seed）
	_, _ = s.UpsertItem(acmeCtx(), configcenter.ConfigItem{
		NamespaceID: "ns-acme-app", Key: "rate.limit", Value: "200", Type: configcenter.TypeText,
	})
	pub, err := s.CreatePublish(acmeCtx(), "ns-acme-app")
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
	// 旧 active 应 rolled-back
	hist, _ := s.ListPublishes(acmeCtx(), "ns-acme-app")
	for _, p := range hist {
		if p.Version == 1 && p.Status != configcenter.StatusRolledBack {
			t.Fatal("v1 应被新版本标 rolled-back")
		}
	}
	// 发现返回 active
	active, ok, _ := s.ActivePublish(acmeCtx(), "ns-acme-app")
	if !ok || active.Version != 2 {
		t.Fatalf("发现应返回 active v2，got ok=%v v=%d", ok, active.Version)
	}
}

// TestRollback 验证回滚激活旧版本。
func TestRollback(t *testing.T) {
	s := NewStore()
	// 再发布一版（v2）
	if _, err := s.CreatePublish(acmeCtx(), "ns-acme-app"); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	// 回滚到 v1
	rb, err := s.RollbackPublish(acmeCtx(), "pub-acme-1")
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if rb.Status != configcenter.StatusActive {
		t.Fatal("回滚后目标版本应 active")
	}
	active, _, _ := s.ActivePublish(acmeCtx(), "ns-acme-app")
	if active.Version != 1 {
		t.Fatalf("回滚后 active 应为 v1，got v%d", active.Version)
	}
}

// TestRollbackActiveRejected 验证回滚当前 active 版本被拒。
func TestRollbackActiveRejected(t *testing.T) {
	s := NewStore()
	if _, err := s.RollbackPublish(acmeCtx(), "pub-acme-1"); err == nil {
		t.Fatal("回滚当前 active 版本应报错")
	}
}

// TestDeleteNamespaceCascade 验证级联清理。
func TestDeleteNamespaceCascade(t *testing.T) {
	s := NewStore()
	if err := s.DeleteNamespace(acmeCtx(), "ns-acme-app"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	items, _ := s.ListItems(acmeCtx(), "ns-acme-app")
	if len(items) != 0 {
		t.Fatalf("删除应级联清 item，剩余 %d", len(items))
	}
}

// TestMissingTenant 验证缺失租户上下文即拒。
func TestMissingTenant(t *testing.T) {
	s := NewStore()
	if _, err := s.ListNamespaces(context.Background()); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}
