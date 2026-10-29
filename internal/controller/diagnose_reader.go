// 诊断 raw 只读入口（spec tmp/diagnosis-agent/impl-spec.md S2.1–S2.3）。
//
// 与 runtime_reader.go 的租户路径不同：本组方法收原始 (namespace, ...) 参数，
// **不经 tenant.Namespace 派生、不做租户 label 归属校验**——服务平台诊断 agent
// （super_admin 语境，跨全 ns 排障 paas/observability/paas-t-*/任意 ns）。
// 调用方（internal/diagnose/tools）必须确保入口已过 adminGuard；本包不做权限判断。
//
// §8 四坑的 Go 侧落点（handoff 实地演练结论，fake client 测不出）：
//   - 坑1 label 规约：PodsBySelector 的入参 selector 必须来自 DeploymentSpec 读到的
//     spec.selector（真实 label 真源），绝不假设 app=<name> 之类的规约。
//   - 坑2 limit 两源不一致：DeploymentSpec 的 containers[].limits 是 limit 真源
//     （container_spec_memory_limit_bytes 在部分集群恒 0，不可信）。
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aitoys/paas/internal/workload"
)

// ContainerSpecInfo 容器资源配置快照（requests/limits 原始字符串，如 "100m"/"512Mi"）。
type ContainerSpecInfo struct {
	Name     string
	Image    string
	Requests ResourceSpecInfo
	Limits   ResourceSpecInfo
}

// ResourceSpecInfo CPU/内存资源量（K8s Quantity 字符串原样保留，空=未设置）。
type ResourceSpecInfo struct {
	CPU    string
	Memory string
}

// DeploymentSpecInfo Deployment spec 快照：selector（坑1 label 真源）+ 容器资源配置
// （坑2 limit 真源）。诊断 T1/T6/T8 的公共数据源。UID 供授权目标快照（重新发布
// 替换 UID 时 scoped 执行器停止，spec §6.2）。
type DeploymentSpecInfo struct {
	Namespace      string
	Name           string
	UID            string
	Replicas       int32
	Selector       map[string]string
	Containers     []ContainerSpecInfo
	InitContainers []ContainerSpecInfo
}

// DeploymentSpec 读任意 ns 的 Deployment spec（selector + 容器 requests/limits）。
// 不存在返 (nil, nil)——由调用方分流为 missing（工具层四态契约），与「集群不可达」的
// error 区分开。
func (r *K8sRuntimeReader) DeploymentSpec(ctx context.Context, ns, name string) (*DeploymentSpecInfo, error) {
	if r.clientset == nil {
		return nil, fmt.Errorf("集群不可用（非集群部署）")
	}
	dep, err := r.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deployment 读取失败（权限不足或 apiserver 不可达）")
	}
	out := &DeploymentSpecInfo{
		Namespace:      ns,
		Name:           name,
		UID:            string(dep.UID),
		Selector:       dep.Spec.Selector.MatchLabels,
		Containers:     containerSpecs(dep.Spec.Template.Spec.Containers),
		InitContainers: containerSpecs(dep.Spec.Template.Spec.InitContainers),
	}
	if dep.Spec.Replicas != nil {
		out.Replicas = *dep.Spec.Replicas
	}
	return out, nil
}

func containerSpecs(cs []corev1.Container) []ContainerSpecInfo {
	out := make([]ContainerSpecInfo, 0, len(cs))
	for _, c := range cs {
		out = append(out, ContainerSpecInfo{
			Name:     c.Name,
			Image:    c.Image,
			Requests: resourceSpecInfo(c.Resources.Requests),
			Limits:   resourceSpecInfo(c.Resources.Limits),
		})
	}
	return out
}

func resourceSpecInfo(rl corev1.ResourceList) ResourceSpecInfo {
	info := ResourceSpecInfo{}
	if v, ok := rl[corev1.ResourceCPU]; ok {
		info.CPU = v.String()
	}
	if v, ok := rl[corev1.ResourceMemory]; ok {
		info.Memory = v.String()
	}
	return info
}

// PodsBySelector 按 Deployment spec.selector 的 matchLabels 选 pod（坑1：selector 由
// 调用方从 DeploymentSpec 反查，本方法不做任何 label 规约假设）。
// 返回完整运行实例（复用租户路径的 pod→instance 映射，含容器状态/conditions/重启数）。
func (r *K8sRuntimeReader) PodsBySelector(ctx context.Context, ns string, matchLabels map[string]string) ([]workload.RuntimeInstance, error) {
	if r.clientset == nil {
		return nil, fmt.Errorf("集群不可用（非集群部署）")
	}
	if len(matchLabels) == 0 {
		return nil, fmt.Errorf("selector 为空（拒绝全 ns 扫描）")
	}
	sel := make([]string, 0, len(matchLabels))
	for k, v := range matchLabels {
		sel = append(sel, k+"="+v)
	}
	pods, err := r.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: strings.Join(sel, ",")})
	if err != nil {
		return nil, fmt.Errorf("pod 读取失败（权限不足或 apiserver 不可达）")
	}
	out := make([]workload.RuntimeInstance, 0, len(pods.Items))
	for i := range pods.Items {
		out = append(out, runtimeInstanceFromPod(&pods.Items[i]))
	}
	return out, nil
}

