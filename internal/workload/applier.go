package workload

import (
	"context"
	"fmt"
	"log"

	"github.com/aitoys/paas/pkg/tenant"
)

// Applier 把期望状态投影到数据面（K8s 启用时写 Workload CRD；nil 时 ApplyRepo 不调用）。
// 解耦控制面（PG/memory）与数据面（K8s CRD）：PG 作 API 查询源，CRD 作 reconcile 源。
type Applier interface {
	// Apply 把工作负载期望状态投影到数据面（CreateOrUpdate CRD）。
	Apply(ctx context.Context, w Workload) error
	// EnsureIfMissing 仅在 CRD 不存在时补建（drift 修复用），存在则跳过——避免把 PG 陈旧状态
	// （如裸 workload 空 image_ref/replicas=0）覆盖 K8s 既有运行态。
	// 返回 (created bool, err error)：true=本次补建，false=已存在跳过。
	EnsureIfMissing(ctx context.Context, w Workload) (bool, error)
	// Delete 从数据面移除（级联清 K8s 资源）。
	Delete(ctx context.Context, id string) error
}

// ApplyRepo 装饰 Repository：Create/Update/UpdateImage/Delete 成功后投影数据面。
// 数据面投影失败不阻塞控制面写（控制面真源优先），但记日志留可观测痕迹，
// 避免「控制面显示创建成功但 K8s 未建」的静默不一致。
type ApplyRepo struct {
	Repository
	applier Applier
}

// NewApplyRepo 包装 inner，在其写操作后调用 applier（applier 为 nil 时透传，无副作用）。
func NewApplyRepo(inner Repository, a Applier) *ApplyRepo {
	return &ApplyRepo{Repository: inner, applier: a}
}

// applyLog 数据面投影失败时记日志（不阻断控制面写）。applier 为 nil 时透传。
func (r *ApplyRepo) applyLog(op, id string, err error) {
	if err != nil {
		log.Printf("[workload-applier] 数据面投影失败 id=%s op=%s err=%v", id, op, err)
	}
}

func (r *ApplyRepo) Create(ctx context.Context, w Workload) error {
	if err := r.Repository.Create(ctx, w); err != nil {
		return err
	}
	if r.applier != nil {
		w = withTenant(ctx, w) // store 从 ctx 写 tenant，但 w 不回填；Apply 前补全 tenant 投影到 CRD label
		r.applyLog("create", w.ID, r.applier.Apply(ctx, w))
	}
	return nil
}

func (r *ApplyRepo) Update(ctx context.Context, id string, replicas int, status string) (Workload, error) {
	saved, err := r.Repository.Update(ctx, id, replicas, status)
	if err != nil {
		return saved, err
	}
	if r.applier != nil {
		saved = withTenant(ctx, saved)
		r.applyLog("update", saved.ID, r.applier.Apply(ctx, saved))
	}
	return saved, nil
}

func (r *ApplyRepo) UpdateImage(ctx context.Context, id, image, imageRef string) (Workload, error) {
	saved, err := r.Repository.UpdateImage(ctx, id, image, imageRef)
	if err != nil {
		return saved, err
	}
	if r.applier != nil {
		saved = withTenant(ctx, saved)
		r.applyLog("update-image", saved.ID, r.applier.Apply(ctx, saved))
	}
	return saved, nil
}

// SetResources 覆盖资源规格并投影数据面（reconciler 据 CRD spec.resources 更新容器 resources）。
func (r *ApplyRepo) SetResources(ctx context.Context, id string, res ResourceSpec) error {
	if err := r.Repository.SetResources(ctx, id, res); err != nil {
		return err
	}
	if r.applier != nil {
		if w, err := r.Repository.Get(ctx, id); err == nil { //nolint:staticcheck // QF1008:装饰器显式转发嵌入实现
			w = withTenant(ctx, w)
			r.applyLog("set-resources", w.ID, r.applier.Apply(ctx, w))
		}
	}
	return nil
}

// SetDomain 设置对外域名并投影数据面（reconciler 据 CRD spec.domain 更新/回收 Ingress）。
// 缺投影会让 canary 独立验证域名只写 PG、CRD spec.domain 不变——applyIngress 跳过，
// 验证入口 Ingress 永远不建（与 SetResources 同款模式）。
func (r *ApplyRepo) SetDomain(ctx context.Context, id string, domain string) error {
	// r.Repository.SetDomain 显式选择嵌入字段：本方法即 SetDomain 装饰器，
	// 简写 r.SetDomain 会无限自递归（QF1008 建议在此处不适用）。
	if err := r.Repository.SetDomain(ctx, id, domain); err != nil { //nolint:staticcheck // QF1008：装饰器须显式走内层
		return err
	}
	if r.applier != nil {
		if w, err := r.Repository.Get(ctx, id); err == nil { //nolint:staticcheck // QF1008:装饰器显式转发嵌入实现
			w = withTenant(ctx, w)
			r.applyLog("set-domain", w.ID, r.applier.Apply(ctx, w))
		}
	}
	return nil
}

