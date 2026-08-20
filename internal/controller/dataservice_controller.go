// Package controller 实现 K8s 数据面 reconciler：watch Workload/DataService CRD 期望状态，
// 落地 Deployment/Job/CronJob 与 StatefulSet+Service+Secret + 回写 status。
package controller

import (
	"context"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/labels"
)

// engineImage 按 Kind+Engine 选默认容器镜像。
// 6 类真实引擎（db=mysql/postgres、cache=redis/valkey、mq=nats、storage=minio、vector=qdrant、search=meilisearch）返非空镜像；
// 其余（mq 的 kafka/rabbitmq/rocketmq）返空 -> reconciler 走 failed 分支不拉起
// （避免 port=0/缺 env 导致 K8s 拒绝创建 Service 死循环或容器 CrashLoopBackOff）。
// engineImage 按 Kind+Engine 选默认容器镜像，registry 非空时用内网 registry（library/<name>:<tag>）。
// 节点拉不到 docker.io 时配 PAAS_IMAGE_REGISTRY=registry.example.local:5000（引擎镜像需先推 <registry>/library/）。
func engineImage(kind, engine, registry string) string {
	var img string
	switch kind {
	case "db":
		switch engine {
		case "mysql":
			img = "mysql:8"
		case "postgres":
			img = "postgres:15-alpine"
		}
	case "cache":
		switch engine {
		case "valkey":
			img = "valkey/valkey:7-alpine"
		case "redis":
			img = "redis:7-alpine"
		}
	case "mq":
		if engine == "nats" {
			img = "nats:2-alpine"
		}
	case "storage":
		img = "minio/minio:latest"
	case "vector":
		if engine == "qdrant" {
			img = "qdrant/qdrant:v1.12.4"
		}
	case "search":
		if engine == "meilisearch" {
			img = "getmeili/meilisearch:v1.22.1"
		}
	}
	if img == "" {
		return ""
	}
	if registry == "" {
		return img
	}
	// 去 repo 前缀（valkey/valkey -> valkey，minio/minio -> minio），统一 library/<name>:<tag>。
	name := img
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return registry + "/library/" + name
}

// displayNameKey 存用户在控制台起的原始名字（可含中文/大写/特殊字符），供 kubectl describe
// 或 -o custom-columns 直观辨认对应哪个数据服务。annotation 不参与 Service/STS selector，
// 可安全变更且不触发 Pod 重建（label 才进 selector 且不可变，故用 annotation 而非 label）。
const displayNameKey = labels.KeyDisplayName

// setDisplayName 把用户名字写进对象 annotation（幂等；CreateOrUpdate 的 mutate 回调里调）。
// 传 *metav1.ObjectMeta，SetMetaDataAnnotation 自动初始化 nil annotations map。
func setDisplayName(meta *metav1.ObjectMeta, name string) {
	metav1.SetMetaDataAnnotation(meta, displayNameKey, name)
}

// dataServiceLabels 返回 DataService 的 K8s 标签（含租户隔离）。
func dataServiceLabels(d *v1alpha1.DataService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "paas",
		labels.KeyTenant:               d.Spec.TenantID,
		labels.KeyDataservice:          d.Name,
		labels.KeyKind:                 d.Spec.Kind,
	}
}

// defaultResources 是数据服务容器的默认资源请求（保守，dev/demo 集群够用）。
func defaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

// dsResources 解析实例管理字段覆盖的资源请求（CRD spec.CPU/Memory 非空用之，否则默认）。
// 非法资源量字符串 panic（MustParse）：录入期（handler/Validate）已校验，reconciler 不兜底错误输入。
func dsResources(d *v1alpha1.DataService) corev1.ResourceRequirements {
	if d.Spec.CPU == "" && d.Spec.Memory == "" {
		return defaultResources()
	}
	req := corev1.ResourceList{}
	if d.Spec.CPU != "" {
		req[corev1.ResourceCPU] = resource.MustParse(d.Spec.CPU)
	}
	if d.Spec.Memory != "" {
		req[corev1.ResourceMemory] = resource.MustParse(d.Spec.Memory)
	}
	return corev1.ResourceRequirements{Requests: req}
}

