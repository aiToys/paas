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

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// 代码仓库状态。
const (
	RepoStatusActive   = "active"
	RepoStatusDisabled = "disabled"
)

// 代码仓库来源（一站式：内置 Gitea 为主，兼容外部 GitHub/GitLab）。
const (
	// RepoSourceInternal 内置 Gitea 仓库：创建时 PaaS 调 Gitea API 建仓，clone 走内网。
	RepoSourceInternal = "internal"
	// RepoSourceExternal 外部仓库：用户填 gitUrl，clone 走公网/外部。
	RepoSourceExternal = "external"
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
	ID           string `json:"id"`
	TenantID     string `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID        string `json:"appId"`
	GitURL       string `json:"gitUrl"` // external：用户填的外部 git URL；internal：Gitea 建仓后回填内网 clone URL
	Branch       string `json:"branch"`
	Dockerfile   string `json:"dockerfile"`
	BuildContext string `json:"buildContext"`
	Status       string `json:"status"`
	// Source 标识仓库来源（internal/external）。空视为 external（兼容历史数据）。
	Source string `json:"source"`
	// GiteaOwner/GiteaRepo 仅 internal 有效：内置 Gitea 的 owner/repo（owner 固定 paas-bot）。
	GiteaOwner string `json:"giteaOwner,omitempty"`
	GiteaRepo  string `json:"giteaRepo,omitempty"`
	// CloneURL 含凭证的 git clone URL（internal：含 paas-bot basic auth；external 空）。
	// json:"-" 永不序列化到响应（防凭证泄漏前端）；builder 内部 Go 调用直接读此字段。
	CloneURL  string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

// Validate 校验仓库字段：external 时 GitURL/Branch 必填；internal 时 GitURL 可空（建仓后回填），
// 但 Branch 必填（Gitea 默认分支）。Dockerfile/BuildContext 空则由调用方补默认值。
func (r CodeRepo) Validate() error {
	if r.Source == "" {
		r.Source = RepoSourceExternal
	}
	if r.Source == RepoSourceInternal {
		// 内置仓库：GitURL 由 Gitea 建仓后回填，校验时可不要求；Branch 必填（默认分支）。
		if r.Branch == "" {
			return errInvalid("branch")
		}
		return nil
	}
	// 外部仓库：GitURL/Branch 必填。
	if r.GitURL == "" {
		return errInvalid("gitUrl")
	}
	if r.Branch == "" {
		return errInvalid("branch")
	}
	if err := validateExternalGitURL(r.GitURL); err != nil {
		return err
	}
	if err := validateBranch(r.Branch); err != nil {
		return err
	}
	return nil
}

// validateExternalGitURL 校验外部仓库 GitURL，防多租户攻击面：
//   - scheme 仅 http/https：拒绝 file:///ssh:///ext:: 等（git 历史多处 ext:: RCE，如 CVE-2018-17456）
//   - 拒绝云元数据端点 169.254.169.254：防 builder 把 PAAS_GIT_TOKEN 拼成 https://<token>@<host> 后
//     被诱导发往云元数据接口窃取节点云凭证
//   - 拒绝 user info 内已含凭证的 URL（防前端把明文凭证写入 GitURL）
//
// 不拒绝私网段（10/172.16/192.168）：企业内部 GitLab 属合法场景。
func validateExternalGitURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errInvalid("gitUrl")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errInvalid("gitUrl")
	}
	if u.User != nil {
		return errInvalid("gitUrl") // GitURL 不得自带凭证（凭证由 injectToken 从 env 注入）
	}
	host := u.Hostname()
	if isMetadataHost(host) {
		return errInvalid("gitUrl")
	}
	return nil
}

// isMetadataHost 判定是否云元数据/回环等高敏地址（builder 不得主动连）。
func isMetadataHost(host string) bool {
	h := strings.ToLower(host)
	switch h {
	case "169.254.169.254", "metadata", "metadata.google.internal":
		return true
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// validateBranch 校验分支名，防 git argv flag 注入（Branch 作 --branch 传入）。
// 仅允许字母/数字/._- 和路径分隔（/），拒绝 -- 前缀与 shell 元字符。
func validateBranch(b string) error {
	if b == "" || strings.HasPrefix(b, "-") {
		return errInvalid("branch")
	}
	for _, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-', c == '/':
		default:
			return errInvalid("branch")
		}
	}
	return nil
}

// BuildRun 是一次构建运行。mock CI runner 创建后异步流转 pending->running->success，
// 成功产出 Image（回填 ImageID）；失败/进行中 ImageID 为空。
type BuildRun struct {
	ID        string            `json:"id"`
	TenantID  string            `json:"tenantId,omitempty"`
	AppID     string            `json:"appId"`
	RepoID    string            `json:"repoId"`
	Trigger   string            `json:"trigger"`
	Commit    string            `json:"commit"`
	Branch    string            `json:"branch"`
	Message   string            `json:"message"`
	Status    string            `json:"status"`
	ImageID   string            `json:"imageId,omitempty"`
	Log       string            `json:"log,omitempty"`
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	// Dockerfile / BuildContext 构建覆盖（可选，空则用 repo 级配置）：
	// 同仓多形态产物（Go 后端 vs node 前端）按次构建指定不同 Dockerfile。
	Dockerfile   string    `json:"dockerfile,omitempty"`
	BuildContext string    `json:"buildContext,omitempty"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt,omitempty"`
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
	Version    string    `json:"version,omitempty"` // 发布版本号（release stage 写入点；空=未发布）
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
	PromotedFrom    string    `json:"promotedFrom,omitempty"` // 晋升来源 release ID（非空=由 promote 产生）
	Version         string    `json:"version,omitempty"`      // 发布版本号（release stage 写入点）
	LaneID          string    `json:"laneId,omitempty"`       // 部署到的泳道（default=基线）
	SourceRunID     string    `json:"sourceRunId,omitempty"`  // 由哪次 pipeline run 部署（追溯）
	CreatedAt       time.Time `json:"createdAt"`
	CreatedBy       string    `json:"createdBy"`
}

