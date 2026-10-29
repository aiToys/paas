// 诊断上下文模型（spec 2026-10-29 §3）：自然语言优先、服务端解析、授权唯一入口。
// 客户端只能提交业务 ID（工作负载/告警/Trace）；namespace/PromQL/LogQL/selector/
// 模型覆盖一律不收。ResolvedResources 复用 diagnose.AuthorizedTarget（已覆盖
// tenant/app/env/workload/service/解析时间，字段语义一致不另造平行结构）。
package assistant

import (
	"errors"
	"net/http"
	"time"

	"github.com/aitoys/paas/internal/diagnose"
)

// ContextKind 上下文锚点类型（spec §1：workload/alert/trace/log 四类来源）。
const (
	ContextKindWorkload = "workload"
	ContextKindAlert    = "alert"
	ContextKindTrace    = "trace"
	ContextKindLog      = "log"
	// ContextSourceText 自然语言文本解析（区别于结构化锚点）。
	ContextSourceText = "text"
)

// ResolutionStatus 解析状态（spec §3）。
const (
	ResolutionResolved          = "resolved"
	ResolutionNeedChoice        = "need_choice"
	ResolutionNeedClarification = "need_clarification"
	ResolutionUnavailable       = "unavailable"
)

// SessionResolved 会话已具备授权目标（旧 target 会话与解析后会话都满足）。
func (s Session) SessionResolved() bool { return s.Target.WorkloadID != "" }

// DiagnosticAnchor 上下文锚点（只含业务 ID / Trace ID，客户端可提交的全部内容）。
type DiagnosticAnchor struct {
	Kind string `json:"kind"` // workload | alert | trace | log
	ID   string `json:"id"`
}

// DiagnosticContext 会话诊断上下文：来源锚点 + 解析产物 + 时间窗 + 状态。
// 未解析会话（draft）只有 ResolutionStatus/Reason，不得伪造 Resolved。
type DiagnosticContext struct {
	Anchors          []DiagnosticAnchor         `json:"anchors,omitempty"`
	Source           string                     `json:"source,omitempty"` // workload | alert | trace | text
	Resolved         *diagnose.AuthorizedTarget `json:"resolved,omitempty"`
	WindowStart      *time.Time                 `json:"windowStart,omitempty"` // nil=未定窗（time.Time 是 struct，omitempty 拦不住零值序列化）
	WindowEnd        *time.Time                 `json:"windowEnd,omitempty"`
	ResolutionStatus string                     `json:"resolutionStatus"`
	ResolutionReason string                     `json:"resolutionReason,omitempty"`
}

// ctxWindow 解析窗便捷构造（nil 安全）。
func ctxWindow(start, end time.Time) (*time.Time, *time.Time) {
	return &start, &end
}

// ContextCandidate 候选目标（服务端生成 ID 并绑定 tenant/user/session，客户端不可伪造）。
type ContextCandidate struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // workload
	WorkloadID   string `json:"workloadId"`
	WorkloadName string `json:"workloadName"`
	AppID        string `json:"appId"`
	EnvID        string `json:"envId"`
	EnvName      string `json:"envName,omitempty"`
	Service      string `json:"service,omitempty"`
	LaneID       string `json:"laneId,omitempty"`
}

// ContextInput 创建会话时的结构化上下文（页面深链带入：告警/Trace 详情页）。
type ContextInput struct {
	Kind string `json:"kind"` // workload | alert | trace
	ID   string `json:"id"`
}

// ErrResolutionRequired 需要用户补充上下文（spec §6：409 target_resolution_required，
// 不建 run、不预留 token）。Status=need_choice 带 Candidates；need_clarification 只带
// 最小追问 Question。
type ErrResolutionRequired struct {
	Status     string             `json:"resolutionStatus"`
	Question   string             `json:"question"`
	Candidates []ContextCandidate `json:"candidates,omitempty"`
	// Anchor 回显触发解析的来源（前端候选卡标注「来自告警/Trace」）。
	Anchor DiagnosticAnchor `json:"anchor,omitempty"`
}

func (e *ErrResolutionRequired) Error() string { return "target_resolution_required" }

// resolutionHTTPStatus 结构化解析错误专用状态码（409；不进通用 HTTPStatus——
// 那会把 ErrResolutionRequired 当 400）。
func resolutionHTTPStatus(err error) (int, bool) {
	var r *ErrResolutionRequired
	if errors.As(err, &r) {
		return http.StatusConflict, true
	}
	return 0, false
}

// 候选缓存 TTL：过期候选 fail closed（重新解析重新生成）。
const candidateTTL = 15 * time.Minute
