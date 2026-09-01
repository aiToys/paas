package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/ai/guardrail"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/provider"
)

const (
	PermAgentRead  = "agent:read"
	PermAgentWrite = "agent:write"
)

// Handler 暴露 Agent REST API。
//
// 路由：
//
//	GET    /api/agents           列表
//	POST   /api/agents           创建
//	GET    /api/agents/{id}      详情
//	PUT    /api/agents/{id}      更新
//	DELETE /api/agents/{id}      删除
//	POST   /api/agents/{id}/run  运行（SSE 流式，OpenAI 兼容输出）
type Handler struct {
	repo      Repository
	runtime   *Runtime
	Authorize func(r *http.Request, perm string) bool
}

func NewHandler(repo Repository, runtime *Runtime) *Handler {
	return &Handler{repo: repo, runtime: runtime}
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// /run 走 SSE，不设 json Content-Type（ServeSSE 内部设 text/event-stream）
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/agents":
		h.serveCollection(w, r)
	case strings.HasPrefix(path, "/api/agents/"):
		h.serveItem(w, r)
	default:
		w.Header().Set("Content-Type", "application/json")
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermAgentRead) {
			return
		}
		list, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermAgentWrite) {
			return
		}
		var a Agent
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.Create(r.Context(), a)
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
	path := strings.TrimRight(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/agents/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		w.Header().Set("Content-Type", "application/json")
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 1:
		h.serveOne(w, r, id)
	case len(parts) == 2 && parts[1] == "run":
		h.serveRun(w, r, id)
	default:
		w.Header().Set("Content-Type", "application/json")
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveOne(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermAgentRead) {
			return
		}
		a, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, a)
	case http.MethodPut:
		if !h.allow(w, r, PermAgentWrite) {
			return
		}
		var a Agent
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		a.ID = id
		saved, err := h.repo.Update(r.Context(), a)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, saved)
	case http.MethodDelete:
		if !h.allow(w, r, PermAgentWrite) {
			return
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveRun 运行 Agent（SSE 流式）。body: {messages:[{role,content}]}。
func (h *Handler) serveRun(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermAgentWrite) {
		return
	}
	if h.runtime == nil {
		w.Header().Set("Content-Type", "application/json")
		httputil.WriteError(w, http.StatusServiceUnavailable, "agent runtime 未装配")
		return
	}
	var req struct {
		Messages       []provider.Message `json:"messages"`
		ConversationID string             `json:"conversationId,omitempty"` // 非空启用多轮记忆（历史前置 + 本轮追加）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.Messages) == 0 {
		w.Header().Set("Content-Type", "application/json")
		httputil.WriteError(w, http.StatusBadRequest, "messages 不能为空")
		return
	}
	// 输入护栏预检：开 SSE 前返干净 422（命中后 ServeSSE 已写头无法改 status）。
	if err := h.runtime.CheckInput(r.Context(), id, req.Messages); err != nil {
		w.Header().Set("Content-Type", "application/json")
		if errors.Is(err, guardrail.ErrBlocked) {
			httputil.WriteError(w, http.StatusUnprocessableEntity, err.Error())
		} else {
			h.writeErr(w, err)
		}
		return
	}
	if err := h.runtime.ServeSSEConv(w, r.Context(), id, req.ConversationID, req.Messages); err != nil {
		// SSE 已开始则无法改 status，仅日志（ServeSSE 内部已 flush 错误信息由 [DONE] 收尾）
		_ = err
	}
}

func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAgentNotFound):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrAgentExists):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case isFieldErr(err):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}
