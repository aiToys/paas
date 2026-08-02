package dataplane

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aitoys/paas/pkg/tenant"
)

// TestK8sReaderTenantIsolation 验证 reader 按租户隔离：同租户返实例，跨租户/无 ctx 返空（不泄漏）。
//
//nolint:staticcheck // corev1.Endpoints 在 K8s v0.36 仍主流；EndpointSlice 迁移留后续
func TestK8sReaderTenantIsolation(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "user-svc", Namespace: "paas", Labels: map[string]string{
			"app.kubernetes.io/managed-by": "paas", "paas.aitoys/tenant": "t-acme",
		}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "user-svc", Namespace: "paas"},
			Subsets: []corev1.EndpointSubset{{
				Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}},
				Ports:     []corev1.EndpointPort{{Port: 8080}},
			}}},
	)
	r := NewEndpointsReader(cs)

	// 同租户：返实例
	insts, err := r.Instances(tenant.WithTenant(context.Background(), "t-acme"), "paas", "user-svc")
	if err != nil || len(insts) != 1 {
		t.Fatalf("同租户应返 1 实例，实际 %d err=%v", len(insts), err)
	}
	// 跨租户：返空不泄漏
	insts2, _ := r.Instances(tenant.WithTenant(context.Background(), "t-globex"), "paas", "user-svc")
	if len(insts2) != 0 {
		t.Fatalf("跨租户应返空（不泄漏），实际 %d", len(insts2))
	}
	// 无 tenant ctx：fail-closed 返空
	insts3, _ := r.Instances(context.Background(), "paas", "user-svc")
	if len(insts3) != 0 {
		t.Fatalf("无 tenant ctx 应 fail-closed 返空，实际 %d", len(insts3))
	}

	// Services 列表同样按租户隔离
	svcsA, _ := r.Services(tenant.WithTenant(context.Background(), "t-acme"), "paas")
	if len(svcsA) != 1 {
		t.Fatalf("同租户应列 1 服务，实际 %d", len(svcsA))
	}
	svcsB, _ := r.Services(tenant.WithTenant(context.Background(), "t-globex"), "paas")
	if len(svcsB) != 0 {
		t.Fatalf("跨租户应列 0 服务，实际 %d", len(svcsB))
	}
}
