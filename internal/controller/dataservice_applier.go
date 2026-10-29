package controller

import (
	"context"
	"fmt"
	"log"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/tenant"
)

// DataServiceK8sApplier 把 dataservice.DataService（领域）投影为 v1alpha1.DataService CRD（期望状态）。
// 实现 dataservice.Applier；由 ApplyRepo 在 PG/memory 写成功后调用。与 K8sApplier 同构。
// 落地 namespace 按租户派生（paas-<tenantID>），写 CRD 前 EnsureNamespace。
type DataServiceK8sApplier struct {
	client.Client
}

// NewDataServiceK8sApplier 创建 applier。ns 按租户派生（保留 namespace 参数向后兼容，未使用）。
func NewDataServiceK8sApplier(cl client.Client, _ ...string) *DataServiceK8sApplier {
	return &DataServiceK8sApplier{Client: cl}
}

// Apply CreateOrUpdate DataService CRD（期望状态）。
// external 模式（接入外部实例）不部署 -> 不建 CRD，纯控制面记录连接信息。
func (a *DataServiceK8sApplier) Apply(ctx context.Context, d dataservice.DataService) error {
	if dataservice.IsExternal(d.Source) {
		return nil // external：平台不部署，连接信息仅存控制面供应用绑定注入
	}
	if d.TenantID == "" {
		return fmt.Errorf("dataservice tenantID 为空，无法派生数据面 namespace")
	}
	if err := EnsureNamespace(ctx, a.Client, d.TenantID); err != nil {
		return fmt.Errorf("ensure namespace: %w", err)
	}
	ns := tenant.Namespace(d.TenantID)
	crd := &v1alpha1.DataService{ObjectMeta: metav1.ObjectMeta{Name: d.ID, Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, a.Client, crd, func() error {
		crd.Spec = v1alpha1.DataServiceSpec{
			TenantID:   d.TenantID,
			AppID:      d.AppID,
			EnvID:      d.EnvID,
			Kind:       d.Kind,
			Name:       d.Name,
			Engine:     dataservice.EngineOf(d), // 复用领域真源（DRY，与 FillConnection 一致）
			Source:     d.Source,
			Spec:       d.Spec,
			Connection: d.Connection, // 含 host/port/credentials/uri；reconciler 据此建 Secret+Svc+STS
		}
		// 实例浅管理字段投影（nil/零值时 reconciler 用默认）。
		if d.Replicas != nil {
			r := int32(*d.Replicas) //nolint:gosec // int32 是 CRD 字段类型，值域用户配置小整数
			crd.Spec.Replicas = &r
		}
		crd.Spec.CPU = d.CPU
		crd.Spec.Memory = d.Memory
		crd.Spec.StorageGB = int32(d.StorageGB) //nolint:gosec // int32 是 CRD 字段类型，值域 GB 级小整数
		crd.Spec.Image = d.Image
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply dataservice crd: %w", err)
	}
	return nil
}

// EnsureCR 幽灵自愈（遗留债务「DB 有行无 CR 无恢复路径」2026-10-29 再次发作后补）：
// CR 存在则跳过（防覆盖运行态——与 workload drift EnsureIfMissing 同语义，周期全量 Apply
// 会引发 STS 意外滚动），缺失则投影重建（reconciler 据此重建 Secret/Svc/STS）。
// 返回是否发生了重建。external / 无租户与 Apply 同规则跳过。
func (a *DataServiceK8sApplier) EnsureCR(ctx context.Context, d dataservice.DataService) (bool, error) {
	if dataservice.IsExternal(d.Source) || d.TenantID == "" {
		return false, nil
	}
	ns := tenant.Namespace(d.TenantID)
	err := a.Get(ctx, client.ObjectKey{Namespace: ns, Name: d.ID}, &v1alpha1.DataService{})
	if err == nil {
		return false, nil // CR 在：不覆盖
	}
	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get dataservice crd: %w", err)
	}
	if err := a.Apply(ctx, d); err != nil {
		return false, err
	}
	return true, nil
}

// Delete 删 DataService CRD（级联清 K8s 资源）。ns 从 ctx tenant 派生（与 Apply 同源）。
// external 模式无 CRD -> 忽略 NotFound。ctx 无 tenant（异常）记日志跳过（K8sApplier.Delete 同款）。
func (a *DataServiceK8sApplier) Delete(ctx context.Context, id string) error {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		log.Printf("[ds-applier] Delete 跳过（ctx 无 tenant）id=%s", id)
		return nil
	}
	ns := tenant.Namespace(tid)
	return client.IgnoreNotFound(a.Client.Delete(ctx, &v1alpha1.DataService{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: ns}}))
}
