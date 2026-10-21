package main

import (
	"context"
	"sort"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/dashboard"
	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/security"
)

// dashboard 聚合计数桥接（依赖倒置：dashboard 只见窄接口，不见业务仓储全貌）。

// appCountBridge 应用计数（dashboard.AppCounter）。
type appCountBridge struct{ repo application.Repository }

func (b appCountBridge) ListAll(ctx context.Context) (int, error) {
	list, err := b.repo.ListAll(ctx)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// billCountBridge 未支付账单计数（dashboard.BillCounter）。
type billCountBridge struct{ repo billing.Repository }

func (b billCountBridge) ListAllBills(ctx context.Context) (int, error) {
	list, err := b.repo.ListAllBills(ctx)
	if err != nil {
		return 0, err
	}
	unpaid := 0
	for _, r := range list {
		if r.Status == billing.StatusUnpaid {
			unpaid++
		}
	}
	return unpaid, nil
}

// runCountBridge 流水线运行计数（dashboard.RunCounter）。
type runCountBridge struct{ repo pipeline.Store }

func (b runCountBridge) ListRuns(ctx context.Context, appID, pipelineID, status string) (int, error) {
	list, err := b.repo.ListRuns(ctx, appID, pipelineID, status)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// auditListBridge 跨租户审计最近记录（dashboard.AuditLister）：security.AuditLog 投影到
// dashboard.AuditEntry，按时间倒序取前 limit 条（首页「最近平台操作」数据源）。
type auditListBridge struct{ repo security.Repository }

func (b auditListBridge) ListRecentAudit(ctx context.Context, limit int) ([]dashboard.AuditEntry, error) {
	list, err := b.repo.ListAllAuditLogs(ctx)
	if err != nil {
		return nil, err
	}
	// 时间倒序（新操作在前）；sort.Slice 拷贝排序不改动仓储返回切片。
	sorted := make([]security.AuditLog, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.After(sorted[j].At) })
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	out := make([]dashboard.AuditEntry, 0, len(sorted))
	for _, e := range sorted {
		out = append(out, dashboard.AuditEntry{
			ID:           e.ID,
			TenantID:     e.TenantID,
			Actor:        e.Actor,
			Action:       e.Action,
			ResourceType: e.ResourceType,
			ResourceID:   e.ResourceID,
			Detail:       e.Detail,
			At:           e.At,
		})
	}
	return out, nil
}

// 编译期接口断言（桥接缺失方法在编译期暴露，非运行期）。
var (
	_ dashboard.AppCounter  = appCountBridge{}
	_ dashboard.BillCounter = billCountBridge{}
	_ dashboard.RunCounter  = runCountBridge{}
	_ dashboard.AuditLister = auditListBridge{}
)
