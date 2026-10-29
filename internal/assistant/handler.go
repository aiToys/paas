// HTTP 入口（spec §4 8 端点）。鉴权：mux 挂 diagnosis:read，写方法（POST）在
// handler 内再查 diagnosis:run（与 workload allow 同款）；响应契约 {data:T}/{error:msg}。
package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
)

// Perm 常量（进 identity.BuiltinRoles；与 platform:diagnostics:run 分开——那是跨
// 租户 admin 面，这是租户内资源诊断）。
const (
	PermDiagnosisRead = "diagnosis:read"
	PermDiagnosisRun  = "diagnosis:run"
)

// Handler 助手 HTTP handler。
type Handler struct {
	svc       *Service
	authorize func(r *http.Request, perm string) bool
}

// NewHandler 构造。authorize 注入 gateway.Require 同款判定（nil=不设限，仅测试用）。
func NewHandler(svc *Service, authorize func(r *http.Request, perm string) bool) *Handler {
	return &Handler{svc: svc, authorize: authorize}
}

// ServeHTTP 路由分发（/api/assistant 前缀已由 mux 剥离到包内匹配）。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/assistant")
	p = strings.Trim(p, "/")
	segs := strings.Split(p, "/")
	switch {
	case len(segs) == 1 && segs[0] == "settings" && r.Method == http.MethodGet:
		httputil.WriteData(w, h.svc.Settings)
	case len(segs) == 1 && segs[0] == "agents" && r.Method == http.MethodGet:
		h.handleAgents(w, r)
	case len(segs) == 1 && segs[0] == "sessions" && r.Method == http.MethodGet:
		h.handleListSessions(w, r)
	case len(segs) == 1 && segs[0] == "sessions" && r.Method == http.MethodPost:
		h.handleCreateSession(w, r)
	case len(segs) == 2 && segs[0] == "sessions" && r.Method == http.MethodGet:
		h.handleGetSession(w, r, segs[1])
	case len(segs) == 3 && segs[0] == "sessions" && segs[2] == "messages" && r.Method == http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		c, err := h.svc.Messages(r.Context(), segs[1], parseBefore(r.URL.Query().Get("before")), limit)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, c)
	case len(segs) == 3 && segs[0] == "sessions" && segs[2] == "runs" && r.Method == http.MethodGet:
		h.handleSessionRuns(w, r, segs[1])
	case len(segs) == 3 && segs[0] == "sessions" && segs[2] == "runs" && r.Method == http.MethodPost:
		h.handleCreateRun(w, r, segs[1])
	case len(segs) == 4 && segs[0] == "sessions" && segs[2] == "context" && segs[3] == "resolve" && r.Method == http.MethodPost:
		h.handleResolveContext(w, r, segs[1])
	case len(segs) == 2 && segs[0] == "runs" && r.Method == http.MethodGet:
		h.handleGetRun(w, r, segs[1])
	case len(segs) == 3 && segs[0] == "runs" && segs[2] == "events" && r.Method == http.MethodGet:
		h.handleEvents(w, r, segs[1])
	case len(segs) == 3 && segs[0] == "runs" && segs[2] == "cancel" && r.Method == http.MethodPost:
		h.handleCancel(w, r, segs[1])
	default:
		httputil.WriteError(w, http.StatusNotFound, "route not found")
	}
}

func (h *Handler) handleAgents(w http.ResponseWriter, r *http.Request) {
	httputil.WriteData(w, h.svc.Agents())
}

func (h *Handler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, PermDiagnosisRun) {
		return
	}
	var req CreateSessionRequest
	if err := decodeRequest(w, r, &req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	sess, err := h.svc.CreateSession(r.Context(), req)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteDataCreated(w, publicSession(sess))
}

func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	sessions, err := h.svc.ListSessions(r.Context(), parseBefore(r.URL.Query().Get("before")), limit)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, publicSession(s))
	}
	httputil.WriteData(w, out)
}

func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request, id string) {
	sess, err := h.svc.GetSession(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, publicSession(sess))
}

// handleSessionRuns 会话任务列表（诊断历史入口；读权限）。
func (h *Handler) handleSessionRuns(w http.ResponseWriter, r *http.Request, sessionID string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := h.svc.SessionRuns(r.Context(), sessionID, limit)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	out := make([]RunView, 0, len(runs))
	for _, run := range runs {
		out = append(out, RunViewOf(run))
	}
	httputil.WriteData(w, out)
}

