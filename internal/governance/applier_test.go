package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/internal/governance/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// mockRouteApplier 记录 Apply/Delete 调用（含 tid+host），可注入错误验证 best-effort 不阻断写。
type mockRouteApplier struct {
	applies []hostCall
	deletes []hostCall
	err     error
}

type hostCall struct{ tid, host string }

func (m *mockRouteApplier) Apply(ctx context.Context, tid, host string) error {
	m.applies = append(m.applies, hostCall{tid, host})
	return m.err
}
func (m *mockRouteApplier) Delete(ctx context.Context, tid, host string) error {
	m.deletes = append(m.deletes, hostCall{tid, host})
	return m.err
}

// TestApplyRepoCreateProjectsRoute CreateRoute 后调 Apply（tid+host）。
func TestApplyRepoCreateProjectsRoute(t *testing.T) {
	mock := &mockRouteApplier{}
	store := memory.NewStore()
	repo := governance.NewApplyRepo(store, mock)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err := repo.CreateRoute(ctx, governance.Route{
		Name: "r1", Host: "a.example.com", Path: "/api",
		ServiceID: "s1", Methods: []string{"GET"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}
	if len(mock.applies) != 1 || mock.applies[0].tid != "t-acme" || mock.applies[0].host != "a.example.com" {
		t.Fatalf("期望 Apply(t-acme,a.example.com)，实际 %+v", mock.applies)
	}
}

// TestApplyRepoCreateSkipsEmptyHost Host 空不下发（无 host 无法对外路由）。
func TestApplyRepoCreateSkipsEmptyHost(t *testing.T) {
	mock := &mockRouteApplier{}
	store := memory.NewStore()
	repo := governance.NewApplyRepo(store, mock)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err := repo.CreateRoute(ctx, governance.Route{
		Name: "r1", Path: "/api", ServiceID: "s1", Methods: []string{"GET"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}
	if len(mock.applies) != 0 {
		t.Fatalf("Host 空不应下发，实际 %d 次", len(mock.applies))
	}
}

// TestApplyRepoDeleteRebuildsHost DeleteRoute 后调 Delete（重建聚合 Ingress）。
func TestApplyRepoDeleteRebuildsHost(t *testing.T) {
	mock := &mockRouteApplier{}
	store := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, _ := store.CreateRoute(ctx, governance.Route{
		Name: "r1", Host: "a.example.com", Path: "/api", ServiceID: "s1",
		Methods: []string{"GET"}, Enabled: true,
	})
	repo := governance.NewApplyRepo(store, mock)

	if err := repo.DeleteRoute(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	if len(mock.deletes) != 1 || mock.deletes[0].host != "a.example.com" {
		t.Fatalf("期望 Delete(*, a.example.com)，实际 %+v", mock.deletes)
	}
}

// TestApplyRepoBestEffortDoesNotBlock applier 返错不阻断控制面写（Route 仍创建成功）。
func TestApplyRepoBestEffortDoesNotBlock(t *testing.T) {
	mock := &mockRouteApplier{err: errors.New("k8s down")}
	store := memory.NewStore()
	repo := governance.NewApplyRepo(store, mock)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	r, err := repo.CreateRoute(ctx, governance.Route{
		Name: "r1", Host: "a.example.com", Path: "/api", ServiceID: "s1",
		Methods: []string{"GET"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("applier 错误不应阻断控制面写: %v", err)
	}
	if r.ID == "" {
		t.Fatal("Route 应已创建（控制面真源优先）")
	}
	// 确认 Route 确实写入 store（裸 store 能查到）。
	got, err := store.GetRoute(ctx, r.ID)
	if err != nil || got.Host != "a.example.com" {
		t.Fatalf("Route 应已落库: %v %+v", err, got)
	}
}

// TestApplyRepoNilApplierTransparent nil applier 透传（非 K8s 部署，行为不变）。
func TestApplyRepoNilApplierTransparent(t *testing.T) {
	store := memory.NewStore()
	repo := governance.NewApplyRepo(store, nil) // nil applier
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err := repo.CreateRoute(ctx, governance.Route{
		Name: "r1", Host: "a.example.com", Path: "/api", ServiceID: "s1",
		Methods: []string{"GET"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("nil applier 透传应无错: %v", err)
	}
}

// TestApplyRepoUpdateRebuildsOldAndNewHost Host 变更时新+旧 host 都重建。
func TestApplyRepoUpdateRebuildsOldAndNewHost(t *testing.T) {
	mock := &mockRouteApplier{}
	store := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, _ := store.CreateRoute(ctx, governance.Route{
		Name: "r1", Host: "old.example.com", Path: "/api", ServiceID: "s1",
		Methods: []string{"GET"}, Enabled: true,
	})
	repo := governance.NewApplyRepo(store, mock)

	_, err := repo.UpdateRoute(ctx, governance.Route{
		ID: r.ID, Name: "r1", Host: "new.example.com", Path: "/api", ServiceID: "s1",
		Methods: []string{"GET"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}
	// 期望 2 次 Apply：新 host + 旧 host（旧聚合 Ingress 需移除本 Route path）。
	if len(mock.applies) != 2 {
		t.Fatalf("期望 2 次 Apply（新+旧 host），实际 %d %+v", len(mock.applies), mock.applies)
	}
	hosts := map[string]bool{mock.applies[0].host: true, mock.applies[1].host: true}
	if !hosts["new.example.com"] || !hosts["old.example.com"] {
		t.Fatalf("期望新+旧 host 都重建，实际 %+v", mock.applies)
	}
}

// TestApplyRepoDeleteServiceRebuildsCascadedHosts 删服务级联删 Route 后，
// 该服务涉及的每个 host 都要重建聚合 Ingress（防残留指向已删服务的 path）。
func TestApplyRepoDeleteServiceRebuildsCascadedHosts(t *testing.T) {
	mock := &mockRouteApplier{}
	store := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 服务 + 3 条 Route（两个不同 host + 一条无 host 不应触发）。
	svc, err := store.CreateService(ctx, governance.Service{
		Name: "svc-1", EnvID: "env-1", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	for _, rt := range []governance.Route{
		{Name: "r1", Host: "a.example.com", Path: "/a", ServiceID: svc.ID, Methods: []string{"GET"}, Enabled: true},
		{Name: "r2", Host: "a.example.com", Path: "/b", ServiceID: svc.ID, Methods: []string{"GET"}, Enabled: true},
		{Name: "r3", Host: "b.example.com", Path: "/c", ServiceID: svc.ID, Methods: []string{"GET"}, Enabled: true},
		{Name: "r4", Path: "/nohost", ServiceID: svc.ID, Methods: []string{"GET"}, Enabled: true},
	} {
		if _, err := store.CreateRoute(ctx, rt); err != nil {
			t.Fatalf("CreateRoute %s: %v", rt.Name, err)
		}
	}
	repo := governance.NewApplyRepo(store, mock)

	if err := repo.DeleteService(ctx, svc.ID); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	// 同 host 去重：期望恰好 2 次 Apply（a.example.com + b.example.com）。
	hosts := map[string]int{}
	for _, c := range mock.applies {
		if c.tid != "t-acme" {
			t.Fatalf("Apply 应带租户 tid，实际 %+v", c)
		}
		hosts[c.host]++
	}
	if len(hosts) != 2 || hosts["a.example.com"] != 1 || hosts["b.example.com"] != 1 {
		t.Fatalf("期望 a/b 各重建 1 次（同 host 去重），实际 %+v", mock.applies)
	}
	// 级联删除后 store 无残留 Route。
	if left, _ := store.ListRoutes(ctx, ""); len(left) != 0 {
		t.Fatalf("级联删 Route 后不应残留，实际 %d 条", len(left))
	}
}
