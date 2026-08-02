package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

func TestDataServiceReconcileCreatesStatefulSet(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-1", Namespace: "default"},
		Spec:       v1alpha1.DataServiceSpec{TenantID: "t-acme", Kind: "db", Name: "ds-1", Engine: "postgres"},
	}
	cl := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(d).
		WithStatusSubresource(&v1alpha1.DataService{}).
		Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-1", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var sts appsv1.StatefulSet
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-1", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("应创建 StatefulSet: %v", err)
	}
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != "postgres:15-alpine" {
		t.Fatalf("postgres 引擎镜像应为 postgres:15-alpine，实得 %s", got)
	}
	// status 应回写 phase + image（ready=0 因 fake client 无 ReadyReplicas）。
	var got v1alpha1.DataService
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-1", Namespace: "default"}, &got)
	if got.Status.Phase != "creating" {
		t.Fatalf("ready=0 时 phase 应为 creating，实得 %s", got.Status.Phase)
	}
	if got.Status.Image != "postgres:15-alpine" {
		t.Fatalf("status.image 应回写落地镜像")
	}
}

func TestDataServiceReconcileUnknownEngineFailsPhase(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-x", Namespace: "default"},
		Spec:       v1alpha1.DataServiceSpec{Kind: "unknown-kind", Name: "ds-x", Engine: "nope"},
	}
	cl := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(d).
		WithStatusSubresource(&v1alpha1.DataService{}).
		Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-x", Namespace: "default"}}); err != nil {
		t.Fatalf("未知 kind 不应返错（应记 failed phase）: %v", err)
	}
	var got v1alpha1.DataService
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-x", Namespace: "default"}, &got)
	if got.Status.Phase != "failed" {
		t.Fatalf("未知 kind/engine 组合 phase 应为 failed，实得 %s", got.Status.Phase)
	}
}

func TestEngineImageCoverage(t *testing.T) {
	cases := map[string]string{
		"db|postgres": "postgres:15-alpine",
		"db|mysql":    "mysql:8",
		"cache|redis": "redis:7-alpine",
		"mq|nats":     "nats:2-alpine",
		"storage|":    "minio/minio:latest",
		"mq|kafka":    "", // 占位引擎返空 -> reconciler 走 failed 不拉起（H4/M5）
		"vector|":     "",
		"search|":     "",
	}
	for k, want := range cases {
		kind := k
		engine := ""
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				kind = k[:i]
				engine = k[i+1:]
				break
			}
		}
		if got := engineImage(kind, engine, ""); got != want {
			t.Errorf("engineImage(%q,%q)=%q, want %q", kind, engine, got, want)
		}
	}
}

