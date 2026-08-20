// Package controller 实现 K8s 数据面 reconciler：watch Workload CRD 期望状态，
// 落 Deployment/Job/CronJob + GPU 反亲和 + 回写 CRD status。
package controller

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/pkg/labels"
)

// WorkloadReconciler watch Workload CRD，把期望状态落到 K8s 资源并回写 status。
type WorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// DPToken 数据面接入 token（API Key），注入 service 类型 Pod env，供 zeus 应用经
	// paas-registry 插件发现 PaaS 服务。空则不注入（K8s 部署但未配 token 的场景）。
	DPToken string
	// DPEndpoint 数据面 API 地址（覆盖默认 http://paas-core.paas.svc.cluster.local/dp）。
	DPEndpoint string
	// OtelEndpoint OTel trace 推送地址（OTLP/HTTP host:port，env PAAS_OTEL_ENDPOINT，
	// 集群内 jaeger.observability.svc:4318）。注入 service 类型 Pod env，让应用自动推 trace
	// 到平台可观测后端（paas-shop observ.Init 据此建 tracer，空则 noop）。与 DPToken 独立——
	// 应用即使不接数据面也应有可观测。空则不注入（未配 OTEL 后端的场景）。
	OtelEndpoint string
	// ClusterID 排障上下文：注入 service 类型 Pod env PAAS_CLUSTER_ID（应用读入 OTel 资源属性
	// paas.cluster，trace 可按集群过滤）。空则不注入（单集群场景，应用侧默认 "default"）。
	ClusterID string
	// IngressClass 是 applyIngress 建的 Ingress 的 ingressClassName（env PAAS_INGRESS_CLASS，
	// 默认 hermes）。空则不设 ingressClassName（集群默认 IngressController 接管）。
	IngressClass string
	// Configs 应用配置查找（依赖倒置）：注入应用×环境级 appconfig（含数据服务连接 + 模型 LLM 凭证）
	// 到 Pod env，让"绑定资源"真正生效。nil 则不注入（dev/无 K8s 场景）。桥接在 cmd/core。
	Configs AppConfigLookup
}

// AppConfigLookup 是应用配置查找接口（依赖倒置，破除 controller→appconfig 业务包依赖）。
// Items 返回应用工作负载应注入的 env 配置项（聚合 {envID} + DefaultEnv 跨环境桶）。
type AppConfigLookup interface {
	Items(ctx context.Context, tenantID, appID, envID string) ([]AppConfigItem, error)
}

// AppConfigItem 是待注入 Pod env 的配置项（Name→env 名，Value→env 值）。
type AppConfigItem struct {
	Name  string
	Value string
}

// appEnvVars 查应用配置并转 K8s EnvVar。Configs nil 或查询失败返空（best-effort，不阻断 reconcile）。
func (r *WorkloadReconciler) appEnvVars(ctx context.Context, w *v1alpha1.Workload) []corev1.EnvVar {
	if r.Configs == nil {
		return nil
	}
	items, err := r.Configs.Items(ctx, w.Spec.TenantID, w.Spec.AppID, w.Spec.EnvID)
	if err != nil {
		// 记日志可见（排障关键：静默失败会让「绑定资源不生效」无从排查，审计第 6 轮 M4）。
		log.Printf("controller: 查应用配置失败 tenant=%s app=%s env=%s: %v", w.Spec.TenantID, w.Spec.AppID, w.Spec.EnvID, err)
		return nil
	}
	if len(items) == 0 {
		return nil
	}
	vars := make([]corev1.EnvVar, 0, len(items))
	for _, it := range items {
		if it.Name == "" {
			continue
		}
		vars = append(vars, corev1.EnvVar{Name: it.Name, Value: it.Value})
	}
	return vars
}

// dpEndpoint 返回数据面 API 地址（空则默认集群内 core Service 地址）。
func (r *WorkloadReconciler) dpEndpoint() string {
	if r.DPEndpoint != "" {
		return r.DPEndpoint
	}
	return "http://paas-core.paas.svc.cluster.local/dp"
}

// labelsFor 返回工作负载的 K8s 标签（含租户/应用隔离 + 泳道 + 服务）。
// lane 空→default（基线）；feature 泳道 Workload 带各自 lane，selector 自然区分，Pod/Service 同款 label 匹配。
// service 非空才加 label（同 app 多服务区分）；空=单服务场景不带，避免污染既有 Workload 致无谓 Pod 重建。
// 注意：selector 仅创建时设（Deployment selector immutable），service label 后加不影响既有 selector 匹配
// （MatchLabels 子集语义，Pod 多带 service label 仍匹配原 selector）。
func labelsFor(w *v1alpha1.Workload) map[string]string {
	lane := sanitizeLane(w.Spec.LaneID)
	m := map[string]string{
		"app.kubernetes.io/managed-by": "paas",
		labels.KeyTenant:               w.Spec.TenantID,
		labels.KeyApp:                  w.Spec.AppID,
		labels.KeyWorkload:             w.Name,
		labels.KeyLane:                 lane,
	}
	if w.Spec.Service != "" {
		m[labels.KeyService] = w.Spec.Service
	}
	return m
}

