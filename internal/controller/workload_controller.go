// Package controller 实现 K8s 数据面 reconciler：watch Workload CRD 期望状态，
// 落 Deployment/Job/CronJob + GPU 反亲和 + 回写 CRD status。
package controller

import (
	"context"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

// WorkloadReconciler watch Workload CRD，把期望状态落到 K8s 资源并回写 status。
type WorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// labelsFor 返回工作负载的 K8s 标签（含租户/应用隔离）。
func labelsFor(w *v1alpha1.Workload) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "paas",
		"paas.aitoys/tenant":           w.Spec.TenantID,
		"paas.aitoys/app":              w.Spec.AppID,
		"paas.aitoys/workload":         w.Name,
	}
}

// podSpec 构造容器 Pod 模板（含 GPU resource + 反亲和）。
func podSpec(w *v1alpha1.Workload) corev1.PodSpec {
	container := corev1.Container{
		Name:  "main",
		Image: w.Spec.Image,
	}
	if w.Spec.Command != "" {
		container.Command = []string{w.Spec.Command}
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
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       podSpec(w),
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply deployment: %w", err)
	}
	// 回写 status.ready（从 Deployment 实际 ready 副本）。
	w.Status.Ready = dep.Status.ReadyReplicas
	w.Status.Status = "running"
	if dep.Status.ReadyReplicas < replicas {
		w.Status.Status = "deploying"
	}
	return r.Status().Update(ctx, w)
}

// applyJob CreateOrUpdate Job（一次性）。
func (r *WorkloadReconciler) applyJob(ctx context.Context, w *v1alpha1.Workload) error {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, job, func() error {
		labels := labelsFor(w)
		job.SetLabels(labels)
		job.Spec.Parallelism = ptrInt32(w.Spec.Replicas)
		// Job 的 PodTemplate 创建后不可变：仅创建时设置；更新期改 image 需删旧建新（本期不处理）。
		if job.CreationTimestamp.IsZero() {
			job.Spec.Template = corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec(w),
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply job: %w", err)
	}
	return nil
}

// applyCronJob CreateOrUpdate CronJob（定时）。
func (r *WorkloadReconciler) applyCronJob(ctx context.Context, w *v1alpha1.Workload) error {
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
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply cronjob: %w", err)
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
		Complete(r)
}
