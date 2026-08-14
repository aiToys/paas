package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// K8sPodReader 查数据服务的运行 Pod（排障用），实现 dataservice.PodReader。
// 按 label paas.aitoys/dataservice=<dsID> 查 Pod（reconciler dataServiceLabels 设此 label）。
// 用 controller-runtime typed client（与 route applier 同款），nil 时 Pods 降级返空。
type K8sPodReader struct {
	client client.Client
}

// NewK8sPodReader 创建 Pod reader。cl 可为 nil（集群外降级，Pods 返空）。
func NewK8sPodReader(cl client.Client) *K8sPodReader {
	return &K8sPodReader{client: cl}
}

// Pods 返回数据服务的运行 Pod 列表。namespace 传空时从 ctx tenant 解析（paas-<tenant>）。
// 集群外（client nil）/ list 失败 best-effort 返空（不报错，与 status reader 同款降级）。
func (r *K8sPodReader) Pods(ctx context.Context, namespace, dsID string) ([]dataservice.PodInfo, error) {
	out := []dataservice.PodInfo{}
	if r == nil || r.client == nil || dsID == "" {
		return out, nil // 集群外降级
	}
	if namespace == "" {
		tid, _ := tenant.TenantFrom(ctx)
		namespace = tenant.Namespace(tid)
	}
	pods := &corev1.PodList{}
	labelSel := client.MatchingLabels{labels.KeyDataservice: dsID}
	if err := r.client.List(ctx, pods, client.InNamespace(namespace), labelSel); err != nil {
		return out, nil // best-effort 降级
	}
	for _, p := range pods.Items {
		out = append(out, dataservice.PodInfo{
			Name:     p.Name,
			Status:   string(p.Status.Phase),
			Ready:    readyString(p.Status.ContainerStatuses),
			Restarts: restartSum(p.Status.ContainerStatuses),
			Node:     p.Spec.NodeName,
			IP:       p.Status.PodIP,
			Age:      ageHuman(p.CreationTimestamp),
			Message:  podMessage(p.Status),
		})
	}
	return out, nil
}

// readyString 返回 "ready/total" 容器就绪比（如 "1/1"）。
func readyString(cs []corev1.ContainerStatus) string {
	if len(cs) == 0 {
		return ""
	}
	ready := 0
	for _, c := range cs {
		if c.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, len(cs))
}

// restartSum 汇总所有容器的重启次数。
func restartSum(cs []corev1.ContainerStatus) int {
	n := 0
	for _, c := range cs {
		n += int(c.RestartCount)
	}
	return n
}

// ageHuman 返回 Pod 启动至今的人类可读时长（如 "5m30s"）。
func ageHuman(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t.Time).Round(time.Second)
	return d.String()
}

// podMessage 提取首个容器的 Waiting/Terminated 状态原因（排障用）。
func podMessage(s corev1.PodStatus) string {
	if len(s.ContainerStatuses) > 0 {
		cs := s.ContainerStatuses[0]
		if cs.State.Waiting != nil && cs.State.Waiting.Message != "" {
			return cs.State.Waiting.Message
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
			return cs.State.Terminated.Message
		}
	}
	return ""
}
