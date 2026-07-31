package builder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// K8sJob 把构建执行体下沉到独立 K8s Job Pod（DooD：挂节点 docker.sock，复用节点 daemon，
// 非 privileged）。core 创建 batch/Job → 轮询完成 → 取 Pod 日志解析 PAAS_DIGEST 行。
//
// 依赖：core 的 ServiceAccount 对 batch/jobs（create/get）+ pods（list）+ pods/log（get）
// 有权限（deploy/charts/paas/templates/rbac.yaml）。controller-runtime typed client 不支持
// pods/log 子资源，故用 client-go kubernetes.Interface。
type K8sJob struct {
	Clientset    kubernetes.Interface
	Namespace    string // Job 落地 namespace（PAAS_K8S_NAMESPACE）
	BuilderImage string // 默认 docker:git；内网 hub.wang.dd:5000/library/docker:git
	// 凭证/仓库（Params 字段为空时回退，与 Real 同款语义）。
	Registry     string
	GitToken     string
	RegistryUser string
	RegistryPass string
	// 可注入（测试）；零值用默认。
	PollInterval time.Duration
	BuildTimeout time.Duration
}

// Real 标记（Store 据此为构建预留长超时；与 Real 同接口契约）。
func (K8sJob) Real() bool { return true }

// Build 创建构建 Job 并阻塞轮询至完成，返不可变 digest。失败返全量 Pod 日志作 Log。
func (k K8sJob) Build(ctx context.Context, p Params) (Result, error) {
	// 全局凭证回退（Params 单条为空时用 K8sJob 配置，与 Real 一致）。
	if p.Registry == "" {
		p.Registry = k.Registry
	}
	if p.GitToken == "" {
		p.GitToken = k.GitToken
	}
	if p.RegistryUser == "" {
		p.RegistryUser = k.RegistryUser
	}
	if p.RegistryPass == "" {
		p.RegistryPass = k.RegistryPass
	}

	// cloneURL 在 Go 侧注入 token（避免 shell 拼接面）；Job 脚本纯读 env。
	cloneURL := injectToken(p.GitURL, p.GitToken)

	// tag：commit 已知用 branch-commit8（与 Real/Mock 一致），空则用 buildID8 派生（Store 通常已 mockCommit 填，此为兜底）。
	tagCommit := p.Commit
	if tagCommit == "" {
		tagCommit = p.BuildID
	}
	tag := p.Branch + "-" + safeShort(tagCommit, 8)
	ref := ImageRef(p, tag)

	jobName := sanitizeK8sName("paas-build-" + p.BuildID)
	ns := k.Namespace
	if ns == "" {
		ns = "default"
	}
	image := k.BuilderImage
	if image == "" {
		image = "docker:git"
	}
	poll := k.PollInterval
	if poll == 0 {
		poll = 3 * time.Second
	}
	deadline := k.BuildTimeout
	if deadline == 0 {
		deadline = 30 * time.Minute
	}

	job := buildJobSpec(image, p, jobName, ref, cloneURL)
	if _, err := k.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return Result{}, fmt.Errorf("创建构建 Job 失败: %w", err)
	}

	// 轮询 Job 状态（KISS：轮询而非 watch+回调，构建本就长耗时；watch 断线重连复杂度过高）。
	timeoutCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		js, err := k.Clientset.BatchV1().Jobs(ns).Get(timeoutCtx, jobName, metav1.GetOptions{})
		if err != nil {
			// Job 被外部删除或集群不可达：尽力取已有日志，返错。
			return Result{Log: k.podLogs(ctx, ns, jobName)}, fmt.Errorf("查询构建 Job 失败: %w", err)
		}
		if js.Status.Succeeded > 0 {
			logs := k.podLogs(ctx, ns, jobName)
			digest, perr := parseDigest(logs)
			if perr != nil {
				return Result{Log: logs}, fmt.Errorf("解析 digest 失败（日志无 PAAS_DIGEST 行）: %w", perr)
			}
			return Result{Digest: digest, Tag: tag, Log: logs}, nil
		}
		if js.Status.Failed > 0 {
			logs := k.podLogs(ctx, ns, jobName)
			return Result{Log: logs}, fmt.Errorf("构建 Job 失败（见 Pod 日志）\n%s", logs)
		}
		select {
		case <-timeoutCtx.Done():
			logs := k.podLogs(context.Background(), ns, jobName)
			return Result{Log: logs}, fmt.Errorf("构建 Job 超时（%s）\n%s", deadline, logs)
		case <-time.After(poll):
		}
	}
}

// podLogs 取 Job 关联 Pod 的容器日志（找 job-name=<jobName> 标签的 Pod）。
// Pod 不存在或取日志失败时返空串（不阻断错误路径，主错误已在 Job 状态）。
func (k K8sJob) podLogs(ctx context.Context, ns, jobName string) string {
	pods, err := k.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	podName := pods.Items[0].Name
	req := k.Clientset.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{Container: "builder"})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer func() { _ = stream.Close() }()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return buf.String()
	}
	return buf.String()
}

// buildJobSpec 构造 batch/Job（DooD：docker:git + 挂节点 docker.sock，非 privileged）。
// 纯函数，便于单测。
func buildJobSpec(builderImage string, p Params, jobName, ref, cloneURL string) *batchv1.Job {
	const dockerSock = "/var/run/docker.sock"
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: jobName,
			Labels: map[string]string{
				"app":                  "paas-builder",
				"paas.devops/build-id": p.BuildID,
				"paas.devops/tenant":   p.TenantID,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: ptr.To[int32](86400), // 1 天后自动清，保留排查窗口
			BackoffLimit:            ptr.To[int32](0),     // 失败不重试，避免重复 build/push
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "paas-builder"},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "builder",
						Image:   builderImage,
						Command: []string{"/bin/sh", "-c"},
						Args:    []string{builderScript},
						Env: []corev1.EnvVar{
							{Name: "CLONE_URL", Value: cloneURL},
							{Name: "BRANCH", Value: p.Branch},
							{Name: "APP_ID", Value: p.AppID},
							{Name: "REGISTRY", Value: p.Registry},
							{Name: "REF", Value: ref},
							{Name: "COMMIT", Value: p.Commit},
							{Name: "DOCKERFILE", Value: p.Dockerfile},
							{Name: "BUILD_CONTEXT", Value: p.BuildContext},
							{Name: "REGISTRY_USER", Value: p.RegistryUser},
							{Name: "REGISTRY_PASS", Value: p.RegistryPass},
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "docker-sock",
							MountPath: dockerSock,
						}},
						// 无 SecurityContext.Privileged：DooD 关键（仅读写 sock 文件，复用节点 daemon）。
					}},
					Volumes: []corev1.Volume{{
						Name: "docker-sock",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: dockerSock,
								Type: ptr.To(corev1.HostPathSocket),
							},
						},
					}},
				},
			},
		},
	}
}

var digestRe = regexp.MustCompile(`(?m)^PAAS_DIGEST=(sha256:[0-9a-f]+)$`)

// parseDigest 从 Pod 日志提取 PAAS_DIGEST 行的 sha256。无匹配返错（BuildRun 记 failed）。
func parseDigest(logs string) (string, error) {
	m := digestRe.FindStringSubmatch(logs)
	if m == nil {
		return "", fmt.Errorf("日志未含 PAAS_DIGEST 标记行")
	}
	return m[1], nil
}

// sanitizeK8sName 把任意字符串规整为合法 K8s 资源名（DNS-1123 label：小写字母数字-，≤63）。
func sanitizeK8sName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}
