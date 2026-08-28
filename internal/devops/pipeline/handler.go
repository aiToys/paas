// handler.go Pipeline HTTP handler：Pipeline/Template CRUD + 权限 + 审计。
//
// 路由（composite 按路径分发，参照 devops handler 模式）：
//
//	/api/applications/{id}/pipelines[/{pid}]  GET/POST/PUT/DELETE
//	/api/pipeline-templates                     GET
//	/api/pipelineruns[...]                      Task 12（run/approve/abort）
//
// 响应契约统一 {data:T}/{error:msg}（httputil.WriteData/WriteServiceError）；
// 权限经 Authorize 注入（依赖倒置，cmd/core 桥接 gateway.Require）；
// 审计经 AuditRecorder 注入（依赖倒置，cmd/core 桥接 security.AuditStore）。
package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/aitoys/paas/internal/core/application"
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
	// isPlatformAdmin 判定调用者是否平台超管（admin 模板 CRUD 用，nil 保守按 false）。
	isPlatformAdmin func(*http.Request) bool
	// appGuard 应用级权限 enforcement（受限应用触发/审批需成员角色）；nil 跳过。
	appGuard *application.AppGuard
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

// WithPlatformAdmin 注入平台超管判定（admin 模板 CRUD 用）。
func WithPlatformAdmin(f func(*http.Request) bool) HandlerOpt {
	return func(h *Handler) { h.isPlatformAdmin = f }
}

// WithAppGuard 注入应用级权限 enforcement（受限应用流水线触发需 write、审批需 release）。
func WithAppGuard(g *application.AppGuard) HandlerOpt {
	return func(h *Handler) { h.appGuard = g }
}

