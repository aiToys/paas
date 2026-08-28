package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/lane"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// laneRunChecker 桥接 pipeline.RunRepository -> workload.LaneActivityChecker（依赖倒置，
// workload 不 import pipeline）。run.Branch == lane 是 L1 锁定的关联约定：
//   - Active：存在非终态（running/paused）run——联调/部署中，禁止回收；
//   - Last：最近终态 run 的 FinishedAt（闲置计时基准）。
//
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

// laneWorkloadLister 桥接 workload.Repository -> lane.WorkloadLister（Detail 聚合，
// 依赖倒置避免 lane -> workload 反向控制）。List 空参数=跨应用全类型，按 lane 过滤。
type laneWorkloadLister struct{ repos workload.Repository }

func (l laneWorkloadLister) ListByLane(ctx context.Context, envID, laneID string) ([]workload.Workload, error) {
	return l.repos.List(ctx, envID, "", laneID, "", "")
}

// laneRunLister 桥接 pipeline.RunRepository -> lane.RunLister（Detail 聚合 + Close 前置校验）。
// run.Branch == lane.Name 是 spec 锁定的关联约定；ListRuns("", "", "") 跨应用全量后按 branch 过滤。
type laneRunLister struct{ runs pipeline.RunRepository }

func (l laneRunLister) ListByBranch(ctx context.Context, branch string) ([]pipeline.RunSummary, error) {
	runs, err := l.runs.ListRuns(ctx, "", "", "")
	if err != nil {
		return nil, err
	}
	out := make([]pipeline.RunSummary, 0, len(runs))
	for _, r := range runs {
		if r.Branch == branch {
			out = append(out, r.Summarize())
		}
	}
	return out, nil
}

// laneStatusBridge 桥接 lane.Repository -> workload.LaneStatusStore（依赖倒置，
// workload 不 import lane）。Mode 取泳道模式（permanent 常驻 GC 跳过）；
// MarkClosed 回收后同步实体 closed（幂等；无实体=纯遗留泳道，忽略）。
type laneStatusBridge struct{ lanes laneRepository }

// laneRepository 收窄 lane.Repository 到 GC 联动所需的最小面（Mode/MarkClosed 仅需 GetByName/Close）。
type laneRepository interface {
	GetByName(ctx context.Context, envID, name string) (lane.Lane, error)
	Close(ctx context.Context, id string) (lane.Lane, error)
}

func (b laneStatusBridge) Mode(ctx context.Context, envID, name string) (string, error) {
	ln, err := b.lanes.GetByName(ctx, envID, name)
	if err != nil {
		return "", err // 不存在等错误交调用方保守跳过
	}
	return ln.Mode, nil
}

func (b laneStatusBridge) MarkClosed(ctx context.Context, envID, name string) error {
	ln, err := b.lanes.GetByName(ctx, envID, name)
	if err != nil {
		// 区分「无实体」（纯遗留泳道，无需标记返 nil）与「查询失败」（DB 错/无租户 ctx，上抛供 GC 日志——终审 I1①）。
		if errors.Is(err, lane.ErrLaneNotFound) {
			return nil
		}
		return err
	}
	if ln.Status == lane.StatusClosed {
		return nil // 幂等
	}
	_, err = b.lanes.Close(ctx, ln.ID)
	return err
}
