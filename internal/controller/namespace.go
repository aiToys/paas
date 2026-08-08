package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aitoys/paas/pkg/tenant"
)

// EnsureNamespace 幂等创建数据面 namespace（tenant.Namespace(tid)），打 managed + tenant label。
// applier 写 namespace-scoped CRD 前调用：目标 ns 必须先存在，否则 CreateOrUpdate CRD 失败。
// 已存在（含非本系统创建的 ns）不覆盖、不报错，仅确保存在。
func EnsureNamespace(ctx context.Context, cl client.Client, tid string) error {
	ns := tenant.Namespace(tid)
	err := cl.Get(ctx, client.ObjectKey{Name: ns}, &corev1.Namespace{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return cl.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "paas",
				"paas.aitoys/tenant":           tid,
			},
		},
	})
}