// TestReconcileMySQLCreatesSecretServicesEnv 验证 mysql 真实落地：Secret+Svc+STS env 引用。
func TestReconcileMySQLCreatesSecretServicesEnv(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-mysql", Namespace: "default"},
		Spec: v1alpha1.DataServiceSpec{
			TenantID: "t-acme", Kind: "db", Name: "ds-mysql", Engine: "mysql",
			Connection: map[string]string{"password": "p4ss", "database": "appdb", "host": "h", "port": "3306"},
		},
	}
	cl := clientfake.NewClientBuilder().
		WithScheme(scheme).WithObjects(d).WithStatusSubresource(&v1alpha1.DataService{}).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-mysql", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	ctx := context.Background()
	// Secret 含 password/database，不含 host/port/uri
	var sec corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{Name: "ds-mysql-secret", Namespace: "default"}, &sec); err != nil {
		t.Fatalf("应创建 Secret: %v", err)
	}
	if sec.StringData["password"] != "p4ss" || sec.StringData["database"] != "appdb" {
		t.Fatalf("Secret 数据错误: %v", sec.StringData)
	}
	if _, ok := sec.StringData["host"]; ok {
		t.Fatalf("Secret 不应含 host（非敏感）")
	}
	// headless + ClusterIP Service
	var hl corev1.Service
	if err := cl.Get(ctx, types.NamespacedName{Name: "ds-mysql-headless", Namespace: "default"}, &hl); err != nil {
		t.Fatalf("应创建 headless Service: %v", err)
	}
	if hl.Spec.ClusterIP != "None" {
		t.Fatalf("headless Service ClusterIP 应为 None")
	}
	var svc corev1.Service
	if err := cl.Get(ctx, types.NamespacedName{Name: "ds-mysql", Namespace: "default"}, &svc); err != nil {
		t.Fatalf("应创建 ClusterIP Service: %v", err)
	}
	if svc.Spec.Ports[0].Port != 3306 {
		t.Fatalf("Service port 应 3306，got %d", svc.Spec.Ports[0].Port)
	}
	// STS env MYSQL_ROOT_PASSWORD secretKeyRef
	var sts appsv1.StatefulSet
	if err := cl.Get(ctx, types.NamespacedName{Name: "ds-mysql", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("应创建 StatefulSet: %v", err)
	}
	env := sts.Spec.Template.Spec.Containers[0].Env
	if env[0].Name != "MYSQL_ROOT_PASSWORD" || env[0].ValueFrom.SecretKeyRef.Name != "ds-mysql-secret" {
		t.Fatalf("MYSQL_ROOT_PASSWORD env 应引用 secret，got %+v", env[0])
	}
	if sts.Spec.ServiceName != "ds-mysql-headless" {
		t.Fatalf("STS ServiceName 应 ds-mysql-headless")
	}
	// OwnerRef（删 CR 级联清）
	if !hasOwner(sts.OwnerReferences, "DataService") {
		t.Fatalf("STS 应有 DataService OwnerRef")
	}
	// display-name 注解（用户名字，供 kubectl 辨认；annotation 不进 selector 故安全可变）
	if got := sts.Annotations[displayNameKey]; got != "ds-mysql" {
		t.Fatalf("STS display-name 注解应为 ds-mysql，got %q", got)
	}
	if got := sts.Spec.Template.Annotations[displayNameKey]; got != "ds-mysql" {
		t.Fatalf("STS template（Pod）display-name 注解应为 ds-mysql，got %q", got)
	}
	if got := sec.Annotations[displayNameKey]; got != "ds-mysql" {
		t.Fatalf("Secret display-name 注解应为 ds-mysql，got %q", got)
	}
	if got := svc.Annotations[displayNameKey]; got != "ds-mysql" {
		t.Fatalf("ClusterIP Service display-name 注解应为 ds-mysql，got %q", got)
	}
}

// TestReconcileSecretIdempotent 验证 Secret 已存在不覆盖（幂等：不重置密码）。
func TestReconcileSecretIdempotent(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-r", Namespace: "default"},
		Spec: v1alpha1.DataServiceSpec{Kind: "cache", Engine: "redis",
			Connection: map[string]string{"password": "new"}},
	}
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-r-secret", Namespace: "default"},
		StringData: map[string]string{"password": "orig"},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(d, existing).WithStatusSubresource(&v1alpha1.DataService{}).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-r", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var sec corev1.Secret
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-r-secret", Namespace: "default"}, &sec)
	if sec.StringData["password"] != "orig" {
		t.Fatalf("已存在 Secret 不应被覆盖，got %s", sec.StringData["password"])
	}
}

// TestReconcileRedisCommandRequirepass 验证 redis 命令注入 --requirepass。
func TestReconcileRedisCommandRequirepass(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-redis", Namespace: "default"},
		Spec:       v1alpha1.DataServiceSpec{Kind: "cache", Engine: "redis", Connection: map[string]string{"password": "x"}},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(d).WithStatusSubresource(&v1alpha1.DataService{}).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-redis", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var sts appsv1.StatefulSet
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-redis", Namespace: "default"}, &sts)
	cmd := sts.Spec.Template.Spec.Containers[0].Command
	if len(cmd) < 2 || cmd[0] != "redis-server" || cmd[1] != "--requirepass" {
		t.Fatalf("redis command 应含 --requirepass，got %v", cmd)
	}
}

// TestReconcileNATS 验证 nats 镜像 + -auth 参数。
func TestReconcileNATS(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-nats", Namespace: "default"},
		Spec:       v1alpha1.DataServiceSpec{Kind: "mq", Engine: "nats", Connection: map[string]string{"token": "tk"}},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(d).WithStatusSubresource(&v1alpha1.DataService{}).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-nats", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var sts appsv1.StatefulSet
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-nats", Namespace: "default"}, &sts)
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != "nats:2-alpine" {
		t.Fatalf("nats 镜像应为 nats:2-alpine，got %s", got)
	}
	args := sts.Spec.Template.Spec.Containers[0].Args
	if len(args) < 2 || args[0] != "-auth" {
		t.Fatalf("nats args 应含 -auth，got %v", args)
	}
}

// hasOwner 判断 OwnerReferences 是否含指定 kind。
func hasOwner(refs []metav1.OwnerReference, kind string) bool {
	for _, r := range refs {
		if r.Kind == kind {
			return true
		}
	}
	return false
}
