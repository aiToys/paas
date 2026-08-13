// drift_reconcile.go 启动期 + admin 手动触发的 PG↔K8s 数据面 drift 修复。
//
// 背景：workload.ApplyRepo 投影失败仅 log 不阻断控制面写（applier.go 注释），
// 或历史裸 workload（PG 有行无 CRD，绕过平台 Release 编排），导致「PG 有记录但 K8s 无资源」。
// 本钩子扫 PG workloads 表全部行，对「PG 有行但 K8s 无 CRD」的调 EnsureIfMissing 补投影，
// reconciler 据此建 K8s Deployment/Job/CronJob/Service，消除 drift。
//
// 关键语义：用 EnsureIfMissing（仅 CRD 缺失才补建），而非 Apply（CreateOrUpdate 会用 PG
// 当前状态覆盖既有 CRD）。drift 场景 PG 可能陈旧（裸 workload 空 image_ref / replicas=0），
// Apply 会把已有运行态（如 Release 编排出来的带 image 的 Deployment）回退成陈旧状态——
// 典型副作用：service 被缩到 0 副本。EnsureIfMissing 存在即跳过，只补真正缺失的。
//
// 设计：保守（只补建不覆盖、不删除 K8s 孤儿，防误删/误覆盖）。
package main

import (
	"context"
	"log"

	"github.com/aitoys/paas/internal/workload"
)

// reconcileWorkloadDrift 扫描全部 workload（跨租户），对「PG 有行无 CRD」的调 EnsureIfMissing 补投影。
// 启动期调用（manager 起来后）+ admin 端点手动触发。保守：存在即跳过，只补缺失。
// applier 为 nil（K8s 未启用）时跳过。返回（已扫描数，已补建数，已跳过数，错误）。
func reconcileWorkloadDrift(ctx context.Context, repo workload.Repository, applier workload.Applier) (int, int, int, error) {
	if applier == nil {
		log.Printf("[drift] K8s 数据面未启用，跳过 workload drift 修复")
		return 0, 0, 0, nil
	}
	// ListAll 跨租户（admin 视图），返回对象带 TenantID（EnsureIfMissing 据 w.TenantID 派生 ns）
	list, err := repo.ListAll(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	created, skipped := 0, 0
	for i := range list {
		w := list[i]
		if w.TenantID == "" {
			// 老 PG 行可能 tenant 空（异常），跳过（EnsureIfMissing 需 tenant 派生 ns）
			log.Printf("[drift] workload %s tenant 为空，跳过", w.ID)
			continue
		}
		ok, err := applier.EnsureIfMissing(ctx, w)
		if err != nil {
			// 单条失败不阻断（继续修其他），记日志留痕
			log.Printf("[drift] 补投影失败 workload %s: %v", w.ID, err)
			continue
		}
		if ok {
			created++
		} else {
			skipped++
		}
	}
	log.Printf("[drift] workload drift 修复完成：扫描 %d，补建 %d，跳过 %d（已存在）", len(list), created, skipped)
	return len(list), created, skipped, nil
}
