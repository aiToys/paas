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

// engineOrDefault 取 spec.engine，缺省按 Kind 给默认（与 KindMeta.Default 对齐）。
func engineOrDefault(d dataservice.DataService) string {
	if e, ok := d.Spec["engine"]; ok && e != "" {
		return e
	}
	switch d.Kind {
	case dataservice.KindDB:
		return "postgres"
	case dataservice.KindCache:
		return "redis"
	case dataservice.KindMQ:
		return "kafka"
	case dataservice.KindStorage:
		return "minio"
	case dataservice.KindVector:
		return "milvus"
	case dataservice.KindSearch:
		return "elasticsearch"
	}
	return ""
}

// Apply CreateOrUpdate DataService CRD（期望状态）。
func (a *DataServiceK8sApplier) Apply(ctx context.Context, d dataservice.DataService) error {
	crd := &v1alpha1.DataService{ObjectMeta: metav1.ObjectMeta{Name: d.ID, Namespace: a.namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, a.Client, crd, func() error {
		crd.Spec = v1alpha1.DataServiceSpec{
			TenantID: d.TenantID,
			AppID:    d.AppID,
			EnvID:    d.EnvID,
			Kind:     d.Kind,
			Name:     d.Name,
			Engine:   engineOrDefault(d),
			Spec:     d.Spec,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply dataservice crd: %w", err)
	}
	return nil
}

// Delete 删 DataService CRD（级联清 K8s 资源）。
func (a *DataServiceK8sApplier) Delete(ctx context.Context, id string) error {
	return a.Client.Delete(ctx, &v1alpha1.DataService{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: a.namespace}})
}
