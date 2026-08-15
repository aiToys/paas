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
	insts, err := r.Instances(tenant.WithTenant(context.Background(), "t-acme"), "paas", "user-svc", "")
	if err != nil || len(insts) != 1 {
		t.Fatalf("同租户应返 1 实例，实际 %d err=%v", len(insts), err)
	}
	// 跨租户：返空不泄漏
	insts2, _ := r.Instances(tenant.WithTenant(context.Background(), "t-globex"), "paas", "user-svc", "")
	if len(insts2) != 0 {
		t.Fatalf("跨租户应返空（不泄漏），实际 %d", len(insts2))
	}
	// 无 tenant ctx：fail-closed 返空
	insts3, _ := r.Instances(context.Background(), "paas", "user-svc", "")
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

// TestK8sReaderLaneFallback 验证 L2 跨泳道降级发现（方案 A：Service 名派生 lane）：
//   - lane=feature-x 且 feature 泳道有 ready 实例 → 返 feature 实例（LaneID=feature-x）
//   - lane=feature-y（无对应 Endpoints）→ 降级返 default 基线（LaneID=default）
//   - lane 空或 default → 直接返 default 基线（向后兼容）
//   - 跨租户访问 feature 泳道 → 返空不泄漏（不降级到他人基线）
//
//nolint:staticcheck // corev1.Endpoints 在 K8s v0.36 仍主流；EndpointSlice 迁移留后续
func TestK8sReaderLaneFallback(t *testing.T) {
	const featureIP = "10.1.0.1"
	const defaultIP = "10.0.0.1"
	cs := fake.NewSimpleClientset(
		// default 基线：user-svc（t-acme）
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "user-svc", Namespace: "paas", Labels: map[string]string{
			"app.kubernetes.io/managed-by": "paas", "paas.aitoys/tenant": "t-acme",
		}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "user-svc", Namespace: "paas"},
			Subsets: []corev1.EndpointSubset{{
				Addresses: []corev1.EndpointAddress{{IP: defaultIP}},
				Ports:     []corev1.EndpointPort{{Port: 8080}},
			}}},
		// feature-x 泳道：user-svc-feature-x（t-acme）
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "user-svc-feature-x", Namespace: "paas", Labels: map[string]string{
			"app.kubernetes.io/managed-by": "paas", "paas.aitoys/tenant": "t-acme",
		}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "user-svc-feature-x", Namespace: "paas"},
			Subsets: []corev1.EndpointSubset{{
				Addresses: []corev1.EndpointAddress{{IP: featureIP}},
				Ports:     []corev1.EndpointPort{{Port: 8080}},
			}}},
		// feature-y 泳道：Service 存在但 Endpoints 无 ready addresses（未变更服务，应降级 default）
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "user-svc-feature-y", Namespace: "paas", Labels: map[string]string{
			"app.kubernetes.io/managed-by": "paas", "paas.aitoys/tenant": "t-acme",
		}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "user-svc-feature-y", Namespace: "paas"},
			Subsets: []corev1.EndpointSubset{{
				NotReadyAddresses: []corev1.EndpointAddress{{IP: "10.1.0.2"}},
				Ports:             []corev1.EndpointPort{{Port: 8080}},
			}}},
	)
	r := NewEndpointsReader(cs)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// lane=feature-x：返 feature 实例
	insts, err := r.Instances(ctx, "paas", "user-svc", "feature-x")
	if err != nil || len(insts) != 1 {
		t.Fatalf("lane=feature-x 应返 1 实例，实际 %d err=%v", len(insts), err)
	}
	if insts[0].IP != featureIP {
		t.Fatalf("lane=feature-x 应返 feature 实例 IP=%s，实际 %s", featureIP, insts[0].IP)
	}
	if insts[0].LaneID != "feature-x" {
		t.Fatalf("feature 实例 LaneID 应为 feature-x，实际 %q", insts[0].LaneID)
	}

	// lane=feature-y：Endpoints 存在但无 ready addresses → 降级 default
	insts2, _ := r.Instances(ctx, "paas", "user-svc", "feature-y")
	if len(insts2) != 1 || insts2[0].IP != defaultIP {
		t.Fatalf("lane=feature-y 应降级返 default 实例 IP=%s，实际 %+v", defaultIP, insts2)
	}
	if insts2[0].LaneID != LaneDefault {
		t.Fatalf("降级 default 实例 LaneID 应为 default，实际 %q", insts2[0].LaneID)
	}

	// lane 未创建（feature-z）：Service/Endpoints 都不存在 → 降级 default
	insts3, _ := r.Instances(ctx, "paas", "user-svc", "feature-z")
	if len(insts3) != 1 || insts3[0].IP != defaultIP {
		t.Fatalf("lane=feature-z（不存在）应降级返 default，实际 %+v", insts3)
	}

	// lane 空：直接返 default（向后兼容）
	insts4, _ := r.Instances(ctx, "paas", "user-svc", "")
	if len(insts4) != 1 || insts4[0].IP != defaultIP {
		t.Fatalf("lane 空应返 default 基线，实际 %+v", insts4)
	}

	// lane=default：直接返 default（显式基线）
	insts5, _ := r.Instances(ctx, "paas", "user-svc", LaneDefault)
	if len(insts5) != 1 || insts5[0].IP != defaultIP {
		t.Fatalf("lane=default 应返 default 基线，实际 %+v", insts5)
	}

	// 跨租户访问 feature 泳道：Service label 不匹配 → 返空（不降级到他人 default，防泄漏）
	ctxGlobex := tenant.WithTenant(context.Background(), "t-globex")
	insts6, _ := r.Instances(ctxGlobex, "paas", "user-svc", "feature-x")
	if len(insts6) != 0 {
		t.Fatalf("跨租户访问应返空（不泄漏/不降级到他人 default），实际 %d", len(insts6))
	}
}
