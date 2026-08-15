package controller

import (
	"context"
	"fmt"
	"log"
	"math"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// clampInt32 把领域 int 安全收敛到 K8s int32（防负值/溢出，gosec G115）。
// 领域 Validate 已约束范围，此处作数据面边界防御性兜底。通用：副本数/端口均经此。
func clampInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}

// K8sApplier 把 workload.Workload（领域）投影为 v1alpha1.Workload CRD（期望状态）。
// 实现 workload.Applier；由 ApplyRepo 在 PG 写成功后调用。
//
// 落地 namespace 按租户派生（paas-<tenantID>，见 tenant.Namespace），不再用固定 ns——
// 控制面/数据面 + 租户级 ns 隔离。写 CRD 前 EnsureNamespace 确保目标 ns 存在。
type K8sApplier struct {
	client.Client
}

// NewK8sApplier 创建 applier。ns 按租户派生，无需注入固定 namespace（保留参数向后兼容，未使用）。
func NewK8sApplier(cl client.Client, _ ...string) *K8sApplier {
	return &K8sApplier{Client: cl}
}

// Apply CreateOrUpdate Workload CRD（期望状态）。
func (a *K8sApplier) Apply(ctx context.Context, w workload.Workload) error {
	if w.TenantID == "" {
		return fmt.Errorf("workload tenantID 为空，无法派生数据面 namespace")
	}
	if err := EnsureNamespace(ctx, a.Client, w.TenantID); err != nil {
		return fmt.Errorf("ensure namespace: %w", err)
	}
	ns := tenant.Namespace(w.TenantID)
	log.Printf("[applier] Apply w.ID=%s ns=%s name=%s port=%d cport=%d domain=%s tenant=%s img=%s", w.ID, ns, w.Name, w.Port, w.ContainerPort, w.Domain, w.TenantID, w.Image)
	crd := &v1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: w.ID, Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, a.Client, crd, func() error {
		crd.Spec = v1alpha1.WorkloadSpec{
			TenantID:      w.TenantID,
			AppID:         w.AppID,
			EnvID:         w.EnvID,
			LaneID:        w.LaneID,
			Service:       w.Service,
			Type:          w.Type,
			Name:          w.Name,
			Image:         w.Image,
			ImageRef:      w.ImageRef,
			Replicas:      clampInt32(w.Replicas),
			Port:          clampInt32(w.Port),          // 端口投影，驱动 reconciler 建 Service + readiness probe
			ContainerPort: clampInt32(w.ContainerPort), // 0 时不建 Service（向后兼容）
			Domain:        w.Domain,                    // 域名投影，驱动 reconciler 建 Ingress（host -> Service:Port）
			Schedule:      w.Schedule,
			Command:       w.Command,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply workload crd: %w", err)
	}
	return nil
}

// EnsureIfMissing 仅在 CRD 不存在时补建（drift 修复用），存在则跳过。
//
// 与 Apply 的区别（关键）：Apply 是 CreateOrUpdate，会用 PG 当前状态覆盖既有 CRD spec。
// drift 修复场景下 PG 可能是陈旧的（裸 workload 空 image_ref / replicas=0），Apply 会把
// 已有运行态（如 Release 编排出来的带 image 的 Deployment）回退成陈旧状态——典型副作用：
// service 被缩到 0 副本。EnsureIfMissing 用 client.Get 先判存在，存在即返 (false,nil) 不动，
// 仅 CRD 缺失时才 Apply 补建，避免覆盖 K8s 既有运行态。
//
// 返回 (created bool, err error)：true=本次补建，false=已存在跳过。
func (a *K8sApplier) EnsureIfMissing(ctx context.Context, w workload.Workload) (bool, error) {
	if w.TenantID == "" {
		return false, fmt.Errorf("workload tenantID 为空，无法派生数据面 namespace")
	}
	ns := tenant.Namespace(w.TenantID)
	var existing v1alpha1.Workload
	err := a.Get(ctx, client.ObjectKey{Name: w.ID, Namespace: ns}, &existing)
	if err == nil {
		// CRD 已存在：跳过，绝不覆盖既有运行态
		return false, nil
	}
	// NotFound 才补建；其余真实错误（权限/连接）上抛，drift 修复记日志跳过该条
	if realErr := client.IgnoreNotFound(err); realErr != nil {
		return false, fmt.Errorf("get workload crd: %w", realErr)
	}
	// NotFound：CRD 缺失，补建
	if err := a.Apply(ctx, w); err != nil {
		return false, err
	}
	return true, nil
}

// Delete 删 Workload CRD（级联清 K8s 资源）。ns 从 ctx tenant 派生（与 Apply 同源）。
// ctx 无 tenant（异常）记日志跳过——控制面 Delete 已成功，CRD 孤儿风险低（正常路径 ctx 均含 tenant：
// 用户操作经身份 ctx，admin 跨租户删经 tenant.WithTenant 派生目标租户 ctx）。
func (a *K8sApplier) Delete(ctx context.Context, id string) error {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		log.Printf("[applier] Delete 跳过（ctx 无 tenant）id=%s", id)
		return nil
	}
	ns := tenant.Namespace(tid)
	return client.IgnoreNotFound(a.Client.Delete(ctx, &v1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: ns}}))
}
