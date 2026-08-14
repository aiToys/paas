package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// TestEngineImageLightEngines 验证 qdrant/meilisearch 返非空镜像（本轮替换 milvus/es 占位）。
func TestEngineImageLightEngines(t *testing.T) {
	cases := map[string]string{
		"vector|qdrant":      "qdrant/qdrant:v1.12.4",
		"search|meilisearch": "meilisearch/meilisearch:v1.10",
		"vector|milvus":      "", // 已弃用重型引擎返空 -> reconciler 走 failed
		"search|elasticsearch": "",
	}
	for k, want := range cases {
		kind, engine := k, ""
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				kind, engine = k[:i], k[i+1:]
				break
			}
		}
		if got := engineImage(kind, engine, ""); got != want {
			t.Errorf("engineImage(%q,%q)=%q, want %q", kind, engine, got, want)
		}
	}
	// registry 非空时内网化（library/<name>:<tag>，去 repo 前缀）。
	if got := engineImage("vector", "qdrant", "hub.wang.dd:5000"); got != "hub.wang.dd:5000/library/qdrant:v1.12.4" {
		t.Errorf("qdrant registry 内网化错误: %s", got)
	}
}

// TestReconcileQdrant 验证 qdrant 真实落地：镜像 + env QDRANT__SERVICE_API_KEY + PVC 持久化。
func TestReconcileQdrant(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-qdrant", Namespace: "default"},
		Spec: v1alpha1.DataServiceSpec{
			TenantID: "t-acme", Kind: "vector", Engine: "qdrant", Name: "ds-qdrant",
			StorageGB: 20,
			Connection: map[string]string{"api_key": "ak-xxx"},
		},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(d).WithStatusSubresource(&v1alpha1.DataService{}).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-qdrant", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	ctx := context.Background()
	var sts appsv1.StatefulSet
	if err := cl.Get(ctx, types.NamespacedName{Name: "ds-qdrant", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("应创建 StatefulSet: %v", err)
	}
	c := sts.Spec.Template.Spec.Containers[0]
	if c.Image != "qdrant/qdrant:v1.12.4" {
		t.Fatalf("qdrant 镜像错误: %s", c.Image)
	}
	if c.Ports[0].ContainerPort != 6333 {
		t.Fatalf("qdrant 端口应 6333, got %d", c.Ports[0].ContainerPort)
	}
	// env QDRANT__SERVICE_API_KEY 引用 secret
	var foundEnv bool
	for _, e := range c.Env {
		if e.Name == "QDRANT__SERVICE_API_KEY" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef.Name == "ds-qdrant-secret" {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Fatalf("qdrant 缺 QDRANT__SERVICE_API_KEY env 引用 secret: %+v", c.Env)
	}
	// Secret 含 api_key
	var sec corev1.Secret
	_ = cl.Get(ctx, types.NamespacedName{Name: "ds-qdrant-secret", Namespace: "default"}, &sec)
	if sec.StringData["api_key"] != "ak-xxx" {
		t.Fatalf("Secret api_key 错误: %v", sec.StringData)
	}
	// PVC 持久化（VolumeClaimTemplates 创建时设）
	if len(sts.Spec.VolumeClaimTemplates) != 1 || sts.Spec.VolumeClaimTemplates[0].Name != "data" {
		t.Fatalf("应有 data PVC 模板, got %+v", sts.Spec.VolumeClaimTemplates)
	}
	got := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	if got.String() != "20Gi" {
		t.Fatalf("StorageGB=20 应映射 20Gi PVC, got %s", got.String())
	}
	// 数据卷挂载到 qdrant 存储目录
	if c.VolumeMounts[0].MountPath != "/qdrant/storage" {
		t.Fatalf("qdrant 数据卷挂载点应 /qdrant/storage, got %s", c.VolumeMounts[0].MountPath)
	}
}

// TestReconcileMeilisearch 验证 meilisearch：镜像 + env MEILI_MASTER_KEY + 端口 7700。
func TestReconcileMeilisearch(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-meili", Namespace: "default"},
		Spec: v1alpha1.DataServiceSpec{
			TenantID: "t-acme", Kind: "search", Engine: "meilisearch", Name: "ds-meili",
			Connection: map[string]string{"master_key": "mk-xxx"},
		},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(d).WithStatusSubresource(&v1alpha1.DataService{}).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-meili", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var sts appsv1.StatefulSet
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-meili", Namespace: "default"}, &sts)
	c := sts.Spec.Template.Spec.Containers[0]
	if c.Image != "meilisearch/meilisearch:v1.10" {
		t.Fatalf("meilisearch 镜像错误: %s", c.Image)
	}
	if c.Ports[0].ContainerPort != 7700 {
		t.Fatalf("meilisearch 端口应 7700, got %d", c.Ports[0].ContainerPort)
	}
	if c.VolumeMounts[0].MountPath != "/meili_data" {
		t.Fatalf("meilisearch 数据卷挂载点应 /meili_data, got %s", c.VolumeMounts[0].MountPath)
	}
	var foundEnv bool
	for _, e := range c.Env {
		if e.Name == "MEILI_MASTER_KEY" && e.ValueFrom != nil {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Fatalf("meilisearch 缺 MEILI_MASTER_KEY env")
	}
}

// TestReconcileDataserviceImageOverride 验证 spec.Image 覆盖默认镜像（版本升级场景）。
func TestReconcileDataserviceImageOverride(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-up", Namespace: "default"},
		Spec: v1alpha1.DataServiceSpec{
			TenantID: "t-acme", Kind: "vector", Engine: "qdrant", Name: "ds-up",
			Image: "qdrant/qdrant:v1.13.0", // 覆盖默认 v1.12.4
			Connection: map[string]string{"api_key": "ak"},
		},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(d).WithStatusSubresource(&v1alpha1.DataService{}).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-up", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var sts appsv1.StatefulSet
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-up", Namespace: "default"}, &sts)
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != "qdrant/qdrant:v1.13.0" {
		t.Fatalf("spec.Image 应覆盖默认镜像, got %s", got)
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

// containsStr 报告 s 是否在 slice 中。
func containsStr(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

// reconcileDS 建 DataService CR 并跑一次 Reconcile，返回 client（供调用方断言 STS/Secret）。
func reconcileDS(t *testing.T, d *v1alpha1.DataService) client.Client {
	t.Helper()
	scheme := newScheme(t)
	cl := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(d).
		WithStatusSubresource(&v1alpha1.DataService{}).
		Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: d.Name, Namespace: d.Namespace}}); err != nil {
		t.Fatalf("Reconcile 失败: %v", err)
	}
	return cl
}

// TestExporterSidecarInjectedForPostgres 验证 postgres STS 注入 exporter sidecar。
func TestExporterSidecarInjectedForPostgres(t *testing.T) {
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-pg", Namespace: "default"},
		Spec: v1alpha1.DataServiceSpec{Kind: "db", Engine: "postgres", Name: "ds-pg", TenantID: "t-acme",
			Connection: map[string]string{"user": "u", "password": "p", "database": "db"}},
	}
	cl := reconcileDS(t, d)
	var sts appsv1.StatefulSet
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-pg", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("取 STS 失败: %v", err)
	}
	names := []string{}
	for _, c := range sts.Spec.Template.Spec.Containers {
		names = append(names, c.Name)
	}
	if !containsStr(names, "exporter") {
		t.Fatalf("postgres 应注入 exporter sidecar，实际容器：%v", names)
	}
	// exporter 容器应有端口名 exporter@9100 + DATA_SOURCE_NAME env。
	var exp *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "exporter" {
			exp = &sts.Spec.Template.Spec.Containers[i]
		}
	}
	if exp == nil || len(exp.Ports) == 0 || exp.Ports[0].Name != "exporter" || exp.Ports[0].ContainerPort != 9100 {
		t.Fatalf("exporter 端口应为 exporter@9100，got %+v", exp)
	}
	hasDSN := false
	for _, e := range exp.Env {
		if e.Name == "DATA_SOURCE_NAME" {
			hasDSN = true
		}
	}
	if !hasDSN {
		t.Fatalf("postgres exporter 应有 DATA_SOURCE_NAME env")
	}
	// exporter 应显式 --web.listen-address=:9100（默认端口各异，强制统一对齐声明的 9100 + scrape job）。
	listenOK := false
	for _, a := range exp.Args {
		if a == "--web.listen-address=:9100" {
			listenOK = true
		}
	}
	if !listenOK {
		t.Fatalf("postgres exporter 应 --web.listen-address=:9100，got args=%v", exp.Args)
	}
}

