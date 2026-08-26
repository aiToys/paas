package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// laneRunChecker 桥接 pipeline.RunRepository -> workload.LaneActivityChecker（依赖倒置，
// workload 不 import pipeline）。run.Branch == lane 是 L1 锁定的关联约定：
//   - Active：存在非终态（running/paused）run——联调/部署中，禁止回收；
//   - Last：最近终态 run 的 FinishedAt（闲置计时基准）。
// ListRuns 按 ctx tenant 过滤，须派生租户 ctx。
type laneRunChecker struct{ runs pipeline.RunRepository }

func (c laneRunChecker) LastActive(ctx context.Context, tenantID, appID, lane string) (workload.LaneActivity, error) {
	ctx = tenant.WithTenant(ctx, tenantID)
	runs, err := c.runs.ListRuns(ctx, appID, "", "")
	if err != nil {
		return workload.LaneActivity{}, err
	}
	var act workload.LaneActivity
	for _, r := range runs {
		if r.Branch != lane {
			continue
		}
		if r.FinishedAt.IsZero() {
			act.Active = true // 非终态 run（running/paused），部署/联调进行中
			continue
		}
		if r.FinishedAt.After(act.Last) {
			act.Last = r.FinishedAt
		}
	}
	return act, nil
}

// envDuration 读 env 时长配置，缺省/解析失败回落默认值（PAAS_LANE_GC_INTERVAL=0 显式禁用）。
func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Printf("[laneGC] env %s=%q 无效，回落默认 %s", key, v, def)
		return def
	}
	return d
}
