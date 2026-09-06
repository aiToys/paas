package memory

import (
	"testing"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/workload"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// TestPromoteReleaseInheritsService promote 必须继承源 release 的服务定位（经 src.WorkloadID
// 反查源 workload 的 Service/ServiceID/LaneID）。Release 记录不持久化 Service——若不继承，
// CreateRelease 的基线匹配退化为 (app,env,lane) 全列表 ORDER BY id 取首个，多服务应用会把
// 镜像错配到无关服务（prod 最早建的 workload 被反复覆盖）。
func TestPromoteReleaseInheritsService(t *testing.T) {
	wl := wlmemory.NewStore()
	s := NewStore(wl)
	ctx := tenant.WithTenant(t.Context(), "t-acme")

	// prod 预置两个服务基线；product 先建（ID 字典序更小——坐实无继承时 ORDER BY id 首个错配的方向）。
	for _, w := range []workload.Workload{
		{ID: "wl-prod-a", AppID: "app-cs", EnvID: "env-prod", LaneID: "default", Service: "product",
			Type: workload.TypeService, Name: "app-cs-product-prod", Replicas: 1, Image: "old-product:1"},
		{ID: "wl-prod-b", AppID: "app-cs", EnvID: "env-prod", LaneID: "default", Service: "order",
			Type: workload.TypeService, Name: "app-cs-order-prod", Replicas: 1, Image: "old-order:1"},
	} {
		if err := wl.Create(ctx, w); err != nil {
			t.Fatal(err)
		}
	}

	// test 环境 order 部署出源 release（CreateRelease 会自动新建 test 基线 workload）。
	seedImage(s, "img-prom-1", "t-acme", "app-cs")
	src, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-test", ImageID: "img-prom-1", Service: "order",
	})
	if err != nil {
		t.Fatal(err)
	}

	rel, err := s.PromoteRelease(ctx, src.ID, "env-prod")
	if err != nil {
		t.Fatal(err)
	}

	// 晋升命中 prod 的 order 基线（非 product）。
	if rel.WorkloadID != "wl-prod-b" {
		t.Fatalf("promote 应命中 prod order 基线 wl-prod-b，got %s", rel.WorkloadID)
	}
	if rel.PromotedFrom != src.ID {
		t.Fatalf("promotedFrom=%q want %q", rel.PromotedFrom, src.ID)
	}
	// order 基线已切新镜像。
	o, err := wl.Get(ctx, "wl-prod-b")
	if err != nil {
		t.Fatal(err)
	}
	if o.Image == "old-order:1" {
		t.Fatal("prod order 基线镜像未更新")
	}
	// product 基线未被污染（错配会覆盖它）。
	p, err := wl.Get(ctx, "wl-prod-a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Image != "old-product:1" {
		t.Fatalf("prod product 基线被错配污染: %s", p.Image)
	}
}

// TestPromoteReleaseSourceWorkloadGone 源 workload 已删（单独删除后 promote 旧 release）必须
// fail-closed 报错——静默降级会让基线匹配回到全列表取首个，错配在生产无感知复发（终审 I2）。
func TestPromoteReleaseSourceWorkloadGone(t *testing.T) {
	wl := wlmemory.NewStore()
	s := NewStore(wl)
	ctx := tenant.WithTenant(t.Context(), "t-acme")

	seedImage(s, "img-gone-1", "t-acme", "app-cs")
	src, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-test", ImageID: "img-gone-1", Service: "order",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 删除源 workload（模拟基线被单独删除），再 promote。
	if err := wl.Delete(ctx, src.WorkloadID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteRelease(ctx, src.ID, "env-prod"); err == nil {
		t.Fatal("源 workload 已删时 promote 必须 fail-closed 报错，不得静默降级")
	}
}
