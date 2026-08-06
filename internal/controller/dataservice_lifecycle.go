package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/aitoys/paas/pkg/labels"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// restartedAtKey 是触发 STS 滚动重建的 annotation key。
// 写入 Pod template annotation，值变化时 StatefulSet controller 重建 Pod（K8s 原生滚动机制）。
const restartedAtKey = labels.KeyRestartedAt

// DSRestarter 实现 dataservice.Restarter：patch 数据服务对应 StatefulSet 的 Pod template
// annotation（restarted-at=<nonce>），触发滚动重建。STS 名 = 数据服务 ID（与 reconciler 一致）。
//
// 实例名（STS）由 reconciler 用 DataService CRD 名（= 数据服务 ID），故 Restart(id) 直接定位 STS。
// namespace 取注入值（PAAS_K8S_NAMESPACE），与 reconciler/applier 同源。
type DSRestarter struct {
	client.Client
	namespace string
}

// NewDSRestarter 创建重启控制器。namespace 为空则 default（与 NewDataServiceK8sApplier 一致）。
func NewDSRestarter(cl client.Client, namespace string) *DSRestarter {
	if namespace == "" {
		namespace = "default"
	}
	return &DSRestarter{Client: cl, namespace: namespace}
}

// Restart patch STS template annotation 触发 Pod 滚动重建。
// 用 GenerateOnce 用作 nonce（reconciler 进程内单调，避免 Date.now 依赖；每次调用递增保证变化）。
// STS 不存在 -> 明确错误（reconciler 尚未建，调用方收 500 提示先创建/启动）。
func (r *DSRestarter) Restart(ctx context.Context, id string) error {
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: r.namespace}}
	patchBase := client.MergeFrom(sts.DeepCopy())
	if sts.ObjectMeta.Annotations == nil {
		sts.ObjectMeta.Annotations = map[string]string{}
	}
	if sts.Spec.Template.ObjectMeta.Annotations == nil {
		sts.Spec.Template.ObjectMeta.Annotations = map[string]string{}
	}
	nonce := time.Now().UTC().Format(time.RFC3339Nano)
	sts.Spec.Template.ObjectMeta.Annotations[restartedAtKey] = nonce
	if err := r.Patch(ctx, sts, patchBase); err != nil {
		return fmt.Errorf("patch statefulset %s for restart: %w", id, err)
	}
	return nil
}
