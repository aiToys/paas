// Package workload 是应用的运行形态领域模型。
// 工作负载归属应用（AppID），分三类：Service（长驻）/ Job（一次性）/ CronJob（定时）。
// 本期进程内 mock；Repository 接口已为未来 K8s controller-runtime 编排铺路
// （期望状态 Replicas vs 就绪状态 Ready 分离）。
package workload

import "time"

// 工作负载类型。
const (
	TypeService = "service" // 长驻工作负载（Deployment 语义）
	TypeJob     = "job"     // 一次性任务
	TypeCronJob = "cronjob" // 定时任务
)

// 工作负载状态。
const (
	StatusRunning   = "running"   // 运行中
	StatusDeploying = "deploying" // 部署中
	StatusFailed    = "failed"    // 异常
	StatusSucceeded = "succeeded" // 成功完成（job/cronjob）
	StatusPending   = "pending"   // 等待调度
)

var validTypes = map[string]struct{}{
	TypeService: {},
	TypeJob:     {},
	TypeCronJob: {},
}

// Workload 是应用的一个运行形态实例。
type Workload struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID    string `json:"appId"`              // 归属应用
	EnvID    string `json:"envId"`              // 归属环境
	LaneID   string `json:"laneId"`             // "default"=基线（单例）；其他=泳道（预留，本期不创建非 default）
	Type     string `json:"type"`               // service / job / cronjob
	Name     string `json:"name"`
	Image    string `json:"image"`
	ImageRef string `json:"imageRef,omitempty"` // 不可变 digest（生产部署锁定，Release 编排写入）
	Replicas int    `json:"replicas"`           // 期望副本（service）；job 并行度；cronjob=0
	Ready    int    `json:"ready"`              // 就绪副本
	Status   string `json:"status"`
	Schedule string `json:"schedule,omitempty"` // cronjob 专属 cron 表达式
	Command  string `json:"command,omitempty"`  // 启动命令（可选）
	// Port 是 Service 对外暴露端口（service 类型且 >0 时建 K8s Service，让其他 Pod 能 DNS 解析）。
	Port          int       `json:"port,omitempty"`
	ContainerPort int       `json:"containerPort,omitempty"` // Pod 监听端口；0 时取 Port
	// Domain 是对外暴露域名（service 类型且非空时，reconciler 自动建 Ingress，host=Domain -> Service:Port）。
	// 让平台用户经「工作负载 spec.domain」声明应用域名，无需手写 Ingress yaml。
	Domain        string    `json:"domain,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// LaneDefault 是基线泳道的标识。基线 = 环境内稳定默认部署（每应用每环境唯一）。
// 本期所有部署都是基线；泳道（非 default）预留不实现路由。
const LaneDefault = "default"

// Validate 校验工作负载字段。
// 规则：type 合法、name/image/envId 非空、cronjob 须有 schedule。
// EnvID 必填：工作负载必须归属某环境（与 dataservice/governance 一致），
// 否则 allowProd 无法判定环境类型 → developer 可绕过 prod:write 创建无环境归属的生产负载。
func (w Workload) Validate() error {
	if _, ok := validTypes[w.Type]; !ok {
		return errInvalid("type")
	}
	if w.Name == "" {
		return errInvalid("name")
	}
	if w.Image == "" {
		return errInvalid("image")
	}
	if w.EnvID == "" {
		return errInvalid("envId")
	}
	if w.Type == TypeCronJob && w.Schedule == "" {
		return errInvalid("schedule")
	}
	return nil
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }

// Instance 是工作负载的一个运行实例（Pod 级），用于详情页展示真实运行态。
// 数据面（K8s）为真源：StatusReader.Instances 查 Pod label paas.aitoys/workload=<id> 回填。
// 非集群部署（无 clientset）返空切片（降级，与 List 状态回填同构）。
type Instance struct {
	Name      string    `json:"name"`               // Pod 名
	Status    string    `json:"status"`             // Pending/Running/Succeeded/Failed/Unknown
	Ready     string    `json:"ready,omitempty"`    // "1/1" 就绪/总数容器
	Restarts  int       `json:"restarts"`           // 重启次数（ Containers 重启次数合计）
	Node      string    `json:"node,omitempty"`     // 调度到的节点
	IP        string    `json:"ip,omitempty"`       // Pod IP
	StartedAt time.Time `json:"startedAt,omitempty"` // 启动时间
	Message   string    `json:"message,omitempty"`  // 状态原因/事件（Pending/Failed 时有意义）
}

// Detail 是工作负载详情（GET /api/workloads/{id}）：期望态 + 实际运行实例聚合。
type Detail struct {
	Workload  Workload   `json:"workload"`
	Instances []Instance `json:"instances"`
}
