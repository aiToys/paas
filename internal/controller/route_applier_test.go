package controller

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aitoys/paas/internal/governance"
	govmemory "github.com/aitoys/paas/internal/governance/memory"
	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// newRouteApplier 构造测试用 K8sRouteApplier：真实 memory governance store（RouteStore+ServiceStore）
// + fake K8s client。返回 (applier, govStore, client) 供测试灌 Route/Service + 断言 Ingress。
func newRouteApplier(t *testing.T, ingressClass string) (*K8sRouteApplier, *govmemory.Store) {
	t.Helper()
	scheme := newScheme(t)
	store := govmemory.NewStore()
	cl := clientfake.NewClientBuilder().WithScheme(scheme).Build()
	return NewK8sRouteApplier(cl, store, store, ingressClass), store
}

// seedSvc 在 govStore 灌一个 Service（带端口），返回其 ID。
func seedSvc(t *testing.T, store *govmemory.Store, tid, name string, port int) string {
	t.Helper()
	ctx := tenant.WithTenant(context.Background(), tid)
	s, err := store.CreateService(ctx, governance.Service{
		Name: name, EnvID: "env1", Protocol: "http", Port: port,
	})
	if err != nil {
		t.Fatalf("seed service: %v", err)
	}
	return s.ID
}

// getIngress 从 fake client 取 Ingress 断言（not found 返错误供跳过）。
func getIngress(applier *K8sRouteApplier, tid, host string) (*networkingv1.Ingress, error) {
	ns := tenant.Namespace(tid)
	ing := &networkingv1.Ingress{}
	err := applier.Client.Get(context.Background(),
		types.NamespacedName{Name: "route-" + tenant.SanitizeName(host), Namespace: ns}, ing)
	return ing, err
}

