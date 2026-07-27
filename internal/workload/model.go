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
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID     string    `json:"appId"`              // 归属应用
	Type      string    `json:"type"`               // service / job / cronjob
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	Replicas  int       `json:"replicas"` // 期望副本（service）；job 并行度；cronjob=0
	Ready     int       `json:"ready"`    // 就绪副本
	Status    string    `json:"status"`
	Schedule  string    `json:"schedule,omitempty"` // cronjob 专属 cron 表达式
	Command   string    `json:"command,omitempty"`  // 启动命令（可选）
	CreatedAt time.Time `json:"createdAt"`
}

// Validate 校验工作负载字段。
// 规则：type 合法、name/image 非空、cronjob 须有 schedule。
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
	if w.Type == TypeCronJob && w.Schedule == "" {
		return errInvalid("schedule")
	}
	return nil
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
