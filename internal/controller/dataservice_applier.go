package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/dataservice"
)

// DataServiceK8sApplier 把 dataservice.DataService（领域）投影为 v1alpha1.DataService CRD（期望状态）。
// 实现 dataservice.Applier；由 ApplyRepo 在 PG/memory 写成功后调用。与 K8sApplier 同构。
type DataServiceK8sApplier struct {
	client.Client
	namespace string
}

// NewDataServiceK8sApplier 创建 applier。namespace 为空则 default。
func NewDataServiceK8sApplier(cl client.Client, namespace string) *DataServiceK8sApplier {
	if namespace == "" {
		namespace = "default"
	}
	return &DataServiceK8sApplier{Client: cl, namespace: namespace}
}

// Apply CreateOrUpdate DataService CRD（期望状态）。
// external 模式（接入外部实例）不部署 -> 不建 CRD，纯控制面记录连接信息。
func (a *DataServiceK8sApplier) Apply(ctx context.Context, d dataservice.DataService) error {
	if dataservice.IsExternal(d.Source) {
		return nil // external：平台不部署，连接信息仅存控制面供应用绑定注入
	}
	crd := &v1alpha1.DataService{ObjectMeta: metav1.ObjectMeta{Name: d.ID, Namespace: a.namespace}}
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
			r := int32(*d.Replicas)
			crd.Spec.Replicas = &r
		}
		crd.Spec.CPU = d.CPU
		crd.Spec.Memory = d.Memory
		crd.Spec.StorageGB = int32(d.StorageGB)
		crd.Spec.Image = d.Image
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply dataservice crd: %w", err)
	}
	return nil
}

// Delete 删 DataService CRD（级联清 K8s 资源）。external 模式无 CRD -> 忽略 NotFound。
func (a *DataServiceK8sApplier) Delete(ctx context.Context, id string) error {
	err := a.Client.Delete(ctx, &v1alpha1.DataService{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: a.namespace}})
	return client.IgnoreNotFound(err)
}
