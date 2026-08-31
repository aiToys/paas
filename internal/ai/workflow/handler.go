package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限（复用 agent:read/write——工作流是智能体编排的一部分）。
const (
	PermWorkflowRead  = "agent:read"
	PermWorkflowWrite = "agent:write"
)

// AuditRecorder 审计记录（依赖倒置，cmd/core 桥接 security.AuditStore，与 pipeline 同源签名）。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// Handler 暴露工作流 REST API。
//
// 路由：
//
//	GET    /api/workflows               列表
//	POST   /api/workflows               创建（含节点定义）
//	GET    /api/workflows/{id}          详情
//	PUT    /api/workflows/{id}          更新
//	DELETE /api/workflows/{id}          删除（级联清运行历史）
//	POST   /api/workflows/{id}/runs     触发运行（body=inputs）
//	GET    /api/workflows/{id}/runs     运行历史
//	GET    /api/workflows/runs/{rid}    运行详情
//	POST   /api/workflows/runs/{rid}/approve?node=   恢复等待中的 approve
//	POST   /api/workflows/runs/{rid}/abort          中止
type Handler struct {
	repo      Repository
	engine    *Engine
	Authorize func(r *http.Request, perm string) bool
	audit     AuditRecorder
	actorFn   func(r *http.Request) string // 审计 actor 解析（cmd/core 桥接 gateway.UserIDFrom）
}

func NewHandler(repo Repository, engine *Engine) *Handler {
	return &Handler{repo: repo, engine: engine}
}

func (h *Handler) WithAuthorize(fn func(r *http.Request, perm string) bool) *Handler {
	h.Authorize = fn
	return h
}

func (h *Handler) WithAudit(a AuditRecorder) *Handler {
	h.audit = a
	return h
}

// WithActorFn 注入 actor 解析（审计用，nil 归 "system"）。
func (h *Handler) WithActorFn(f func(r *http.Request) string) *Handler {
	h.actorFn = f
	return h
}

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
	case path == "/api/workflows":
		h.serveCollection(w, r)
	case strings.HasPrefix(path, "/api/workflows/runs/"):
		h.serveRun(w, r, strings.TrimPrefix(path, "/api/workflows/runs/"))
	case strings.HasPrefix(path, "/api/workflows/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermWorkflowRead) {
			return
		}
		list, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
	case http.MethodPost:
		if !h.allow(w, r, PermWorkflowWrite) {
			return
		}
		var d WorkflowDef
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if err := d.Validate(); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := h.repo.Create(r.Context(), d)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		h.recordAudit(r, "workflow_create", saved.ID, saved.Name)
		httputil.WriteDataCreated(w, saved)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/workflows/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if len(parts) == 2 && parts[1] == "runs" {
		h.serveRuns(w, r, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermWorkflowRead) {
			return
		}
		d, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, d)
	case http.MethodPut:
		if !h.allow(w, r, PermWorkflowWrite) {
			return
		}
		var d WorkflowDef
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		d.ID = id
		if err := d.Validate(); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := h.repo.Update(r.Context(), d)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		h.recordAudit(r, "workflow_update", saved.ID, saved.Name)
		httputil.WriteData(w, saved)
	case http.MethodDelete:
		if !h.allow(w, r, PermWorkflowWrite) {
			return
		}
		d, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			h.writeErr(w, err)
			return
		}
		h.recordAudit(r, "workflow_delete", id, d.Name)
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveRuns /{id}/runs：GET 历史 / POST 触发。
func (h *Handler) serveRuns(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermWorkflowRead) {
			return
		}
		list, err := h.repo.ListRuns(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, list)
	case http.MethodPost:
		if !h.allow(w, r, PermWorkflowWrite) {
			return
		}
		d, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		inputs := map[string]string{}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&inputs); err != nil {
				httputil.WriteError(w, http.StatusBadRequest, "invalid body（期望 inputs 对象）")
				return
			}
		}
		run, err := h.engine.Start(r.Context(), d, inputs)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusConflict, err)
			return
		}
		h.recordAudit(r, "workflow_run", d.ID, run.ID)
		httputil.WriteDataCreated(w, run)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveRun 运行详情/approve/abort：rest 形如 "{rid}" / "{rid}/approve" / "{rid}/abort"。
func (h *Handler) serveRun(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	rid := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !h.allow(w, r, PermWorkflowRead) {
			return
		}
		run, err := h.repo.GetRun(r.Context(), rid)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, run)
		return
	}
	if !h.allow(w, r, PermWorkflowWrite) {
		return
	}
	switch {
	case parts[1] == "approve" && r.Method == http.MethodPost:
		node := r.URL.Query().Get("node")
		if node == "" {
			httputil.WriteError(w, http.StatusBadRequest, "node 参数必填（待确认节点 id）")
			return
		}
		if err := h.engine.Approve(r.Context(), rid, node); err != nil {
			h.writeErr(w, err)
			return
		}
		h.recordAudit(r, "workflow_approve", rid, node)
		w.WriteHeader(http.StatusNoContent)
	case parts[1] == "abort" && r.Method == http.MethodPost:
		if err := h.engine.Abort(r.Context(), rid); err != nil {
			h.writeErr(w, err)
			return
		}
		h.recordAudit(r, "workflow_abort", rid, "")
		w.WriteHeader(http.StatusNoContent)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// writeErr 映射领域 sentinel 到 HTTP（与 skill 同款）。
func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrWorkflowNotFound), errors.Is(err, ErrRunNotFound):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrWorkflowExists), errors.Is(err, ErrRunNotPaused), errors.Is(err, ErrNodeNotApprove):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidDef):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}

func (h *Handler) recordAudit(r *http.Request, action, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	tid, _ := tenant.TenantFrom(r.Context())
	actor := "system"
	if h.actorFn != nil {
		if a := h.actorFn(r); a != "" {
			actor = a
		}
	}
	if err := h.audit.Record(r.Context(), tid, actor, action, "workflow", resourceID, detail); err != nil {
		// 审计失败不阻断主流程（与 pipeline 同款取舍），日志留痕
		log.Printf("[workflow] 审计记录失败 action=%s: %v", action, err)
	}
}
