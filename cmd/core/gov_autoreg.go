package main

// 治理服务「部署即注册」装配（2026-10-13）：workload 创建 → 治理 Service 自动注册。
// 动机：应用详情-服务治理 tab 此前只读且要求用户跳平台页手动注册（test-web 实测旅程
// 断链：应用内建服务实体与治理服务是两张表，部署后治理侧永远空）。治理 Service 是纯
// 注册元数据（实例真源=K8s Endpoints 自动发现），自动注册零 K8s 副作用，直接消灭手动步骤。

import (
	"context"
	"log"

	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// govAutoRegister 生成「workload → 治理 Service」注册回调（OnCreateRepo 注入用）。
// Name=工作负载名（== K8s Service 名，数据面发现靠此对齐）；失败仅日志不阻断创建。
func govAutoRegister(govRepo governance.ServiceStore) func(context.Context, workload.Workload) {
	return func(ctx context.Context, w workload.Workload) {
		if err := governance.AutoRegisterService(ctx, govRepo, governance.Service{
			AppID: w.AppID, EnvID: w.EnvID, Name: w.Name,
			Protocol: governance.ProtocolHTTP, Port: w.Port,
			Desc: "工作负载部署自动注册",
		}); err != nil {
			log.Printf("[gov-autoreg] 工作负载 %s 治理服务注册失败: %v", w.ID, err)
		}
	}
}

// backfillGovernanceServices 启动自愈：对账全部 service 型工作负载，缺治理 Service 的
// 补注册——覆盖自动注册上线前的存量（recalcUsageIfDrift 同款先例）。幂等（同名跳过）；
// ListAll 跨租户，逐租户派生 ctx（治理 repo 强制按 ctx 租户过滤）。
func backfillGovernanceServices(ctx context.Context, stores *Stores) {
	gov := stores.Governance // Repository 内嵌 ServiceStore，接口直接可传
	wls, err := stores.Workload.ListAll(ctx)
	if err != nil {
		log.Printf("[gov-autoreg] 存量对账跳过（ListAll 失败）: %v", err)
		return
	}
	byTenant := map[string][]workload.Workload{}
	for _, w := range wls {
		if w.Type == workload.TypeService && w.Port > 0 {
			byTenant[w.TenantID] = append(byTenant[w.TenantID], w)
		}
	}
	n := 0
	for tid, ws := range byTenant {
		tctx := tenant.WithTenant(ctx, tid)
		for _, w := range ws {
			if err := governance.AutoRegisterService(tctx, gov, governance.Service{
				AppID: w.AppID, EnvID: w.EnvID, Name: w.Name,
				Protocol: governance.ProtocolHTTP, Port: w.Port,
				Desc: "工作负载部署自动注册",
			}); err != nil {
				log.Printf("[gov-autoreg] 存量补注册失败 tenant=%s workload=%s: %v", tid, w.ID, err)
			} else {
				n++
			}
		}
	}
	if n > 0 {
		log.Printf("[gov-autoreg] 存量补注册 %d 个治理服务", n)
	}
}