// sanitizeLane 泳道值清洗为合法 K8s label（集成分支名含 /，如 integration/20260815-1）。
// 非 [A-Za-z0-9-_.] 字符替换为 -，截断 63 字符，首尾非字母数字剔除；空/清洗后空 → default。
// 仅影响 label 值（Service 名等仍用原始 lane 派生，见 dataplane/endpoints.go）。
func sanitizeLane(lane string) string {
	if lane == "" {
		return "default"
	}
	var b []byte
	for _, r := range lane {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b = append(b, byte(r)) //nolint:gosec // 上行 case 已限定 ASCII 区间，rune->byte 无溢出
		default:
			b = append(b, '-')
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	out := strings.Trim(string(b), "-_.")
	if out == "" {
		return "default"
	}
	return out
}

// prometheusAnnotations 返回 service 类型工作负载 Pod 的 Prometheus 自动发现注解。
// 仅 type=service 且端口>0：Prometheus kubernetes-pods job 按 prometheus.io/scrape=true 发现 Pod，
// 抓 <port>/metrics（复用业务端口，paas-shop 等服务已在业务端口暴露 /metrics）。
// job/cronjob 无常驻 HTTP server，不抓。端口取 ContainerPort，缺省取 Port。
// 配合 Pod template 的 paas.aitoys/app + paas.aitoys/workload label（labelsFor），metrics.go
// 可按 namespace + pod 正则聚合应用级 RED 指标（RPS/延迟/错误率）。
func prometheusAnnotations(w *v1alpha1.Workload) map[string]string {
	if w.Spec.Type != "service" {
		return nil
	}
	port := w.Spec.ContainerPort
	if port <= 0 {
		port = w.Spec.Port
	}
	if port <= 0 {
		return nil
	}
	return map[string]string{
		"prometheus.io/scrape": "true",
		"prometheus.io/port":   strconv.Itoa(int(port)),
		"prometheus.io/path":   "/metrics",
	}
}
func podSpec(w *v1alpha1.Workload) corev1.PodSpec {
	container := corev1.Container{
		Name:            "main",
		Image:           w.Spec.Image,
		ImagePullPolicy: corev1.PullAlways, // 同 tag 更新镜像时强制拉新（防 IfNotPresent 缓存旧镜像，与 dataservice STS 一致）
	}
	if w.Spec.Command != "" {
		// Command 含空格（如 "nginx -v"）用 /bin/sh -c 执行（K8s Command 是 exec form，
		// 直接包成单元素 slice 会把整串当可执行文件名找不到）；无空格则 exec 单命令。
		if strings.Contains(w.Spec.Command, " ") {
			container.Command = []string{"/bin/sh", "-c", w.Spec.Command}
		} else {
			container.Command = []string{w.Spec.Command}
		}
	}
	// 端口声明 + readiness probe（TCP，对应用零侵入：容器 listen 即 ready）。
	// readiness 驱动 K8s Endpoints 维护 ready 集合，是数据面服务发现（/dp/instances）的真源。
	// ContainerPort 缺省（0）时取 Port：applyService 在 Port>0 即建 Service，podSpec 必须同步
	// 声明端口 + probe，否则 Pod 无 readiness probe → kubelet 默认 Ready=True → 进程未 listen 即被路由。
	cport := w.Spec.ContainerPort
	if cport <= 0 {
		cport = w.Spec.Port
	}
	if cport > 0 {
		container.Ports = []corev1.ContainerPort{{ContainerPort: cport}}
		container.ReadinessProbe = &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(cport)}},
			PeriodSeconds: 10, FailureThreshold: 3,
		}
	}
	if w.Spec.GPU.Count > 0 {
		q := resource.MustParse(strconv.Itoa(w.Spec.GPU.Count))
		container.Resources.Requests = corev1.ResourceList{
			corev1.ResourceName("nvidia.com/gpu"): q,
		}
		container.Resources.Limits = container.Resources.Requests
	}
	spec := corev1.PodSpec{
		Containers: []corev1.Container{container},
	}
	// GPU 反亲和：分散到不同节点（topologyKey hostname）。
	if w.Spec.GPU.Count > 0 {
		spec.Affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
					LabelSelector: &metav1.LabelSelector{MatchLabels: labelsFor(w)},
					TopologyKey:   "kubernetes.io/hostname",
				}},
			},
		}
	}
	return spec
}

