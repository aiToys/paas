package prompt

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

const (
	PermPromptRead  = "prompt:read"
	PermPromptWrite = "prompt:write"
)

// Handler 暴露 prompt 管理 REST API。
//
// 路由：
//
//	GET    /api/prompts            列表（全部版本）
//	POST   /api/prompts            创建（同 name 自动 version+1 且激活）
//	GET    /api/prompts/{id}       取单版本
//	DELETE /api/prompts/{id}       删单版本（删 active 自动激活最新剩余版本）
//	POST   /api/prompts/{id}/activate 激活该版本
//	GET    /api/prompts/active?name= 取当前激活版本（Agent 渲染用）
type Handler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

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
	case path == "/api/prompts":
		h.serveCollection(w, r)
	case path == "/api/prompts/active":
		h.serveActive(w, r)
	case strings.HasPrefix(path, "/api/prompts/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermPromptRead) {
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
		if !h.allow(w, r, PermPromptWrite) {
			return
		}
		var p Prompt
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.Create(r.Context(), p)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermPromptRead) {
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name 不能为空")
		return
	}
	p, err := h.repo.GetActive(r.Context(), name)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, p)
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/prompts/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 1:
		switch r.Method {
		case http.MethodGet:
			if !h.allow(w, r, PermPromptRead) {
				return
			}
			p, err := h.repo.Get(r.Context(), id)
			if err != nil {
				h.writeErr(w, err)
				return
			}
			httputil.WriteData(w, p)
		case http.MethodDelete:
			if !h.allow(w, r, PermPromptWrite) {
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
	case len(parts) == 2 && parts[1] == "activate":
		if r.Method != http.MethodPost {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !h.allow(w, r, PermPromptWrite) {
			return
		}
		p, err := h.repo.SetActive(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, p)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrPromptNotFound), errors.Is(err, ErrNoActivePrompt):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrPromptExists):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case isFieldErr(err):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}