// dataVolumeMount 返回各引擎数据目录挂载路径（PVC "data" 卷挂载点）。
// 持久化各引擎默认数据目录，重启不丢数据（redis/nats 默认内存模式挂载无害，需配 save+dir 才落盘）。
func dataVolumeMount(kind, engine string) string {
	switch kind {
	case "db":
		if engine == "postgres" {
			return "/var/lib/postgresql/data"
		}
		return "/var/lib/mysql" // mysql
	case "cache":
		return "/data" // redis
	case "mq":
		return "/data" // nats（默认内存，挂载无害）
	case "storage":
		return "/data" // minio
	case "vector":
		return "/qdrant/storage" // qdrant
	case "search":
		return "/meili_data" // meilisearch
	}
	return "/data"
}

// storageSize 解析实例管理字段 StorageGB（0=默认 10Gi），返回 PVC storage 请求量。
func storageSize(d *v1alpha1.DataService) resource.Quantity {
	gb := int64(10)
	if d.Spec.StorageGB > 0 {
		gb = int64(d.Spec.StorageGB)
	}
	return resource.MustParse(fmt.Sprintf("%dGi", gb))
}

// secretData 按 Kind 从 spec.Connection 挑建 Secret 需要的敏感 key（不把 host/port/uri 放 Secret）。
func secretData(d *v1alpha1.DataService) map[string]string {
	c := d.Spec.Connection
	switch d.Spec.Kind {
	case "db": // mysql/postgres（user+password+database 共用 Secret；postgres user=postgres，mysql user=root）
		return map[string]string{"user": c["user"], "password": c["password"], "database": c["database"]}
	case "cache": // redis
		return map[string]string{"password": c["password"]}
	case "mq": // nats
		return map[string]string{"token": c["token"]}
	case "storage": // minio
		return map[string]string{"accessKey": c["accessKey"], "secretKey": c["secretKey"]}
	case "vector": // qdrant
		return map[string]string{"api_key": c["api_key"]}
	case "search": // meilisearch
		return map[string]string{"master_key": c["master_key"]}
	}
	return map[string]string{}
}

// containerFor 按 Kind+Engine 构造容器：env（secretKeyRef 引用 <name>-secret）+ 启动参数注入密码 + 暴露端口。
// redis/nats 用 $(VAR) 展开（K8s 在 command/args 解析前注入 env，$(VAR) 生效）。
func containerFor(d *v1alpha1.DataService, image string) corev1.Container {
	secretName := d.Name + "-secret"
	ref := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: key}
	}
	envFrom := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref(key)}}
	}
	port := dataservice.EnginePort(d.Spec.Kind, d.Spec.Engine)
	c := corev1.Container{
		Name:            "main",
		Image:           image,
		ImagePullPolicy: corev1.PullAlways, // tag 不变时强制拉最新 digest（镜像更新后节点不缓存旧版，如 arm64->amd64）
		Ports:           []corev1.ContainerPort{{Name: "svc", ContainerPort: port}},
		Resources:       dsResources(d),
		// 数据卷挂载（持久化数据目录，与 VolumeClaimTemplates "data" 对应）。
		VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: dataVolumeMount(d.Spec.Kind, d.Spec.Engine)}},
	}
	switch d.Spec.Kind {
	case "db": // mysql / postgres（按 engine 选 env key；postgres 需 POSTGRES_USER=postgres 与连接串一致）
		if d.Spec.Engine == "postgres" {
			c.Env = []corev1.EnvVar{
				envFrom("POSTGRES_USER", "user"),
				envFrom("POSTGRES_PASSWORD", "password"),
				envFrom("POSTGRES_DB", "database"),
			}
		} else { // mysql
			c.Env = []corev1.EnvVar{
				envFrom("MYSQL_ROOT_PASSWORD", "password"),
				envFrom("MYSQL_DATABASE", "database"),
			}
		}
	case "cache": // redis
		c.Command = []string{"redis-server", "--requirepass", "$(REDIS_PASSWORD)"}
		c.Env = []corev1.EnvVar{envFrom("REDIS_PASSWORD", "password")}
	case "mq": // nats：-m 8222 开 HTTP 监控端口（供 exporter 采集 varz/connz）
		c.Args = []string{"-auth", "$(NATS_TOKEN)", "-m", "8222"}
		c.Env = []corev1.EnvVar{envFrom("NATS_TOKEN", "token")}
	case "storage": // minio：entrypoint=minio，用 Args 传子命令（Command 会覆盖 entrypoint 致 server 找不到）
		c.Args = []string{"server", "/data", "--console-address", ":9001"}
		c.Env = []corev1.EnvVar{
			envFrom("MINIO_ROOT_USER", "accessKey"),
			envFrom("MINIO_ROOT_PASSWORD", "secretKey"),
		}
	case "vector": // qdrant：API key 鉴权（默认镜像 entrypoint 即起服务，无需 Args）
		c.Env = []corev1.EnvVar{envFrom("QDRANT__SERVICE_API_KEY", "api_key")}
	case "search": // meilisearch：master key 鉴权（≥16 字节，randString(24) 满足）
		c.Env = []corev1.EnvVar{envFrom("MEILI_MASTER_KEY", "master_key")}
	}
	return c
}