// Reconcile 期望→实际：取 CRD → 按 type 映射目标资源 → CreateOrUpdate → 回写 status。
func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var w v1alpha1.Workload
	if err := r.Get(ctx, req.NamespacedName, &w); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch w.Spec.Type {
	case "cronjob":
		return ctrl.Result{}, r.applyCronJob(ctx, &w)
	case "job":
		return ctrl.Result{}, r.applyJob(ctx, &w)
	default: // service → Deployment
		return ctrl.Result{}, r.applyDeployment(ctx, &w)
	}
}

// applyDeployment CreateOrUpdate Deployment + 回写 status.ready。
func (r *WorkloadReconciler) applyDeployment(ctx context.Context, w *v1alpha1.Workload) error {
	replicas := w.Spec.Replicas
	orig := w.Status.DeepCopy() // 回写前快照：值未变跳过 Update，防自触发 reconcile 循环
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		labels := labelsFor(w)
		dep.SetLabels(labels)
		dep.Spec.Replicas = &replicas
		// Selector 在 Deployment 创建后不可变：仅在创建时设置，避免更新时 reconcile 死循环。
		if dep.CreationTimestamp.IsZero() {
			dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		dep.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: prometheusAnnotations(w)},
			Spec:       podSpec(w),
		}
		c := &dep.Spec.Template.Spec.Containers[0]
		// 应用配置注入：数据服务连接（DATABASE_URL 等）+ 模型 LLM 凭证（PAAS_LLM_*）。
		// 让"绑定资源"真正生效到 Pod env；best-effort，查询失败跳过。
		if envVars := r.appEnvVars(ctx, w); len(envVars) > 0 {
			c.Env = append(c.Env, envVars...)
		}
		// service 类型注入数据面发现 env（zeus 应用经 paas-registry 插件用）。
		// DPToken 空则跳过（K8s 部署但未配 token，不影响 Deployment 本身）。
		if w.Spec.Type == "service" && r.DPToken != "" {
			c.Env = append(c.Env,
				corev1.EnvVar{Name: "PAAS_DP_ENDPOINT", Value: r.dpEndpoint()},
				corev1.EnvVar{Name: "PAAS_DP_TOKEN", Value: r.DPToken},
				corev1.EnvVar{Name: "PAAS_TENANT_ID", Value: w.Spec.TenantID},
			)
		}
		// OTel trace 推送 endpoint（service/cronjob Pod 自动建 tracer 推 Jaeger）。
		// 空则跳过（未配 OTEL 后端时应用 observ.Init noop，功能不受影响）。
		// 与 DPToken 独立注入：可观测是横切能力，应用不接数据面也应有 trace。
		// cronjob 也注入：statsworker 等定时任务的执行链路同样需要可观测（dogfooding 实测 noop 缺口）。
		if (w.Spec.Type == "service" || w.Spec.Type == "cronjob") && r.OtelEndpoint != "" {
			c.Env = append(c.Env,
				corev1.EnvVar{Name: "PAAS_OTEL_ENDPOINT", Value: r.OtelEndpoint},
				corev1.EnvVar{Name: "PAAS_CLUSTER_ID", Value: r.ClusterID},
				corev1.EnvVar{Name: "PAAS_LANE_ID", Value: sanitizeLane(w.Spec.LaneID)},
			)
		}
		// OwnerReference：CR 删除时 K8s GC 自动清理 Deployment（SetupWithManager 的 Owns 生效前提，
		// 否则 ownerRef 空 → 删 CR 不删 Deployment → Pod/GPU 永久泄漏）。
		return controllerutil.SetControllerReference(w, dep, r.Scheme)
	})
	if err != nil {
		// 失败 best-effort 回写 failed 态（原 error 优先返回，触发 controller 重试）。
		w.Status.Status = "failed"
		_ = r.Status().Update(ctx, w)
		return fmt.Errorf("apply deployment: %w", err)
	}
	// 先建 K8s Service 再回写 status（一次 Status().Update，避免 running/deploying 抖动 + apiserver 写放大）。
	// service 类型且声明 Port 时建 Service；失败回写 failed + 返 error 触发重试（CreateOrUpdate 幂等）。
	if serr := r.applyService(ctx, w); serr != nil {
		w.Status.Status = "failed"
		_ = r.Status().Update(ctx, w)
		return fmt.Errorf("apply service: %w", serr)
	}
	// 建 Ingress（service 类型且声明 Domain 时）：host=Domain -> Service:Port。
	// 失败 best-effort 回写 failed + 返 error 触发重试（CreateOrUpdate 幂等）。
	if ierr := r.applyIngress(ctx, w); ierr != nil {
		w.Status.Status = "failed"
		_ = r.Status().Update(ctx, w)
		return fmt.Errorf("apply ingress: %w", ierr)
	}
	// 回写 status.ready（从 Deployment 实际 ready 副本）。
	// 值未变时跳过 Update：无条件回写会自触发 reconcile 循环 + apiserver 写放大（审计第 6 轮 I3）。
	w.Status.Ready = dep.Status.ReadyReplicas
	w.Status.Status = "running"
	if dep.Status.ReadyReplicas < replicas {
		w.Status.Status = "deploying"
	}
	if w.Status.Ready != orig.Ready || w.Status.Status != orig.Status {
		if err := r.Status().Update(ctx, w); err != nil {
			return err
		}
	}
	return nil
}

