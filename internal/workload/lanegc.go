package workload

import (
	"context"
	"fmt"
	"log"
	"time"
)

// LaneActivityChecker 查询某 (app, lane) 的最近活跃时间（最近终态 run 的 FinishedAt；无 run 返零值）。
// 依赖倒置：workload 不 import pipeline。查询错误时调用方跳过该 lane（fail-open 下轮再试）。
type LaneActivityChecker interface {
	LastActive(ctx context.Context, tenantID, appID, lane string) (time.Time, error)
}

// EnvType 查询环境类型（prod 跳过回收）。依赖倒置：复用 environment.EnvTypeResolver 语义。
type EnvType func(ctx context.Context, envID string) (string, error)

// LaneGC 闲置泳道回收：周期扫描非 default 泳道 Workload，闲置超 TTL 且无活跃 run 则删除。
// 「闲置」= max(Workload.CreatedAt, 最近终态 run FinishedAt) 距今超 TTL（Workload 无 UpdatedAt，
// 以创建时间 + run 活跃度近似，CI 重跑会刷新 run 侧时间，足够判定）。
type LaneGC struct {
	Repos    Repository       // 跨租户列出（ListAll）+ 删除（经 Applier 装饰）
	Runs     LaneActivityChecker
	EnvType  EnvType
	TTL      time.Duration    // 闲置阈值（默认 72h）
	MaxSweep int              // 单轮删除上限（默认 20）
	Now      func() time.Time // 测试注入时钟
	Log      *log.Logger      // 可空（nil 用 log.Default）
	Audit    AdminAuditRecorder // 可空（nil 跳过审计）；回收是删除用户资源，审计只增不删（合规）
}

// Sweep 执行一轮回收，返回删除数。单轮上限 MaxSweep（防 TTL 误配短引发雪崩）；
// prod 环境泳道跳过（灰度泳道回收策略留后续）；run 查询失败跳过该 lane（fail-open 下轮再试）。
func (g *LaneGC) Sweep(ctx context.Context) int {
	now := g.Now()
	ttl := g.TTL
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	maxSweep := g.MaxSweep
	if maxSweep <= 0 {
		maxSweep = 20
	}
	wls, err := g.Repos.ListAll(ctx)
	if err != nil {
		g.logf("laneGC: list all: %v", err)
		return 0
	}
	deleted := 0
	for _, w := range wls {
		if deleted >= maxSweep {
			break
		}
		if w.LaneID == "" || w.LaneID == LaneDefault {
			continue // 基线不回收
		}
		if et, err := g.EnvType(ctx, w.EnvID); err == nil && et == "prod" {
			continue // 生产泳道不自动回收
		}
		idleSince := w.CreatedAt
		if last, err := g.Runs.LastActive(ctx, w.TenantID, w.AppID, w.LaneID); err != nil {
			continue // 查询失败跳过，下轮再试
		} else if last.After(idleSince) {
			idleSince = last
		}
		if now.Sub(idleSince) <= ttl {
			continue // 仍在 TTL 内
		}
		if err := g.Repos.Delete(ctx, w.ID); err != nil {
			g.logf("laneGC: delete %s: %v", w.ID, err)
			continue
		}
		g.logf("laneGC: 回收闲置泳道 workload=%s app=%s lane=%s（闲置 %s）",
			w.ID, w.AppID, w.LaneID, now.Sub(idleSince).Round(time.Minute))
		if g.Audit != nil {
			// best-effort：审计失败不阻断回收（日志已留痕），与 handler 层审计同款取舍。
			_ = g.Audit.Record(ctx, w.TenantID, "lane-gc", "lane_gc", "workload", w.ID,
				fmt.Sprintf("回收闲置泳道 app=%s lane=%s 闲置=%s", w.AppID, w.LaneID, now.Sub(idleSince).Round(time.Minute)))
		}
		deleted++
	}
	return deleted
}

func (g *LaneGC) logf(format string, args ...any) {
	if g.Log != nil {
		g.Log.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Start 启动周期回收（间隔 interval，<=0 用默认 30min）。返回停止函数。
// ctx 取消即退出（cmd/core 用 baseCtx 派生，进程退出即停）。
func (g *LaneGC) Start(ctx context.Context, interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.Sweep(ctx)
			}
		}
	}()
	return func() { <-done }
}
