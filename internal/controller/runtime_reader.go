// Package controller —— K8sRuntimeReader/RuntimeActor 实现（spec 2026-10-17）。
//
// 读：运行态聚合（Deployment + Pod + 容器级）、UID 精确事件、重启/重建进度观察。
// 写：Eviction 驱逐（尊重 PDB）、exec 终端流（client-go remotecommand）。
// 所有入口验证 ctx 租户与 workload 归属（label 匹配），跨租户统一 not found 语义。
package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// K8sRuntimeReader 实现 workload.RuntimeReader + RuntimeActor + ExecStreamer。
// clientset/restConfig 为 nil 时返回 unavailable（非集群部署，handler 映射 503 降级）。
type K8sRuntimeReader struct {
	clientset kubernetes.Interface
	cfg       *rest.Config
}

// NewK8sRuntimeReader 构造。cfg 供 exec 的 SPDY 通道（clientset 内部 config 不暴露，
// 须由 startManager 显式传入；nil 时 exec 能力禁用，读/驱逐不受影响）。
func NewK8sRuntimeReader(cs kubernetes.Interface, cfg *rest.Config) *K8sRuntimeReader {
	return &K8sRuntimeReader{clientset: cs, cfg: cfg}
}

// unavailable 返回 source=unavailable 的运行态（非集群部署）。
func unavailable() workload.Runtime {
	return workload.Runtime{
		Observation: workload.Observation{Source: workload.ObsSourceNone, State: workload.ObsStateUnavailable},
		Instances:   []workload.RuntimeInstance{},
		Capabilities: capSet(func(c *workload.Capability) {
			c.Allowed, c.ReasonCode, c.Message = false, "not_cluster", "非集群部署，运行态不可用"
		}),
	}
}

// capSet 生成全套能力（fn 统一改写每一项）。
func capSet(fn func(*workload.Capability)) workload.RuntimeCapabilities {
	all := []workload.Capability{}
	for range 5 {
		all = append(all, workload.Capability{})
	}
	for i := range all {
		fn(&all[i])
	}
	return workload.RuntimeCapabilities{Logs: all[0], Events: all[1], Restart: all[2], Recreate: all[3], Exec: all[4]}
}

// Runtime 读取完整运行态（观察 + 实例 + 容器 + 能力）。
func (r *K8sRuntimeReader) Runtime(ctx context.Context, workloadID string) (workload.Runtime, error) {
	if r.clientset == nil || workloadID == "" {
		return unavailable(), nil
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return unavailable(), nil
	}
	ns := tenant.Namespace(tid)
	now := time.Now()
	out := workload.Runtime{
		Observation: workload.Observation{Source: workload.ObsSourceKubernetes, State: workload.ObsStateFresh, ObservedAt: now},
		Instances:   []workload.RuntimeInstance{},
		Capabilities: workload.RuntimeCapabilities{
			Logs:   workload.Capability{Allowed: true},
			Events: workload.Capability{Allowed: true},
		},
	}
	labelSel := fmt.Sprintf("app.kubernetes.io/managed-by=paas,paas.aitoys/tenant=%s,paas.aitoys/workload=%s", tid, workloadID)
	pods, err := r.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
	if err != nil {
		out.Observation.State = workload.ObsStateUnavailable
		out.Observation.Error = "集群实例读取失败（数据面不可达或权限不足）"
		return out, nil // 读取失败显式标 unavailable，不返空实例伪装「没有」
	}
	for i := range pods.Items {
		out.Instances = append(out.Instances, runtimeInstanceFromPod(&pods.Items[i]))
	}
	// Deployment 存在性决定 restart/recreate/exec 能力（job/cronjob 无滚动重启语义）。
	dep, err := r.clientset.AppsV1().Deployments(ns).Get(ctx, workloadID, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		out.Capabilities.Restart = workload.Capability{Allowed: false, ReasonCode: "not_cluster", Message: "未发现数据面 Deployment（未部署或非集群）"}
		out.Capabilities.Recreate = out.Capabilities.Restart
		out.Capabilities.Exec = out.Capabilities.Restart
	case err != nil:
		out.Observation.State = workload.ObsStateStale
		out.Observation.Error = "Deployment 读取失败，实例数据可能滞后"
	default:
		replicas := int32(0)
		if dep.Spec.Replicas != nil {
			replicas = *dep.Spec.Replicas
		}
		if replicas == 0 {
			out.Capabilities.Restart = workload.Capability{Allowed: false, ReasonCode: "zero_replicas", Message: "副本为 0，先恢复副本再重启"}
		} else {
			out.Capabilities.Restart = workload.Capability{Allowed: true}
		}
		out.Capabilities.Recreate = workload.Capability{Allowed: true}
		if r.cfg != nil {
			out.Capabilities.Exec = workload.Capability{Allowed: true}
		} else {
			out.Capabilities.Exec = workload.Capability{Allowed: false, ReasonCode: "disabled", Message: "终端通道未配置"}
		}
	}
	return out, nil
}

