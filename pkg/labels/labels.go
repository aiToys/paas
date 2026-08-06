// Package labels 集中平台 K8s label/annotation key 常量（单一真源，防拼写漂移）。
//
// controller 给 Pod/Service/STS 打这些 label，dataplane/status_reader 按 label 过滤与越权校验
// （如 status_reader.go 跨租户/跨工作负载隔离依赖 Labels[KeyTenant]/Labels[KeyWorkload] 精确匹配）。
// 散落字符串易拼错导致隔离失效，故集中。
package labels

// Domain 是平台 K8s label/annotation 命名域。
const Domain = "paas.aitoys"

// Label key（Pod/Service/STS/Job 共用，selector 与越权校验依赖）。
const (
	KeyTenant      = Domain + "/tenant"      // 租户隔离（多租户必带，status_reader/dataplane 越权校验依据）
	KeyApp         = Domain + "/app"         // 工作负载归属应用（observability 应用级查询依据）
	KeyWorkload    = Domain + "/workload"    // 工作负载名（Pod→Workload 反查）
	KeyDataservice = Domain + "/dataservice" // 数据服务名（Pod→DataService 反查）
	KeyKind        = Domain + "/kind"        // 数据服务 Kind（db/cache/...）
)

// Annotation key（不进 selector，可变更，不触发 Pod 重建）。
const (
	KeyRestartedAt = Domain + "/restarted-at"  // 触发 STS 滚动重建（值变化 → Pod 重建）
	KeyDisplayName = Domain + "/display-name"  // 用户起的展示名（kubectl describe 直观辨认）
)
