package workload

import (
	"context"
	"fmt"
	"log"
	"time"

	"strings"

	"github.com/aitoys/paas/pkg/tenant"
)

// LaneActivity 某条泳道的活跃度判定结果。
type LaneActivity struct {
	Active bool      // 有进行中（非终态）run——联调/部署中，禁止回收
	Last   time.Time // 最近终态 run 的 FinishedAt；无 run 返零值
}

// LaneActivityChecker 查询某 (app, lane) 的活跃度（进行中 run + 最近终态时间）。
// 依赖倒置：workload 不 import pipeline。查询错误时调用方跳过该 lane（fail-open 下轮再试）。
type LaneActivityChecker interface {
	LastActive(ctx context.Context, tenantID, appID, lane string) (LaneActivity, error)
}

// EnvType 查询环境类型（prod 跳过回收）。依赖倒置：复用 environment.EnvTypeResolver 语义。
type EnvType func(ctx context.Context, envID string) (string, error)

// QuotaDec 回收后配额回退（-1）。可空；cmd/core 桥接 billing（与 handler 删除路径同源）。
type QuotaDec func(ctx context.Context, tenantID string)

// laneModePermanent 与 lane.ModePermanent 对齐（workload 不 import lane，靠桥接层保证一致）。
const laneModePermanent = "permanent"

// LaneOverrideCleaner 泳道配置覆盖级联清理（依赖倒置：configcenter.LaneOverrideCleaner 同形接口，
// workload 不 import configcenter，靠 cmd/core 桥接保证一致）。
type LaneOverrideCleaner interface {
	CleanLane(ctx context.Context, tenantID, envID, laneID string) error
}

// LaneStatusStore 泳道实体的模式/状态查询（依赖倒置：workload 不 import lane）。
// Mode 返回 lane.Mode；无实体（纯遗留泳道）返 "" 照旧回收。
// MarkClosed 回收后同步实体 closed（无实体忽略；best-effort，失败不阻断回收）。
type LaneStatusStore interface {
	Mode(ctx context.Context, envID, name string) (string, error)
	MarkClosed(ctx context.Context, envID, name string) error
}

// LaneGC 闲置泳道回收：周期扫描非 default 泳道 Workload，闲置超 TTL 且无活跃 run 则删除。
// 「闲置」= 无进行中 run 且 max(Workload.CreatedAt, 最近终态 run FinishedAt) 距今超 TTL
// （Workload 无 UpdatedAt，以创建时间 + run 活跃度近似，CI 重跑会刷新 run 侧时间，足够判定）。
type LaneGC struct {
	Repos    Repository // 跨租户列出（ListAll）+ 删除（经 Applier 装饰）
	Runs     LaneActivityChecker
	Lanes    LaneStatusStore // 可空（nil 跳过实体联动：无 permanent 跳过/无 MarkClosed）
	EnvType  EnvType
	Quota    QuotaDec              // 可空（nil 跳过配额回退）
	TTL      time.Duration         // 闲置阈值（默认 72h）
	MaxSweep int                   // 单轮删除上限（默认 20）
	Now      func() time.Time      // 测试注入时钟
	Log      *log.Logger           // 可空（nil 用 log.Default）
	Audit    AdminAuditRecorder    // 可空（nil 跳过审计）；回收是删除用户资源，审计只增不删（合规）
	Cleaners []LaneOverrideCleaner // 可空；泳道全组回收后级联清依赖资源（如配置中心泳道覆盖），best-effort
}

// Sweep 执行一轮回收，返回删除数。单轮上限 MaxSweep（防 TTL 误配短引发雪崩）；
// prod 环境泳道跳过（灰度泳道回收策略留后续）；EnvType/run 查询失败跳过该 lane（保守，下轮再试）。
// Delete/EnvType/审计均按 Workload.TenantID 派生租户 ctx——store 层强制租户过滤，
// 无租户 ctx 会全部报错致 GC 静默失效（R1-R5 审计 Critical 修复）。
func (g *LaneGC) Sweep(ctx context.Context) int {
	nowFn := g.Now
	if nowFn == nil {
		nowFn = time.Now // nil 兜底：装配漏注入时不再 SIGSEGV（k8s 生产暴露，31 次重启根因）
	}
	now := nowFn()
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
	// LastActive 按 (app, lane) 去重缓存：同泳道多服务 Workload 不重复查（R4 N+1 修复）。
	lastActiveCache := map[string]LaneActivity{}
	// 泳道维度回收记录：key=(tenant,env,lane)，val=[该泳道本轮删除数, 删除后剩余数]。
	// 全部删完才 MarkClosed（同泳道多服务部分删除时不提前关闭实体）。
	laneRemaining := map[string]int{}
	laneDeletedCnt := map[string]int{}
	for _, w := range wls {
		if w.LaneID != "" && w.LaneID != LaneDefault {
			laneRemaining[w.TenantID+"/"+w.EnvID+"/"+w.LaneID]++
		}
	}
	deleted := 0
	for _, w := range wls {
		if deleted >= maxSweep {
			break
		}
		if ctx.Err() != nil {
			break // 进程退出：剩余候选下轮（GC 幂等重扫，无中间态）
		}
		if w.LaneID == "" || w.LaneID == LaneDefault {
			continue // 基线不回收
		}
		wctx := tenant.WithTenant(ctx, w.TenantID) // store 层强制租户，派生后 EnvType/Delete 才能通过
		// prod 护栏 fail-closed：查询失败也跳过（环境 store 抖动期间不误删生产泳道）。
		et, err := g.EnvType(wctx, w.EnvID)
		if err != nil || et == "prod" {
			continue
		}
		// 泳道实体联动：Mode=permanent（常驻联调）跳过；查询失败也跳过（保守，下轮再试）。
		if g.Lanes != nil {
			mode, err := g.Lanes.Mode(wctx, w.EnvID, w.LaneID)
			if err != nil || mode == laneModePermanent {
				continue
			}
		}
		ck := w.TenantID + "/" + w.AppID + "/" + w.LaneID
		act, ok := lastActiveCache[ck]
		if !ok {
			var err error
			act, err = g.Runs.LastActive(wctx, w.TenantID, w.AppID, w.LaneID)
			if err != nil {
				continue // 查询失败跳过，下轮再试
			}
			lastActiveCache[ck] = act
		}
		if act.Active {
			continue // 有进行中 run（联调/部署中），禁止回收（R1 Important 修复）
		}
		idleSince := w.CreatedAt
		if act.Last.After(idleSince) {
			idleSince = act.Last
		}
		if now.Sub(idleSince) <= ttl {
			continue // 仍在 TTL 内
		}
		if err := g.Repos.Delete(wctx, w.ID); err != nil {
			g.logf("laneGC: delete %s: %v", w.ID, err)
			continue
		}
		g.afterReclaim(wctx, w, now.Sub(idleSince).Round(time.Minute))
		deleted++
		// key 含 tenant：多租户同 envID+laneName 不互扣（终审 I1②）。
		lk := w.TenantID + "/" + w.EnvID + "/" + w.LaneID
		laneDeletedCnt[lk]++
		laneRemaining[lk]--
	}
	// 同泳道全部工作负载删除完才 MarkClosed（部分删除不提前关闭，剩余下轮删完再关）。
	// ctx 需派生泳道租户——store 层强制 TenantOrErr，基础 ctx（无租户）会让 MarkClosed 静默失效（终审 I1①）。
	for lk, n := range laneDeletedCnt {
		if laneRemaining[lk] == 0 && n > 0 {
			parts := strings.SplitN(lk, "/", 3)
			wctx := tenant.WithTenant(ctx, parts[0])
			g.markLaneClosed(wctx, parts[1], parts[2])
			g.cleanLaneOverrides(wctx, parts[0], parts[1], parts[2])
		}
	}
	return deleted
}

