package main

import (
	"context"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/workload"
)

// appWorkloadStats 实现 application.WorkloadStats：聚合租户内各应用的工作负载统计，
// 供应用列表派生 Replicas/Status（真实化，覆盖 seed 假值）。
//
// 注入 StatusReader 在聚合前回填 K8s 真实 Ready/Status（非 store 静态值），
// 与 workload handler 读路径同源，保证应用卡片展示的工作负载状态真实一致。
type appWorkloadStats struct {
	wlRepo workload.Repository
	status workload.StatusReader
}

// StatsByTenant 返回 map[appID]AppStats。StatusReader 失败仅忽略（降级用 store 原值聚合）。
func (s *appWorkloadStats) StatsByTenant(ctx context.Context) (map[string]application.AppStats, error) {
	wls, err := s.wlRepo.List(ctx, "", "", "", "") // appID="" 返租户内全部工作负载
	if err != nil {
		return nil, err
	}
	if s.status != nil {
		_ = s.status.FillStatus(ctx, wls)
	}
	stats := map[string]application.AppStats{}
	for _, w := range wls {
		st := stats[w.AppID]
		st.Total += w.Replicas
		st.Ready += w.Ready
		if w.Status == workload.StatusDeploying {
			st.Deploying++
		}
		if w.Status == workload.StatusFailed {
			st.Failed++
		}
		stats[w.AppID] = st
	}
	return stats, nil
}
