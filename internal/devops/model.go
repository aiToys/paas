// Package devops 是 DevOps 子系统的领域模型：代码 -> 构建 -> 镜像 -> 发布 -> 回滚 主链路。
//
// 四实体：
//   - CodeRepo：代码仓库绑定（Git URL/分支/Dockerfile），归属应用
//   - BuildRun：构建运行实例，mock CI runner 异步流转 pending->running->success 并产出 Image
//   - Image：构建产物，digest 不可变真源（生产部署锁这个），tag 可变
//   - Release：发布单，把某镜像以某策略部署到某环境，编排目标环境的基线 Workload
//
// 本期进程内 mock（mock CI runner 模拟构建产出，不接真实 Git/Docker/Registry）；
// Repository 接口为未来接真实 OCI registry / Tekton / Argo 铺路。
package devops

import "time"

// 代码仓库状态。
const (
	RepoStatusActive   = "active"
	RepoStatusDisabled = "disabled"
)

// 构建触发来源。
const (
	TriggerPush   = "push"
	TriggerManual = "manual"
	TriggerPR     = "pr"
)

// 构建运行状态。
const (
	BuildPending = "pending"
	BuildRunning = "running"
	BuildSuccess = "success"
	BuildFailed  = "failed"
)

// 镜像状态。
const (
	ImageReady = "ready"
)

// 发布策略。起步只实现 rolling；blue-green/canary 接口预留，归后续切片。
const (
	StrategyRolling   = "rolling"
	StrategyBlueGreen = "blue-green"
	StrategyCanary    = "canary"
)

// 发布状态。
const (
	ReleasePending    = "pending"
	ReleaseDeploying  = "deploying"
	ReleaseSucceeded  = "succeeded"
	ReleaseFailed     = "failed"
	ReleaseRolledBack = "rolled-back"
)

// DefaultDockerfile / DefaultBuildContext 是未指定时的默认值。
const (
	DefaultDockerfile   = "Dockerfile"
	DefaultBuildContext = "."
)

// CodeRepo 是应用绑定的代码仓库（Git）。归属应用，一个应用可绑多个仓库。
type CodeRepo struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID        string    `json:"appId"`
	GitURL       string    `json:"gitUrl"`
	Branch       string    `json:"branch"`
	Dockerfile   string    `json:"dockerfile"`
	BuildContext string    `json:"buildContext"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Validate 校验仓库字段：GitURL/Branch 必填；Dockerfile/BuildContext 空则由调用方补默认值。
func (r CodeRepo) Validate() error {
	if r.GitURL == "" {
		return errInvalid("gitUrl")
	}
	if r.Branch == "" {
		return errInvalid("branch")
	}
	return nil
}

// BuildRun 是一次构建运行。mock CI runner 创建后异步流转 pending->running->success，
// 成功产出 Image（回填 ImageID）；失败/进行中 ImageID 为空。
type BuildRun struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"`
	AppID      string    `json:"appId"`
	RepoID     string    `json:"repoId"`
	Trigger    string    `json:"trigger"`
	Commit     string    `json:"commit"`
	Branch     string    `json:"branch"`
	Message    string    `json:"message"`
	Status     string    `json:"status"`
	ImageID    string    `json:"imageId,omitempty"`
	Log        string    `json:"log,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// Image 是构建产物。digest 是不可变真源（生产部署锁这个），tag 可变。
// 来源记录 BuildRun + commit，可追溯；归属应用，跨环境复用（test 验证通过晋升 prod）。
type Image struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"`
	AppID      string    `json:"appId"`
	Registry   string    `json:"registry"`
	Tag        string    `json:"tag"`
	Digest     string    `json:"digest"`
	Source     string    `json:"source"`
	Branch     string    `json:"branch"`
	BuildRunID string    `json:"buildRunId"`
	BuiltAt    time.Time `json:"builtAt"`
	Status     string    `json:"status"`
}

// Release 是一次发布：把某镜像以某策略部署到某环境，编排目标环境的基线 Workload。
// PreviousImageID 记录发布前的镜像，用于回滚。生产发布受 prod:write 保护。
type Release struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenantId,omitempty"`
	AppID           string    `json:"appId"`
	EnvID           string    `json:"envId"`
	ImageID         string    `json:"imageId"`
	ImageDigest     string    `json:"imageDigest"` // 冗余快照（部署时镜像 digest）
	Strategy        string    `json:"strategy"`
	Status          string    `json:"status"`
	WorkloadID      string    `json:"workloadId"`
	PreviousImageID string    `json:"previousImageId,omitempty"` // 回滚指针
	IsRollback      bool      `json:"isRollback"`
	CreatedAt       time.Time `json:"createdAt"`
	CreatedBy       string    `json:"createdBy"`
}

// ReleaseInput 是创建发布的输入。编排（找/建 Workload + 更新镜像 + 记录回滚指针）由
// ReleaseRepository.CreateRelease 内部完成，调用方只提供意图。
type ReleaseInput struct {
	AppID     string `json:"appId"`
	EnvID     string `json:"envId"`
	ImageID   string `json:"imageId"`
	Strategy  string `json:"strategy"`
	CreatedBy string `json:"-"` // handler 从身份 ctx 注入，非用户提交
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
