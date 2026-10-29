// 授权诊断目标（spec §6.2）：服务端解析、不可由客户端修改的快照。
// 定义在 diagnose（执行域）而非 assistant（外壳），依赖方向 assistant → diagnose 单向。
package diagnose

import "time"

const (
	// TargetTypeWorkload 诊断目标类型（首期仅 workload/Deployment）。
	TargetTypeWorkload = "workload"
)

// AuthorizedTarget 服务端解析后的目标快照。记录资源 UID 与解析时间；
// 重新发布致 Deployment UID 替换时执行器停止并提示重选目标。
type AuthorizedTarget struct {
	Type          string    `json:"type"` // workload
	WorkloadID    string    `json:"workloadId"`
	WorkloadName  string    `json:"workloadName"`
	AppID         string    `json:"appId"`
	EnvID         string    `json:"envId"`
	EnvName       string    `json:"envName,omitempty"`
	LaneID        string    `json:"laneId,omitempty"`
	Service       string    `json:"service,omitempty"`
	Namespace     string    `json:"namespace"` // 服务端解析（paas-t-<tenant>）
	DeploymentUID string    `json:"deploymentUID,omitempty"`
	ResolvedAt    time.Time `json:"resolvedAt"`
}