// exporterImage 按 Kind+Engine 选 exporter 镜像（sidecar 注入用）。
// minio/qdrant/meilisearch 引擎内置 /metrics（无需 sidecar）返空。
// registry 非空时内网化（与 engineImage 同款 library/<name>:<tag>，去 repo 前缀）。
// 节点拉不到 docker.io 时配 PAAS_IMAGE_REGISTRY，exporter 镜像需先推 <registry>/library/。
func exporterImage(kind, engine, registry string) string {
	var img string
	switch kind {
	case "db":
		switch engine {
		case "postgres":
			img = "prometheuscommunity/postgres-exporter:v0.15.0"
		case "mysql":
			img = "prom/mysqld-exporter:v0.15.1"
		}
	case "cache": // redis/valkey 共用
		img = "oliver006/redis_exporter:v1.62.0"
	case "mq":
		if engine == "nats" {
			img = "natsio/prometheus-nats-exporter:0.16.0"
		}
	}
	if img == "" {
		return "" // storage/vector/search 引擎内置 metrics，无 sidecar
	}
	if registry == "" {
		return img
	}
	name := img
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return registry + "/library/" + name
}

// exporterSidecar 按 Kind+Engine 构造 exporter sidecar 容器（无则返 nil）。
// 凭证复用主容器 Secret（secretKeyRef，不重新生成）；sidecar 名固定 exporter，端口 9100。
// exporter 经 localhost 连主容器（同 Pod），故凭证 env 既供 exporter 连库又供其自身读取。
func exporterSidecar(d *v1alpha1.DataService, registry string) *corev1.Container {
	img := exporterImage(d.Spec.Kind, d.Spec.Engine, registry)
	if img == "" {
		return nil // 引擎内置 metrics，无 sidecar
	}
	secretName := d.Name + "-secret"
	ref := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: key}
	}
	envFrom := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref(key)}}
	}
	port := dataservice.EnginePort(d.Spec.Kind, d.Spec.Engine)
	c := &corev1.Container{
		Name:            "exporter",
		Image:           img,
		ImagePullPolicy: corev1.PullAlways,
		Ports:           []corev1.ContainerPort{{Name: "exporter", ContainerPort: 9100}},
		Resources:       defaultResources(),
	}
	switch d.Spec.Kind {
	case "db":
		if d.Spec.Engine == "postgres" {
			// pg exporter 连库串：经 localhost 连同 Pod 的 postgres，凭证从 Secret 取。
			// K8s env 的 $(VAR) 引用要求被引用 env 先于引用者定义，故凭证 env 必须排在 DSN 之前。
			c.Env = []corev1.EnvVar{
				envFrom("POSTGRES_USER", "user"),
				envFrom("POSTGRES_PASSWORD", "password"),
				envFrom("POSTGRES_DB", "database"),
				{Name: "DATA_SOURCE_NAME", Value: fmt.Sprintf("postgresql://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:%d/$(POSTGRES_DB)?sslmode=disable", port)},
			}
			// postgres-exporter 默认监听 9187，强制 9100 与声明端口 + scrape job 对齐。
			c.Args = []string{"--web.listen-address=:9100"}
		} else { // mysql
			c.Env = []corev1.EnvVar{
				envFrom("MYSQL_ROOT_PASSWORD", "password"),
				{Name: "DATA_SOURCE_NAME", Value: fmt.Sprintf("root:$(MYSQL_ROOT_PASSWORD)@(localhost:%d)/", port)},
			}
			// mysqld-exporter 默认 9104，强制 9100 对齐。
			c.Args = []string{"--web.listen-address=:9100"}
		}
	case "cache": // redis/valkey
		c.Env = []corev1.EnvVar{
			{Name: "REDIS_ADDR", Value: fmt.Sprintf("redis://localhost:%d", port)},
			envFrom("REDIS_PASSWORD", "password"),
		}
		// redis_exporter 默认 9121，强制 9100 对齐。
		c.Args = []string{"--web.listen-address=:9100"}
	case "mq": // nats exporter：连 nats 的 HTTP 监控端口（默认 8222）采集 varz/connz。
		// 用法：prometheus-nats-exporter <flags> url（server URL 是末尾位置参数）。
		// -port 9100 对齐声明端口 + scrape job；-varz/-connz/-serverz 指定采集端点。
		// 监控端口由主容器 -m 8222 开启。
		c.Args = []string{"-port", "9100", "-varz", "-connz", "-serverz", "http://localhost:8222"}
	}
	return c
}