// platformAdmin 判定调用者是否平台超管；未注入时保守按 false（最小权限）。
func (h *Handler) platformAdmin(r *http.Request) bool {
	if h.isPlatformAdmin == nil {
		return false
	}
	return h.isPlatformAdmin(r)
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
	case strings.HasPrefix(r.URL.Path, "/api/admin/pipeline-templates"):
		h.serveAdminTemplates(w, r) // Task B2
	case strings.HasPrefix(r.URL.Path, "/api/admin/pipelineruns"):
		h.serveAdminRuns(w, r) // admin 跨租户 PipelineRun 总览
	case strings.HasPrefix(r.URL.Path, "/api/pipeline-templates"):
		h.serveTemplates(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/pipelineruns"):
		h.serveRuns(w, r) // Task 12
	case strings.HasPrefix(r.URL.Path, "/api/webhooks/pipeline/"):
		h.serveWebhook(w, r) // webhook 触发（token 鉴权，无 auth 中间件）
	case strings.HasPrefix(r.URL.Path, "/api/applications/"):
		h.serveAppPipelines(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveWebhook 处理 POST /api/webhooks/pipeline/{pid}?token=<Token>（Gitea push event）。
// 无 auth 中间件（token 鉴权）；不挂 /docs 公开契约（内部端点，webhook 源调）。
func (h *Handler) serveWebhook(w http.ResponseWriter, r *http.Request) {
	pid := strings.TrimPrefix(r.URL.Path, "/api/webhooks/pipeline/")
	pid = strings.Trim(pid, "/")
	if pid == "" || strings.Contains(pid, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.webhookTrigger(w, r, pid)
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
			for i := range list {
				list[i] = maskTrigger(list[i])
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
			// get 返回 token（同租户可见，designer 展示 webhook URL 配 Gitea 用；list 才 mask）
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
	// trigger type 校验（空默认 manual；cron 暂未实现 -> 400）
	switch body.Trigger.Type {
	case "", TriggerManual:
		body.Trigger.Type = TriggerManual
	case TriggerWebhook:
		// webhook：normalize 时生成 token（若未提供）
	default:
		httputil.WriteError(w, http.StatusBadRequest, "unsupported trigger type（仅支持 manual/webhook）")
		return
	}
	p := Pipeline{
		AppID: appID, Name: body.Name, Kind: body.Kind, TemplateID: body.TemplateID,
		ParamOverrides: body.ParamOverrides, Trigger: normalizeTrigger(body.Trigger),
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
	// trigger type 校验 + token 保留（更新时前端不传 token -> 保留原 token，避免 webhook URL 失效）
	switch p.Trigger.Type {
	case "", TriggerManual:
		p.Trigger.Type = TriggerManual
		p.Trigger.Token = "" // manual 无需 token
	case TriggerWebhook:
		if p.Trigger.Token == "" {
			p.Trigger.Token = existing.Trigger.Token // 保留原 token
			if p.Trigger.Token == "" {
				p.Trigger.Token = generateWebhookToken() // 原本也无（旧 pipeline），生成
			}
		}
	default:
		httputil.WriteError(w, http.StatusBadRequest, "unsupported trigger type（仅支持 manual/webhook）")
		return
	}
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

// serveAdminRuns 处理 GET /api/admin/pipelineruns（super_admin 跨租户 PipelineRun 总览）。
// 返回全租户最近运行（带 TenantID，LIMIT 1000），可选 ?status= 过滤。只读。
func (h *Handler) serveAdminRuns(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden: super_admin required")
		return
	}
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := h.runs.ListAllRuns(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// serveAdminTemplates 处理 /api/admin/pipeline-templates[/{id}]（super_admin CRUD 公共模板）。
// 保护：仅 super_admin；create 强制 Builtin=false（防伪造 builtin）；update/delete 走 Repository，
// builtin 模板由 store 层拒（ErrTemplateBuiltin→409）。模板为平台级（tenant_id=NULL）+ 全部租户自定义。
func (h *Handler) serveAdminTemplates(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden: super_admin required")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/pipeline-templates")
	rest = strings.Trim(rest, "/")
	// /api/admin/pipeline-templates（列表 / 创建）
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			list, err := h.templates.ListTemplates(r.Context())
			if err != nil {
				httputil.WriteInternalError(w, err)
				return
			}
			httputil.WriteData(w, list)
		case http.MethodPost:
			var t PipelineTemplate
			if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
				httputil.WriteError(w, http.StatusBadRequest, "invalid body")
				return
			}
			t.Builtin = false // admin 创建必为非 builtin（builtin 走代码发版）
			// 平台级公共模板（tenant 空）无需补租户：store 对平台预置不强制 tenant_id。
			saved, err := h.templates.CreateTemplate(r.Context(), t)
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			h.recordAudit(r, "create", "pipeline_template", saved.ID, "")
			httputil.WriteDataCreated(w, saved)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	// /api/admin/pipeline-templates/{id}
	id := rest
	switch r.Method {
	case http.MethodPut:
		var t PipelineTemplate
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		t.ID = id
		t.Builtin = false
		updated, err := h.templates.UpdateTemplate(r.Context(), t)
		if err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		h.recordAudit(r, "update", "pipeline_template", id, "")
		httputil.WriteData(w, updated)
	case http.MethodDelete:
		if err := h.templates.DeleteTemplate(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		h.recordAudit(r, "delete", "pipeline_template", id, "")
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveRuns 处理 /api/pipelineruns[/{id}[/stages/{idx}/approve|/abort]]。
//
//	GET  /api/pipelineruns?appId=&pipelineId=&status=  列表
//	GET  /api/pipelineruns/{id}                       详情
//	POST /api/pipelineruns/{id}/stages/{idx}/approve  恢复 paused run
//	POST /api/pipelineruns/{id}/abort                 终止 run
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
		// 应用级权限：审批门禁是生产发布动作，受限应用需 release（app-maintainer+）。
		if h.appGuard != nil {
			if run, err := h.runs.GetRun(r.Context(), id); err == nil {
				if !h.allowApp(w, r, run.AppID, application.AppActionRelease) {
					return
				}
			}
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

	// POST /{id}/stages/{idx}/canary 金丝雀验证决策（确认放量 promote / 终止 terminate）
	if r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "stages" && parts[3] == "canary" {
		if !h.allow(w, r, PermPipelineWrite) {
			return
		}
		if h.engine == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
			return
		}
		var in struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Action != "promote" && in.Action != "terminate" {
			httputil.WriteError(w, http.StatusBadRequest, "action 必须是 promote 或 terminate")
			return
		}
		run, err := h.runs.GetRun(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		// 应用级权限：金丝雀放量是生产发布动作，受限应用需 release（app-maintainer+）。
		if !h.allowApp(w, r, run.AppID, application.AppActionRelease) {
			return
		}
		stageIdx := atoiSafe(parts[2])
		// prod:write 护栏：promote 直接改生产基线 Workload 镜像（比 approve 更强的写操作）。
		if in.Action == "promote" && h.envType != nil && stageIdx >= 0 && stageIdx < len(run.StageRuns) {
			if envID, _ := run.StageRuns[stageIdx].Input["envId"].(string); envID != "" {
				if etype, err := h.envType(r.Context(), envID); err == nil && etype == environment.TypeProd && !h.hasProdWrite(w, r) {
					return
				}
			}
		}
		if err := h.engine.CanaryResume(r.Context(), id, stageIdx, in.Action == "promote"); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		h.recordAudit(r, "canary_"+in.Action, "pipeline_run", id, fmt.Sprintf("stage=%d", stageIdx))
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
	// POST /{id}/retry 重试失败 run（从失败 stage 重新推进，调试闭环）
	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "retry" {
		if !h.allow(w, r, PermPipelineWrite) {
			return
		}
		// 应用级权限：重试会重新执行后续 stage（含 CD 的 deploy/release），按 run 的
		// stage 构成判定动作（与 triggerRun 同源：含 deploy/release/promote/approve → release）。
		if h.appGuard != nil {
			if run, err := h.runs.GetRun(r.Context(), id); err == nil {
				action := application.AppActionWrite
				for _, sr := range run.StageRuns {
					if sr.Type == StageDeploy || sr.Type == StageRelease || sr.Type == StagePromote || sr.Type == StageApprove || sr.Type == StageCanary {
						action = application.AppActionRelease
						break
					}
				}
				if !h.allowApp(w, r, run.AppID, action) {
					return
				}
			}
		}
		if h.engine == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
			return
		}
		if err := h.engine.Retry(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		h.recordAudit(r, "retry", "pipeline_run", id, "")
		httputil.WriteData(w, map[string]string{"retried": id})
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// allowApp 校验受限应用的应用级权限；未注入/非受限放行，不通过回 403。
func (h *Handler) allowApp(w http.ResponseWriter, r *http.Request, appID, action string) bool {
	if h.appGuard == nil || h.appGuard.Allow(r, appID, action) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: 无该应用的应用级权限（"+action+"）")
	return false
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
	resolved, rerr := ResolveStages(r.Context(), tpl.Stages, p.ParamOverrides, h.paramResolver, appID, body.Branch)
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
	// 应用级权限：受限应用触发需成员角色——含 deploy/release/promote/approve stage 的 CD 流水线
	// 需 release 权限（app-maintainer+，测试人员被拦），纯 CI（build/test）需 write（app-developer+）。
	if h.appGuard != nil {
		action := application.AppActionWrite
		for _, st := range resolved {
			if st.Type == StageDeploy || st.Type == StageRelease || st.Type == StagePromote || st.Type == StageApprove || st.Type == StageCanary {
				action = application.AppActionRelease
				break
			}
		}
		if !h.allowApp(w, r, appID, action) {
			return
		}
	}
	// prod:write 校验：deploy stage 目标环境为 prod，或 promote stage 目标环境（前序 deploy envId
	// 的下一阶）为 prod 时，要求调用者持 prod:write（防 developer 经 [deploy test, promote] 绕过）
	if !h.allowProdFlow(w, r, resolved) {
		return
	}
	created, err := h.triggerRunInternal(r.Context(), appID, pid, resolved, body.Branch, body.Commit, body.Version, TriggerManual)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	h.recordAudit(r, "run", "pipeline_run", created.ID, fmt.Sprintf("pipeline=%s branch=%s", pid, body.Branch))
	httputil.WriteDataCreated(w, created)
}

// triggerRunInternal 触发 run 核心：resolved stages -> 单实例校验 -> 创建 run + 启动 engine。
// perm + prod:write 校验由调用方做（manual triggerRun 做；webhook 跳过，approve 门禁兜底 CD）。
// 错误经 toHTTPStatus 映射：validateDeployEnvs 失败 -> 400，ErrActiveRunExists -> 409，NotFound -> 404。
func (h *Handler) triggerRunInternal(ctx context.Context, appID, pid string, resolved []StageDef, branch, commit, version, trigger string) (PipelineRun, error) {
	if msg := validateDeployEnvs(resolved); msg != "" {
		return PipelineRun{}, errors.New(msg)
	}
	active, err := h.runs.HasActiveRun(ctx, pid)
	if err != nil {
		return PipelineRun{}, err
	}
	if active {
		return PipelineRun{}, ErrActiveRunExists
	}
	repoID := ""
	if h.repos != nil {
		if rid, rerr := h.repos.ResolveInternalRepo(ctx, appID); rerr == nil {
			repoID = rid
		}
	}
	stageRuns := make([]StageRun, len(resolved))
	for i, s := range resolved {
		stageRuns[i] = StageRun{Index: i, Type: s.Type, Name: s.Name, Status: StagePending, Input: s.Params}
	}
	run := PipelineRun{
		PipelineID: pid, AppID: appID, Branch: branch, Commit: commit,
		RepoID: repoID, Version: version, Trigger: trigger,
		Status: RunRunning, CurrentStage: 0, StageRuns: stageRuns,
	}
	created, err := h.runs.CreateRun(ctx, run)
	if err != nil {
		return PipelineRun{}, err
	}
	if h.engine != nil {
		h.engine.Start(ctx, created.ID)
	}
	return created, nil
}

// webhookTrigger POST /api/webhooks/pipeline/{pid}?token=<Token>。
// 接收 Gitea/GitHub push event，验证 token + 解析 branch/commit + 触发 run。
// 无 auth 中间件（token 鉴权）；webhook 触发跳过 prod:write（CD pipeline 靠 approve 门禁兜底）。
// 不匹配分支 glob 或非分支 push（tag）静默返 200（webhook 源不感知平台内部跳过）。
func (h *Handler) webhookTrigger(w http.ResponseWriter, r *http.Request, pid string) {
	ctx := r.Context()
	// webhook 无 tenant ctx，跨租户按 ID 查 pipeline（token 鉴权提供安全性）
	p, err := h.pipes.GetPipelineAny(ctx, pid)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if p.Trigger.Type != TriggerWebhook {
		httputil.WriteError(w, http.StatusBadRequest, "pipeline trigger is not webhook")
		return
	}
	// token 常量时间比较（防时序枚举）；pipeline 未配 token 或请求无 token 拒
	token := r.URL.Query().Get("token")
	if p.Trigger.Token == "" || token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(p.Trigger.Token)) != 1 {
		httputil.WriteError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	// 解析 Gitea push event（ref/after；Gitea 与 GitHub 字段名一致）
	var push struct {
		Ref   string `json:"ref"`
		After string `json:"after"`
	}
	if err := json.NewDecoder(r.Body).Decode(&push); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid push payload")
		return
	}
	branch := strings.TrimPrefix(push.Ref, "refs/heads/")
	if branch == push.Ref || branch == "" {
		// 非分支引用（tag/PR 等），静默忽略（返 200 避免 webhook 源重试）
		w.WriteHeader(http.StatusOK)
		return
	}
	// 分支 glob 匹配（Trigger.Branch 空=全部分支）
	if p.Trigger.Branch != "" && !globMatch(p.Trigger.Branch, branch) {
		w.WriteHeader(http.StatusOK)
		return
	}
	// 派生 pipeline 租户 ctx（后续 GetTemplate/CreateRun 要求 tenant；webhook 触发归属 pipeline 租户）
	wctx := tenant.WithTenant(ctx, p.TenantID)
	// 解析模板 stages + 占位符（webhook branch 注入 {{run.branch}}）
	tpl, terr := h.templates.GetTemplate(wctx, p.TemplateID)
	if terr != nil {
		httputil.WriteError(w, http.StatusNotFound, "pipeline template not found")
		return
	}
	resolved, rerr := ResolveStages(wctx, tpl.Stages, p.ParamOverrides, h.paramResolver, p.AppID, branch)
	if rerr != nil {
		httputil.WriteError(w, http.StatusBadRequest, "参数解析失败: "+rerr.Error())
		return
	}
	// webhook 触发免 prod:write（无用户身份），但若 stages 触达 prod 必须含 approve 门禁兜底，
	// 防去掉 approve 的 CD 模板被 webhook 直接部署生产（manual 路径靠调用者 prod:write，webhook 靠 approve）。
	if h.targetsProd(wctx, resolved) && !hasApproveGate(resolved) {
		httputil.WriteError(w, http.StatusForbidden, "webhook 触发的生产部署流水线必须包含 approve 审批门禁")
		return
	}
	created, err := h.triggerRunInternal(wctx, p.AppID, pid, resolved, branch, push.After, "", TriggerWebhook)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	// audit：webhook 无用户身份，actor="webhook"，tenant 取 pipeline 租户
	h.recordAuditCtx(wctx, p.TenantID, "webhook", "run", "pipeline_run", created.ID, fmt.Sprintf("pipeline=%s branch=%s via webhook", pid, branch))
	w.WriteHeader(http.StatusOK)
}

// generateWebhookToken 生成 32 字节随机 hex（webhook URL 鉴权 token）。
func generateWebhookToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error()) // 无熵优于弱 token
	}
	return hex.EncodeToString(b)
}

// normalizeTrigger 规范化触发配置：webhook 类型且无 token 时自动生成（创建/更新 pipeline 用）。
func normalizeTrigger(t PipelineTrigger) PipelineTrigger {
	if t.Type == TriggerWebhook && t.Token == "" {
		t.Token = generateWebhookToken()
	}
	return t
}

// maskTrigger 清空 trigger token（get/list 返回前端时调用，防泄漏）。
// create/update 响应保留 token（前端展示一次 webhook URL 后由用户保存）。
func maskTrigger(p Pipeline) Pipeline {
	if p.Trigger.Token != "" {
		p.Trigger.Token = ""
	}
	return p
}

// globMatch 简单分支 glob 匹配（支持 * 通配，如 "feature-*" 匹配 "feature-x"）。
// 用 path.Match（分支名无路径分隔符，等价于 shell glob）。
func globMatch(pattern, name string) bool {
	matched, err := path.Match(pattern, name)
	return err == nil && matched
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

// allowProdFlow 扫描 deploy + promote + canary stage 的目标环境，任一为 prod 要求 PermProdWrite。
//
// deploy stage：直接取 params.envId 查类型。
// promote stage：取前序最近 deploy stage 的 envId，经 promoteTargetType 算下一阶环境类型
// （promote 提升的是前序 deploy 产生的 release，target = NextPromoteTarget(前序 deploy envId)）。
// 覆盖 [deploy test, promote] 这类把变更间接发布到 prod 的链路（防绕过 prod:write）。
func (h *Handler) allowProdFlow(w http.ResponseWriter, r *http.Request, stages []StageDef) bool {
	if !h.targetsProd(r.Context(), stages) {
		return true // 不触 prod，放行
	}
	return h.hasProdWrite(w, r)
}

// targetsProd 静态判定 stages 是否触达 prod 环境（deploy 到 prod，或 promote 链路目标 prod）。
// 解析失败时保守按 prod 处理（fail-closed，防 env 查不到时绕过）。供 allowProdFlow 与 webhook 门禁复用。
func (h *Handler) targetsProd(ctx context.Context, stages []StageDef) bool {
	if h.envType == nil && h.promoteTargetType == nil {
		return false // 未注入环境解析（测试场景），不触 prod
	}
	lastDeployEnvID := ""
	for _, s := range stages {
		switch s.Type {
		case StageDeploy:
			lastDeployEnvID = strOr(s.Params, "envId", "")
			if lastDeployEnvID == "" {
				continue
			}
			if h.envType == nil {
				return true // 已触 deploy 但无法解析类型，fail-closed
			}
			if etype, err := h.envType(ctx, lastDeployEnvID); err != nil || etype == environment.TypeProd {
				return true
			}
		case StageCanary:
			// canary 隐含对目标环境部署/放量，与 deploy 同判（fail-closed）。
			if h.envType == nil {
				return true // 无法解析类型，保守按触 prod 处理（与 deploy case 同语义）
			}
			if etype, err := h.envType(ctx, strOr(s.Params, "envId", "")); err != nil || etype == environment.TypeProd {
				return true
			}
		case StagePromote:
			if lastDeployEnvID == "" || h.promoteTargetType == nil {
				continue
			}
			if etype, err := h.promoteTargetType(ctx, lastDeployEnvID); err != nil || etype == environment.TypeProd {
				return true
			}
		}
	}
	return false
}

// hasApproveGate 判定 stages 是否含 approve（人工审批门禁）stage。
// webhook 触发的 CD pipeline 免 prod:write，强制要求 approve 门禁兜底（防去掉 approve 的模板被 webhook 直接部署 prod）。
func hasApproveGate(stages []StageDef) bool {
	for _, s := range stages {
		if s.Type == StageApprove {
			return true
		}
	}
	return false
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
		errors.Is(err, ErrNotPaused), errors.Is(err, ErrStageNotCurrent), errors.Is(err, ErrNotRunning),
		errors.Is(err, ErrNotFailed), errors.Is(err, ErrTemplateBuiltin):
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
	h.recordAuditCtx(r.Context(), tid, actor, action, resourceType, resourceID, detail)
}

// recordAuditCtx 记审计（ctx 版本，供 webhook 等无 *http.Request 的触发路径用）。
// webhook 无用户身份，调用方传 actor="webhook" + pipeline 租户作 tid。
func (h *Handler) recordAuditCtx(ctx context.Context, tid, actor, action, resourceType, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(ctx, tid, actor, action, resourceType, resourceID, detail)
}
