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
	wls, err := s.wlRepo.List(ctx, "", "", "", "", "") // appID="" 返租户内全部工作负载
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

// envUsageChecker 实现 environment.EnvUsageChecker：环境删除前查该环境下工作负载数
// （非 0 拒删 409，防悬挂 envId 引用——allowProd fail-closed + 发布阶序断裂）。
// 桥接 workload store（environment 不 import workload：后者已 import environment 会循环）。
type envUsageChecker struct{ wl workload.Repository }

func (c envUsageChecker) CountByEnv(ctx context.Context, envID string) (int, error) {
	// List 按 ctx 租户过滤（Repository 强制）；appID/lane/type/service 空串=不过滤。
	wls, err := c.wl.List(ctx, envID, "", "", "", "")
	if err != nil {
		return 0, err
	}
	return len(wls), nil
}

// wlServiceLister 查 app×env 已部署的服务集合（devops.WorkloadServiceLister 桥接，
// 多服务发布 service 必填校验用）。Workload.Service 非空值去重；基线+泳道合并。
type wlServiceLister struct{ repo workload.Repository }

func (l *wlServiceLister) DeployedServices(ctx context.Context, appID, envID string) ([]string, error) {
	// laneID 空串=不过滤（含基线 + 全部泳道），服务形态以 app 维度判定
	wls, err := l.repo.List(ctx, envID, appID, "", workload.TypeService, "")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(wls))
	out := make([]string, 0, len(wls))
	for _, w := range wls {
		if w.Service == "" {
			continue // 单服务 Workload（Service 空）不算多服务形态
		}
		if _, ok := seen[w.Service]; !ok {
			seen[w.Service] = struct{}{}
			out = append(out, w.Service)
		}
	}
	return out, nil
}

// appIDListAdapter 列当前租户应用 ID 集合（devops.AppIDLister 桥接，
// registry catalog/tags 租户过滤用）。List 按 ctx 租户过滤（Repository 强制）。
type appIDListAdapter struct{ repo application.Repository }

func (a *appIDListAdapter) List(ctx context.Context) ([]string, error) {
	apps, err := a.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(apps))
	for _, app := range apps {
		out = append(out, app.ID)
	}
	return out, nil
}
