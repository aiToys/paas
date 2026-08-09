// Package pipeline 实现 DevOps 流水线编排：声明式 stage 序列 + 异步状态机推进。
// 借鉴 Spinnaker（Application 1:N Pipeline）+ ArgoCD（artifact source 解耦）。
package pipeline

import (
	"errors"
	"time"
)

// sentinel 错误
var (
	errNameRequired      = errors.New("name required")
	errAppRequired       = errors.New("appId required")
	ErrInvalidKind       = errors.New("invalid kind")
	ErrNoStages          = errors.New("no stages")
	ErrInvalidStageType  = errors.New("invalid stage type")
	errStageNameRequired = errors.New("stage name required")
	ErrNotPaused         = errors.New("run not paused")
	ErrStageNotCurrent   = errors.New("stage not current")
	ErrTemplateRequired  = errors.New("templateId required")
	ErrTemplateNotFound  = errors.New("pipeline template not found")
)

// Kind 流水线分类（UI 分组 + 职责划分）。
const (
	KindCI     = "ci"     // 测试联调流水线：build→deploy(test 泳道)→test（无版本、无合并）
	KindCD     = "cd"     // 上线发布流水线：approve→deploy(prod 基线)→release(版本)→baseline(合并主干)
	KindCustom = "custom"
)

// RunStatus PipelineRun 状态机。
const (
	RunRunning   = "running"
	RunPaused    = "paused"    // 等 approve/test-manual
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunAborted   = "aborted"
)

// StageStatus StageRun 状态。
const (
	StagePending = "pending"
	StageRunning = "running"
	StageSuccess = "succeeded"
	StageFailed  = "failed"
	StageWaiting = "waiting" // 等 approve
	StageSkipped = "skipped"
)

// LaneDefault 泳道默认值（基线）。与 workload.LaneDefault 同值；
// pipeline 层独立声明避免 import 循环（workload 不应成为 pipeline 的依赖）。
const LaneDefault = "default"

// StageType stage 类型枚举。
const (
	StageBuild    = "build"
	StageDeploy   = "deploy"
	StageTest     = "test"
	StageApprove  = "approve"
	StagePromote  = "promote"
	StageRelease  = "release"  // 打版本号里程碑（git tag + Image.version），不部署
	StageBaseline = "baseline"
)

// ImageSource deploy stage 的镜像来源（CI/CD 解耦关键）。
const (
	ImagePriorBuild  = "priorBuild"  // 本流水线前序 build stage 产出
	ImageSelected    = "selected"    // 指定 imageId（CD 消费 CI 产物）
	ImageLatestReady = "latestReady" // app 最新 ready Image
)

// TestMode test stage 子模式。
const (
	TestSmoke  = "smoke"  // HTTP 探活（自动）
	TestManual = "manual" // 人工确认
)

// StageDef 阶段定义（模板与 Pipeline 共用）。
type StageDef struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// ParamDef 模板参数声明（admin 模板编辑器文档化；运行时靠占位符解析，此字段非必填）。
type ParamDef struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`        // string|env|repo
	Default     any    `json:"default,omitempty"`     // 默认值（可含占位符 {{app.env.test}} 等）
	Description string `json:"description,omitempty"`
}

// PipelineTrigger 流水线触发配置（Plan 1 只实现 manual；webhook/cron 占位 Plan 3）。
type PipelineTrigger struct {
	Type     string   `json:"type"`              // manual|webhook|cron（Plan 1: manual）
	Branch   string   `json:"branch,omitempty"`  // webhook: glob
	Events   []string `json:"events,omitempty"`  // webhook: ["push"]
	Token    string   `json:"-"`                 // webhook: URL token（不序列化前端）
	Schedule string   `json:"schedule,omitempty"` // cron: 表达式
}

// PipelineTemplate 模板（平台预置 builtin + 租户自定义）。
type PipelineTemplate struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId,omitempty"` // ""=平台预置
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Description string     `json:"description,omitempty"`
	Stages      []StageDef `json:"stages"`
	Params      []ParamDef `json:"params,omitempty"` // 参数声明（文档化；运行时占位符解析）
	Builtin     bool       `json:"builtin,omitempty"`
}

// Pipeline 应用绑定的流水线（绑定模板 + 参数覆盖，非 per-app 复制 stages）。
// Stages 运行时从 Template 解析（ResolveStages），模板升级自动传播到此绑定的后续 run。
type Pipeline struct {
	ID             string          `json:"id"`
	TenantID       string          `json:"tenantId"`
	AppID          string          `json:"appId"`
	Name           string          `json:"name"`
	Kind           string           `json:"kind"`
	TemplateID     string           `json:"templateId,omitempty"`
	ParamOverrides map[string]any  `json:"paramOverrides,omitempty"` // app 覆盖模板默认参数
	Trigger        PipelineTrigger `json:"trigger"`
	Disabled       bool            `json:"disabled,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// PipelineRun 一次运行（异步状态机载体）。
type PipelineRun struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenantId"`
	AppID        string     `json:"appId"`
	PipelineID   string     `json:"pipelineId"`
	Branch       string     `json:"branch"`
	Commit       string     `json:"commit,omitempty"`
	RepoID       string     `json:"repoId,omitempty"` // run 时解析 app 绑定的 internal CodeRepo（build stage 用）
	Trigger      string     `json:"trigger"`          // manual|webhook|cron
	TriggerRef   string     `json:"triggerRef,omitempty"`
	Status       string     `json:"status"`
	CurrentStage int        `json:"currentStage"`
	StageRuns    []StageRun `json:"stageRuns"`
	Version      string     `json:"version,omitempty"` // baseline 写入
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   time.Time  `json:"finishedAt,omitempty"`
}

// StageRun 单阶段执行记录（输出链载体）。
type StageRun struct {
	Index     int            `json:"index"`
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Status    string         `json:"status"`
	Input     map[string]any `json:"input,omitempty"`
	Output    map[string]any `json:"output,omitempty"`
	StartedAt time.Time      `json:"startedAt,omitempty"`
	FinishedAt time.Time     `json:"finishedAt,omitempty"`
	Error     string         `json:"error,omitempty"`
	Log       string         `json:"log,omitempty"` // 执行过程关键事件（append-only，logf helper 写入）
}

// StageRun Output 已知 key（stage 输出链）。
const (
	OutImageID       = "imageId"
	OutReleaseID     = "releaseId"
	OutWorkloadDomain = "workloadDomain"
	OutVersion       = "version"
	OutMergeSHA      = "mergeSha"
)

// Validate Pipeline 基本校验（绑定模型：校验 TemplateID，不校验 Stages--运行时从模板解析）。
func (p Pipeline) Validate() error {
	if p.Name == "" {
		return errNameRequired
	}
	if p.AppID == "" {
		return errAppRequired
	}
	switch p.Kind {
	case KindCI, KindCD, KindCustom:
		// valid
	default:
		return ErrInvalidKind
	}
	if p.TemplateID == "" {
		return ErrTemplateRequired
	}
	return nil
}

func (s StageDef) validate() error {
	switch s.Type {
	case StageBuild, StageDeploy, StageTest, StageApprove, StagePromote, StageRelease, StageBaseline:
		// valid
	default:
		return ErrInvalidStageType
	}
	if s.Name == "" {
		return errStageNameRequired
	}
	return nil
}
