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
	ErrNotFailed         = errors.New("run not failed")
	ErrTemplateRequired  = errors.New("templateId required")
	ErrTemplateBuiltin   = errors.New("builtin template cannot be modified")
)

// Kind 流水线分类（UI 分组 + 职责划分）。
const (
	KindCI     = "ci"    // 测试联调流水线：build→deploy(test 泳道)→test（无版本、无合并）
	KindCD     = "cd"    // 上线发布流水线：approve→deploy(prod 基线)→release(版本)→baseline(合并主干)
	KindAgent  = "agent" // AI 研发流水线：agentrun 执行域（需求→代码分支→构建→部署）
	KindCustom = "custom"
)

// RunStatus PipelineRun 状态机。
const (
	RunRunning   = "running"
	RunPaused    = "paused" // 等 approve/test-manual
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
	StageAborted = "aborted" // run 被 Abort 时，残留 running 的 stage 标此（数据一致）
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
	StageRelease  = "release" // 打版本号里程碑（git tag + Image.version），不部署
	StageBaseline = "baseline"
	StageCanary   = "canary" // 金丝雀验证（并行验证式：canary 泳道部署 + 人工观察 + 确认放量/终止）
	StageAgent    = "agent"  // AI 研发（agentrun 执行域）：需求→代码分支，产出经 Output.branch 衔接后续 build/baseline
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
	Type        string `json:"type,omitempty"`    // string|env|repo
	Default     any    `json:"default,omitempty"` // 默认值（可含占位符 {{app.env.test}} 等）
	Description string `json:"description,omitempty"`
}

// PipelineTrigger 流水线触发配置（manual / webhook / cron）。
type PipelineTrigger struct {
	Type     string   `json:"type"`               // manual|webhook|cron
	Branch   string   `json:"branch,omitempty"`   // webhook: 分支 glob（如 "main" 或 "feature-*"；空=全部分支）
	Events   []string `json:"events,omitempty"`   // webhook: ["push"]（保留字段，当前仅处理 push）
	Token    string   `json:"token,omitempty"`    // webhook: URL token（持久化供端点验证；get/list 时清空不返回前端）
	Schedule string   `json:"schedule,omitempty"` // cron: 5 字段表达式（如 "0 2 * * *"）
}

// 触发类型常量。
const (
	TriggerManual  = "manual"
	TriggerWebhook = "webhook"
	TriggerCron    = "cron"
)

// WebhookPath 返回 pipeline 的 webhook 接收端点路径（不含 token；前端拼接 baseURL+token 展示）。
// POST 此路径 + ?token=<Token> + Gitea push event body 触发 run。
func WebhookPath(pid string) string { return "/api/webhooks/pipeline/" + pid }

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
	// Version builtin 模板版本号（破坏性改动 +1）。seed 时若代码 Version > DB Version，
	// 覆盖 stages/name/description/params（绕过 builtin 拒改保护，平台级发版升级路径）。
	// 租户自定义模板 Version 恒 0（不参与升级）。存量 builtin 经 migration 0025 回填为 1。
	Version int `json:"version,omitempty"`
}

// Pipeline 应用绑定的流水线（绑定模板 + 参数覆盖，非 per-app 复制 stages）。
// Stages 运行时从 Template 解析（ResolveStages），模板升级自动传播到此绑定的后续 run。
type Pipeline struct {
	ID             string          `json:"id"`
	TenantID       string          `json:"tenantId"`
	AppID          string          `json:"appId"`
	Name           string          `json:"name"`
	Kind           string          `json:"kind"`
	TemplateID     string          `json:"templateId,omitempty"`
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
	// 模板身份快照（DefinitionSnapshot）：触发时固化，模板后续升级不影响在跑/可查 run 的归因。
	TemplateID      string `json:"templateId,omitempty"`
	TemplateVersion int    `json:"templateVersion,omitempty"`
	// PlanHash 执行计划指纹（preview 与 trigger 共用算法）：trigger 请求携带时服务端
	// 锁定产物后重算比对，不一致 409——防「预览到执行之间新构建完成」改变候选产物。
	PlanHash string `json:"planHash,omitempty"`
	// Artifact 触发时锁定的候选产物（imageSource=latestReady 预解析一次固化）。
	// 后续执行/审批后推进/Retry 均引用该快照，不再动态选 latest（产物锁定，spec §6）。
	Artifact *ArtifactBinding `json:"artifact,omitempty"`
	// Decisions 结构化决策记录（manual-test 确认/拒绝、审批通过/拒绝，append-only）。
	Decisions []DecisionRecord `json:"decisions,omitempty"`
	// Lane 触发时从最终解析 stages 提取的部署泳道（首个 deploy 段 lane；canary 段运行时
	// 派生 canary-<runID> 不计入）。泳道关闭保护按此真实目标关联，不再按 Branch==lane 名
	// 字符串匹配（spec 2026-10-17 L5：遗漏显式命名泳道，混同跨环境同名分支）。
	Lane       string    `json:"lane,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// ArtifactBinding 交付产物绑定（触发时冻结的候选五元组，spec §6 产物锁定）。
type ArtifactBinding struct {
	ServiceID  string `json:"serviceId,omitempty"` // 多服务应用的目标服务（可空=单服务）
	RepoID     string `json:"repoId,omitempty"`
	Commit     string `json:"commit,omitempty"` // 镜像构建 commit（元数据真源；无 meta 时退 run 触发 commit）
	ImageID    string `json:"imageId,omitempty"`
	Digest     string `json:"digest,omitempty"` // 跨环境晋升同一候选的锚点
	BuildRunID string `json:"buildRunId,omitempty"`
}

// DecisionRecord 结构化决策（区别于审计日志：挂在 run 上供详情页时间线展示）。
type DecisionRecord struct {
	StageIndex int       `json:"stageIndex"`
	Type       string    `json:"type"`   // manual-test|approve|canary
	Action     string    `json:"action"` // confirm|reject|promote|terminate
	Reason     string    `json:"reason,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	At         time.Time `json:"at"`
}

