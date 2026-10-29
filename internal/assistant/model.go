// Package assistant 用户控制台全局 AI 助手（spec 2026-10-27）：官方 Agent 目录、
// 会话、异步任务与事件。诊断执行仍归 internal/diagnose；本包只做外壳（会话/调度/
// 历史/统一事件），cmd/core 仅装配。
//
// 隔离基线（spec §6）：仓储所有读取与变更强制 tenant + owner user；请求只接受
// 业务 ID，namespace/PromQL/LogQL 等由服务端从 AuthorizedTarget 解析，客户端不可指定。
package assistant

import (
	"encoding/json"
	"time"

	"github.com/aitoys/paas/internal/diagnose"
	"github.com/aitoys/paas/internal/diagnose/scoped"
)

// —— 执行状态 / 报告质量 / 严重性（spec §7 三维度分离）——

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusTimedOut  = "timed_out"
)

const (
	QualitySufficient    = "sufficient"
	QualityPartial       = "partial"
	QualityInconclusive  = "inconclusive"
	QualityInvalidOutput = "invalid_output"
)

// 终态判定（worker CAS 与前端轮询停止共用）。
func IsTerminal(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	}
	return false
}

// TargetType 诊断目标类型（首期仅 workload）。
const (
	TargetTypeWorkload = "workload"
)

// TargetRef 创建会话时的目标引用（只含业务 ID + 可选聚焦；服务端解析成 AuthorizedTarget）。
type TargetRef struct {
	Type string `json:"type"` // workload
	ID   string `json:"id"`   // 工作负载 ID（非 K8s 名）
}

// FocusRef 可选聚焦对象（实例抽屉带入；服务端校验归属）。
type FocusRef struct {
	PodName      string `json:"podName,omitempty"`
	PodUID       string `json:"podUID,omitempty"`
	Container    string `json:"container,omitempty"`
	AlertEventID string `json:"alertEventId,omitempty"`
}

// Session 助手会话（Agent + 目标固定；换 Agent/目标创建新会话）。
// Target 非空 = 已解析（旧 target 会话与解析后会话同构，执行链路零特判）；
// Target 空 + Context = draft 会话（自然语言优先，首个问题时解析，spec 2026-10-29）。
type Session struct {
	ID           string                    `json:"id"`
	TenantID     string                    `json:"-"`
	OwnerUserID  string                    `json:"ownerUserId"`
	AgentKey     string                    `json:"agentKey"`
	AgentVersion string                    `json:"agentVersion"`
	Target       diagnose.AuthorizedTarget `json:"target"`
	// Context 诊断上下文（来源锚点/窗口/解析状态；nil=旧会话兼容，投影层补齐）。
	Context *DiagnosticContext `json:"context,omitempty"`
	// Candidates 多候选待选状态的服务端候选（不落库；need_choice 会话投影携带）。
	Candidates []ContextCandidate `json:"candidates,omitempty"`
	Focus      FocusRef           `json:"focus,omitempty"`
	CreatedAt  time.Time          `json:"createdAt"`
	UpdatedAt  time.Time          `json:"updatedAt"`
}

// RunUsage 用量（provider=真实 usage；estimated=字符粗估，口径明示不冒充）。
type RunUsage struct {
	Tokens      int    `json:"tokens"`
	UsageSource string `json:"usageSource"` // provider | estimated
}

// Run 单次诊断任务。
type Run struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"sessionId"`
	TenantID       string          `json:"-"`
	OwnerUserID    string          `json:"ownerUserId"`
	Question       string          `json:"question"`
	Reply          string          `json:"reply,omitempty"`
	Mode           string          `json:"mode,omitempty"`
	LeaseOwner     string          `json:"-"`
	LeaseExpires   time.Time       `json:"-"`
	Symptom        string          `json:"symptom,omitempty"`
	ParentRunID    string          `json:"parentRunId,omitempty"` // 追问关联上一终态 run
	WindowStart    time.Time       `json:"windowStart"`           // 创建时固定 UTC，防执行漂移
	WindowEnd      time.Time       `json:"windowEnd"`
	Status         string          `json:"status"`
	Quality        string          `json:"quality,omitempty"` // 终态填
	Report         json.RawMessage `json:"report,omitempty"`  // 版本化用户侧报告（脱敏投影）
	Evidence       []Evidence      `json:"evidence,omitempty"`
	Model          string          `json:"model,omitempty"`
	Usage          RunUsage        `json:"usage,omitempty"`
	IdempotencyKey string          `json:"-"`
	RequestHash    string          `json:"-"`
	CancelFlag     bool            `json:"-"`
	ErrorCode      string          `json:"errorCode,omitempty"`
	ErrorMsg       string          `json:"errorMsg,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
}

// Active 活动任务判定（同会话单活动约束）。
func (r Run) Active() bool { return r.Status == StatusQueued || r.Status == StatusRunning }

// Event 结构化进度事件（seq 单调；payload 脱敏展示数据）。
type Event struct {
	Seq       int             `json:"seq"`
	RunID     string          `json:"runId"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// 事件类型（spec §9）。
const (
	EventRunQueued     = "run.queued"
	EventRunStarted    = "run.started"
	EventToolStarted   = "tool.started"
	EventToolCompleted = "tool.completed"
	EventReportReady   = "report.ready"
	EventRunFailed     = "run.failed"
	EventRunCancelled  = "run.cancelled"
	EventRunTimedOut   = "run.timed_out"
)

// Evidence 用户侧证据（账本投影；进入模型与存储前已过滤/脱敏/截断）。
// 真源在 scoped 包（账本产出方）；别名保持 API 契约与包内引用稳定。
type Evidence = scoped.Evidence

// —— 请求/响应 DTO ——

// CreateSessionRequest 创建会话请求。三种形态（spec 2026-10-29 §6）：
// 旧版 target（兼容）；结构化 context（页面深链：告警/Trace 详情页）；都空=draft
// 自然语言会话（首个问题时解析，未解析不建 run）。
type CreateSessionRequest struct {
	AgentKey string        `json:"agentKey"`
	Target   TargetRef     `json:"target"`
	Context  *ContextInput `json:"context,omitempty"`
	Focus    *FocusRef     `json:"focus,omitempty"`
}

// CreateRunRequest 创建任务请求（question 必填 ≤2000 字符；lookback 5-360 分钟）。
type CreateRunRequest struct {
	Question        string `json:"question"`
	Mode            string `json:"mode,omitempty"` // investigate 重新取证；explain 基于已有报告解释
	LookbackMinutes int    `json:"lookbackMinutes,omitempty"`
	Symptom         string `json:"symptom,omitempty"`
}

// RunView run 的用户侧视图（列表/详情共用；隐藏 lease/幂等等内部字段）。
type RunView struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"sessionId"`
	Question    string     `json:"question"`
	Reply       string     `json:"reply,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	Symptom     string     `json:"symptom,omitempty"`
	ParentRunID string     `json:"parentRunId,omitempty"`
	WindowStart time.Time  `json:"windowStart"`
	WindowEnd   time.Time  `json:"windowEnd"`
	Status      string     `json:"status"`
	Quality     string     `json:"quality,omitempty"`
	Report      any        `json:"report,omitempty"`
	Evidence    []Evidence `json:"evidence,omitempty"`
	Model       string     `json:"model,omitempty"`
	Usage       RunUsage   `json:"usage,omitempty"`
	ErrorCode   string     `json:"errorCode,omitempty"`
	ErrorMsg    string     `json:"errorMsg,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}
