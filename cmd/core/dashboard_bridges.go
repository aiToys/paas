package main

import (
	"context"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/dashboard"
	"github.com/aitoys/paas/internal/devops/pipeline"
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

// 编译期接口断言（桥接缺失方法在编译期暴露，非运行期）。
var (
	_ dashboard.AppCounter  = appCountBridge{}
	_ dashboard.BillCounter = billCountBridge{}
	_ dashboard.RunCounter  = runCountBridge{}
)
