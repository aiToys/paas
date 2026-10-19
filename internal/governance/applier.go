package governance

import (
	"context"
	"log"

	"github.com/aitoys/paas/pkg/tenant"
)

// RouteApplier 把 governance.Route 投影到数据面（K8s 启用时按 host 聚合建/更/删 Ingress）。
//
// 与 workload.Applier 同款「控制面真源 + 数据面投影」哲学：PG/memory 作 API 查询源，
// Ingress 作流量入口真源。Route 不是 K8s 资源（无 CRD），故按 (tenant ns, host) 聚合成
// 单条多 path Ingress（多 Route 共享同 host 共用一条），而非一 Route 一 Ingress。
//
// nil 时 ApplyRepo 不调用（非 K8s 部署 / 测试场景，CRUD 行为不变）。
type RouteApplier interface {
	// Apply 重建 (tenant ns, host) 聚合 Ingress：取该租户同 host 全部 enabled Route，
	// 各自解析 Service.Name+Port 合成多 path；无剩余 enabled Route 则删整条 Ingress。
	Apply(ctx context.Context, tid, host string) error
	// Delete 语义等同 Apply 的「无剩余则删」（重建聚合 Ingress）。
	Delete(ctx context.Context, tid, host string) error
}

// ApplyRepo 装饰 Repository：Route 写成功后投影数据面（best-effort，失败仅 log）。
//
// 数据面投影失败不阻塞控制面写（控制面真源优先），但记日志留可观测痕迹，
// 避免「控制面显示创建成功但 Ingress 未建」的静默不一致（与 workload.ApplyRepo.applyLog 同款）。
type ApplyRepo struct {
	Repository
	applier RouteApplier
}

// NewApplyRepo 包装 inner，在其 Route 写操作后调用 applier（applier 为 nil 时透传，无副作用）。
func NewApplyRepo(inner Repository, a RouteApplier) *ApplyRepo {
	return &ApplyRepo{Repository: inner, applier: a}
}

// applyLog 数据面投影失败时记日志（不阻断控制面写）。
func (r *ApplyRepo) applyLog(op, id, host string, err error) {
	if err != nil {
		log.Printf("[governance-route-applier] 数据面投影失败 id=%s host=%s op=%s err=%v", id, host, op, err)
	}
}