// afterReclaim 单个 workload 删除成功后的配额回退 + 日志 + 审计（Sweep 与 ReclaimLane 共用）。
func (g *LaneGC) afterReclaim(wctx context.Context, w Workload, idle time.Duration) {
	if g.Quota != nil {
		g.Quota(wctx, w.TenantID) // 配额回退，防反复创建+GC 后配额泄漏（R3 F2 修复）
	}
	g.logf("laneGC: 回收泳道 workload=%s app=%s lane=%s（闲置 %s）", w.ID, w.AppID, w.LaneID, idle)
	if g.Audit != nil {
		// best-effort：审计失败不阻断回收（日志已留痕），与 handler 层审计同款取舍。
		_ = g.Audit.Record(wctx, w.TenantID, "lane-gc", "lane_close", "workload", w.ID,
			fmt.Sprintf("回收泳道 workload app=%s lane=%s 闲置=%s", w.AppID, w.LaneID, idle))
	}
}

// markLaneClosed 泳道内全部 workload 删除成功后同步实体 closed（best-effort：无实体/失败仅日志）。
func (g *LaneGC) markLaneClosed(ctx context.Context, envID, laneName string) {
	if g.Lanes == nil {
		return
	}
	if err := g.Lanes.MarkClosed(ctx, envID, laneName); err != nil {
		g.logf("laneGC: mark lane closed env=%s lane=%s: %v", envID, laneName, err)
	}
}

// cleanLaneOverrides 泳道全组回收后级联清依赖资源（当前：配置中心泳道覆盖）。
// best-effort：失败仅日志，不阻断回收（覆盖遗留不影响正确性——泳道已消失，无人带该 lane 拉配置）。
func (g *LaneGC) cleanLaneOverrides(wctx context.Context, tenantID, envID, laneName string) {
	for _, c := range g.Cleaners {
		if c == nil {
			continue
		}
		if err := c.CleanLane(wctx, tenantID, envID, laneName); err != nil {
			g.logf("laneGC: clean lane overrides env=%s lane=%s: %v", envID, laneName, err)
		}
	}
}

// ReclaimLane 同步回收某条泳道的全部工作负载（lane handler 关闭泳道时调用）。
// 逐 workload：派生租户 ctx -> Delete -> Quota(-1) -> 审计 lane_close（detail 含回收数由调用方拼装）。
// 仅管删：TTL 判定/prod 跳过/活跃 run 阻止在 Sweep 层或 handler Close 前置完成。
// 单个删除失败继续删其余（best-effort，剩余遗留由 GC 兜底清扫），返回成功删除数。
func (g *LaneGC) ReclaimLane(ctx context.Context, tenantID, envID, laneName string) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	wctx := tenant.WithTenant(ctx, tenantID) // store 层强制租户过滤
	// List 空参数 = 跨应用全类型，按 (env, lane) 过滤。
	wls, err := g.Repos.List(wctx, envID, "", laneName, "", "")
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, w := range wls {
		if ctx.Err() != nil {
			break // 进程退出：剩余遗留由 GC 兜底
		}
		if err := g.Repos.Delete(wctx, w.ID); err != nil {
			g.logf("laneGC: reclaim delete %s: %v", w.ID, err)
			continue
		}
		g.afterReclaim(wctx, w, 0)
		deleted++
	}
	if deleted > 0 {
		g.markLaneClosed(wctx, envID, laneName)
		g.cleanLaneOverrides(wctx, tenantID, envID, laneName)
	}
	return deleted, nil
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