// runtimeInstanceFromPod 把 Pod 映射为完整运行实例（UID/容器/conditions/删除时间）。
func runtimeInstanceFromPod(p *corev1.Pod) workload.RuntimeInstance {
	base := podToInstance(p)
	ins := workload.RuntimeInstance{Instance: base, UID: string(p.UID)}
	if p.DeletionTimestamp != nil {
		ins.DeletionTimestamp = &p.DeletionTimestamp.Time
	}
	for _, c := range p.Status.Conditions {
		ins.Conditions = append(ins.Conditions, workload.PodCondition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message,
		})
	}
	for _, cs := range p.Status.ContainerStatuses {
		cst := workload.ContainerState{Name: cs.Name, Ready: cs.Ready, Restarts: int(cs.RestartCount)} //nolint:gosec // G115
		switch {
		case cs.State.Running != nil:
			cst.State = "running"
		case cs.State.Waiting != nil:
			cst.State = "waiting"
			cst.Reason = cs.State.Waiting.Reason
			cst.Message = cs.State.Waiting.Message
		case cs.State.Terminated != nil:
			cst.State = "terminated"
			cst.Reason = cs.State.Terminated.Reason
			cst.Message = cs.State.Terminated.Message
			cst.LastExitCode = cs.State.Terminated.ExitCode
		}
		if cs.LastTerminationState.Terminated != nil {
			cst.LastExitCode = cs.LastTerminationState.Terminated.ExitCode
			cst.LastTerminationReason = cs.LastTerminationState.Terminated.Reason
		}
		ins.Containers = append(ins.Containers, cst)
	}
	// Pod 级 reason：取最近一个非 Running 容器的等待/终止原因（镜像拉取失败/崩溃等）。
	for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
		cs := p.Status.ContainerStatuses[i]
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			ins.Reason = w.Reason
			break
		}
		if t := cs.State.Terminated; t != nil && t.Reason != "" {
			ins.Reason = t.Reason
			break
		}
	}
	return ins
}

