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
	"github.com/aitoys/paas/pkg/tenant"
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

// PromoteTargetTypeResolver 解析 promote stage 目标环境类型（前序 deploy envId 的下一阶环境）。
// 用于 triggerRun 时静态预演 promote 链：目标 prod 则要求 PermProdWrite（与 deploy 同源横切）。
// nil 时跳过 promote prod 校验（测试场景）。
type PromoteTargetTypeResolver func(ctx context.Context, envID string) (string, error)

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
	// promoteTargetType 解析 promote stage 的目标环境类型（nil 跳过 promote prod 校验）。
	// 取前序 deploy stage 的 envId → NextPromoteTarget(envId).Type；目标 prod 要求 PermProdWrite
	// （防 developer 经 [deploy test, promote] 链路绕过 prod:write 把变更发布到生产）。
	promoteTargetType PromoteTargetTypeResolver
	// repos 解析 app 绑定的 internal CodeRepo（nil 时 repoID 空）。
	repos RepoResolver
	// paramResolver 解析模板占位符 {{app.env.*}}/{{app.repo}}（nil 时占位符原样，测试场景）。
	paramResolver ParamResolver
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

// WithPromoteTargetType 注入 promote 目标环境类型解析（promote 到 prod 校验）。
func WithPromoteTargetType(f PromoteTargetTypeResolver) HandlerOpt {
	return func(h *Handler) { h.promoteTargetType = f }
}

// WithRepoResolver 注入 CodeRepo 解析器。
func WithRepoResolver(r RepoResolver) HandlerOpt {
	return func(h *Handler) { h.repos = r }
}

// WithParamResolver 注入模板占位符解析器（{{app.env.*}}/{{app.repo}}）。
func WithParamResolver(p ParamResolver) HandlerOpt {
	return func(h *Handler) { h.paramResolver = p }
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
// 支持从模板创建（templateId 非空时校验模板存在 + 继承 Kind；stages 运行时从模板解析，不复制）。
func (h *Handler) createPipeline(w http.ResponseWriter, r *http.Request, appID string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	var body struct {
		Name           string          `json:"name"`
		Kind           string          `json:"kind"`
		TemplateID     string          `json:"templateId"`
		ParamOverrides map[string]any  `json:"paramOverrides"`
		Trigger        PipelineTrigger `json:"trigger"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p := Pipeline{
		AppID: appID, Name: body.Name, Kind: body.Kind, TemplateID: body.TemplateID,
		ParamOverrides: body.ParamOverrides, Trigger: body.Trigger,
	}
	// 从模板创建：templateId 非空时校验存在 + 继承 Kind（stages 运行时解析，不复制）
	if body.TemplateID != "" {
		tpl, err := h.templates.GetTemplate(r.Context(), body.TemplateID)
		if err != nil {
			httputil.WriteError(w, http.StatusNotFound, "pipeline template not found")
			return
		}
		p.Kind = tpl.Kind
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
	// 从模板解析 stages（占位符 {{app.env.*}}/{{app.repo}} + ParamOverrides 实例化）。
	tpl, terr := h.templates.GetTemplate(r.Context(), p.TemplateID)
	if terr != nil {
		httputil.WriteError(w, http.StatusNotFound, "pipeline template not found")
		return
	}
	resolved, rerr := ResolveStages(r.Context(), tpl.Stages, p.ParamOverrides, h.paramResolver, appID)
	if rerr != nil {
		httputil.WriteError(w, http.StatusBadRequest, "参数解析失败: "+rerr.Error())
		return
	}
	// deploy stage envId 必填校验（fail-fast，避免注定失败的 run 占住单实例串行槽位）。
	// 占位符解析后 envId 仍空（app 无对应环境）则拒。
	if msg := validateDeployEnvs(resolved); msg != "" {
		httputil.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	// prod:write 校验：deploy stage 目标环境为 prod，或 promote stage 目标环境（前序 deploy envId
	// 的下一阶）为 prod 时，要求调用者持 prod:write（防 developer 经 [deploy test, promote] 绕过）
	if !h.allowProdFlow(w, r, resolved) {
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
	// 初始化 stage_runs（Input 存 resolved params，engine 用 sr.Input 取 stage.Params）
	stageRuns := make([]StageRun, len(resolved))
	for i, s := range resolved {
		stageRuns[i] = StageRun{Index: i, Type: s.Type, Name: s.Name, Status: StagePending, Input: s.Params}
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

// validateDeployEnvs 校验每个 deploy stage 必须含 envId（fail-fast）。
// 返回非空中文消息表示校验失败（handler 返 400）。
func validateDeployEnvs(stages []StageDef) string {
	for _, s := range stages {
		if s.Type == StageDeploy && strOr(s.Params, "envId", "") == "" {
			return "deploy stage 缺 envId 参数: " + s.Name
		}
	}
	return ""
}

// allowProdFlow 扫描 deploy + promote stage 的目标环境，任一为 prod 要求 PermProdWrite。
//
// deploy stage：直接取 params.envId 查类型。
// promote stage：取前序最近 deploy stage 的 envId，经 promoteTargetType 算下一阶环境类型
// （promote 提升的是前序 deploy 产生的 release，target = NextPromoteTarget(前序 deploy envId)）。
// 覆盖 [deploy test, promote] 这类把变更间接发布到 prod 的链路（防绕过 prod:write）。
func (h *Handler) allowProdFlow(w http.ResponseWriter, r *http.Request, stages []StageDef) bool {
	if h.envType == nil && h.promoteTargetType == nil {
		return true // 未注入环境解析（测试场景），跳过
	}
	lastDeployEnvID := ""
	for _, s := range stages {
		switch s.Type {
		case StageDeploy:
			lastDeployEnvID = strOr(s.Params, "envId", "")
			if lastDeployEnvID == "" || h.envType == nil {
				continue
			}
			if etype, err := h.envType(r.Context(), lastDeployEnvID); err == nil && etype == environment.TypeProd {
				if !h.hasProdWrite(w, r) {
					return false
				}
			}
		case StagePromote:
			if lastDeployEnvID == "" || h.promoteTargetType == nil {
				continue
			}
			if etype, err := h.promoteTargetType(r.Context(), lastDeployEnvID); err == nil && etype == environment.TypeProd {
				if !h.hasProdWrite(w, r) {
					return false
				}
			}
		}
	}
	return true
}

// hasProdWrite 校验调用者持 prod:write，失败写 403 返 false。
func (h *Handler) hasProdWrite(w http.ResponseWriter, r *http.Request) bool {
	if h.Authorize == nil || h.Authorize(r, PermProdWrite) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+PermProdWrite)
	return false
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
// tenantID 从 ctx 取（租户管理员可在本租户审计日志查到 pipeline 操作）；ctx 无租户归 "platform"
// （adapter 层 identityAuditAdapter 对空 tenantID 兜底归 platform，与 identity/auth 同源）。
func (h *Handler) recordAudit(r *http.Request, action, resourceType, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	actor := ""
	if h.actorFn != nil {
		actor = h.actorFn(r)
	}
	tid, _ := tenant.TenantFrom(r.Context())
	_ = h.audit.Record(r.Context(), tid, actor, action, resourceType, resourceID, detail)
}
