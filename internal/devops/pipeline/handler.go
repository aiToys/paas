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
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
)

// 权限常量（identity.BuiltinRoles developer/viewer 对齐）。
const (
	PermPipelineRead  = "pipeline:read"
	PermPipelineWrite = "pipeline:write"
	// PermProdWrite 生产写权限（与 identity.PermProdWrite 同值；本地定义避免 pipeline->identity import）。
	PermProdWrite = "prod:write"
)

// AuditRecorder 审计记录（依赖倒置，避免 pipeline->identity import）。
// 与 identity.AuditRecorder 同源签名，cmd/core 装配桥接。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// EnvTypeResolver 生产 deploy 校验（pipeline run 的 deploy stage 到 prod 要求 prod:write）。
type EnvTypeResolver func(ctx context.Context, envID string) (string, error)

// RepoResolver 解析 app 绑定的 internal CodeRepo ID（build stage 用；nil 时 repoID 空）。
type RepoResolver interface {
	ResolveInternalRepo(ctx context.Context, appID string) (string, error)
}

// Handler Pipeline HTTP 入口（composite 按路径分发）。
type Handler struct {
	pipes     Repository
	runs      RunRepository
	templates TemplateRepository
	engine    *Engine
	// Authorize 权限校验（nil 跳过，测试场景）。
	Authorize func(r *http.Request, perm string) bool
	// envType 生产 deploy 校验（nil 跳过 prod:write 检查）。
	envType EnvTypeResolver
	// repos 解析 app 绑定的 internal CodeRepo（nil 时 repoID 空）。
	repos RepoResolver
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

// WithEnvType 注入环境类型解析（生产 deploy 校验）。
func WithEnvType(f EnvTypeResolver) HandlerOpt {
	return func(h *Handler) { h.envType = f }
}

// WithRepoResolver 注入 CodeRepo 解析器。
func WithRepoResolver(r RepoResolver) HandlerOpt {
	return func(h *Handler) { h.repos = r }
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

	// /api/applications/{id}/pipelines/{pid}/run
	if len(parts) == 4 && parts[3] == "run" {
		if r.Method != http.MethodPost {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.triggerRun(w, r, appID, parts[2])
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

// serveRuns 处理 /api/pipelineruns[/{id}[/stages/{idx}/approve|/abort]]。
//   GET  /api/pipelineruns?appId=&pipelineId=&status=  列表
//   GET  /api/pipelineruns/{id}                       详情
//   POST /api/pipelineruns/{id}/stages/{idx}/approve  恢复 paused run
//   POST /api/pipelineruns/{id}/abort                 终止 run
func (h *Handler) serveRuns(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/pipelineruns")
	rest = strings.Trim(rest, "/")

	// GET 列表
	if rest == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermPipelineRead) {
			return
		}
		list, err := h.runs.ListRuns(r.Context(),
			r.URL.Query().Get("appId"),
			r.URL.Query().Get("pipelineId"),
			r.URL.Query().Get("status"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}

	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]

	// GET /{id}
	if r.Method == http.MethodGet && len(parts) == 1 {
		if !h.allow(w, r, PermPipelineRead) {
			return
		}
		run, err := h.runs.GetRun(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		httputil.WriteData(w, run)
		return
	}

	// POST /{id}/stages/{idx}/approve
	if r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "stages" && parts[3] == "approve" {
		if !h.allow(w, r, PermPipelineWrite) {
			return
		}
		if h.engine == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
			return
		}
		stageIdx := atoiSafe(parts[2])
		if err := h.engine.Resume(r.Context(), id, stageIdx); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		h.recordAudit(r, "approve", "pipeline_run", id, fmt.Sprintf("stage=%d", stageIdx))
		httputil.WriteData(w, map[string]string{"resumed": id})
		return
	}

	// POST /{id}/abort
	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "abort" {
		if !h.allow(w, r, PermPipelineWrite) {
			return
		}
		if h.engine == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
			return
		}
		if err := h.engine.Abort(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		h.recordAudit(r, "abort", "pipeline_run", id, "")
		httputil.WriteData(w, map[string]string{"aborted": id})
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// triggerRun POST /api/applications/{id}/pipelines/{pid}/run。
// body {branch, commit?, version?} -> 建 PipelineRun + engine.Start。
// prod:write 横切：deploy stage 目标环境为 prod 要求调用者持 prod:write。
// 单实例串行：已有 running/paused run 拒绝（ErrActiveRunExists）。
func (h *Handler) triggerRun(w http.ResponseWriter, r *http.Request, appID, pid string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	var body struct {
		Branch  string `json:"branch"`
		Commit  string `json:"commit"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Branch == "" {
		httputil.WriteError(w, http.StatusBadRequest, "branch required")
		return
	}
	p, err := h.pipes.GetPipeline(r.Context(), pid)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	// prod:write 校验：deploy stage 目标环境为 prod 时要求调用者持 prod:write
	if !h.allowProdDeploy(w, r, p.Stages) {
		return
	}
	// 单实例串行：已有 running/paused run 拒绝
	active, err := h.runs.HasActiveRun(r.Context(), pid)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if active {
		httputil.WriteServiceError(w, http.StatusConflict, ErrActiveRunExists)
		return
	}
	// 解析 repoID（app 绑定的 internal CodeRepo；build stage 用）
	repoID := ""
	if h.repos != nil {
		if rid, rerr := h.repos.ResolveInternalRepo(r.Context(), appID); rerr == nil {
			repoID = rid
		}
	}
	// 初始化 stage_runs
	stageRuns := make([]StageRun, len(p.Stages))
	for i, s := range p.Stages {
		stageRuns[i] = StageRun{Index: i, Type: s.Type, Name: s.Name, Status: StagePending}
	}
	run := PipelineRun{
		PipelineID: pid, AppID: appID, Branch: body.Branch, Commit: body.Commit,
		RepoID: repoID, Version: body.Version, Trigger: "manual",
		Status: RunRunning, CurrentStage: 0, StageRuns: stageRuns,
	}
	created, err := h.runs.CreateRun(r.Context(), run)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	h.recordAudit(r, "run", "pipeline_run", created.ID, fmt.Sprintf("pipeline=%s branch=%s", pid, body.Branch))
	if h.engine != nil {
		h.engine.Start(r.Context(), created.ID)
	}
	httputil.WriteDataCreated(w, created)
}

// allowProdDeploy 扫描 deploy stage 目标环境，prod 要求 prod:write（防 developer 经 CI 直接 deploy prod）。
func (h *Handler) allowProdDeploy(w http.ResponseWriter, r *http.Request, stages []StageDef) bool {
	if h.envType == nil {
		return true // 未注入环境解析（测试场景），跳过
	}
	for _, s := range stages {
		if s.Type != StageDeploy {
			continue
		}
		envID := strOr(s.Params, "envId", "")
		if envID == "" {
			continue
		}
		etype, err := h.envType(r.Context(), envID)
		if err == nil && etype == environment.TypeProd {
			if h.Authorize != nil && !h.Authorize(r, PermProdWrite) {
				httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+PermProdWrite)
				return false
			}
		}
	}
	return true
}

// atoiSafe 字符串转 int（失败返 0，stage idx 用）。
func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ---------- 辅助 ----------

// toHTTPStatus 仓储 sentinel -> HTTP 状态码。
func toHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrPipelineNotFound), errors.Is(err, ErrRunNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrPipelineExists), errors.Is(err, ErrActiveRunExists),
		errors.Is(err, ErrRunExists), errors.Is(err, ErrTemplateExists),
		errors.Is(err, ErrNotPaused), errors.Is(err, ErrStageNotCurrent), errors.Is(err, ErrNotRunning):
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