// ReleaseInput 是创建发布的输入。编排（找/建 Workload + 更新镜像 + 记录回滚指针）由
// ReleaseRepository.CreateRelease 内部完成，调用方只提供意图。
type ReleaseInput struct {
	AppID     string `json:"appId"`
	EnvID     string `json:"envId"`
	LaneID    string `json:"laneId,omitempty"`    // 部署到的泳道（空=default 基线，向后兼容）
	Service   string `json:"service,omitempty"`   // 部署到的服务（同 app 多服务场景，如 paas-shop product/recommend/...）；空=单服务（向后兼容）
	ServiceID string `json:"serviceId,omitempty"` // 关联服务实体 ID（Phase 1）；优先按 (app,env,lane,serviceID) 匹配基线 Workload，空退化按 Service 名
	ImageID   string `json:"imageId"`
	Strategy  string `json:"strategy"`
	// Port/ContainerPort 仅在「新建基线 Workload」时设定（驱动 reconciler 建 Service，供 smoke 探活/服务发现）。
	// 复用既有 Workload 时忽略（端口属 Workload 既有配置）。0 = 不建 Service（向后兼容）。
	Port          int    `json:"port,omitempty"`
	ContainerPort int    `json:"containerPort,omitempty"`
	CreatedBy     string `json:"-"` // handler 从身份 ctx 注入，非用户提交
}

type fieldErr struct{ field string }

// ServiceDef 是 ServiceLookup 返回的服务定义快照（依赖倒置本地结构体，
// 避免 devops -> internal/service 跨模块耦合；cmd/core 桥接转换）。
type ServiceDef struct {
	ID       string
	Name     string
	Port     int
	Replicas int
}

// ServiceLookup 供 CreateRelease 新建基线 Workload 时取服务定义（Port/Replicas 来源）。
// 依赖倒置：cmd/core 桥接 internal/service Repository；未注入（nil/返回错误）时行为不变。
type ServiceLookup interface {
	GetService(ctx context.Context, appID, serviceID string) (ServiceDef, error)
}

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }

// BaselineWorkloadName 生成基线 Workload 名（CreateRelease 新建时用，memory+pg 共享，DRY）。
//
// 命名规则：
//   - 多服务（service 非空，如 product/recommend）：`<app>-<service>-svc[-<lane>]`
//   - 单服务（service 空，向后兼容）：`<app>-svc[-<lane>]`
//
// lane=default 不带后缀；非 default（feature 泳道/集成分支名）追加 `-<lane>` 并清洗为
// DNS-1035 合法字符（集成分支名含 /，如 integration/20260815-1 → integration-20260815-1），
// 因该名即 K8s Service 名（DNS-1035）。同 app×env×lane×service 唯一。
func BaselineWorkloadName(appID, service, lane string) string {
	base := appID + "-svc"
	if service != "" {
		base = appID + "-" + service + "-svc"
	}
	if lane != "" && lane != "default" {
		base = base + "-" + lane
	}
	return dns1035(base)
}

// dns1035 清洗为 K8s Service 名合法字符（DNS-1035：小写字母数字与 -，首字母，≤63）。
// 大写转小写；其余非法字符替换为 -；首尾剔除非字母数字；截断 63。
func dns1035(name string) string {
	var b []byte
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b = append(b, byte(r)) //nolint:gosec // case 已限定 ASCII 区间，rune->byte 无溢出
		case r >= 'A' && r <= 'Z':
			b = append(b, byte(r-'A'+'a')) //nolint:gosec // case 已限定 A-Z 区间，算术结果必在 a-z
		default:
			b = append(b, '-')
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	out := strings.Trim(string(b), "-")
	// DNS-1035 要求首字符为字母：数字开头前缀 n（理论不达，appID 均 a-z 开头）
	if out != "" && (out[0] < 'a' || out[0] > 'z') {
		out = "n" + out
	}
	return out
}
