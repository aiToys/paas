package controller

import (
	"context"
	"fmt"
	"io"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// K8sStatusReader 实现 workload.StatusReader：从 K8s Deployment/Job/CronJob 实际状态
// 回填工作负载 Ready/Status，让前端展示真实运行态（非 store 静态值）。
//
// 资源名匹配：reconciler 建 Deployment/Job/CronJob 的 name = CRD name = Workload.ID，
// 故按资源名（=Workload.ID）map 回填。label selector 限定本租户（paas.aitoys/tenant），
// 与 dataplane reader 多租户隔离语义一致。
//
// clientset 为 nil 时 no-op（降级：非集群部署，handler 透传 store 原值）。
type K8sStatusReader struct {
	clientset kubernetes.Interface
	namespace string
}

// NewK8sStatusReader 构造 reader。namespace 为空则 default。
func NewK8sStatusReader(cs kubernetes.Interface, namespace string) *K8sStatusReader {
	if namespace == "" {
		namespace = "default"
	}
	return &K8sStatusReader{clientset: cs, namespace: namespace}
}

// FillStatus 批量回填工作负载 Ready/Status（按租户 label 查 K8s 资源，按 ID 匹配）。
func (r *K8sStatusReader) FillStatus(ctx context.Context, wls []workload.Workload) error {
	if r.clientset == nil || len(wls) == 0 {
		return nil
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		// fail-closed：无租户上下文不回填，保持 store 原值（防跨租户误回填）。
		return nil
	}
	labelSel := "app.kubernetes.io/managed-by=paas,paas.aitoys/tenant=" + tid
	deploys, err := r.clientset.AppsV1().Deployments(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}
	jobs, err := r.clientset.BatchV1().Jobs(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	crons, err := r.clientset.BatchV1().CronJobs(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
	if err != nil {
		return fmt.Errorf("list cronjobs: %w", err)
	}

	// map by 资源名（=Workload.ID）。
	depReady := map[string]int32{}
	depReps := map[string]int32{}
	for _, d := range deploys.Items {
		depReady[d.Name] = d.Status.ReadyReplicas
		depReps[d.Name] = d.Status.Replicas
	}
	jobSucc := map[string]int32{}
	jobFailed := map[string]int32{}
	for _, j := range jobs.Items {
		jobSucc[j.Name] = j.Status.Succeeded
		jobFailed[j.Name] = j.Status.Failed
	}
	cronActive := map[string]int{}
	for _, c := range crons.Items {
		cronActive[c.Name] = len(c.Status.Active)
	}

	for i := range wls {
		w := &wls[i]
		switch w.Type {
		case workload.TypeService:
			ready, ok := depReady[w.ID]
			if !ok {
				continue // 无匹配 Deployment（K8s 未起或未投影），保持 store 原值
			}
			w.Ready = int(ready) //nolint:gosec // G115: 副本数实际不超 int 范围
			w.Status = workload.StatusRunning
			if ready < clampInt32(w.Replicas) { // clampInt32 安全收敛 int→int32（applier.go，gosec G115）
				w.Status = workload.StatusDeploying
			}
			// Progressing=False（ProgressDeadlineExceeded）判失败：镜像拉不到/启动崩溃卡死。
			// 仅当无 ready 副本且 Progressing 明确失败时标 failed，避免 deploying 期误判。
			if ready == 0 && w.Replicas > 0 && deployFailed(deploys.Items, w.ID) {
				w.Status = workload.StatusFailed
			}
		case workload.TypeJob:
			if _, ok := jobSucc[w.ID]; !ok {
				continue
			}
			w.Ready = int(jobSucc[w.ID]) //nolint:gosec // G115
			w.Status = workload.StatusRunning
			if jobFailed[w.ID] > 0 {
				w.Status = workload.StatusFailed
			}
			if jobSucc[w.ID] > 0 {
				w.Status = workload.StatusSucceeded
			}
		case workload.TypeCronJob:
			if _, ok := cronActive[w.ID]; !ok {
				continue
			}
			w.Ready = cronActive[w.ID]
			w.Status = workload.StatusRunning
		}
	}
	return nil
}

// deployFailed 检查 Deployment 是否 Progressing=False（ProgressDeadlineExceeded），
// 即滚动升级超时失败（镜像拉不到/崩溃）。返回 true 表示明确失败态。
func deployFailed(items []appsv1.Deployment, name string) bool {
	for _, d := range items {
		if d.Name != name {
			continue
		}
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing && c.Status == "False" {
				return true
			}
		}
		return false
	}
	return false
}

// Instances 返回某工作负载的运行实例（Pod 级）。
// 按 label paas.aitoys/workload=<id> + 租户隔离 label 查 Pod，映射为 workload.Instance。
// service=Deployment 的 Pod；job=Job 的 Pod（含已完成）；cronjob=CronJob 当前活跃 Job 派生的 Pod。
// 无 clientset / 无租户上下文 / 查询失败 / 无匹配 Pod 均返空切片（降级，不报错——
// 详情页仍展示期望态，实例区空提示）。
func (r *K8sStatusReader) Instances(ctx context.Context, workloadID string) ([]workload.Instance, error) {
	if r.clientset == nil || workloadID == "" {
		return []workload.Instance{}, nil
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return []workload.Instance{}, nil
	}
	labelSel := fmt.Sprintf("app.kubernetes.io/managed-by=paas,paas.aitoys/tenant=%s,paas.aitoys/workload=%s", tid, workloadID)
	pods, err := r.clientset.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
	if err != nil {
		return []workload.Instance{}, nil // 降级：查询失败返空，详情页不 5xx
	}
	out := make([]workload.Instance, 0, len(pods.Items))
	for _, p := range pods.Items {
		out = append(out, podToInstance(&p))
	}
	return out, nil
}

// podToInstance 把 K8s Pod 映射为 workload.Instance（含就绪/重启/状态原因）。
func podToInstance(p *corev1.Pod) workload.Instance {
	ins := workload.Instance{
		Name:   p.Name,
		Status: string(p.Status.Phase),
		Node:   p.Spec.NodeName,
		IP:     p.Status.PodIP,
	}
	// StartTime 在 Pod 未调度/启动前可能为 nil（Pending），需 nil 守卫。
	if p.Status.StartTime != nil {
		ins.StartedAt = p.Status.StartTime.Time
	}
	// 容器级聚合：就绪容器数/总数 + 重启次数合计。
	ready, total, restarts := 0, len(p.Status.ContainerStatuses), 0
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += int(cs.RestartCount) //nolint:gosec // G115: 重启次数不超 int
	}
	if total > 0 {
		ins.Ready = fmt.Sprintf("%d/%d", ready, total)
	}
	ins.Restarts = restarts
	// Pending/Failed 时取最近一个等待/终止原因作 message，便于排查（镜像拉取失败/崩等）。
	if waiting := p.Status.ContainerStatuses; len(waiting) > 0 {
		if w := waiting[len(waiting)-1].State.Waiting; w != nil && w.Message != "" {
			ins.Message = w.Message
		} else if t := waiting[len(waiting)-1].State.Terminated; t != nil && t.Message != "" {
			ins.Message = t.Message
		}
	}
	return ins
}

// PodLogs 返回某 Pod 的日志流（K8s Pods.Logs）。调用方负责 Close。
// 安全：先校验 Pod 归属本租户 + 本工作负载（label 匹配），防跨租户/越权读他人 Pod 日志。
// 无 clientset / 无租户上下文 / Pod 不属于该 workload 返 error，handler 映射降级。
func (r *K8sStatusReader) PodLogs(ctx context.Context, workloadID, podName string, tailLines int64, previous bool) (io.ReadCloser, error) {
	if r.clientset == nil || workloadID == "" || podName == "" {
		return nil, fmt.Errorf("日志不可用（非集群部署或参数缺失）")
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return nil, fmt.Errorf("无租户上下文")
	}
	// 越权校验：取 Pod 确认其 label 同时含本租户 + 本 workload，否则拒绝（不泄漏存在性，统一 not found）。
	pod, err := r.clientset.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Pod 不存在: %s", podName)
	}
	if pod.Labels[labels.KeyTenant] != tid || pod.Labels[labels.KeyWorkload] != workloadID {
		return nil, fmt.Errorf("Pod 不存在: %s", podName)
	}
	opts := &corev1.PodLogOptions{
		Previous: previous,
	}
	if tailLines > 0 {
		tl := tailLines
		opts.TailLines = &tl
	}
	return r.clientset.CoreV1().Pods(r.namespace).GetLogs(podName, opts).Stream(ctx)
}

// 编译期断言：K8sStatusReader 实现 workload.StatusReader。
var _ workload.StatusReader = (*K8sStatusReader)(nil)
