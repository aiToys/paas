package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReconcileCreatesDeployment(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-1", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{TenantID: "t-acme", AppID: "app-cs", Type: "service",
			Name: "wl-1", Image: "nginx", Replicas: 3},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-1", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var dep appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-1", Namespace: "default"}, &dep); err != nil {
		t.Fatalf("应创建 Deployment: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 3 {
		t.Fatalf("replicas 应为 3")
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "nginx" {
		t.Fatalf("镜像应为 nginx")
	}
}

func TestReconcileIdempotent(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-2", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{Type: "service", Name: "wl-2", Image: "nginx", Replicas: 1},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	nn := types.NamespacedName{Name: "wl-2", Namespace: "default"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("首次 reconcile 失败: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("二次 reconcile 应幂等: %v", err)
	}
}

func TestReconcileGPUAntiAffinity(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-gpu", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{Type: "service", Name: "wl-gpu", Image: "vllm/vllm", Replicas: 1, GPU: v1alpha1.GPURequest{Count: 1}},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-gpu", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var dep appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-gpu", Namespace: "default"}, &dep); err != nil {
		t.Fatalf("应创建 Deployment: %v", err)
	}
	if _, ok := dep.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]; !ok {
		t.Fatalf("GPU 工作负载应声明 nvidia.com/gpu limit")
	}
	if dep.Spec.Template.Spec.Affinity == nil || dep.Spec.Template.Spec.Affinity.PodAntiAffinity == nil {
		t.Fatalf("GPU 工作负载应配置 podAntiAffinity")
	}
}

func TestReconcileCronJob(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-cron", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{Type: "cronjob", Name: "wl-cron", Image: "busybox", Schedule: "*/5 * * * *"},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-cron", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile cronjob 失败: %v", err)
	}
	var cj batchv1.CronJob
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-cron", Namespace: "default"}, &cj); err != nil {
		t.Fatalf("应创建 CronJob: %v", err)
	}
	if cj.Spec.Schedule != "*/5 * * * *" {
		t.Fatalf("schedule 不符: %s", cj.Spec.Schedule)
	}
}