// TestNoSidecarForMinio 验证 minio（内置 metrics）不注入 exporter sidecar。
func TestNoSidecarForMinio(t *testing.T) {
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-minio", Namespace: "default"},
		Spec: v1alpha1.DataServiceSpec{Kind: "storage", Engine: "minio", Name: "ds-minio", TenantID: "t-acme",
			Connection: map[string]string{"accessKey": "a", "secretKey": "b"}},
	}
	cl := reconcileDS(t, d)
	var sts appsv1.StatefulSet
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-minio", Namespace: "default"}, &sts)
	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == "exporter" {
			t.Fatalf("minio 不应注入 exporter sidecar（内置 metrics）")
		}
	}
}

// TestExporterSidecarForRedisAndNATS 验证 cache/mq 也注入 sidecar。
func TestExporterSidecarForRedisAndNATS(t *testing.T) {
	for _, tc := range []struct {
		name, kind, engine string
		conn               map[string]string
	}{
		{"redis", "cache", "redis", map[string]string{"password": "p"}},
		{"nats", "mq", "nats", map[string]string{"token": "t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &v1alpha1.DataService{
				ObjectMeta: metav1.ObjectMeta{Name: "ds-" + tc.name, Namespace: "default"},
				Spec:       v1alpha1.DataServiceSpec{Kind: tc.kind, Engine: tc.engine, Name: "ds-" + tc.name, TenantID: "t-acme", Connection: tc.conn},
			}
			cl := reconcileDS(t, d)
			var sts appsv1.StatefulSet
			_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-" + tc.name, Namespace: "default"}, &sts)
			if !containsStsContainer(&sts, "exporter") {
				t.Fatalf("%s 应注入 exporter sidecar", tc.name)
			}
		})
	}
}

