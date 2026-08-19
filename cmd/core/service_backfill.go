// 存量回填：服务模型 Phase 1——为无 ServiceID 的既有 Workload
// 按 Workload.Service（多服务模型字段）幂等 GetOrCreate Service 实体并回填 ServiceID。
// 启动时在 buildAllStores（内存/PG 两路径同源）seed 完成后调用，失败仅记日志不阻断启动。
package main

import (
	"context"
	"log"

	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/service"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// backfillTenantIDs 拉租户列表并执行存量回填（启动挂载入口，失败仅记日志不阻断）。
func backfillTenantIDs(ctx context.Context, idb identity.Repository, svcRepo service.Repository, wlRepo workload.Repository) {
	ts, err := idb.ListTenants(ctx)
	if err != nil {
		log.Printf("[backfill] 拉取租户列表失败: %v", err)
		return
	}
	ids := make([]string, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	if err := backfillServices(ctx, svcRepo, wlRepo, ids); err != nil {
		log.Printf("[backfill] 服务存量回填失败: %v", err)
	}
}

// backfillServices 遍历各租户 workloads，为无 ServiceID 的负载
// 按 Workload.Service GetOrCreate Service 实体并回填 ServiceID。
// 幂等：ServiceID 非空跳过；GetOrCreateByName 按 (app, name) 去重。
func backfillServices(ctx context.Context, svcRepo service.Repository, wlRepo workload.Repository, tenantIDs []string) error {
	for _, tid := range tenantIDs {
		tctx := tenant.WithTenant(ctx, tid)
		wls, err := wlRepo.List(tctx, "", "", "", "", "") // envID/appID/laneID/wtype/service 空串不过滤
		if err != nil {
			log.Printf("[backfill] 租户 %s 工作负载查询失败: %v", tid, err) // best-effort，不阻断启动
			continue
		}
		for _, wl := range wls {
			if wl.ServiceID != "" {
				continue
			}
			name := wl.Service
			if name == "" {
				name = "main" // 单服务老数据统一归 "main"
			}
			typ := service.TypeBackend
			// job→cron 归类取舍：无更精确的映射（job 是一次性任务非服务），归 cron 类
			// 便于 UI 按「任务型」聚合展示，后续可在服务 tab 手动改类型。
			if wl.Type == workload.TypeCronJob || wl.Type == workload.TypeJob {
				typ = service.TypeCron
			}
			svc, err := svcRepo.GetOrCreateByName(tctx, wl.AppID, name, typ, func(s *service.Service) {
				s.Port, s.Replicas = wl.Port, wl.Replicas
				if typ == service.TypeCron {
					// cron 类型 Validate 要求非空：cronjob 从工作负载透传；
					// job 无 cron 语义，给最小占位（后续可在服务 tab 手动改）。
					if wl.Type == workload.TypeCronJob {
						s.Schedule = wl.Schedule
					} else {
						s.Schedule = "@manual"
					}
				}
			})
			if err != nil {
				log.Printf("[backfill] 工作负载 %s 建服务 %s 失败: %v", wl.ID, name, err)
				continue
			}
			if err := wlRepo.SetServiceID(tctx, wl.ID, svc.ID); err != nil { // best-effort
				log.Printf("[backfill] 工作负载 %s 回填 ServiceID 失败: %v", wl.ID, err)
			}
		}
	}
	return nil
}
