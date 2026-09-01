package eval

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermEvalRead  = "agent:read" // 评估用例随 Agent 权限
	PermEvalWrite = "agent:write"
)

// Handler 暴露 Agent 评估 REST API。
//
// 路由：
//
//	GET    /api/agent-evals?agentId=  列用例（agentId 为空列全部）
//	POST   /api/agent-evals            创建用例（body=EvalCase）
//	DELETE /api/agent-evals/{id}       删用例
//	POST   /api/agent-evals/run?agentId=  跑某 Agent 全部用例（返 []EvalResult）
//	GET    /api/agent-evals/runs?agentId= 评估历史（最近 20 次/agent）
//	GET    /api/agent-evals/runs/{id}     单次历史详情（含逐用例结果）
type Handler struct {
	repo      Repository
	service   *Service
	runs      RunRepository
	Authorize func(r *http.Request, perm string) bool
}

func NewHandler(repo Repository, svc *Service) *Handler { return &Handler{repo: repo, service: svc} }

// WithRuns 注入评估历史仓储。
func (h *Handler) WithRuns(r RunRepository) *Handler { h.runs = r; return h }

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/agent-evals":
		h.serveCollection(w, r)
	case path == "/api/agent-evals/run":
		h.serveRun(w, r)
	case path == "/api/agent-evals/runs":
		h.serveRuns(w, r)
	case strings.HasPrefix(path, "/api/agent-evals/runs/"):
		h.serveRunItem(w, r, strings.TrimPrefix(path, "/api/agent-evals/runs/"))
	case strings.HasPrefix(path, "/api/agent-evals/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermEvalRead) {
			return
		}
		agentID := r.URL.Query().Get("agentId")
		list, err := h.repo.List(r.Context(), agentID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermEvalWrite) {
			return
		}
		var c EvalCase
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.Create(r.Context(), c)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermEvalWrite) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/agent-evals/")
	id = strings.TrimRight(id, "/")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, map[string]string{"deleted": id})
}

// serveRun 跑某 Agent 全部用例（?agentId=）。
func (h *Handler) serveRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermEvalRead) {
		return
	}
	agentID := r.URL.Query().Get("agentId")
	if agentID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "agentId 必填")
		return
	}
	if h.service == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "评估服务未装配")
		return
	}
	results, err := h.service.RunAll(r.Context(), agentID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, results)
}

// serveRuns 评估历史列表（?agentId= 可选过滤）。
func (h *Handler) serveRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermEvalRead) {
		return
	}
	if h.runs == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "评估历史未装配")
		return
	}
	list, err := h.runs.ListRuns(r.Context(), r.URL.Query().Get("agentId"))
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// serveRunItem 单次评估历史详情。
func (h *Handler) serveRunItem(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet || id == "" {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermEvalRead) {
		return
	}
	if h.runs == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "评估历史未装配")
		return
	}
	run, err := h.runs.GetRun(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, run)
}

func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case err == ErrEvalRunNotFound:
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case err == ErrEvalCaseNotFound:
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case err == ErrEvalUnavailable:
		httputil.WriteError(w, http.StatusServiceUnavailable, err.Error())
	case err == ErrEvalCaseExists:
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case IsFieldErr(err):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}