// CheckResult 结构化检查结果（smoke 探活等；区别于泛化「测试通过」，spec §7）。
type CheckResult struct {
	Type     string    `json:"type"` // smoke|manual
	Target   string    `json:"target,omitempty"`
	Expected string    `json:"expected,omitempty"`
	Actual   string    `json:"actual,omitempty"`
	Status   string    `json:"status"`         // pass|fail|unknown
	Note     string    `json:"note,omitempty"` // 如探活降级路径说明
	At       time.Time `json:"at"`
}

// StageRun 单阶段执行记录（输出链载体）。
type StageRun struct {
	Index      int            `json:"index"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Input      map[string]any `json:"input,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
	Checks     []CheckResult  `json:"checks,omitempty"` // 结构化质量结果（探活目标/预期/实际）
	StartedAt  time.Time      `json:"startedAt,omitempty"`
	FinishedAt time.Time      `json:"finishedAt,omitempty"`
	Error      string         `json:"error,omitempty"`
	Log        string         `json:"log,omitempty"` // 执行过程关键事件（append-only，logf helper 写入）
}

// StageRun Output 已知 key（stage 输出链）。
const (
	OutImageID        = "imageId"
	OutReleaseID      = "releaseId"
	OutWorkloadDomain = "workloadDomain"
	OutWorkloadID     = "workloadId" // deploy stage：workload ID（smoke 探活经 WorkloadProber 取集群内地址）
	OutVersion        = "version"
	OutMergeSHA       = "mergeSha"

	OutCanaryWorkloadID = "canaryWorkloadId" // canary stage：并行验证 workload ID（promote/终止清理用）
	OutCanaryDomain     = "canaryDomain"     // canary stage：验证域名（指标卡/直链）

	OutAgentBranch  = "branch"  // agent stage：产出分支名（build branchSource=priorAgent / Change 注册消费）
	OutAgentRunName = "runName" // agent stage：agentrun 侧 run 名（观测深链 / Retry workspaceFrom）
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
	case KindCI, KindCD, KindAgent, KindCustom:
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
	case StageBuild, StageDeploy, StageTest, StageApprove, StagePromote, StageRelease, StageBaseline, StageCanary:
		// valid
	default:
		return ErrInvalidStageType
	}
	if s.Name == "" {
		return errStageNameRequired
	}
	return nil
}

// RunSummary 是 run 的轻量摘要（跨模块聚合展示用，如 lane 详情 RecentRuns；
// 避免 lane 包 import 全量 PipelineRun 耦合 StageRuns 大字段）。
type RunSummary struct {
	ID         string    `json:"id"`
	AppID      string    `json:"appId"`
	PipelineID string    `json:"pipelineId"`
	Branch     string    `json:"branch"`
	Lane       string    `json:"lane,omitempty"` // 触发时提取的部署泳道（关闭保护真实目标关联）
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// Summarize 投影 PipelineRun 为摘要。
func (r PipelineRun) Summarize() RunSummary {
	return RunSummary{
		ID: r.ID, AppID: r.AppID, PipelineID: r.PipelineID,
		Branch: r.Branch, Lane: r.Lane, Status: r.Status,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
}
