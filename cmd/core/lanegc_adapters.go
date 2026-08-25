package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/pkg/tenant"
)

// laneRunChecker 桥接 pipeline.RunRepository -> workload.LaneActivityChecker（依赖倒置，
// workload 不 import pipeline）。最近活跃 = 该 (app, lane) 全部 run 的最近 FinishedAt
// （run.Branch == lane 是 L1 锁定的关联约定）。ListRuns 按 ctx tenant 过滤，须派生租户 ctx。
type laneRunChecker struct{ runs pipeline.RunRepository }

func (c laneRunChecker) LastActive(ctx context.Context, tenantID, appID, lane string) (time.Time, error) {
	ctx = tenant.WithTenant(ctx, tenantID)
	runs, err := c.runs.ListRuns(ctx, appID, "", "")
	if err != nil {
		return time.Time{}, err
	}
	var last time.Time
	for _, r := range runs {
		if r.Branch != lane || r.FinishedAt.IsZero() {
			continue
		}
		if r.FinishedAt.After(last) {
			last = r.FinishedAt
		}
	}
	return last, nil
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
