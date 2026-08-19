package main

import (
	"context"
	"testing"

	svcmemory "github.com/aitoys/paas/internal/service/memory"
	"github.com/aitoys/paas/internal/workload"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// 场景：两租户各一 app，workload 带 Service="bff"（多服务）与 Service=""（单服务老数据），
// 另有一个 cronjob。期望：bff → Service{name:bff,type:backend}；空 Service → {name:"main",type:backend}；
// cronjob → type:cron（schedule 透传）；二次调用幂等（不重复建）；workload.ServiceID 被回填。
func TestBackfillIdempotent(t *testing.T) {
	ctx := context.Background()
	svcRepo := svcmemory.NewStore()
	wlRepo := wlmemory.NewStore()

	seeds := []workload.Workload{
		{ID: "wl-a-bff", TenantID: "t-acme", AppID: "app-a", EnvID: "env-test", Name: "wl-a-bff",
			Type: workload.TypeService, Image: "nginx", Service: "bff", Port: 8080, Replicas: 2},
		{ID: "wl-a-main", TenantID: "t-acme", AppID: "app-a", EnvID: "env-test", Name: "wl-a-main",
			Type: workload.TypeService, Image: "nginx", Replicas: 1},
		{ID: "wl-a-cron", TenantID: "t-acme", AppID: "app-a", EnvID: "env-test", Name: "wl-a-cron",
			Type: workload.TypeCronJob, Image: "busybox", Schedule: "*/5 * * * *"},
		{ID: "wl-g-bff", TenantID: "t-globex", AppID: "app-g", EnvID: "env-test", Name: "wl-g-bff",
			Type: workload.TypeService, Image: "nginx", Service: "bff", Replicas: 1},
	}
	for _, w := range seeds {
		if err := wlRepo.Create(tenant.WithTenant(ctx, w.TenantID), w); err != nil {
			t.Fatalf("seed workload %s: %v", w.ID, err)
		}
	}

	tenants := []string{"t-acme", "t-globex"}
	// service.List(ctx, appID) 按应用字面过滤，计数按 (租户, app) 维度求和。
	count := func() int {
		n := 0
		for _, tid := range tenants {
			for _, appID := range []string{"app-a", "app-g"} {
				svcs, err := svcRepo.List(tenant.WithTenant(ctx, tid), appID)
				if err != nil {
					t.Fatalf("list services: %v", err)
				}
				n += len(svcs)
			}
		}
		return n
	}
	run := func() int {
		if err := backfillServices(ctx, svcRepo, wlRepo, tenants); err != nil {
			t.Fatalf("backfillServices: %v", err)
		}
		return count()
	}

	first := run()
	// acme: bff + main（service 型与 cronjob 同归 "main"，GetOrCreate 按名去重共享）；globex: bff。
	if want := 3; first != want {
		t.Fatalf("第一遍 services 数 = %d, want %d", first, want)
	}
	if second := run(); second != first {
		t.Fatalf("第二遍不幂等: %d != %d", second, first)
	}

	// 断言 ServiceID 回填 + 类型/名称/端口/副本正确。
	acmeCtx := tenant.WithTenant(ctx, "t-acme")
	wls, err := wlRepo.List(acmeCtx, "", "app-a", "", "", "")
	if err != nil {
		t.Fatalf("list workloads: %v", err)
	}
	got := map[string]workload.Workload{}
	for _, w := range wls {
		got[w.ID] = w
	}
	assert := func(wlID, wantName, wantType string, wantPort, wantReplicas int) {
		w := got[wlID]
		if w.ServiceID == "" {
			t.Fatalf("%s ServiceID 未回填", wlID)
		}
		svc, err := svcRepo.Get(acmeCtx, "app-a", w.ServiceID)
		if err != nil {
			t.Fatalf("get service for %s: %v", wlID, err)
		}
		if svc.Name != wantName || svc.Type != wantType {
			t.Fatalf("%s → {name:%s,type:%s}, want {name:%s,type:%s}", wlID, svc.Name, svc.Type, wantName, wantType)
		}
		if svc.Port != wantPort || svc.Replicas != wantReplicas {
			t.Fatalf("%s → {port:%d,replicas:%d}, want {port:%d,replicas:%d}", wlID, svc.Port, svc.Replicas, wantPort, wantReplicas)
		}
	}
	assert("wl-a-bff", "bff", "backend", 8080, 2)

	// 空 Service 的 service 型与 cronjob 同归 "main"：GetOrCreate 按名去重共享，
	// 类型由先处理的负载决定（List 按 ID 排序，wl-a-cron 先建 → type=cron，schedule 透传）。
	mainSvc, err := svcRepo.Get(acmeCtx, "app-a", got["wl-a-main"].ServiceID)
	if err != nil {
		t.Fatalf("get main service: %v", err)
	}
	if mainSvc.Type != "cron" || mainSvc.Schedule != "*/5 * * * *" {
		t.Fatalf("main → {type:%s,schedule:%q}, want {cron, 透传自 cronjob}", mainSvc.Type, mainSvc.Schedule)
	}
	if got["wl-a-cron"].ServiceID == "" || got["wl-a-cron"].ServiceID != got["wl-a-main"].ServiceID {
		t.Fatalf("同 app 空 Service 应共享同名 main 服务")
	}
}
