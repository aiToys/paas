// Package builder 提供构建执行体（mock/process/k8s）+ 构建日志流（k8s Pod logs follow）。
//
// BuildLogStreamer 把「构建中 Job Pod 的实时日志」暴露为 io.ReadCloser（follow），
// 供 devops handler SSE 端点转发给前端，让构建排障等同 GitHub Actions（逐行流式）。
// 集群外（clientset nil）不装配 → handler 降级 503。
package builder

import (
	"context"
	"errors"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aitoys/paas/pkg/tenant"
)

// ErrNoBuildPod 表示构建 Job 的 Pod 尚未就绪（ContainerCreating）或不存在。
// handler 收到此错误时发 SSE 心跳保连接，等 Pod ready 后重试。
var ErrNoBuildPod = errors.New("build pod not ready or not found")

// logTailLines 限制 follow 流的起始回溯行数（防超大日志一次性灌满；新日志持续追加）。
const logTailLines int64 = 1000

// BuildLogStreamer 暴露构建 Pod 的实时日志流（follow）。
// 依赖倒置：devops handler 经此接口拿流，不直接持有 k8s clientset。
type BuildLogStreamer interface {
	// StreamBuildLogs 返回 buildID 对应构建 Pod 的 follow 日志流。
	// 调用方负责 Close。Pod 未就绪/不存在返 ErrNoBuildPod。
	StreamBuildLogs(ctx context.Context, buildID, tenantID string) (io.ReadCloser, error)
}

// K8sBuildLogStreamer 经 clientset 拉 Job Pod 的 follow 日志。
// clientset 为 nil 时 NewK8sBuildLogStreamer 返 nil（cmd/core 装配时判空降级）。
type K8sBuildLogStreamer struct {
	Clientset kubernetes.Interface
}

// NewK8sBuildLogStreamer 构造 streamer；cs nil 时返 nil（集群外降级）。
// 返回接口类型而非具体指针：cs nil 时返真 nil 接口（非 typed-nil），避免 WithBuildLogStreamer
// 装箱后 h.logStreamer != nil 误判（Go 接口装箱语义：(*K8sBuildLogStreamer)(nil) 装箱为非 nil 接口，
// 致 handler 降级分支不触发，前端等 30s 才收到「Pod 未就绪」而非「非集群部署」）。
func NewK8sBuildLogStreamer(cs kubernetes.Interface) BuildLogStreamer {
	if cs == nil {
		return nil
	}
	return &K8sBuildLogStreamer{Clientset: cs}
}

// StreamBuildLogs 找 job-name=<BuildJobName(buildID)> 的 Pod，GetLogs(follow, tail=1000) 返流。
// Pod 未就绪/不存在返 ErrNoBuildPod（handler 发心跳等）。
func (s *K8sBuildLogStreamer) StreamBuildLogs(ctx context.Context, buildID, tenantID string) (io.ReadCloser, error) {
	if s == nil || s.Clientset == nil {
		return nil, ErrNoBuildPod
	}
	ns := tenant.Namespace(tenantID)
	jobName := BuildJobName(buildID)
	pods, err := s.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, ErrNoBuildPod
	}
	podName := pods.Items[0].Name
	tail := logTailLines
	req := s.Clientset.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Container:  "builder",
		Follow:     true,
		TailLines:  &tail,
		Timestamps: false,
	})
	return req.Stream(ctx)
}
