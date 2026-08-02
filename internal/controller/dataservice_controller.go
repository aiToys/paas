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
)

// engineImage 按 Kind+Engine 选默认容器镜像。
// 聚焦 4 类真实引擎（db=mysql/postgres、cache=redis/valkey、mq=nats、storage=minio）返非空镜像；
// 其余（mq 的 kafka/rabbitmq/rocketmq、vector、search）返空 -> reconciler 走 failed 分支不拉起
// （避免 port=0/缺 env 导致 K8s 拒绝创建 Service 死循环或容器 CrashLoopBackOff）。
// engineImage 按 Kind+Engine 选默认容器镜像，registry 非空时用内网 registry（library/<name>:<tag>）。
// 节点拉不到 docker.io 时配 PAAS_IMAGE_REGISTRY=hub.wang.dd:5000（引擎镜像需先推 <registry>/library/）。
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
const displayNameKey = "paas.aitoys/display-name"

// setDisplayName 把用户名字写进对象 annotation（幂等；CreateOrUpdate 的 mutate 回调里调）。
// 传 *metav1.ObjectMeta，SetMetaDataAnnotation 自动初始化 nil annotations map。
func setDisplayName(meta *metav1.ObjectMeta, name string) {
	metav1.SetMetaDataAnnotation(meta, displayNameKey, name)
}

// dataServiceLabels 返回 DataService 的 K8s 标签（含租户隔离）。
func dataServiceLabels(d *v1alpha1.DataService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "paas",
		"paas.aitoys/tenant":           d.Spec.TenantID,
		"paas.aitoys/dataservice":      d.Name,
		"paas.aitoys/kind":             d.Spec.Kind,
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
		Resources:       defaultResources(),
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
	case "mq": // nats
		c.Args = []string{"-auth", "$(NATS_TOKEN)"}
		c.Env = []corev1.EnvVar{envFrom("NATS_TOKEN", "token")}
	case "storage": // minio：entrypoint=minio，用 Args 传子命令（Command 会覆盖 entrypoint 致 server 找不到）
		c.Args = []string{"server", "/data", "--console-address", ":9001"}
		c.Env = []corev1.EnvVar{
			envFrom("MINIO_ROOT_USER", "accessKey"),
			envFrom("MINIO_ROOT_PASSWORD", "secretKey"),
		}
	}
	return c
}

// DataServiceReconciler watch DataService CRD，把期望状态落到 K8s（Secret+Service+StatefulSet）并回写 status。
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
	image := engineImage(d.Spec.Kind, d.Spec.Engine, os.Getenv("PAAS_IMAGE_REGISTRY"))
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
	d.Status.Image = image
	d.Status.Phase = "creating"
	if sts.Status.ReadyReplicas >= 1 {
		d.Status.Phase = "running"
	}
	if err := r.Status().Update(ctx, &d); err != nil {
		return ctrl.Result{}, fmt.Errorf("update dataservice status: %w", err)
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

// applyStatefulSet CreateOrUpdate StatefulSet（env 引用 Secret）+ 返回最新状态供回写。
func (r *DataServiceReconciler) applyStatefulSet(ctx context.Context, d *v1alpha1.DataService, image string) (*appsv1.StatefulSet, error) {
	replicas := int32(1) // 数据服务起步单副本（KISS）；集群/HA 留后续
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: d.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		labels := dataServiceLabels(d)
		setDisplayName(&sts.ObjectMeta, d.Spec.Name)
		sts.SetLabels(labels)
		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = d.Name + "-headless"
		if sts.CreationTimestamp.IsZero() {
			sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		tmpl := corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{containerFor(d, image)}},
		}
		setDisplayName(&tmpl.ObjectMeta, d.Spec.Name) // Pod 继承注解，kubectl get pod -o wide 可见
		sts.Spec.Template = tmpl
		return controllerutil.SetControllerReference(d, sts, r.Scheme)
	})
	return sts, err
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