// recordApply 回写单条 Route 的下发状态（spec 2026-10-17 专项04 §4 证据链）：
// 成功=applied+时间；失败=failed+脱敏原因（err 可能含 apiserver 细节，截断防超长）。
// best-effort：回写失败仅日志（投影本身已 best-effort，状态字段不反阻塞）。
// ctx 用 WithoutCancel：投影后调用方可能立即结束请求（原 ctx 取消会丢租户与连接）。
func (r *ApplyRepo) recordApply(ctx context.Context, id string, err error) {
	wctx := context.WithoutCancel(ctx)
	if err == nil {
		//nolint:staticcheck // QF1008：显式走嵌入字段，防本装饰器未来覆写 SetRouteApplyStatus 自递归（同文件既有取舍）
		if serr := r.Repository.SetRouteApplyStatus(wctx, id, "applied", ""); serr != nil {
			log.Printf("[governance-route-applier] 回写下发状态失败 id=%s err=%v", id, serr)
		}
		return
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	//nolint:staticcheck // QF1008：同上，显式走内层
	if serr := r.Repository.SetRouteApplyStatus(wctx, id, "failed", msg); serr != nil {
		log.Printf("[governance-route-applier] 回写下发状态失败 id=%s err=%v", id, serr)
	}
}

// routeTenant 补全 route.TenantID：store 从 ctx 写 tenant 但不回填传入的 route，
// 投影需 tid 派生 ns（paas-<tenant>），空值致 ns 错误。
func routeTenant(ctx context.Context, r Route) (string, Route) {
	if r.TenantID == "" {
		if tid, ok := tenant.TenantFrom(ctx); ok {
			r.TenantID = tid
		}
	}
	return r.TenantID, r
}

// CreateRoute 装饰：写成功后投影（Host 非空才下发，Host 空无法对外路由）。
func (r *ApplyRepo) CreateRoute(ctx context.Context, route Route) (Route, error) {
	saved, err := r.Repository.CreateRoute(ctx, route)
	if err != nil {
		return saved, err
	}
	if r.applier != nil && saved.Host != "" {
		tid, saved := routeTenant(ctx, saved)
		if tid != "" {
			aerr := r.applier.Apply(ctx, tid, saved.Host)
			r.applyLog("create", saved.ID, saved.Host, aerr)
			r.recordApply(ctx, saved.ID, aerr)
		}
	}
	return saved, nil
}

// UpdateRoute 装饰：写成功后投影。Host 变更时新旧 host 都需重建（旧 host 可能剩其他 Route，
// 新 host 加入新 path），故对旧+新 host 分别 Apply（去重在 applier 内自然幂等）。
func (r *ApplyRepo) UpdateRoute(ctx context.Context, route Route) (Route, error) {
	old, _ := r.Repository.GetRoute(ctx, route.ID)     //nolint:staticcheck // 显式走被装饰的 Repository，避免自调用递归
	saved, err := r.Repository.UpdateRoute(ctx, route) //nolint:staticcheck // 显式走被装饰的 Repository
	if err != nil {
		return saved, err
	}
	if r.applier != nil && saved.Host != "" {
		tid, saved := routeTenant(ctx, saved)
		if tid != "" {
			aerr := r.applier.Apply(ctx, tid, saved.Host)
			r.applyLog("update", saved.ID, saved.Host, aerr)
			r.recordApply(ctx, saved.ID, aerr)
			// Host 变更：旧 host 的聚合 Ingress 需重建（移除本 Route 的 path）。
			if old.Host != "" && old.Host != saved.Host {
				r.applyLog("update-old", saved.ID, old.Host, r.applier.Apply(ctx, tid, old.Host))
			}
		}
	}
	return saved, nil
}

// DeleteRoute 装饰：删成功后重建该 host 聚合 Ingress（无剩余则删整条）。
func (r *ApplyRepo) DeleteRoute(ctx context.Context, id string) error {
	// 取被删 Route 的 Host + tenant（删之前取，删后 GetRoute not found）。
	old, err := r.Repository.GetRoute(ctx, id) //nolint:staticcheck // 显式走被装饰的 Repository
	if err != nil {
		return err // not found 直接返，与裸 Repository 语义一致
	}
	if err := r.Repository.DeleteRoute(ctx, id); err != nil {
		return err
	}
	if r.applier != nil && old.Host != "" {
		tid, _ := routeTenant(ctx, old)
		if tid != "" {
			r.applyLog("delete", id, old.Host, r.applier.Delete(ctx, tid, old.Host))
		}
	}
	return nil
}

// DeleteService 装饰：store 的 DeleteService 会级联删该服务全部 Route（memory/PG 同款），
// 若不投影，这些 Route 所在 host 的聚合 Ingress 会残留指向已删服务的 path（流量持续
// 路由到已删除后端），直到同 host 其他 Route 变更才被动自愈。删前收集该服务 Route 的
// host 集合，删后逐 host 重建（按剩余 enabled Route 决定去留）。
func (r *ApplyRepo) DeleteService(ctx context.Context, id string) error {
	var hosts []string
	tid, _ := tenant.TenantFrom(ctx)
	if r.applier != nil && tid != "" {
		// r.Repository.ListRoutes 显式选择嵌入字段：本方法即 DeleteService 装饰器，
		// 简写 r.ListRoutes 若被覆写会自递归（与 workload.SetDomain 同款取舍）。
		if routes, lerr := r.Repository.ListRoutes(ctx, id); lerr == nil { //nolint:staticcheck // QF1008：显式走内层避免未来覆写自递归
			seen := map[string]bool{}
			for _, rt := range routes {
				if rt.Host != "" && !seen[rt.Host] {
					seen[rt.Host] = true
					hosts = append(hosts, rt.Host)
				}
			}
		}
	}
	if err := r.Repository.DeleteService(ctx, id); err != nil {
		return err
	}
	for _, host := range hosts {
		r.applyLog("delete-service", id, host, r.applier.Apply(ctx, tid, host))
	}
	return nil
}