// 数据服务有状态，统一落 StatefulSet（稳定网络标识）；ClusterIP Service 供应用访问；Secret 存凭证供 Pod env 引用。
type DataServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile 期望->实际：取 CRD -> 解析镜像 -> 无镜像跳过(failed) -> 建 Secret+headless/ClusterIP Svc+STS -> 回写 status。
func (r *DataServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var d v1alpha1.DataService
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	orig := d.Status.DeepCopy() // 回写前快照：值未变跳过 Update，防自触发 reconcile 循环
	// 镜像：实例管理字段 Image（版本升级）优先于按 Kind+Engine 推断的默认镜像。
	image := d.Spec.Image
	if image == "" {
		image = engineImage(d.Spec.Kind, d.Spec.Engine, os.Getenv("PAAS_IMAGE_REGISTRY"))
	}
	if image == "" {
		// 未知 Kind/Engine 组合（含占位 vector/search/kafka 等）：记 failed，不拉起未知镜像（安全默认）。
		d.Status.Phase = "failed"
		_ = r.Status().Update(ctx, &d)
		return ctrl.Result{}, nil
	}
	if err := r.applySecret(ctx, &d); err != nil {
		r.markFailed(ctx, &d)
		return ctrl.Result{}, fmt.Errorf("apply dataservice secret: %w", err)
	}
	if err := r.applyServices(ctx, &d); err != nil {
		r.markFailed(ctx, &d)
		return ctrl.Result{}, fmt.Errorf("apply dataservice services: %w", err)
	}
	sts, err := r.applyStatefulSet(ctx, &d, image)
	if err != nil {
		r.markFailed(ctx, &d)
		return ctrl.Result{}, fmt.Errorf("apply dataservice statefulset: %w", err)
	}
	// 回写 status（从 StatefulSet 实际 ready 副本）。
	// 值未变时跳过 Update：无条件回写会自触发 reconcile 循环 + apiserver 写放大（审计第 6 轮 I3）。
	d.Status.Image = image
	d.Status.Phase = "creating"
	if sts.Status.ReadyReplicas >= 1 {
		d.Status.Phase = "running"
	}
	if d.Status.Image != orig.Image || d.Status.Phase != orig.Phase {
		if err := r.Status().Update(ctx, &d); err != nil {
			return ctrl.Result{}, fmt.Errorf("update dataservice status: %w", err)
		}
	}
	return ctrl.Result{}, nil
}

// markFailed best-effort 回写 failed 态（apply 失败时），原 error 由调用方返回触发重试。
func (r *DataServiceReconciler) markFailed(ctx context.Context, d *v1alpha1.DataService) {
	d.Status.Phase = "failed"
	_ = r.Status().Update(ctx, d)
}

// applySecret CreateOrUpdate 凭证 Secret。幂等：Secret 已有数据则不覆盖（避免重置密码 -> 与 domain 不一致）。
func (r *DataServiceReconciler) applySecret(ctx context.Context, d *v1alpha1.DataService) error {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: d.Name + "-secret", Namespace: d.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sec, func() error {
		setDisplayName(&sec.ObjectMeta, d.Spec.Name)
		if len(sec.Data) == 0 && len(sec.StringData) == 0 {
			sec.StringData = secretData(d) // 仅首次创建写入；后续 reconcile 保留原值（密码不可变）
		}
		return controllerutil.SetControllerReference(d, sec, r.Scheme)
	})
	return err
}