// SetMetricsPath 覆盖指标端点路径并投影数据面（reconciler 据 CRD spec.metricsPath 增删
// prometheus.io/scrape 注解）。缺投影会只写 PG、CRD 不变——Pod 注解不更新，声明了也抓不到
// （与 SetDomain 同款模式）。
func (r *ApplyRepo) SetMetricsPath(ctx context.Context, id string, path string) error {
	// 显式走内层嵌入字段，避免自递归（同 SetDomain）。
	if err := r.Repository.SetMetricsPath(ctx, id, path); err != nil { //nolint:staticcheck // QF1008：装饰器须显式走内层
		return err
	}
	if r.applier != nil {
		if w, err := r.Repository.Get(ctx, id); err == nil { //nolint:staticcheck // QF1008:装饰器显式转发嵌入实现
			w = withTenant(ctx, w)
			r.applyLog("set-metrics-path", w.ID, r.applier.Apply(ctx, w))
		}
	}
	return nil
}

// SetRestartToken 写滚动重启令牌并投影数据面（CRD spec.restartToken → reconciler 注
// Pod template annotation → 滚动）。缺投影会只写 PG、CRD 不变——重启永不生效（与
// SetDomain/SetMetricsPath 同款模式）。空令牌拒绝（令牌只能被新值覆盖）。
func (r *ApplyRepo) SetRestartToken(ctx context.Context, id string, token string) error {
	if token == "" {
		return fmt.Errorf("restartToken 不可为空")
	}
	// 显式走内层嵌入字段，避免自递归（同 SetDomain）。
	if err := r.Repository.SetRestartToken(ctx, id, token); err != nil { //nolint:staticcheck // QF1008：装饰器须显式走内层
		return err
	}
	if r.applier != nil {
		if w, err := r.Repository.Get(ctx, id); err == nil { //nolint:staticcheck // QF1008：装饰器显式转发嵌入实现
			w = withTenant(ctx, w)
			r.applyLog("set-restart-token", w.ID, r.applier.Apply(ctx, w))
		}
	}
	return nil
}

// UpdateSchedule 修改 cron schedule 并投影数据面（reconciler 据 CRD spec.schedule 更新 K8s CronJob）。
func (r *ApplyRepo) UpdateSchedule(ctx context.Context, id, schedule string) (Workload, error) {
	saved, err := r.Repository.UpdateSchedule(ctx, id, schedule)
	if err != nil {
		return saved, err
	}
	if r.applier != nil {
		saved = withTenant(ctx, saved)
		r.applyLog("update-schedule", saved.ID, r.applier.Apply(ctx, saved))
	}
	return saved, nil
}

func (r *ApplyRepo) Delete(ctx context.Context, id string) error {
	if err := r.Repository.Delete(ctx, id); err != nil {
		return err
	}
	if r.applier != nil {
		if err := r.applier.Delete(ctx, id); err != nil {
			log.Printf("[workload-applier] 数据面删除投影失败 id=%s op=delete err=%v", id, err)
		}
	}
	return nil
}

// withTenant 补全 w.TenantID：store 从 ctx 写 tenant 但不回填传入的 w，
// Apply 投影 CRD 需 tenant 作 label（paas.aitoys/tenant），空值致 selector/label 缺失。
func withTenant(ctx context.Context, w Workload) Workload {
	if w.TenantID == "" {
		if tid, ok := tenant.TenantFrom(ctx); ok {
			w.TenantID = tid
		}
	}
	return w
}

// OnCreateRepo 在 Create 成功后触发回调（治理服务自动注册等增强动作）。
// 依赖倒置：workload 不感知 governance，cmd/core 注入回调；仅 service 类型触发
// （job/cronjob 无服务语义）。回调失败仅记日志、panic 不传播——注册属元数据
// 增强，绝不阻断创建主路径。回调 nil 时透传（无副作用）。
type OnCreateRepo struct {
	Repository
	OnCreate func(ctx context.Context, w Workload)
}

func (r *OnCreateRepo) Create(ctx context.Context, w Workload) error {
	if err := r.Repository.Create(ctx, w); err != nil {
		return err
	}
	if r.OnCreate == nil || w.Type != TypeService {
		return nil
	}
	func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("[workload-oncreate] 回调 panic recovered: %v", p)
			}
		}()
		r.OnCreate(ctx, w)
	}()
	return nil
}
