package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:generate=true
//
// DataServiceSpec 是数据服务期望状态（控制面下发，reconciler 据此落 K8s StatefulSet）。
// Kind 取 dataservice 领域常量（db|cache|mq|storage|vector|search）；
// Engine 取 spec.engine（postgres|redis|kafka|...），reconciler 据 Kind+Engine 选容器镜像。
type DataServiceSpec struct {
	TenantID string            `json:"tenantId"`
	AppID    string            `json:"appId,omitempty"`
	EnvID    string            `json:"envId"`
	Kind     string            `json:"kind"`           // db|cache|mq|storage|vector|search
	Name     string            `json:"name"`           // 租户内唯一
	Engine   string            `json:"engine"`         // postgres|redis|kafka|minio|qdrant|meilisearch|...
	Source   string            `json:"source,omitempty"` // managed（平台托管）| external（接入外部，不部署）
	Spec     map[string]string `json:"spec,omitempty"` // 原始表单字段（version/size_gb/...）
	// 实例浅管理字段（控制面下发，reconciler 据此调 STS）：
	//   - Replicas：副本数，nil/0 默认 1（写 0 = 停，scale 0）；多副本集群/HA 留后续。
	//   - CPU/Memory：覆盖默认 resources request（空 = defaultResources）。
	//   - StorageGB：PVC 容量 GiB（0 = 默认 10）；仅扩容（K8s 限制不可缩）。
	//   - Image：覆盖默认镜像（含 tag，版本升级用）。
	Replicas  *int32 `json:"replicas,omitempty"`
	CPU       string `json:"cpu,omitempty"`
	Memory    string `json:"memory,omitempty"`
	StorageGB int32  `json:"storageGb,omitempty"`
	Image     string `json:"image,omitempty"`
	// Connection 平台生成的连接信息（host/port/credentials/uri），控制面下发，reconciler 读：
	//   - 敏感 key（password/token/secretKey/database/accessKey）→ 建 Secret，Pod env 引用；
	//   - host/port/uri → 可观测/调试（不进 Secret）。
	Connection map[string]string `json:"connection,omitempty"`
}

// DataServiceStatus 是 reconcile 后的实际状态（reconciler 回写）。
type DataServiceStatus struct {
	Ready int32  `json:"ready"`
	Phase string `json:"phase"` // running|creating|failed
	Image string `json:"image"` // 实际落地的容器镜像（可观测）
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
//
// DataService 是平台数据服务资源的 K8s 期望状态声明。
type DataService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DataServiceSpec   `json:"spec,omitempty"`
	Status            DataServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
//
// DataServiceList 是 DataService 列表。
type DataServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DataService `json:"items"`
}
