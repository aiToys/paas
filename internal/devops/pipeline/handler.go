// handler.go Pipeline HTTP handler：Pipeline/Template CRUD + 权限 + 审计。
//
// 路由（composite 按路径分发，参照 devops handler 模式）：
//   /api/applications/{id}/pipelines[/{pid}]  GET/POST/PUT/DELETE
//   /api/pipeline-templates                     GET
//   /api/pipelineruns[...]                      Task 12（run/approve/abort）
//
// 响应契约统一 {data:T}/{error:msg}（httputil.WriteData/WriteServiceError）；
// 权限经 Authorize 注入（依赖倒置，cmd/core 桥接 gateway.Require）；
// 审计经 AuditRecorder 注入（依赖倒置，cmd/core 桥接 security.AuditStore）。
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// 权限常量（identity.BuiltinRoles developer/viewer 对齐）。
const (
	PermPipelineRead  = "pipeline:read"
	PermPipelineWrite = "pipeline:write"
)

// AuditRecorder 审计记录（依赖倒置，避免 pipeline->identity import）。
// 与 identity.AuditRecorder 同源签名，cmd/core 装配桥接。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// Handler Pipeline HTTP 入口（composite 按路径分发）。
type Handler struct {
	pipes     Repository
	runs      RunRepository
	templates TemplateRepository
	engine    *Engine
	// Authorize 权限校验（nil 跳过，测试场景）。
	Authorize func(r *http.Request, perm string) bool
	// audit 审计记录器（nil 跳过）。
	audit   AuditRecorder
	actorFn func(r *http.Request) string
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithAuthorize 注入权限校验。
func WithAuthorize(f func(r *http.Request, perm string) bool) HandlerOpt {
	return func(h *Handler) { h.Authorize = f }
}

// WithAudit 注入审计记录器。
func WithAudit(a AuditRecorder) HandlerOpt {
	return func(h *Handler) { h.audit = a }
}

// WithActorFn 注入 actor 解析（从 r 取 userID，审计用）。
func WithActorFn(f func(r *http.Request) string) HandlerOpt {
	return func(h *Handler) { h.actorFn = f }
}

// NewHandler 构造 Handler。engine 可选（CRUD 不用，run 推进用，Task 12）。
func NewHandler(pipes Repository, runs RunRepository, templates TemplateRepository, engine *Engine, opts ...HandlerOpt) *Handler {
	h := &Handler{pipes: pipes, runs: runs, templates: templates, engine: engine}
	for _, o := range opts {
		o(h)
	}
	return h
}

// allow 权限校验，失败写 403。
func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 分发 /api/applications/{id}/pipelines[...] 与 /api/pipeline-templates 与 /api/pipelineruns[...]。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/pipeline-templates"):
		h.serveTemplates(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/pipelineruns"):
		h.serveRuns(w, r) // Task 12
	case strings.HasPrefix(r.URL.Path, "/api/applications/"):
		h.serveAppPipelines(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveAppPipelines 处理 /api/applications/{id}/pipelines[/{pid}]。
func (h *Handler) serveAppPipelines(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	// /api/applications/{id}/pipelines[/{pid}]
	if len(parts) < 2 || parts[1] != "pipelines" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]

	// /api/applications/{id}/pipelines
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			if !h.allow(w, r, PermPipelineRead) {
				return
			}
			list, err := h.pipes.ListPipelines(r.Context(), appID)
			if err != nil {
				httputil.WriteInternalError(w, err)
				return
			}
			httputil.WriteData(w, list)
		case http.MethodPost:
			h.createPipeline(w, r, appID)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// /api/applications/{id}/pipelines/{pid}
	if len(parts) == 3 {
		pid := parts[2]
		switch r.Method {
		case http.MethodGet:
			if !h.allow(w, r, PermPipelineRead) {
				return
			}
			p, err := h.pipes.GetPipeline(r.Context(), pid)
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			httputil.WriteData(w, p)
		case http.MethodPut:
			h.updatePipeline(w, r, appID, pid)
		case http.MethodDelete:
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			if err := h.pipes.DeletePipeline(r.Context(), pid); err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			h.recordAudit(r, "delete", "pipeline", pid, "")
			httputil.WriteData(w, map[string]string{"deleted": pid})
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// createPipeline POST /api/applications/{id}/pipelines。
// 支持从模板创建（templateId 非空时 GetTemplate 复制 stages，用户可后续改）。
func (h *Handler) createPipeline(w http.ResponseWriter, r *http.Request, appID string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	var body struct {
		Name       string          `json:"name"`
		Kind       string          `json:"kind"`
		TemplateID string          `json:"templateId"`
		Stages     []StageDef      `json:"stages"`
		Trigger    PipelineTrigger `json:"trigger"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p := Pipeline{
		AppID: appID, Name: body.Name, Kind: body.Kind, TemplateID: body.TemplateID,
		Stages: body.Stages, Trigger: body.Trigger,
	}
	// 从模板创建：templateId 非空时 GetTemplate 复制 stages（用户可后续改）
	if body.TemplateID != "" {
		tpl, err := h.templates.GetTemplate(r.Context(), body.TemplateID)
		if err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		p.Kind = tpl.Kind
		if len(body.Stages) == 0 {
			p.Stages = cloneStages(tpl.Stages)
		}
	}
	created, err := h.pipes.CreatePipeline(r.Context(), p)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	h.recordAudit(r, "create", "pipeline", created.ID, "")
	httputil.WriteDataCreated(w, created)
}

// updatePipeline PUT /api/applications/{id}/pipelines/{pid}。
// 保留 TenantID/CreatedAt（不可改），AppID 以路径为准（防越权改归属）。
func (h *Handler) updatePipeline(w http.ResponseWriter, r *http.Request, appID, pid string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	var p Pipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	existing, err := h.pipes.GetPipeline(r.Context(), pid)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	p.ID = pid
	p.TenantID = existing.TenantID
	p.AppID = appID
	p.CreatedAt = existing.CreatedAt
	updated, err := h.pipes.UpdatePipeline(r.Context(), p)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	httputil.WriteData(w, updated)
}

// serveTemplates GET /api/pipeline-templates（平台预置 + 租户自定义，平台级共享）。
func (h *Handler) serveTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermPipelineRead) {
		return
	}
	list, err := h.templates.ListTemplates(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// serveRuns 占位（Task 12 实现 run/approve/abort）。
func (h *Handler) serveRuns(w http.ResponseWriter, r *http.Request) {
	httputil.WriteError(w, http.StatusNotImplemented, "pipeline runs: not implemented yet")
}

// ---------- 辅助 ----------

// toHTTPStatus 仓储 sentinel -> HTTP 状态码。
func toHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrPipelineNotFound), errors.Is(err, ErrRunNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrPipelineExists), errors.Is(err, ErrActiveRunExists),
		errors.Is(err, ErrRunExists), errors.Is(err, ErrTemplateExists):
		return http.StatusConflict
	case errors.Is(err, ErrNoTenant):
		return http.StatusBadRequest
	}
	return http.StatusBadRequest
}

// recordAudit 记审计（audit/actorFn nil 跳过）。
func (h *Handler) recordAudit(r *http.Request, action, resourceType, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	actor := ""
	if h.actorFn != nil {
		actor = h.actorFn(r)
	}
	_ = h.audit.Record(r.Context(), "", actor, action, resourceType, resourceID, detail)
}