// PodEvents 返回某 Pod 的关联事件（UID 精确过滤；UID 空按名字+kind 过滤）。
func (r *K8sRuntimeReader) PodEvents(ctx context.Context, workloadID, podName, podUID string) ([]workload.PodEvent, error) {
	if r.clientset == nil {
		return nil, fmt.Errorf("事件不可用（非集群部署）")
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return nil, fmt.Errorf("无租户上下文")
	}
	if err := r.checkPodOwnership(ctx, workloadID, podName); err != nil {
		return nil, err
	}
	sel := fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", podName)
	if podUID != "" {
		sel += ",involvedObject.uid=" + podUID
	}
	evs, err := r.clientset.CoreV1().Events(tenant.Namespace(tid)).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("事件读取失败（数据面不可达或权限不足）")
	}
	out := make([]workload.PodEvent, 0, len(evs.Items))
	for _, e := range evs.Items {
		out = append(out, workload.PodEvent{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: e.Count, At: e.LastTimestamp.Time})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// RestartProgress 观察滚动重启：最新 RS 的 token + Deployment 完成态。
func (r *K8sRuntimeReader) RestartProgress(ctx context.Context, workloadID, token string) (string, string, error) {
	if r.clientset == nil || token == "" {
		return "notfound", "", fmt.Errorf("观察不可用（非集群部署或令牌为空）")
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return "notfound", "", fmt.Errorf("无租户上下文")
	}
	ns := tenant.Namespace(tid)
	dep, err := r.clientset.AppsV1().Deployments(ns).Get(ctx, workloadID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "notfound", "数据面无 Deployment（期望状态尚未投影）", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("deployment 读取失败: %w", err)
	}
	// 最新 RS（按 revision 注解排序），比对 template 上的 restart token。
	rss, err := r.clientset.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=paas,paas.aitoys/tenant=" + tid})
	if err != nil {
		return "", "", fmt.Errorf("replicaset 读取失败: %w", err)
	}
	var latest *appsv1.ReplicaSet
	for i := range rss.Items {
		rs := &rss.Items[i]
		if !ownedByDeployment(rs.OwnerReferences, workloadID) {
			continue
		}
		if latest == nil || rsRevision(rs) > rsRevision(latest) {
			latest = rs
		}
	}
	if latest == nil {
		return "progressing", "等待数据面创建新版本 ReplicaSet", nil
	}
	rsToken := latest.Spec.Template.Annotations[restartTokenAnnotationKey]
	if rsToken != token {
		return "superseded", fmt.Sprintf("最新版本令牌为 %s（已有更新的发布覆盖本次重启）", shortToken(rsToken)), nil
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas == 0 {
		return "completed", "副本为 0（缩容状态，无需滚动）", nil
	}
	// 滚动完成判定锚定 RS 级状态：Deployment 聚合 status（UpdatedReplicas 等）存在滞后
	// 窗口——新 RS 已创建、status 仍反映旧版满编（1/1/1），聚合判定会假性 completed
	// （集群实测 0.49s 误判，幂等窗口形同虚设）。改为：最新 RS 自身满编就绪 + 历史
	// RS 副本归零，两者共同构成「滚动完成」的确切定义，不受聚合滞后影响。
	newReplicas, newReady := latest.Status.Replicas, latest.Status.ReadyReplicas
	staleReplicas := int32(0)
	for i := range rss.Items {
		rs := &rss.Items[i]
		if rs == latest || !ownedByDeployment(rs.OwnerReferences, workloadID) {
			continue
		}
		staleReplicas += rs.Status.Replicas
	}
	rev := latest.Annotations["deployment.kubernetes.io/revision"]
	if newReplicas == replicas && newReady == replicas && staleReplicas == 0 {
		return "completed", fmt.Sprintf("revision=%s 新副本 %d/%d 已就绪（旧副本已退场）", rev, newReady, replicas), nil
	}
	if newReady > 0 || staleReplicas > 0 {
		return "running", fmt.Sprintf("滚动中：新版就绪 %d/%d，旧版残留 %d", newReady, replicas, staleReplicas), nil
	}
	return "reconciling", fmt.Sprintf("新版本已投影（revision=%s），等待滚动启动", rev), nil
}

// RecreateProgress 观察实例重建：旧 UID 退场 + 同 workload 新 Pod 就绪。
func (r *K8sRuntimeReader) RecreateProgress(ctx context.Context, workloadID, oldUID string) (string, string, error) {
	if r.clientset == nil || oldUID == "" {
		return "progressing", "", fmt.Errorf("观察不可用（非集群部署或 UID 为空）")
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return "progressing", "", fmt.Errorf("无租户上下文")
	}
	labelSel := fmt.Sprintf("app.kubernetes.io/managed-by=paas,paas.aitoys/tenant=%s,paas.aitoys/workload=%s", tid, workloadID)
	pods, err := r.clientset.CoreV1().Pods(tenant.Namespace(tid)).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
	if err != nil {
		return "", "", fmt.Errorf("pod 读取失败: %w", err)
	}
	oldGone := true
	readyNew := 0
	for i := range pods.Items {
		p := &pods.Items[i]
		if string(p.UID) == oldUID {
			if p.DeletionTimestamp == nil {
				oldGone = false // 旧实例仍在运行
			}
			continue
		}
		if podFullyReady(p) {
			readyNew++
		}
	}
	if oldGone && readyNew > 0 {
		return "completed", fmt.Sprintf("旧实例已退场，%d 个替代实例就绪", readyNew), nil
	}
	if oldGone {
		return "progressing", "旧实例已退场，等待替代实例就绪", nil
	}
	return "progressing", "等待旧实例优雅退出", nil
}

// podFullyReady 判 Pod 完全就绪（Ready condition=True 且全部容器 ready；Running+0/0 不算）。
func podFullyReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
		return false
	}
	readyCond := false
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			readyCond = true
		}
	}
	if !readyCond {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// Evict 驱逐一个 Pod（Eviction API：保留优雅退出、尊重 PDB）。
// 校验链：租户 label → workload label → UID 精确 → owner 是平台托管的 Deployment（拒裸 Pod）。
func (r *K8sRuntimeReader) Evict(ctx context.Context, workloadID, podName, podUID string) error {
	if r.clientset == nil {
		return fmt.Errorf("重建不可用（非集群部署）")
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return fmt.Errorf("无租户上下文")
	}
	ns := tenant.Namespace(tid)
	pod, err := r.clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("pod 不存在: %s", podName) // 统一 not found，不泄漏
	}
	if pod.Labels[labels.KeyTenant] != tid || pod.Labels[labels.KeyWorkload] != workloadID {
		return fmt.Errorf("pod 不存在: %s", podName)
	}
	if podUID != "" && string(pod.UID) != podUID {
		return workload.ErrUIDMismatch
	}
	if err := requireManagedDeployment(ctx, r.clientset, ns, workloadID, pod.OwnerReferences); err != nil {
		return err
	}
	eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns}}
	if err := r.clientset.PolicyV1().Evictions(ns).Evict(ctx, eviction); err != nil {
		if apierrors.IsTooManyRequests(err) {
			return workload.ErrTooManyEvict
		}
		return fmt.Errorf("驱逐请求失败: %w", err)
	}
	return nil
}