// applyServices 建 headless Service（StatefulSet 必需）+ ClusterIP Service（应用访问，host 基于此名）。
func (r *DataServiceReconciler) applyServices(ctx context.Context, d *v1alpha1.DataService) error {
	port := dataservice.EnginePort(d.Spec.Kind, d.Spec.Engine)
	labels := dataServiceLabels(d)
	// headless（StatefulSet ServiceName 引用）
	hl := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: d.Name + "-headless", Namespace: d.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, hl, func() error {
		setDisplayName(&hl.ObjectMeta, d.Spec.Name)
		hl.SetLabels(labels)
		hl.Spec.ClusterIP = "None"
		hl.Spec.Selector = labels
		hl.Spec.Ports = []corev1.ServicePort{{Name: "svc", Port: port, TargetPort: intstrFromInt(int(port))}}
		return controllerutil.SetControllerReference(d, hl, r.Scheme)
	}); err != nil {
		return err
	}
	// ClusterIP（应用访问）
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: d.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		setDisplayName(&svc.ObjectMeta, d.Spec.Name)
		svc.SetLabels(labels)
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{Name: "svc", Port: port, TargetPort: intstrFromInt(int(port))}}
		return controllerutil.SetControllerReference(d, svc, r.Scheme)
	})
	return err
}

// applyStatefulSet CreateOrUpdate StatefulSet（env 引用 Secret + PVC 持久化）+ 返回最新状态供回写。
//   - replicas 取 spec.Replicas（nil/0=1；显式 0=停 scale-to-zero）。
//   - VolumeClaimTemplates 仅首次创建设（STS 该字段不可变）；存量 PVC size 变化靠 resizePVC 单独扩容
//     （依赖 StorageClass AllowVolumeExpansion，Pod 重启后文件系统扩容，不可缩）。
func (r *DataServiceReconciler) applyStatefulSet(ctx context.Context, d *v1alpha1.DataService, image string) (*appsv1.StatefulSet, error) {
	replicas := int32(1) // 数据服务起步单副本（KISS）；集群/HA 留后续
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas // 显式 0 = 停（scale-to-zero）
	}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: d.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		labels := dataServiceLabels(d)
		setDisplayName(&sts.ObjectMeta, d.Spec.Name)
		sts.SetLabels(labels)
		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = d.Name + "-headless"
		if sts.CreationTimestamp.IsZero() {
			sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
			// VolumeClaimTemplates 是不可变字段，仅创建时设；存量 PVC 扩容走 resizePVC。
			sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: storageSize(d)},
					},
				},
			}}
		}
		containers := []corev1.Container{containerFor(d, image)}
		if sc := exporterSidecar(d, os.Getenv("PAAS_IMAGE_REGISTRY")); sc != nil {
			containers = append(containers, *sc) // 引擎 exporter sidecar（db/cache/mq），内置 metrics 引擎跳过
		}
		tmpl := corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       corev1.PodSpec{Containers: containers},
		}
		setDisplayName(&tmpl.ObjectMeta, d.Spec.Name) // Pod 继承注解，kubectl get pod -o wide 可见
		sts.Spec.Template = tmpl
		return controllerutil.SetControllerReference(d, sts, r.Scheme)
	})
	if err != nil {
		return sts, err
	}
	// 存量 PVC 扩容（spec.StorageGB 变大时更新 PVC，依赖 StorageClass AllowVolumeExpansion）。best-effort。
	r.resizePVC(ctx, d)
	return sts, nil
}

// resizePVC best-effort 扩容 STS 的 data PVC 到 spec 期望 size（仅扩不缩）。
// STS VolumeClaimTemplates 不可变，故存量 PVC 直接 Update spec.resources.requests.storage。
func (r *DataServiceReconciler) resizePVC(ctx context.Context, d *v1alpha1.DataService) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: fmt.Sprintf("data-%s-0", d.Name), Namespace: d.Namespace,
	}}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pvc), pvc); err != nil {
		return // PVC 不存在（STS 尚未建）或获取失败，跳过。
	}
	want := storageSize(d)
	cur := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if cur.Cmp(want) >= 0 {
		return // 当前 >= 期望（含缩容场景），不变更（K8s PVC 不可缩）。
	}
	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = want
	_ = r.Update(ctx, pvc) // best-effort；失败下次 reconcile 重试
}

// intstrFromInt 构造 int 类型的 IntOrString（Service targetPort 用）。
func intstrFromInt(v int) intstr.IntOrString {
	return intstr.FromInt(v)
}

// SetupWithManager 注册 reconciler 到 manager（watch DataService，own Secret/Service/StatefulSet）。
func (r *DataServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DataService{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
