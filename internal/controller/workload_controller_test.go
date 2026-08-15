package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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
	if err := networkingv1.AddToScheme(s); err != nil {
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

// OtelEndpoint 注入 service 类型 Pod env，与 DPToken 独立（应用不接数据面也应有 trace）。
func TestReconcileInjectsOtelEndpoint(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-otel", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{TenantID: "t-acme", AppID: "app-cs", Type: "service",
			Name: "wl-otel", Image: "nginx", Replicas: 1},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	// OtelEndpoint 配了 DPToken 没配：验证独立注入，只应有 OTEL env。
	r := &WorkloadReconciler{Client: cl, Scheme: scheme, OtelEndpoint: "jaeger.observability.svc:4318"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-otel", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var dep appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-otel", Namespace: "default"}, &dep); err != nil {
		t.Fatalf("应创建 Deployment: %v", err)
	}
	var otel, dpToken string
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		switch e.Name {
		case "PAAS_OTEL_ENDPOINT":
			otel = e.Value
		case "PAAS_DP_TOKEN":
			dpToken = e.Value
		}
	}
	if otel != "jaeger.observability.svc:4318" {
		t.Fatalf("PAAS_OTEL_ENDPOINT 应注入，got %q", otel)
	}
	if dpToken != "" {
		t.Fatalf("DPToken 未配不应注入，got %q", dpToken)
	}
}

// OtelEndpoint 空时不注入（未配 OTEL 后端，应用 observ.Init noop 功能不受影响）。
func TestReconcileNoOtelEndpointWhenEmpty(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-no-otel", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{TenantID: "t-acme", AppID: "app-cs", Type: "service",
			Name: "wl-no-otel", Image: "nginx", Replicas: 1},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-no-otel", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var dep appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-no-otel", Namespace: "default"}, &dep); err != nil {
		t.Fatalf("应创建 Deployment: %v", err)
	}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "PAAS_OTEL_ENDPOINT" {
			t.Fatalf("OtelEndpoint 空时不应注入，got %q", e.Value)
		}
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
	// CronJob 衍生 Job 的 Pod template 必须显式 restartPolicy=Never
	//（默认 Always 会被 apiserver 拒绝，致 CronJob 永远无法调度 Job）。
	if rp := cj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy; rp != corev1.RestartPolicyNever {
		t.Fatalf("CronJob Pod restartPolicy 应为 Never，实际 %s", rp)
	}
	// 同 applyJob：衍生 Job 失败一次即终止（BackoffLimit=0）+ 1 天后 GC，防永久残留。
	if bl := cj.Spec.JobTemplate.Spec.BackoffLimit; bl == nil || *bl != 0 {
		t.Fatalf("CronJob BackoffLimit 应为 0，实际 %v", bl)
	}
	if ttl := cj.Spec.JobTemplate.Spec.TTLSecondsAfterFinished; ttl == nil || *ttl != 86400 {
		t.Fatalf("CronJob TTLSecondsAfterFinished 应为 86400，实际 %v", ttl)
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
	// service + port>0 应注入 Prometheus 自动发现注解（抓业务端口 /metrics，应用级 RED 指标数据源）
	ann := dep.Spec.Template.Annotations
	if ann["prometheus.io/scrape"] != "true" || ann["prometheus.io/port"] != "8080" || ann["prometheus.io/path"] != "/metrics" {
		t.Fatalf("service Pod 应注 prometheus.io/scrape|port|path 注解, 实际 %v", ann)
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
	// port=0 不注 prometheus 注解（无业务端口可抓）
	var dep appsv1.Deployment
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "wl-noport", Namespace: "default"}, &dep)
	if a := dep.Spec.Template.Annotations; a != nil && a["prometheus.io/scrape"] == "true" {
		t.Fatalf("Port=0 不应注 prometheus.io/scrape, 实际 %v", a)
	}
}

