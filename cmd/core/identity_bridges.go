package main

import (
	"context"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/lane"
	"github.com/aitoys/paas/internal/workload"
)

// tenantOffboardingBridge 跨域统计租户名下资源（identity 注销预检，只读）。
// 阻塞口径与真实删除路径对齐：只有 users 会让 DeleteTenant 返回 409；
// 其余项是提示——冻结/删除都不代删数据面实例，需管理员逐项下线。
type tenantOffboardingBridge struct {
	users   identity.Repository
	apps    application.Repository
	wls     workload.Repository
	ds      dataservice.Repository
	lanes   lane.Repository
	billing billing.Repository
}

// Count 返回逐类资源统计。任一来源读取失败即整体失败：
// 预检清单宁可报错，也不给出「部分为 0」的失真结论。
func (b tenantOffboardingBridge) Count(ctx context.Context, tenantID string) ([]identity.OffboardingItem, error) {
	users, err := b.users.ListUsers(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	items := []identity.OffboardingItem{{
		Kind: "users", Label: "用户", Count: len(users), Blocking: true,
		Note: "有用户时删除租户会被拒（409），请先删除或转移用户",
	}}

	apps, err := b.apps.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	items = append(items, identity.OffboardingItem{
		Kind: "applications", Label: "应用", Count: countByTenant(apps, tenantID, func(a application.Application) string { return a.TenantID }),
		Note: "删除应用会级联清理绑定的工作负载与配置，请确认无在用服务",
	})

	wls, err := b.wls.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	items = append(items, identity.OffboardingItem{
		Kind: "workloads", Label: "工作负载", Count: countByTenant(wls, tenantID, func(w workload.Workload) string { return w.TenantID }),
		Note: "数据面 Pod 不会随平台删除自动下线，需先缩容/删除工作负载",
	})

	ds, err := b.ds.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	items = append(items, identity.OffboardingItem{
		Kind: "dataservices", Label: "数据服务", Count: countByTenant(ds, tenantID, func(d dataservice.DataService) string { return d.TenantID }),
		Note: "托管实例的持久卷不会自动回收，需先备份并按需删除实例",
	})

	lanes, err := b.lanes.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	items = append(items, identity.OffboardingItem{
		Kind: "lanes", Label: "泳道", Count: countByTenant(lanes, tenantID, func(l lane.Lane) string { return l.TenantID }),
		Note: "未关闭的泳道会持续占用数据面资源，建议先执行关闭回收",
	})

	bills, err := b.billing.ListAllBills(ctx)
	if err != nil {
		return nil, err
	}
	unpaid := 0
	for _, bl := range bills {
		if bl.TenantID == tenantID && bl.Status != "paid" {
			unpaid++
		}
	}
	items = append(items, identity.OffboardingItem{
		Kind: "unpaid_bills", Label: "未结算账单", Count: unpaid,
		Note: "删除租户不会清除账单记录；未结算账单请在结算后再下线",
	})
	return items, nil
}

// countByTenant 统计切片中归属指定租户的元素个数（ListAll 跨租户返回，需本地过滤）。
func countByTenant[T any](in []T, tenantID string, tenantOf func(T) string) int {
	n := 0
	for _, v := range in {
		if tenantOf(v) == tenantID {
			n++
		}
	}
	return n
}
