package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// TestK8sPodReaderListsByDataserviceLabel 验证按 paas.aitoys/dataservice label 查 Pod。
func TestK8sPodReaderListsByDataserviceLabel(t *testing.T) {
	scheme := newScheme(t)
	mkPod := func(name, dsID, ns string, ready bool, restarts int32, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns,
				Labels: map[string]string{labels.KeyDataservice: dsID, labels.KeyTenant: "t-acme"},
			},
			Spec: corev1.PodSpec{NodeName: "kb2"},
			Status: corev1.PodStatus{
				Phase: phase,
				PodIP: "10.0.0.1",
				ContainerStatuses: []corev1.ContainerStatus{
					{Ready: ready, RestartCount: restarts, Name: "main"},
				},
			},
		}
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mkPod("ds-pg-0", "ds-pg", "paas-t-acme", true, 0, corev1.PodRunning),
		mkPod("ds-pg-1", "ds-pg", "paas-t-acme", false, 2, corev1.PodRunning),
		mkPod("ds-other-0", "ds-other", "paas-t-acme", true, 0, corev1.PodRunning),
	).Build()

	r := NewK8sPodReader(cl)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	pods, err := r.Pods(ctx, "", "ds-pg")
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if len(pods) != 2 {
		t.Fatalf("ds-pg 应有 2 Pod，got %d", len(pods))
	}
	// 跨租户隔离：globex ns 查不到 acme 的 Pod。
	globexPods, _ := r.Pods(tenant.WithTenant(context.Background(), "t-globex"), "", "ds-pg")
	if len(globexPods) != 0 {
		t.Fatalf("跨租户应查不到 Pod，got %d", len(globexPods))
	}
}

// TestK8sPodReaderNilClientDegrades 验证集群外（nil client）降级返空不报错。
func TestK8sPodReaderNilClientDegrades(t *testing.T) {
	r := NewK8sPodReader(nil)
	pods, err := r.Pods(context.Background(), "", "ds-pg")
	if err != nil {
		t.Fatalf("nil client 应降级返空非报错: %v", err)
	}
	if len(pods) != 0 {
		t.Fatalf("nil client 应返空切片，got %d", len(pods))
	}
}
