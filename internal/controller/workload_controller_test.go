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
	ctrl "sigs.k8s.io/controller-runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

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
		Spec:       v1alpha1.WorkloadSpec{Type: "service", Name: "wl-2", Image: "nginx", Replicas: 1},
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
		Spec:       v1alpha1.WorkloadSpec{Type: "service", Name: "wl-gpu", Image: "vllm/vllm", Replicas: 1, GPU: v1alpha1.GPURequest{Count: 1}},
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
		Spec:       v1alpha1.WorkloadSpec{Type: "cronjob", Name: "wl-cron", Image: "busybox", Schedule: "*/5 * * * *"},
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

// TestReconcileServiceCreatesService 验证 service 类型 + Port>0 时建 K8s Service（多微服务 DNS 互调前提）。
func TestReconcileServiceCreatesService(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-svc", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{Type: "service", Name: "wl-svc", Image: "nginx", Replicas: 1,
			Port: 80, ContainerPort: 8080},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-svc", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var svc corev1.Service
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-svc", Namespace: "default"}, &svc); err != nil {
		t.Fatalf("应创建 K8s Service: %v", err)
	}
	// selector 匹配 Pod label（数据面发现 + Deployment 选 Pod 双用）
	if svc.Spec.Selector["paas.aitoys/workload"] != "wl-svc" {
		t.Fatalf("Service selector 应匹配 paas.aitoys/workload=wl-svc，实际 %v", svc.Spec.Selector)
	}
	// 端口映射 Port→ContainerPort
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 {
		t.Fatalf("Service Port 应为 80，实际 %v", svc.Spec.Ports)
	}
	if svc.Spec.Ports[0].TargetPort.IntVal != 8080 {
		t.Fatalf("TargetPort 应为 8080，实际 %v", svc.Spec.Ports[0].TargetPort)
	}
	// OwnerRef 指 CR（删 CR 级联清 Service）
	if len(svc.GetOwnerReferences()) != 1 || svc.GetOwnerReferences()[0].Name != "wl-svc" {
		t.Fatalf("Service OwnerRef 应指向 Workload CR")
	}
	// Deployment Pod 模板应有 readiness probe（驱动 Endpoints ready，数据面发现真源）
	var dep appsv1.Deployment
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "wl-svc", Namespace: "default"}, &dep)
	c := dep.Spec.Template.Spec.Containers[0]
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 8080 {
		t.Fatalf("container 应声明 containerPort 8080，实际 %v", c.Ports)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.TCPSocket == nil {
		t.Fatalf("container 应有 TCP readiness probe")
	}
}

// TestReconcileServiceNoPortSkipsService 验证 Port=0 时不建 Service（向后兼容）。
func TestReconcileServiceNoPortSkipsService(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-noport", Namespace: "default"},
		Spec:       v1alpha1.WorkloadSpec{Type: "service", Name: "wl-noport", Image: "nginx", Replicas: 1},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-noport", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var svc corev1.Service
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-noport", Namespace: "default"}, &svc); err == nil {
		t.Fatalf("Port=0 不应建 Service")
	}
}

// TestReconcileJobNoService 验证 job 类型不建 Service。
func TestReconcileJobNoService(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-job", Namespace: "default"},
		Spec:       v1alpha1.WorkloadSpec{Type: "job", Name: "wl-job", Image: "busybox", Port: 80},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-job", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var svc corev1.Service
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-job", Namespace: "default"}, &svc); err == nil {
		t.Fatalf("job 类型不应建 Service")
	}
}
