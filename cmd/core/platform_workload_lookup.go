package main

import (
	"context"

	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/workload"
)

// platformWorkloadLookup 桥接 workload.Repository → controller.PlatformWorkloadLookup（依赖倒置）。
// 孤儿 CR 回收用：CR 的 name 即平台 workload ID。Get 需要租户 ctx 而回收是平台级动作，
// 故用 ListAll（跨租户）对账。
type platformWorkloadLookup struct {
	repo workload.Repository
}

// Exists 判断平台侧是否存在指定 ID 的工作负载（ListAll 遍历对账）。
func (p platformWorkloadLookup) Exists(ctx context.Context, id string) (bool, error) {
	if p.repo == nil {
		return true, nil // 无仓库（异常装配）：宁可不回收也不误删
	}
	all, err := p.repo.ListAll(ctx)
	if err != nil {
		return false, err
	}
	for _, w := range all {
		if w.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// platformDataExists 桥接 dataservice.Repository → controller.PlatformExistsFunc（依赖倒置）。
// 数据服务孤儿 CR 回收用：CR 的 name 即平台数据服务 ID（ds-<ts>-<n>）。与 workload 同理，
// Get 需要租户 ctx 而回收是平台级动作，故用 ListAll（跨租户）对账。
func platformDataExists(repo dataservice.Repository) controller.PlatformExistsFunc {
	return func(ctx context.Context, id string) (bool, error) {
		if repo == nil {
			return true, nil // 无仓库（异常装配）：宁可不回收也不误删
		}
		all, err := repo.ListAll(ctx)
		if err != nil {
			return false, err
		}
		for _, d := range all {
			if d.ID == id {
				return true, nil
			}
		}
		return false, nil
	}
}
