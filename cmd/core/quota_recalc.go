package main

// quota_recalc.go —— 配额用量启动自愈（对账审计 C 节修复，2026-09-04）。
//
// 背景：demo seed 直插表不走 CheckAndInc、历史删除路径回收缺失（如 dataservice 租户侧
// 删除曾无 -1），billing_usages.counts 与实际行数漂移（reconcile.sh C 节抓到 5 条）。
// 处理：启动时把「行数计数型」资源（applications/workloads/dataservices，与表行数 1:1）
// 重算为各租户实际行数。计量型资源（models/gpu/tokens/storage_gb）非行数语义，不参与。
// 幂等：计数一致时零写操作；并发安全（IncUsage 行锁）。修复后 reconcile C 节应恒为零。

import (
	"context"
	"log"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/pkg/tenant"
)

// recalcUsageIfDrift 按租户把计数型资源用量对齐实际行数（best-effort，失败不阻断启动；
// GC 类启动任务在 server 起来前完成，无并发写入窗口）。
func recalcUsageIfDrift(ctx context.Context, s *Stores) {
	ts, err := s.Identity.ListTenants(ctx)
	if err != nil {
		log.Printf("[quota-recalc] 列租户失败（跳过自愈）: %v", err)
		return
	}
	for _, t := range ts {
		tctx := tenant.WithTenant(ctx, t.ID)
		apps, aErr := s.Application.List(tctx)
		wls, wErr := s.Workload.List(tctx, "", "", "", "", "")
		dss, dErr := s.DataService.List(tctx, "")
		if aErr != nil || wErr != nil || dErr != nil {
			log.Printf("[quota-recalc] %s 行数查询失败（跳过该租户）: app=%v wl=%v ds=%v", t.ID, aErr, wErr, dErr)
			continue
		}
		recalcOne(tctx, s, billing.ResApplications, len(apps))
		recalcOne(tctx, s, billing.ResWorkloads, len(wls))
		recalcOne(tctx, s, billing.ResDataservices, len(dss))
	}
}

// recalcOne 单资源对齐：counts[resource] != actual 时 IncUsage 补差值（负 delta 修泄漏、
// 正 delta 补漏计）。
func recalcOne(ctx context.Context, s *Stores, resource string, actual int) {
	u, err := s.Billing.GetUsage(ctx)
	if err != nil {
		log.Printf("[quota-recalc] %s 读用量失败: %v", resource, err)
		return
	}
	if cur := u.Counts[resource]; cur != actual {
		if _, err := s.Billing.IncUsage(ctx, resource, actual-cur); err != nil {
			log.Printf("[quota-recalc] %s %s: %d→%d 修正失败: %v", u.TenantID, resource, cur, actual, err)
			return
		}
		log.Printf("[quota-recalc] %s %s: %d→%d（与实际行数对齐）", u.TenantID, resource, cur, actual)
	}
}
