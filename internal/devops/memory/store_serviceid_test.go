package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/devops"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeSvcLookup 桩：按 (app, serviceID) 返回预置 ServiceDef。
type fakeSvcLookup struct {
	defs map[string]devops.ServiceDef // key = appID + "|" + serviceID
}

func (f *fakeSvcLookup) GetService(_ context.Context, appID, serviceID string) (devops.ServiceDef, error) {
	if d, ok := f.defs[appID+"|"+serviceID]; ok {
		return d, nil
	}
	return devops.ServiceDef{}, errors.New("service not found")
}

// TestReleaseServiceIDReuse 同 (app,env,lane,serviceID) 二次 deploy 复用同一 Workload（不新建），
// 且新建时 Port/Replicas 来自 ServiceLookup。
func TestReleaseServiceIDReuse(t *testing.T) {
	wl := wlmemory.NewStore()
	lookup := &fakeSvcLookup{defs: map[string]devops.ServiceDef{
		"app-cs|svc-bff": {ID: "svc-bff", Name: "bff", Port: 8080, Replicas: 3},
	}}
	s := NewStore(wl, WithServiceLookup(lookup))
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	seedImage(s, "img-sid-1", "t-acme", "app-cs")

	in := devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-acme-test", ImageID: "img-sid-1",
		Service: "bff", ServiceID: "svc-bff",
	}
	rel1, err := s.CreateRelease(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	// 第一次：新建 Workload 应带 ServiceID + Port/Replicas 来自 ServiceDef
	wls, _ := wl.List(ctx, "env-acme-test", "app-cs", "default", "service", "bff")
	if len(wls) != 1 {
		t.Fatalf("want 1 workload, got %d", len(wls))
	}
	first := wls[0]
	if first.ServiceID != "svc-bff" {
		t.Fatalf("ServiceID=%q want svc-bff", first.ServiceID)
	}
	if first.Port != 8080 || first.Replicas != 3 {
		t.Fatalf("Port/Replicas=(%d,%d) want (8080,3) from ServiceDef", first.Port, first.Replicas)
	}

	// 第二次：同 (app,env,lane,serviceID) 复用同一 Workload
	seedImage(s, "img-sid-2", "t-acme", "app-cs")
	in.ImageID = "img-sid-2"
	rel2, err := s.CreateRelease(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if rel2.WorkloadID != rel1.WorkloadID {
		t.Fatalf("second release should reuse workload %s, got %s", rel1.WorkloadID, rel2.WorkloadID)
	}
	wls2, _ := wl.List(ctx, "env-acme-test", "app-cs", "default", "service", "bff")
	if len(wls2) != 1 {
		t.Fatalf("want still 1 workload after re-release, got %d", len(wls2))
	}
}

// TestReleaseServiceIDLookupMissFallback ServiceLookup 查不到时行为不变（用 input 值，不报错）。
func TestReleaseServiceIDLookupMissFallback(t *testing.T) {
	wl := wlmemory.NewStore()
	s := NewStore(wl, WithServiceLookup(&fakeSvcLookup{})) // 空 defs，必 miss
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	seedImage(s, "img-sid-3", "t-acme", "app-cs")

	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-acme-test", ImageID: "img-sid-3",
		Service: "bff", ServiceID: "svc-ghost", Port: 9090,
	})
	if err != nil {
		t.Fatal(err)
	}
	wls, _ := wl.List(ctx, "env-acme-test", "app-cs", "default", "service", "bff")
	if len(wls) != 1 || wls[0].ServiceID != "svc-ghost" || wls[0].Port != 9090 || wls[0].Replicas != 1 {
		t.Fatalf("fallback workload wrong: %+v (rel=%s)", wls, rel.ID)
	}
}
