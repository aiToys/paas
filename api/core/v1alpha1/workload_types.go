// Package v1alpha1 是 Workload CRD 的 API 定义（控制面下发的期望状态）。
// group core.aitoys.github.com；WorkloadReconciler watch 并落到 K8s Deployment/Job/CronJob。
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GPURequest 是 GPU 资源请求（显存核算 + 反亲和）。
type GPURequest struct {
	// Count 是请求的 GPU 卡数（映射 nvidia.com/gpu resource）。
	Count int `json:"count,omitempty"`
	// MemoryMB 是请求显存 MB（显存核算参考；本期以 Count 为准，细粒度留后续）。
	MemoryMB int `json:"memoryMB,omitempty"`
}

// WorkloadSpec 是工作负载期望状态（控制面下发，reconciler 据此落 K8s 资源）。
type WorkloadSpec struct {
	TenantID string     `json:"tenantId"`
	AppID    string     `json:"appId"`
	EnvID    string     `json:"envId"`
	LaneID   string     `json:"laneId,omitempty"`
	Service  string     `json:"service,omitempty"` // 同 app 多服务区分（product/recommend/...）；空=单服务
	Type     string     `json:"type"`              // service|job|cronjob
	Name     string     `json:"name"`
	Image    string     `json:"image"`
	ImageRef string     `json:"imageRef,omitempty"`
	Replicas int32      `json:"replicas,omitempty"` // service 副本；job 并行度
	Schedule string     `json:"schedule,omitempty"` // cronjob 专属
	Command  string     `json:"command,omitempty"`
	GPU      GPURequest `json:"gpu,omitempty"`
	// Port 是 Service 对外暴露端口（service 类型且 >0 时 reconciler 才建 K8s Service）。
	// +kubebuilder:validation:Minimum=1
	Port int32 `json:"port,omitempty"`
	// ContainerPort 是 Pod 监听端口；为 0 时 reconciler 取 Port。建 Service 时映射 Port→ContainerPort，
	// 并据此给容器声明 containerPort + readiness probe（TCP，零侵入）。
	// +kubebuilder:validation:Minimum=1
	ContainerPort int32 `json:"containerPort,omitempty"`
	// Domain 是工作负载对外暴露的域名（service 类型且非空时，reconciler 自动建 networkingv1.Ingress，
	// host=Domain，path / -> 本工作负载 Service:Port）。空则不建 Ingress（仅集群内 DNS 可达）。
	// ingressClassName 取 reconciler.IngressClass（env PAAS_INGRESS_CLASS，默认 hermes）。
	Domain string `json:"domain,omitempty"`
	// Resources 是容器资源规格（CPU/内存 requests/limits，K8s Quantity 字符串）。
	// 空字段跳过映射（BestEffort）；生产环境由控制面 handler 强制非空（禁 BestEffort）。
	Resources ResourceSpec `json:"resources,omitempty"`
}

// ResourceSpec 是容器资源规格（K8s Quantity 字符串，如 "500m"/"2"/"512Mi"/"1Gi"）。
// 与 internal/workload.ResourceSpec / application.ResourceTemplate 同构。
type ResourceSpec struct {
	CPURequest string `json:"cpuRequest,omitempty"`
	CPULimit   string `json:"cpuLimit,omitempty"`
	MemRequest string `json:"memRequest,omitempty"`
	MemLimit   string `json:"memLimit,omitempty"`
}

// WorkloadStatus 是 reconcile 后的实际状态（reconciler 回写）。
type WorkloadStatus struct {
	Ready  int32  `json:"ready"`
	Status string `json:"status"` // running|deploying|failed
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
//
// Workload 是平台工作负载的 K8s 期望状态声明。
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkloadSpec   `json:"spec,omitempty"`
	Status            WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
//
// WorkloadList 是 Workload 列表。
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}