// applyService CreateOrUpdate K8s Service（仅 type=service 且 Port>0）。
// Service 名 = Workload 名；selector 匹配 Pod label paas.aitoys/workload；端口映射 Port→ContainerPort。
// OwnerRef 设 CR，删 CR 级联清 Service。readiness probe 驱动 Endpoints ready 集合（数据面发现真源）。
func (r *WorkloadReconciler) applyService(ctx context.Context, w *v1alpha1.Workload) error {
	if w.Spec.Type != "service" || w.Spec.Port <= 0 {
		return nil
	}
	svcName := w.Spec.Name
	if svcName == "" {
		svcName = w.Name
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: w.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		labels := labelsFor(w)
		svc.SetLabels(labels)
		// selector 创建后不可变：仅创建时设置，避免更新时 reconcile 死循环。
		if svc.CreationTimestamp.IsZero() {
			svc.Spec.Selector = labels
		}
		cport := w.Spec.ContainerPort
		if cport <= 0 {
			cport = w.Spec.Port
		}
		svc.Spec.Ports = []corev1.ServicePort{{
			Port: w.Spec.Port, TargetPort: intstr.FromInt32(cport), Protocol: corev1.ProtocolTCP,
		}}
		return controllerutil.SetControllerReference(w, svc, r.Scheme)
	})
	return err
}

// applyIngress CreateOrUpdate networkingv1.Ingress（仅 type=service 且 Port>0 且 Domain 非空）。
// Ingress 名 = Workload 名（与 Service 同名）；host=Domain，path / -> Service:Port，pathType Prefix。
// ingressClassName 取 r.IngressClass（env PAAS_INGRESS_CLASS，默认 hermes）。
// OwnerRef 设 CR，删 CR 级联清 Ingress。Domain 空则跳过（仅集群内 DNS 可达，不对外暴露）。
func (r *WorkloadReconciler) applyIngress(ctx context.Context, w *v1alpha1.Workload) error {
	if w.Spec.Type != "service" || w.Spec.Port <= 0 || w.Spec.Domain == "" {
		return nil
	}
	svcName := w.Spec.Name
	if svcName == "" {
		svcName = w.Name
	}
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: w.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ing, func() error {
		labels := labelsFor(w)
		ing.SetLabels(labels)
		pathType := networkingv1.PathTypePrefix
		rule := networkingv1.IngressRule{
			Host: w.Spec.Domain,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     "/",
						PathType: &pathType,
						Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
							Name: svcName,
							Port: networkingv1.ServiceBackendPort{Number: w.Spec.Port},
						}},
					}},
				},
			},
		}
		ing.Spec.Rules = []networkingv1.IngressRule{rule}
		if r.IngressClass != "" {
			ing.Spec.IngressClassName = &r.IngressClass
		}
		return controllerutil.SetControllerReference(w, ing, r.Scheme)
	})
	return err
}

