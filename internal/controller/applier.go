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
type K8sApplier struct {
	client.Client
	namespace string // CRD 落地 namespace（PAAS_K8S_NAMESPACE，默认 default）
}

// NewK8sApplier 创建 applier。namespace 为空则 default。
func NewK8sApplier(cl client.Client, namespace string) *K8sApplier {
	if namespace == "" {
		namespace = "default"
	}
	return &K8sApplier{Client: cl, namespace: namespace}
}

// Apply CreateOrUpdate Workload CRD（期望状态）。
func (a *K8sApplier) Apply(ctx context.Context, w workload.Workload) error {
	log.Printf("[applier] Apply w.ID=%s name=%s port=%d cport=%d tenant=%s img=%s", w.ID, w.Name, w.Port, w.ContainerPort, w.TenantID, w.Image)
	crd := &v1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: w.ID, Namespace: a.namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, a.Client, crd, func() error {
		crd.Spec = v1alpha1.WorkloadSpec{
			TenantID:      w.TenantID,
			AppID:         w.AppID,
			EnvID:         w.EnvID,
			LaneID:        w.LaneID,
			Type:          w.Type,
			Name:          w.Name,
			Image:         w.Image,
			ImageRef:      w.ImageRef,
			Replicas:      clampInt32(w.Replicas),
			Port:          clampInt32(w.Port),          // 端口投影，驱动 reconciler 建 Service + readiness probe
			ContainerPort: clampInt32(w.ContainerPort), // 0 时不建 Service（向后兼容）
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

// Delete 删 Workload CRD（级联清 K8s 资源）。
func (a *K8sApplier) Delete(ctx context.Context, id string) error {
	return a.Client.Delete(ctx, &v1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: a.namespace}})
}