// checkPodOwnership 读路径归属校验（logs/events 共用；不校验 UID——读以名字为准）。
func (r *K8sRuntimeReader) checkPodOwnership(ctx context.Context, workloadID, podName string) error {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return fmt.Errorf("无租户上下文")
	}
	pod, err := r.clientset.CoreV1().Pods(tenant.Namespace(tid)).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("pod 不存在: %s", podName)
	}
	if pod.Labels[labels.KeyTenant] != tid || pod.Labels[labels.KeyWorkload] != workloadID {
		return fmt.Errorf("pod 不存在: %s", podName)
	}
	return nil
}

// ContainerLogs 容器级日志（container 非空时按容器过滤；previous 取上次终止容器）。
// UID 非空时精确匹配（防名字复用读到别的实例）。
func (r *K8sRuntimeReader) ContainerLogs(ctx context.Context, workloadID, podName, podUID, container string, tailLines int64, previous bool) (io.ReadCloser, error) {
	if r.clientset == nil || workloadID == "" || podName == "" {
		return nil, fmt.Errorf("日志不可用（非集群部署或参数缺失）")
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return nil, fmt.Errorf("无租户上下文")
	}
	ns := tenant.Namespace(tid)
	pod, err := r.clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod 不存在: %s", podName)
	}
	if pod.Labels[labels.KeyTenant] != tid || pod.Labels[labels.KeyWorkload] != workloadID {
		return nil, fmt.Errorf("pod 不存在: %s", podName)
	}
	if podUID != "" && string(pod.UID) != podUID {
		return nil, workload.ErrUIDMismatch
	}
	if container != "" && !containerExists(pod, container) {
		return nil, fmt.Errorf("容器不存在: %s", container)
	}
	opts := &corev1.PodLogOptions{Previous: previous, Container: container}
	if tailLines > 0 {
		tl := tailLines
		opts.TailLines = &tl
	}
	return r.clientset.CoreV1().Pods(ns).GetLogs(podName, opts).Stream(ctx)
}

func containerExists(pod *corev1.Pod, name string) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == name {
			return true
		}
	}
	return false
}

// ownedByDeployment 判断 owner refs 指向给定名字的 Deployment（controller ref）。
func ownedByDeployment(refs []metav1.OwnerReference, deploymentName string) bool {
	for _, ref := range refs {
		if ref.Kind == "Deployment" && ref.Name == deploymentName {
			return true
		}
	}
	return false
}

// requireManagedDeployment 校验 Pod 的 owner 链（Pod→RS→Deployment=workloadID）。
// 裸 Pod / 已完成 Job 的 Pod 无重建语义（控制器不会补副本），拒绝。
func requireManagedDeployment(ctx context.Context, cs kubernetes.Interface, ns, workloadID string, podOwnerRefs []metav1.OwnerReference) error {
	for _, ref := range podOwnerRefs {
		if ref.Kind != "ReplicaSet" {
			continue
		}
		rs, err := cs.AppsV1().ReplicaSets(ns).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("实例归属校验失败（ReplicaSet 不可读）")
		}
		if !ownedByDeployment(rs.OwnerReferences, workloadID) {
			return workload.ErrNotManaged
		}
		return nil
	}
	return workload.ErrNotManaged
}