// DiagEvent 诊断事件（K8s event 最小子集 + involvedObject 归属）。
type DiagEvent struct {
	UID     string // involvedObject UID，租户诊断拒绝名称复用造成的历史串读
	Type    string
	Reason  string
	Message string
	Count   int32
	At      time.Time
	Object  string // involvedObject 的 kind/name
}

// NamespaceEvents 按任意 ns 查事件：objectName 非空按 involvedObject.name 过滤；
// sinceMin>0 限时间窗（按 lastTimestamp/eventTime）；warningOnly 只取 Warning。
// 事件有 TTL（默认 1h 聚合转储）——窗口内查空是 empty 不是 missing（§8坑3），
// 该语义由调用方四态分流，本方法只如实返回查询结果。
func (r *K8sRuntimeReader) NamespaceEvents(ctx context.Context, ns, objectName string, sinceMin int, warningOnly bool) ([]DiagEvent, error) {
	if r.clientset == nil {
		return nil, fmt.Errorf("集群不可用（非集群部署）")
	}
	sel := ""
	if objectName != "" {
		sel = "involvedObject.name=" + objectName
	}
	evs, err := r.clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("事件读取失败（权限不足或 apiserver 不可达）")
	}
	var cutoff time.Time
	if sinceMin > 0 {
		cutoff = time.Now().Add(-time.Duration(sinceMin) * time.Minute)
	}
	out := make([]DiagEvent, 0, len(evs.Items))
	for _, e := range evs.Items {
		if warningOnly && e.Type != corev1.EventTypeWarning {
			continue
		}
		at := e.LastTimestamp.Time
		if at.IsZero() {
			at = e.EventTime.Time
		}
		if !cutoff.IsZero() && !at.IsZero() && at.Before(cutoff) {
			continue
		}
		out = append(out, DiagEvent{
			UID: string(e.InvolvedObject.UID), Type: e.Type, Reason: e.Reason, Message: e.Message,
			Count: e.Count, At: at,
			Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// RawPodLogs 读任意 ns 的容器日志（previous=true 取上次终止容器）。
// grep 非空时服务端逐行过滤（含关键字的行才返回）——大日志不全量回传，
// 诊断场景只要错误证据不要全量流。tail<=0 默认 200 行。
func (r *K8sRuntimeReader) RawPodLogs(ctx context.Context, ns, pod, container string, tail int64, previous bool, grep string) (string, error) {
	if r.clientset == nil {
		return "", fmt.Errorf("集群不可用（非集群部署）")
	}
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	opts := &corev1.PodLogOptions{Previous: previous, Container: container, TailLines: &tail}
	rc, err := r.clientset.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		// Pod 不存在 / 容器未启动等 apiserver 侧错误原样透传（调用方分流 error/missing）。
		return "", fmt.Errorf("日志读取失败: %w", err)
	}
	defer func() { _ = rc.Close() }()
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, rerr := rc.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	logs := string(buf)
	if grep != "" {
		var kept []string
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, grep) {
				kept = append(kept, line)
			}
		}
		logs = strings.Join(kept, "\n")
	}
	return logs, nil
}

// PodWorkloadRef 从 pod 反查所属 Deployment 的 spec（owner 链 Pod→RS→Deployment）。
// 诊断 T6 的 limit 真源入口（§8坑2：limit 以 deploy spec 为准，不信 Prometheus 的
// container_spec_memory_limit_bytes）。裸 Pod / Job Pod / 反查链断裂返 (nil, nil)，
// 调用方降级为「无 spec 参照，仅报实时用量」。
func (r *K8sRuntimeReader) PodWorkloadRef(ctx context.Context, ns, podName string) (*DeploymentSpecInfo, error) {
	if r.clientset == nil {
		return nil, fmt.Errorf("集群不可用（非集群部署）")
	}
	pod, err := r.clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pod 读取失败（权限不足或 apiserver 不可达）")
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != "ReplicaSet" {
			continue
		}
		rs, err := r.clientset.AppsV1().ReplicaSets(ns).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return nil, nil // RS 不可读（删除竞态等）：降级无参照
		}
		for _, oref := range rs.OwnerReferences {
			if oref.Kind == "Deployment" {
				return r.DeploymentSpec(ctx, ns, oref.Name)
			}
		}
	}
	return nil, nil
}

// HasLivePods 校验目标服务是否有活体 pod（§8坑4：区分常驻服务与历史数据——
// paas-shop 类按需拉起的临时项目，Jaeger 里只有历史 trace）。
// 按 Deployment spec.selector 判断（与 PodsBySelector 同源，坑1 反查语义）。
func (r *K8sRuntimeReader) HasLivePods(ctx context.Context, ns, deployment string) (bool, error) {
	spec, err := r.DeploymentSpec(ctx, ns, deployment)
	if err != nil {
		return false, err
	}
	if spec == nil {
		return false, nil
	}
	pods, err := r.PodsBySelector(ctx, ns, spec.Selector)
	if err != nil {
		return false, err
	}
	for i := range pods {
		ins := pods[i]
		// 活体 = 非 Pending 且未被删除（Running/Succeeded 都算「该项目现存」；正在终止不算）。
		if ins.Status != "" && ins.Status != "Pending" && ins.DeletionTimestamp == nil {
			return true, nil
		}
	}
	return false, nil
}
