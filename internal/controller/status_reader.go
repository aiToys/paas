package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aitoys/paas/internal/workload"
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
			if ready < clampReplicas(w.Replicas) { // clampReplicas 安全收敛 int→int32（applier.go，gosec G115）
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

// 编译期断言：K8sStatusReader 实现 workload.StatusReader。
var _ workload.StatusReader = (*K8sStatusReader)(nil)