func rsRevision(rs *appsv1.ReplicaSet) int64 {
	v := rs.Annotations["deployment.kubernetes.io/revision"]
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func shortToken(t string) string {
	if len(t) > 18 {
		return t[:18] + "…"
	}
	return t
}

// —— exec 终端（remotecommand SPDY 桥）——

// k8sExecSession 实现 workload.ExecSession（remotecommand 流的管道化封装）。
type k8sExecSession struct {
	stdin     io.WriteCloser
	stdout    io.Reader
	sizeQueue *terminalSizeQueue
	cancel    context.CancelFunc
	done      chan error
	closeOnce sync.Once
}

// Stream 建立容器终端会话（校验归属/UID/容器/运行态后走 SPDY exec）。
func (r *K8sRuntimeReader) Stream(ctx context.Context, workloadID, podName, podUID, container, shell string) (workload.ExecSession, error) {
	if r.clientset == nil || r.cfg == nil {
		return nil, workload.ErrTerminalDisabled
	}
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return nil, fmt.Errorf("无租户上下文")
	}
	ns := tenant.Namespace(tid)
	pod, err := r.clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod 不存在: %s", podName)
	}
	if pod.Labels[labels.KeyTenant] != tid || pod.Labels[labels.KeyWorkload] != workloadID {
		return nil, fmt.Errorf("pod 不存在: %s", podName)
	}
	if podUID != "" && string(pod.UID) != podUID {
		return nil, workload.ErrUIDMismatch
	}
	// 仅 Running 容器可 exec（Pending/终止容器无 shell 进程可附）。
	if container == "" {
		if len(pod.Spec.Containers) == 0 {
			return nil, fmt.Errorf("容器不存在")
		}
		container = pod.Spec.Containers[0].Name
	}
	var cs *corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == container {
			cs = &pod.Status.ContainerStatuses[i]
			break
		}
	}
	if cs == nil {
		return nil, fmt.Errorf("容器不存在: %s", container)
	}
	if cs.State.Running == nil || pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return nil, fmt.Errorf("容器未在运行（仅 Running 容器支持终端）")
	}
	if err := requireManagedDeployment(ctx, r.clientset, ns, workloadID, pod.OwnerReferences); err != nil {
		return nil, err
	}
	req := r.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{shell},
			Stdin:   true, Stdout: true, Stderr: true, TTY: true,
			Container: container,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(r.cfg, http.MethodPost, req.URL())
	if err != nil {
		return nil, fmt.Errorf("终端通道建立失败: %w", err)
	}
	stdinR, stdinW := io.Pipe()   // 浏览器输入 → stdinR → 容器
	stdoutR, stdoutW := io.Pipe() // 容器输出 → stdoutW → 浏览器
	tq := &terminalSizeQueue{resize: make(chan remotecommand.TerminalSize, 4)}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	s := &k8sExecSession{stdin: stdinW, stdout: stdoutR, sizeQueue: tq, cancel: cancel, done: done}
	go func() {
		err := exec.StreamWithContext(sctx, remotecommand.StreamOptions{
			Stdin:             stdinR,
			Stdout:            stdoutW,
			Stderr:            stdoutW, // 终端模式 stderr 并入 stdout（xterm 单流）
			Tty:               true,
			TerminalSizeQueue: tq,
		})
		_ = stdoutW.Close()
		_ = stdinR.Close()
		done <- err
	}()
	return s, nil
}

func (s *k8sExecSession) Stdin() io.WriteCloser { return s.stdin }
func (s *k8sExecSession) Stdout() io.Reader     { return s.stdout }
func (s *k8sExecSession) Resize(cols, rows uint16) {
	select {
	case s.sizeQueue.resize <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default: // 丢弃过时的 resize（最新尺寸会在下一次 resize 或初始尺寸生效）
	}
}
func (s *k8sExecSession) Wait() error {
	return <-s.done
}
func (s *k8sExecSession) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		_ = s.stdin.Close()
	})
}

// terminalSizeQueue 实现 remotecommand.TerminalSizeQueue（resize 事件通道）。
type terminalSizeQueue struct {
	resize chan remotecommand.TerminalSize
}

func (t *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-t.resize
	if !ok {
		return nil
	}
	return &size
}

// 编译期断言。
var _ workload.RuntimeReader = (*K8sRuntimeReader)(nil)
var _ workload.RuntimeActor = (*K8sRuntimeReader)(nil)
var _ workload.ExecStreamer = (*K8sRuntimeReader)(nil)
var _ workload.ExecSession = (*k8sExecSession)(nil)
