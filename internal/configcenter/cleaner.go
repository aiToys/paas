package configcenter

import (
	"context"

	"github.com/aitoys/paas/pkg/tenant"
)

// NewLaneOverrideCleaner 基于 Repository 实现泳道覆盖级联清理（泳道回收路径消费）。
// 桥接对象：cmd/core 装配传入 stores.ConfigCenter。
func NewLaneOverrideCleaner(repo Repository) LaneOverrideCleaner {
	return repoCleaner{repo: repo}
}

type repoCleaner struct{ repo Repository }

// CleanLane 按 (env, lane) 跨 app 列出并逐条物理删除（tenant 从 ctx——调用方须派生泳道租户 ctx）。
// 单条删除失败继续其余（best-effort，返回首个错误供日志）。
func (c repoCleaner) CleanLane(ctx context.Context, tenantID, envID, laneID string) error {
	if tenantID != "" {
		ctx = tenant.WithTenant(ctx, tenantID)
	}
	ovs, err := c.repo.ListLaneOverridesForClean(ctx, envID, laneID)
	if err != nil {
		return err
	}
	var firstErr error
	for _, o := range ovs {
		if err := c.repo.DeleteLaneOverride(ctx, o.AppID, envID, laneID, o.Key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