// TestRouteApplierSingleRouteCreatesIngress 单 Route（host+path）建聚合 Ingress，后端 Service:Port。
func TestRouteApplierSingleRouteCreatesIngress(t *testing.T) {
	applier, store := newRouteApplier(t, "hermes")
	tid := "t-acme"
	ctx := tenant.WithTenant(context.Background(), tid)
	svcID := seedSvc(t, store, tid, "paas-shop-bff", 8080)

	_, err := store.CreateRoute(ctx, governance.Route{
		Name: "r1", Host: "shop.local", Path: "/api", ServiceID: svcID,
		Methods: []string{"GET"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}

	if err := applier.Apply(ctx, tid, "shop.local"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, err := getIngress(applier, tid, "shop.local")
	if err != nil {
		t.Fatalf("期望 Ingress 已建: %v", err)
	}
	if ing.Spec.Rules[0].Host != "shop.local" {
		t.Fatalf("host 不符: %s", ing.Spec.Rules[0].Host)
	}
	p := ing.Spec.Rules[0].HTTP.Paths[0]
	if p.Path != "/api" || p.Backend.Service.Name != "paas-shop-bff" || p.Backend.Service.Port.Number != 8080 {
		t.Fatalf("后端不符: path=%s svc=%s port=%d", p.Path, p.Backend.Service.Name, p.Backend.Service.Port.Number)
	}
	if ing.Labels[labels.KeyRouteHost] != "shop.local" || ing.Labels[labels.KeyTenant] != tid {
		t.Fatalf("聚合 label 缺失: %+v", ing.Labels)
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "hermes" {
		t.Fatalf("ingressClassName 应为 hermes")
	}
}

// TestRouteApplierMultiRouteSameHostAggregates 同 host 多 Route 聚合成单条多 path Ingress。
func TestRouteApplierMultiRouteSameHostAggregates(t *testing.T) {
	applier, store := newRouteApplier(t, "hermes")
	tid := "t-acme"
	ctx := tenant.WithTenant(context.Background(), tid)
	bff := seedSvc(t, store, tid, "bff", 8080)
	product := seedSvc(t, store, tid, "product", 8081)

	store.CreateRoute(ctx, governance.Route{Name: "r1", Host: "shop.local", Path: "/api", ServiceID: bff, Methods: []string{"GET"}, Enabled: true})
	store.CreateRoute(ctx, governance.Route{Name: "r2", Host: "shop.local", Path: "/products", ServiceID: product, Methods: []string{"GET"}, Enabled: true})

	if err := applier.Apply(ctx, tid, "shop.local"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ing, err := getIngress(applier, tid, "shop.local")
	if err != nil {
		t.Fatalf("期望单条聚合 Ingress: %v", err)
	}
	if len(ing.Spec.Rules[0].HTTP.Paths) != 2 {
		t.Fatalf("期望 2 path 聚合，实际 %d", len(ing.Spec.Rules[0].HTTP.Paths))
	}
}

// TestRouteApplierNoEnabledRouteDeletesIngress 无剩余 enabled Route 删整条 Ingress。
func TestRouteApplierNoEnabledRouteDeletesIngress(t *testing.T) {
	applier, store := newRouteApplier(t, "hermes")
	tid := "t-acme"
	ctx := tenant.WithTenant(context.Background(), tid)
	svcID := seedSvc(t, store, tid, "bff", 8080)
	store.CreateRoute(ctx, governance.Route{Name: "r1", Host: "shop.local", Path: "/api", ServiceID: svcID, Methods: []string{"GET"}, Enabled: true})

	applier.Apply(ctx, tid, "shop.local") // 先建
	// 禁用该 Route（Enabled=false）→ 重建应删 Ingress。
	r, _ := store.ListRoutes(ctx, "")
	store.UpdateRoute(ctx, governance.Route{ID: r[0].ID, Name: "r1", Host: "shop.local", Path: "/api", ServiceID: svcID, Methods: []string{"GET"}, Enabled: false})

	if err := applier.Apply(ctx, tid, "shop.local"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := getIngress(applier, tid, "shop.local"); err == nil {
		t.Fatal("无 enabled Route 应删整条 Ingress")
	}
}

// TestRouteApplierHostEmptySkips Host/tid 空跳过（无操作，无错误）。
func TestRouteApplierHostEmptySkips(t *testing.T) {
	applier, _ := newRouteApplier(t, "hermes")
	if err := applier.Apply(context.Background(), "t-acme", ""); err != nil {
		t.Fatalf("Host 空应跳过无错: %v", err)
	}
	if err := applier.Apply(context.Background(), "", "shop.local"); err != nil {
		t.Fatalf("tid 空应跳过无错: %v", err)
	}
}

// TestRouteApplierServiceNotFoundSkipsPath Service 不存在跳过该 path（best-effort），不报错。
func TestRouteApplierServiceNotFoundSkipsPath(t *testing.T) {
	applier, store := newRouteApplier(t, "hermes")
	tid := "t-acme"
	ctx := tenant.WithTenant(context.Background(), tid)
	bff := seedSvc(t, store, tid, "bff", 8080)
	// 一条正常 + 一条指向不存在的 service。
	store.CreateRoute(ctx, governance.Route{Name: "r1", Host: "shop.local", Path: "/api", ServiceID: bff, Methods: []string{"GET"}, Enabled: true})
	store.CreateRoute(ctx, governance.Route{Name: "r2", Host: "shop.local", Path: "/bad", ServiceID: "no-such-svc", Methods: []string{"GET"}, Enabled: true})

	if err := applier.Apply(ctx, tid, "shop.local"); err != nil {
		t.Fatalf("Service 不存在应跳过不报错: %v", err)
	}
	ing, err := getIngress(applier, tid, "shop.local")
	if err != nil {
		t.Fatalf("正常 path 应仍建 Ingress: %v", err)
	}
	if len(ing.Spec.Rules[0].HTTP.Paths) != 1 {
		t.Fatalf("坏 path 跳过，期望 1 path，实际 %d", len(ing.Spec.Rules[0].HTTP.Paths))
	}
}

// TestRouteApplierIdempotent 重复 Apply spec 一致（CreateOrUpdate 幂等，不报错）。
func TestRouteApplierIdempotent(t *testing.T) {
	applier, store := newRouteApplier(t, "hermes")
	tid := "t-acme"
	ctx := tenant.WithTenant(context.Background(), tid)
	svcID := seedSvc(t, store, tid, "bff", 8080)
	store.CreateRoute(ctx, governance.Route{Name: "r1", Host: "shop.local", Path: "/api", ServiceID: svcID, Methods: []string{"GET"}, Enabled: true})

	for i := 0; i < 3; i++ {
		if err := applier.Apply(ctx, tid, "shop.local"); err != nil {
			t.Fatalf("第 %d 次 Apply 幂等应无错: %v", i, err)
		}
	}
	ing, _ := getIngress(applier, tid, "shop.local")
	if len(ing.Spec.Rules[0].HTTP.Paths) != 1 {
		t.Fatalf("幂等后应仍 1 path，实际 %d", len(ing.Spec.Rules[0].HTTP.Paths))
	}
}

// TestRouteApplierDeleteEqualsRebuild Delete 语义=重建聚合（剩 path 留 / 无剩删整条）。
func TestRouteApplierDeleteEqualsRebuild(t *testing.T) {
	applier, store := newRouteApplier(t, "hermes")
	tid := "t-acme"
	ctx := tenant.WithTenant(context.Background(), tid)
	bff := seedSvc(t, store, tid, "bff", 8080)
	store.CreateRoute(ctx, governance.Route{Name: "r1", Host: "shop.local", Path: "/api", ServiceID: bff, Methods: []string{"GET"}, Enabled: true})

	// Delete 调用前 Route 仍在 → Delete(=Apply) 会重建（保留 Ingress，因为 Route 还在）。
	if err := applier.Delete(ctx, tid, "shop.local"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := getIngress(applier, tid, "shop.local"); err != nil {
		t.Fatal("Route 仍在时 Delete 应保留聚合 Ingress")
	}
}