func (h *Handler) handleCreateRun(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !h.allow(w, r, PermDiagnosisRun) {
		return
	}
	var req CreateRunRequest
	if err := decodeRequest(w, r, &req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	run, err := h.svc.CreateRun(r.Context(), sessionID, req, r.Header.Get("Idempotency-Key"))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	// 202 受理（异步执行；Location 指向轮询端点）。
	w.Header().Set("Location", "/api/assistant/runs/"+run.ID)
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"data": RunViewOf(run)})
}

// handleResolveContext 候选确认（spec §6：candidateId 服务端生成；伪造 404+审计）。
func (h *Handler) handleResolveContext(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !h.allow(w, r, PermDiagnosisRun) {
		return
	}
	var req struct {
		CandidateID string `json:"candidateId"`
	}
	if err := decodeRequest(w, r, &req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(req.CandidateID) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "candidateId 必填")
		return
	}
	sess, err := h.svc.ResolveContext(r.Context(), sessionID, strings.TrimSpace(req.CandidateID))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, publicSession(sess))
}

func (h *Handler) handleGetRun(w http.ResponseWriter, r *http.Request, id string) {
	run, err := h.svc.GetRun(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, RunViewOf(run))
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request, runID string) {
	afterSeq, _ := strconv.Atoi(r.URL.Query().Get("afterSeq"))
	events, err := h.svc.ListEvents(r.Context(), runID, afterSeq)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, events)
}

func (h *Handler) handleCancel(w http.ResponseWriter, r *http.Request, id string) {
	if !h.allow(w, r, PermDiagnosisRun) {
		return
	}
	run, err := h.svc.Cancel(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, RunViewOf(run))
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.authorize == nil || h.authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	// 结构化解析错误（spec §6：409 target_resolution_required 携带 question/
	// candidates/resolutionStatus；不建 run 不扣预算的前置信号）。
	if code, ok := resolutionHTTPStatus(err); ok {
		var r *ErrResolutionRequired
		_ = errors.As(err, &r)
		body := map[string]any{
			"error":            "target_resolution_required",
			"resolutionStatus": r.Status, "question": r.Question,
		}
		if len(r.Candidates) > 0 {
			body["candidates"] = r.Candidates
		}
		if r.Anchor.Kind != "" {
			body["anchor"] = r.Anchor
		}
		httputil.WriteJSON(w, code, body)
		return
	}
	httputil.WriteError(w, HTTPStatus(err), err.Error())
}

// publicSession 会话用户侧投影（隐藏 TenantID；draft 会话不下发零值 target，
// 补 resolutionStatus 摘要——未解析会话不得伪造 AuthorizedTarget，spec §6）。
func publicSession(s Session) map[string]any {
	out := map[string]any{
		"id": s.ID, "ownerUserId": s.OwnerUserID, "agentKey": s.AgentKey,
		"agentVersion": s.AgentVersion, "focus": s.Focus,
		"createdAt": s.CreatedAt, "updatedAt": s.UpdatedAt,
	}
	if s.SessionResolved() {
		out["target"] = s.Target
	} else {
		out["resolutionStatus"] = s.contextStatus()
		if s.Context != nil && s.Context.ResolutionReason != "" {
			out["resolutionReason"] = s.Context.ResolutionReason
		}
	}
	if s.Context != nil {
		out["context"] = s.Context
	}
	if len(s.Candidates) > 0 {
		out["candidates"] = s.Candidates
	}
	return out
}

func (s Session) contextStatus() string {
	if s.Context == nil {
		return ResolutionNeedClarification // 旧 draft 兼容（理论不出现）
	}
	return s.Context.ResolutionStatus
}

// RunViewOf Run → RunView（报告 raw → any；终态才有）。
func RunViewOf(r Run) RunView {
	v := RunView{
		ID: r.ID, SessionID: r.SessionID, Question: r.Question, Symptom: r.Symptom, Reply: r.Reply, Mode: r.Mode,
		ParentRunID: r.ParentRunID, WindowStart: r.WindowStart, WindowEnd: r.WindowEnd,
		Status: r.Status, Quality: r.Quality, Evidence: r.Evidence, Model: r.Model,
		Usage: r.Usage, ErrorCode: r.ErrorCode, ErrorMsg: r.ErrorMsg,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if len(r.Report) > 0 {
		var rep any
		if json.Unmarshal(r.Report, &rep) == nil {
			v.Report = rep
		}
	}
	return v
}

func parseBefore(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

func decodeRequest(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("请求体只能包含一个 JSON 对象")
	}
	return nil
}
