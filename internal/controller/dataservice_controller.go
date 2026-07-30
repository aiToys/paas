package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

// engineImage 按 Kind+Engine 选默认容器镜像（开源起步期占位，真实 Operator 留后续）。
// 未命中组合返回空串，reconciler 据此跳过落地（避免拉起未知镜像）。
func engineImage(kind, engine string) string {
	switch kind {
	case "db":
		switch engine {
		case "mysql":
			return "mysql:8"
		default:
			return "postgres:15-alpine"
		}
	case "cache":
		switch engine {
		case "valkey":
			return "valkey/valkey:7-alpine"
		default:
			return "redis:7-alpine"
		}
	case "mq":
		switch engine {
		case "rabbitmq":
			return "rabbitmq:3-management"
		case "rocketmq":
			return "apache/rocketmqclient:latest"
		default:
			return "bitnami/kafka:3.7"
		}
	case "storage":
		// 对象存储（MinIO 协议）；真实多节点集群留后续。
		return "minio/minio:latest"
	case "vector":
		switch engine {
		case "qdrant":
			return "qdrant/qdrant:latest"
		default:
			return "milvusdb/milvus:latest"
		}
	case "search":
		switch engine {
		case "opensearch":
			return "opensearchproject/opensearch:2"
		default:
			return "docker.elastic.co/elasticsearch/elasticsearch:8.13.0"
		}
	}
	return ""
}

// dataServiceLabels 返回 DataService 的 K8s 标签（含租户隔离）。
func dataServiceLabels(d *v1alpha1.DataService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "paas",
		"paas.aitoys/tenant":           d.Spec.TenantID,
		"paas.aitoys/dataservice":      d.Name,
		"paas.aitoys/kind":             d.Spec.Kind,
	}
}

// DataServiceReconciler watch DataService CRD，把期望状态落到 K8s StatefulSet 并回写 status。
// 数据服务有状态，统一落 StatefulSet（稳定网络标识 + 持久卷），与 Workload 落 Deployment 同构。
type DataServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile 期望→实际：取 CRD → 解析镜像 → 无镜像跳过 → CreateOrUpdate StatefulSet → 回写 status。
func (r *DataServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var d v1alpha1.DataService
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	image := engineImage(d.Spec.Kind, d.Spec.Engine)
	if image == "" {
		// 未知 Kind/Engine 组合：记 failed，不拉起未知镜像（安全默认）。
		d.Status.Phase = "failed"
		_ = r.Status().Update(ctx, &d)
		return ctrl.Result{}, nil
	}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: d.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		labels := dataServiceLabels(&d)
		sts.SetLabels(labels)
		replicas := int32(1) // 数据服务起步单副本（KISS）；集群/HA 留后续
		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = d.Name + "-headless"
		// Selector 创建后不可变：仅创建时设置。
		if sts.CreationTimestamp.IsZero() {
			sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		sts.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "main",
					Image: image,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
				}},
			},
		}
		// 让 StatefulSet 归属 CRD（删 CRD 级联清 StatefulSet）。
		return ctrl.SetControllerReference(&d, sts, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("apply dataservice statefulset: %w", err)
	}
	// 回写 status（从 StatefulSet 实际 ready 副本）。
	d.Status.Ready = sts.Status.ReadyReplicas
	d.Status.Image = image
	d.Status.Phase = "creating"
	if sts.Status.ReadyReplicas >= 1 {
		d.Status.Phase = "running"
	}
	if err := r.Status().Update(ctx, &d); err != nil {
		return ctrl.Result{}, fmt.Errorf("update dataservice status: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager 注册 reconciler 到 manager（watch DataService，own StatefulSet）。
func (r *DataServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DataService{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}
