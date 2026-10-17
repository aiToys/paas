// Package workload —— 服务运行态与实例运维（spec 2026-10-17）。
//
// 本文件是运行态读取/写操作的领域契约：观察（Observation）显式区分「读不到」与「没有」，
// 实例带 UID/容器/conditions，能力（Capability）解释每个操作的可用性。
// 依赖倒置：本包不依赖 K8s，由 cmd/core 注入 controller.K8sRuntimeReader。
package workload

import (
	"context"
	"io"
	"time"
)

// 观察来源与状态。
const (
	ObsSourceKubernetes = "kubernetes"
	ObsSourceExternal   = "external"
	ObsSourceNone       = "unavailable"

	ObsStateFresh       = "fresh"
	ObsStateStale       = "stale"
	ObsStateUnavailable = "unavailable"
)

// Observation 描述运行态数据面的采集状况。
// source=unavailable 表示非集群部署/读取器未注入——空实例列表不能声称 fresh（假健康）。
type Observation struct {
	Source     string    `json:"source"`
	State      string    `json:"state"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
	Error      string    `json:"error,omitempty"` // 脱敏错误；不得含上游凭证
}

// ContainerState 是 Pod 内容器运行态。
type ContainerState struct {
	Name                  string `json:"name"`
	State                 string `json:"state"` // waiting|running|terminated
	Ready                 bool   `json:"ready"`
	Restarts              int    `json:"restarts"`
	Reason                string `json:"reason,omitempty"` // waiting/termination 原因（CrashLoopBackOff 等）
	Message               string `json:"message,omitempty"`
	LastExitCode          int32  `json:"lastExitCode,omitempty"` // 最近一次退出码
	LastTerminationReason string `json:"lastTerminationReason,omitempty"`
}

// PodCondition 是 Pod condition（Ready/PodScheduled 等的原样结构）。
type PodCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// RuntimeInstance 是运行实例（Pod 级）的完整形态：在 Instance 基础上补 UID/容器/conditions。
// UID 是写操作（重建/终端）的目标校验键——名字可复用，UID 不可。
type RuntimeInstance struct {
	Instance
	UID               string           `json:"uid,omitempty"`
	Reason            string           `json:"reason,omitempty"` // Pod 级等待/终止原因
	Conditions        []PodCondition   `json:"conditions,omitempty"`
	DeletionTimestamp *time.Time       `json:"deletionTimestamp,omitempty"`
	Containers        []ContainerState `json:"containers,omitempty"`
}

// Capability 说明一个操作是否可用及原因。allowed 仅作展示提示；操作请求服务端仍重新鉴权。
type Capability struct {
	Allowed    bool   `json:"allowed"`
	ReasonCode string `json:"reasonCode,omitempty"` // not_cluster|wrong_type|zero_replicas|disabled|forbidden
	Message    string `json:"message,omitempty"`
}

// RuntimeCapabilities 是运行页各操作的可用性集合。
type RuntimeCapabilities struct {
	Logs     Capability `json:"logs"`
	Events   Capability `json:"events"`
	Restart  Capability `json:"restart"`
	Recreate Capability `json:"recreate"`
	Exec     Capability `json:"exec"`
}

// Runtime 是 GET /api/workloads/{id}/runtime 的聚合响应。
type Runtime struct {
	Workload        Workload            `json:"workload"`
	Observation     Observation         `json:"observation"`
	Instances       []RuntimeInstance   `json:"instances"`
	Capabilities    RuntimeCapabilities `json:"capabilities"`
	ActiveOperation *Operation          `json:"activeOperation,omitempty"`
}

// PodEvent 是与某 Pod 关联的 K8s 事件（结构化，按 UID 精确过滤）。
type PodEvent struct {
	Type    string    `json:"type"` // Normal|Warning
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Count   int32     `json:"count,omitempty"`
	At      time.Time `json:"at"`
}

// RuntimeReader 是运行态读取的依赖倒置接口（cmd/core 注入 controller.K8sRuntimeReader）。
// 所有方法必须验证 ctx 租户与 workload 归属；错误信息脱敏（不含 K8s 凭证/内部地址）。
type RuntimeReader interface {
	// Runtime 读取某工作负载的完整运行态（观察 + 实例 + 容器）。非集群返回 source=unavailable。
	Runtime(ctx context.Context, workloadID string) (Runtime, error)
	// PodEvents 返回某 Pod（UID 精确匹配）的关联事件。UID 空时按名字过滤（向后兼容旧调用）。
	PodEvents(ctx context.Context, workloadID, podName, podUID string) ([]PodEvent, error)
	// ContainerLogs 容器级日志：container 非空按容器过滤（多容器 Pod 选定目标）；
	// podUID 非空时精确匹配（防实例名复用读到别的实例日志）。previous 取上次终止容器。
	ContainerLogs(ctx context.Context, workloadID, podName, podUID, container string, tailLines int64, previous bool) (io.ReadCloser, error)
	// RestartProgress 观察滚动重启进度：phase ∈ {notfound, progressing, running, completed, superseded}。
	// completed 需新 token 的 RS 已 updated/ready/available 达到期望；superseded = 已有更新的发布覆盖。
	RestartProgress(ctx context.Context, workloadID, token string) (phase string, detail string, err error)
	// RecreateProgress 观察实例重建进度：phase ∈ {progressing, completed}。
	// completed = 旧 UID 已退场且同 workload 出现 Ready 新 Pod。
	RecreateProgress(ctx context.Context, workloadID, oldUID string) (phase string, detail string, err error)
}

// RuntimeActor 是运行态写操作的依赖倒置接口（数据面执行器）。
type RuntimeActor interface {
	// Evict 驱逐一个 Pod（Eviction API，尊重 PDB 与优雅退出）。
	// 校验：Pod 归属本租户 + 本 workload + UID 匹配 + 属平台托管控制器（非裸 Pod）。
	// PDB 阻断返回 429 语义错误（ErrTooManyRequests）；UID 不匹配返回 ErrUIDMismatch。
	Evict(ctx context.Context, workloadID, podName, podUID string) error
}

// ExecSession 是一个已建立的容器终端会话（依赖倒置：workload 不依赖 client-go）。
type ExecSession interface {
	Stdin() io.WriteCloser // 终端输入通道（写进容器）
	Stdout() io.Reader     // 终端输出通道（容器 stdout/stderr 合流）
	Resize(cols, rows uint16)
	Wait() error // 阻塞至 shell 退出
	Close()      // 主动终止会话
}

// ExecStreamer 建立容器终端会话（cmd/core 桥接 client-go remotecommand）。
type ExecStreamer interface {
	Stream(ctx context.Context, workloadID, podName, podUID, container, shell string) (ExecSession, error)
}