func containsStsContainer(sts *appsv1.StatefulSet, name string) bool {
	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == name {
			return true
		}
	}
	return false
}

// TestExporterImageCoverage 验证 exporterImage 按 kind+engine 选镜像（内置 metrics 返空）。
func TestExporterImageCoverage(t *testing.T) {
	cases := map[string]string{
		"db|postgres": "prometheuscommunity/postgres-exporter:v0.15.0",
		"db|mysql":    "prom/mysqld-exporter:v0.15.1",
		"cache|redis": "oliver006/redis_exporter:v1.62.0",
		"cache|valkey": "oliver006/redis_exporter:v1.62.0",
		"mq|nats":     "natsio/prometheus-nats-exporter:0.16.0",
		"storage|minio": "", // 内置 metrics
		"vector|qdrant":  "",
		"search|meilisearch": "",
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
		if got := exporterImage(kind, engine, ""); got != want {
			t.Errorf("exporterImage(%q,%q)=%q, want %q", kind, engine, got, want)
		}
	}
	// registry 内网化：去 repo 前缀。
	if got := exporterImage("db", "postgres", "hub.wang.dd:5000"); got != "hub.wang.dd:5000/library/postgres-exporter:v0.15.0" {
		t.Errorf("registry 内网化错误: %s", got)
	}
}