// applyJob CreateOrUpdate Job（一次性）+ 回写 status。
func (r *WorkloadReconciler) applyJob(ctx context.Context, w *v1alpha1.Workload) error {
	origJob := w.Status.DeepCopy() // 回写前快照：值未变跳过 Update
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, job, func() error {
		labels := labelsFor(w)
		job.SetLabels(labels)
		job.Spec.Parallelism = ptrInt32(w.Spec.Replicas)
		// Job 完成后 1 天自动清理（与 devops builder.K8sJob 一致），减少 etcd 存储 + list/watch 噪音。
		job.Spec.TTLSecondsAfterFinished = ptrInt32(86400)
		// 失败不重试（与 devops builder.K8sJob 对齐）：BackoffLimit=0 让 Job 失败一次即终止，
		// 这样 status 回写「Failed>0→failed」与 K8s 实际状态一致（默认 6 次重试期间会误判）。
		if job.CreationTimestamp.IsZero() {
			job.Spec.BackoffLimit = ptrInt32(0)
		}
		// Job 的 PodTemplate 创建后不可变：仅创建时设置；更新期改 image 需删旧建新（本期不处理）。
		if job.CreationTimestamp.IsZero() {
			job.Spec.Template = corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec(w),
			}
			// Job/CronJob 的 Pod template 必须显式设 restartPolicy（仅 OnFailure/Never 合法，
			// 默认 Always 会被 apiserver 拒绝 -> "Required value" 报错致 Job 永远创建失败）。
			// 与 BackoffLimit=0 语义一致：失败即终止不重启，status 回写与 K8s 实际状态对齐。
			job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
			// 应用配置注入（创建时，PodTemplate 之后不可变；新增绑定需删旧建新）。
			if envVars := r.appEnvVars(ctx, w); len(envVars) > 0 {
				job.Spec.Template.Spec.Containers[0].Env = append(
					job.Spec.Template.Spec.Containers[0].Env, envVars...)
			}
		}
		// OwnerReference：CR 删除时 GC 清理 Job（同 Deployment）。
		return controllerutil.SetControllerReference(w, job, r.Scheme)
	})
	if err != nil {
		w.Status.Status = "failed"
		_ = r.Status().Update(ctx, w)
		return fmt.Errorf("apply job: %w", err)
	}
	// 回写 status（Job 一次性：Succeeded→succeeded，Failed→failed，否则 running）。
	// 值未变时跳过 Update：防自触发 reconcile 循环 + apiserver 写放大（审计第 6 轮 I3）。
	w.Status.Ready = job.Status.Succeeded
	w.Status.Status = "running"
	if job.Status.Failed > 0 {
		w.Status.Status = "failed"
	}
	if job.Status.Succeeded > 0 {
		w.Status.Status = "succeeded"
	}
	if w.Status.Ready != origJob.Ready || w.Status.Status != origJob.Status {
		return r.Status().Update(ctx, w)
	}
	return nil
}

// applyCronJob CreateOrUpdate CronJob（定时）+ 回写 status。
func (r *WorkloadReconciler) applyCronJob(ctx context.Context, w *v1alpha1.Workload) error {
	origCJ := w.Status.DeepCopy() // 回写前快照：值未变跳过 Update
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cj, func() error {
		labels := labelsFor(w)
		cj.SetLabels(labels)
		cj.Spec.Schedule = w.Spec.Schedule
		cj.Spec.JobTemplate = batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec(w),
			}},
		}
		// 同 applyJob：CronJob 衍生的 Job Pod 必须显式 restartPolicy（Never），否则 apiserver 拒绝。
		cj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
		// 同 applyJob：衍生 Job 失败一次即终止（BackoffLimit=0）+ 1 天后 GC（防完成 Job/Pod 永久残留拖慢 list/watch）。
		cj.Spec.JobTemplate.Spec.BackoffLimit = ptrInt32(0)
		cj.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = ptrInt32(86400)
		// 应用配置注入（创建时，JobTemplate PodTemplate 不可变；新增绑定需删旧建新）。
		if envVars := r.appEnvVars(ctx, w); len(envVars) > 0 {
			tmpl := &cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
			tmpl.Env = append(tmpl.Env, envVars...)
		}
		// OwnerReference：CR 删除时 GC 清理 CronJob（同 Deployment）。
		return controllerutil.SetControllerReference(w, cj, r.Scheme)
	})
	if err != nil {
		w.Status.Status = "failed"
		_ = r.Status().Update(ctx, w)
		return fmt.Errorf("apply cronjob: %w", err)
	}
	// 回写 status（CronJob 持续调度：Active 数为运行中 Job 数）。
	// 值未变时跳过 Update：防自触发 reconcile 循环 + apiserver 写放大（审计第 6 轮 I3）。
	w.Status.Ready = int32(len(cj.Status.Active)) //nolint:gosec // G115: CronJob Active 数为运行中 Job 数，实际不会超 int32 范围
	w.Status.Status = "running"
	if w.Status.Ready != origCJ.Ready || w.Status.Status != origCJ.Status {
		return r.Status().Update(ctx, w)
	}
	return nil
}

func ptrInt32(v int32) *int32 { return &v }

// SetupWithManager 注册 reconciler 到 manager（watch Workload，own Deployment/Job/CronJob）。
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Workload{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Owns(&batchv1.CronJob{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Complete(r)
}