// TestReconcileServiceWithDomainCreatesIngress 验证 service + Port>0 + Domain 非空时建 Ingress（应用域名->自动暴露）。
func TestReconcileServiceWithDomainCreatesIngress(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-svc", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{Type: "service", Name: "wl-svc", Image: "nginx", Replicas: 1,
			Port: 80, ContainerPort: 8080, Domain: "shop.example.com"},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme, IngressClass: "hermes"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-svc", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var ing networkingv1.Ingress
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-svc", Namespace: "default"}, &ing); err != nil {
		t.Fatalf("应创建 Ingress: %v", err)
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "hermes" {
		t.Fatalf("ingressClassName 应为 hermes，实际 %v", ing.Spec.IngressClassName)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "shop.example.com" {
		t.Fatalf("Ingress host 应为 shop.example.com，实际 %v", ing.Spec.Rules)
	}
	path := ing.Spec.Rules[0].HTTP.Paths[0]
	if path.Path != "/" {
		t.Fatalf("Ingress path 应为 /，实际 %s", path.Path)
	}
	if path.PathType == nil || *path.PathType != networkingv1.PathTypePrefix {
		t.Fatalf("pathType 应为 Prefix，实际 %v", path.PathType)
	}
	if path.Backend.Service == nil || path.Backend.Service.Name != "wl-svc" || path.Backend.Service.Port.Number != 80 {
		t.Fatalf("backend 应指向 wl-svc:80，实际 %v", path.Backend.Service)
	}
	if len(ing.GetOwnerReferences()) != 1 || ing.GetOwnerReferences()[0].Name != "wl-svc" {
		t.Fatalf("Ingress OwnerRef 应指向 Workload CR")
	}
}

// TestReconcileServiceNoDomainSkipsIngress 验证 Domain 空时不建 Ingress（仅集群内 DNS 可达）。
func TestReconcileServiceNoDomainSkipsIngress(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-nohost", Namespace: "default"},
		Spec:       v1alpha1.WorkloadSpec{Type: "service", Name: "wl-nohost", Image: "nginx", Replicas: 1, Port: 80},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme, IngressClass: "hermes"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-nohost", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var ing networkingv1.Ingress
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-nohost", Namespace: "default"}, &ing); err == nil {
		t.Fatalf("Domain 空不应建 Ingress")
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
	// Job Pod template 必须显式 restartPolicy=Never（默认 Always 被 apiserver 拒绝）。
	var jb batchv1.Job
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-job", Namespace: "default"}, &jb); err != nil {
		t.Fatalf("应创建 Job: %v", err)
	}
	if rp := jb.Spec.Template.Spec.RestartPolicy; rp != corev1.RestartPolicyNever {
		t.Fatalf("Job Pod restartPolicy 应为 Never，实际 %s", rp)
	}
}

// TestReconcileLabelsLane 验证 Workload 的 lane label（L2）：Spec.LaneID 空时打 default，
// feature 泳道 Workload 打对应 lane。前端/governance 按 lane 分组用。
func TestReconcileLabelsLane(t *testing.T) {
	scheme := newScheme(t)
	// 基线 Workload（无 LaneID）→ default label
	base := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-base", Namespace: "default"},
		Spec:       v1alpha1.WorkloadSpec{TenantID: "t-acme", AppID: "app-cs", Type: "service", Name: "wl-base", Image: "nginx", Replicas: 1},
	}
	// feature-x 泳道 Workload → feature-x label
	feat := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-feat", Namespace: "default"},
		Spec:       v1alpha1.WorkloadSpec{TenantID: "t-acme", AppID: "app-cs", Type: "service", Name: "wl-feat", Image: "nginx", Replicas: 1, LaneID: "feature-x"},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(base, feat).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	for _, name := range []string{"wl-base", "wl-feat"} {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}); err != nil {
			t.Fatalf("reconcile %s 失败: %v", name, err)
		}
	}
	// 基线 Deployment label = default
	var depBase appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-base", Namespace: "default"}, &depBase); err != nil {
		t.Fatalf("应创建基线 Deployment: %v", err)
	}
	if got := depBase.Labels["paas.aitoys/lane"]; got != "default" {
		t.Fatalf("基线 lane label 应 default，got %q", got)
	}
	if got := depBase.Spec.Template.Labels["paas.aitoys/lane"]; got != "default" {
		t.Fatalf("基线 Pod 模板 lane label 应 default，got %q", got)
	}
	// feature Deployment label = feature-x
	var depFeat appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-feat", Namespace: "default"}, &depFeat); err != nil {
		t.Fatalf("应创建 feature Deployment: %v", err)
	}
	if got := depFeat.Labels["paas.aitoys/lane"]; got != "feature-x" {
		t.Fatalf("feature lane label 应 feature-x，got %q", got)
	}
}
