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
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
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
	// imageMeta 镜像元数据（产物锁定时补 digest/commit/buildRunId）；nil 仅锁 imageId。
	imageMeta ImageMetaResolver
	// deliveries webhook 投递记录（D5-5）；nil 时不记录（投递仍正常触发，仅无可查证据）。
	deliveries DeliveryRepository
}

// WithDeliveries 注入 webhook 投递记录仓储（运维查询 + 幂等重放）。
func WithDeliveries(d DeliveryRepository) HandlerOpt {
	return func(h *Handler) { h.deliveries = d }
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
// WithImageMeta 注入镜像元数据解析（产物锁定 digest/commit/buildRunId）。
func WithImageMeta(f ImageMetaResolver) HandlerOpt {
	return func(h *Handler) { h.imageMeta = f }
}

func WithAppGuard(g *application.AppGuard) HandlerOpt {
	return func(h *Handler) { h.appGuard = g }
}

// platformAdmin 判定调用者是否平台管理员（超管或平台资源角色）；未注入时保守按 false。
// 注入函数在 cmd/core 装配层定义（依赖倒置）；外层 adminGuard 已按 method 精确控权，
// 此处是第二道粗粒度检查（挡纯租户角色）——文案不再写死 super_admin。
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
	case strings.HasPrefix(r.URL.Path, "/api/admin/pipelines"):
		h.serveAdminPipelines(w, r) // admin 跨租户 Pipeline 列表（模板引用预检）
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
	// /api/applications/{id}/agent-ship/run —— 智能研发一条龙触发（2026-10-13）。
	if len(parts) == 3 && parts[1] == "agent-ship" && parts[2] == "run" {
		if r.Method != http.MethodPost {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.triggerAgentShipRun(w, r, parts[0])
		return
	}
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
				writePipeErr(w, err)
				return
			}
			// 归属校验：路径 appID 与 pipeline 实际归属不符统一 404（不泄漏存在性），
			// 防同租户跨应用「移花接木」（安全审计 I1，与 serveRepoPulls 同款）。
			if p.AppID != appID {
				httputil.WriteError(w, http.StatusNotFound, "pipeline 不存在")
				return
			}
			// token 按权限分流（权限审计 C2）：webhook token 可匿名触发 run（webhook 路径
			// 无用户身份），viewer 持 pipeline:read 拿到即读权限提权到写。仅持 pipeline:write
			// （designer 编辑态拼 webhook URL）返回明文，只读一律 mask。
			if h.Authorize != nil && !h.Authorize(r, PermPipelineWrite) {
				p = maskTrigger(p)
			}
			httputil.WriteData(w, p)
		case http.MethodPut:
			h.updatePipeline(w, r, appID, pid)
		case http.MethodDelete:
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			// 应用级权限（权限审计 I3）：删流水线属应用级写操作。
			if !h.allowApp(w, r, appID, application.AppActionWrite) {
				return
			}
			// 归属校验（同 get 分支注释）：不符 404，防跨应用删他人流水线。
			if dp, err := h.pipes.GetPipeline(r.Context(), pid); err != nil {
				writePipeErr(w, err)
				return
			} else if dp.AppID != appID {
				httputil.WriteError(w, http.StatusNotFound, "pipeline 不存在")
				return
			}
			// 活跃 run 前置拦截：级联删 runs 会蒸发 in-flight run 的记录（engine 后续
			// UpdateRun 报 NotFound 静默退出、K8s 构建 Job 孤儿无记录可寻）。
			active, err := h.runs.HasActiveRun(r.Context(), pid)
			if err != nil {
				httputil.WriteServiceError(w, http.StatusInternalServerError, err)
				return
			}
			if active {
				httputil.WriteError(w, http.StatusConflict, "流水线有进行中的运行，请先终止再删除")
				return
			}
			if err := h.pipes.DeletePipeline(r.Context(), pid); err != nil {
				writePipeErr(w, err)
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

	// /api/applications/{id}/pipelines/{pid}/preview —— 执行计划预演（只读，spec §6）。
	if len(parts) == 4 && parts[3] == "preview" {
		if r.Method != http.MethodPost {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.previewRun(w, r, appID, parts[2])
		return
	}

	// /api/applications/{id}/pipelines/{pid}/deliveries —— webhook 投递记录（D5-5）。
	if len(parts) == 4 && parts[3] == "deliveries" {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.listDeliveries(w, r, appID, parts[2])
		return
	}

	// /api/applications/{id}/pipelines/{pid}/deliveries/{deliveryId}/replay —— 幂等重放。
	if len(parts) == 6 && parts[3] == "deliveries" && parts[5] == "replay" {
		if r.Method != http.MethodPost {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.replayDelivery(w, r, appID, parts[2], parts[4])
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// listDeliveries GET /api/applications/{id}/pipelines/{pid}/deliveries?limit=N
// 返回最近投递（accepted/duplicate/skipped/rejected/failed 全量，倒序），供用户自查
// 「push 了为什么没触发」。未装配投递仓储时 501 如实告知（不返空列表冒充「没有投递」）。
func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request, appID, pid string) {
	if !h.allow(w, r, PermPipelineRead) {
		return
	}
	p, err := h.pipes.GetPipeline(r.Context(), pid)
	if err != nil {
		writePipeErr(w, err)
		return
	}
	// 归属校验：路径 appID 与 pipeline 实际归属不符统一 404（同 GET /{pid}，不泄漏存在性）。
	if p.AppID != appID {
		httputil.WriteError(w, http.StatusNotFound, "pipeline 不存在")
		return
	}
	if h.deliveries == nil {
		httputil.WriteError(w, http.StatusNotImplemented, "webhook 投递记录未启用")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	list, err := h.deliveries.ListDeliveries(r.Context(), pid, limit)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
		return
	}
	httputil.WriteData(w, list)
}

// replayDelivery POST .../deliveries/{deliveryId}/replay
// 重放历史投递：复用记录的 branch/commit 重新走触发链（不是重发 HTTP 请求，避免二次校验
// token/签名）。幂等保障：
//   - 记录已有 RunID 且 run 仍存在 → 返回该 run，不新建（重复点击/双重提交无副作用）；
//   - 已有 active run → 409（单实例串行，与手动触发一致）；
//   - 无可重放提交（非分支引用/分支删除）→ 400。
//
// 重放者是用户身份，故比 webhook 路径更严格：appGuard 按 stage 组合 + prod:write 全走。
func (h *Handler) replayDelivery(w http.ResponseWriter, r *http.Request, appID, pid, did string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	if h.deliveries == nil {
		httputil.WriteError(w, http.StatusNotImplemented, "webhook 投递记录未启用")
		return
	}
	p, err := h.pipes.GetPipeline(r.Context(), pid)
	if err != nil {
		writePipeErr(w, err)
		return
	}
	if p.AppID != appID {
		httputil.WriteError(w, http.StatusNotFound, "pipeline 不存在")
		return
	}
	d, err := h.deliveries.GetDelivery(r.Context(), pid, did)
	if err != nil {
		writePipeErr(w, err)
		return
	}
	// 幂等 no-op：该投递已生成过 run 且 run 仍在 → 原样返回（不增 replays，无副作用）。
	if d.RunID != "" {
		if run, rerr := h.runs.GetRun(r.Context(), d.RunID); rerr == nil {
			h.recordAudit(r, "replay_noop", "pipeline_run", run.ID, fmt.Sprintf("pipeline=%s delivery=%s（已有运行，未新建）", pid, did))
			httputil.WriteData(w, map[string]any{
				"run": run, "delivery": d, "replayed": false,
				"reason": "该投递已生成运行 " + run.ID + "，本次重放未新建",
			})
			return
		}
		// run 已被清理 → 视为可重放（历史记录漂移不应永久锁死重放路径）。
	}
	// 重放复用历史 branch/commit（记录的是当时事实，不随当前分支头变化）。
	if d.Branch == "" || strings.Trim(d.Commit, "0") == "" {
		httputil.WriteError(w, http.StatusBadRequest, "该投递无可重放的提交（非分支引用或分支删除事件）")
		return
	}
	tpl, terr := h.templates.GetTemplate(r.Context(), p.TemplateID)
	if terr != nil {
		httputil.WriteError(w, http.StatusNotFound, "pipeline template not found")
		return
	}
	resolved, rerr := ResolveStages(r.Context(), tpl.Stages, p.ParamOverrides, h.paramResolver, p.AppID, d.Branch)
	if rerr != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, rerr)
		return
	}
	if msg := validateDeployEnvs(resolved); msg != "" {
		httputil.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	// 应用级权限按 stage 组合判定（与手动触发同源）——重放不能成为绕过 appGuard 的后门。
	if h.appGuard != nil && !h.allowApp(w, r, appID, pipelineGuardAction(resolved)) {
		return
	}
	// prod:write：重放有用户身份，比 webhook（靠 approve 门禁兜底）更严格。
	if !h.allowProdFlow(w, r, resolved) {
		return
	}
	created, cerr := h.triggerRunInternal(r.Context(), appID, pid, tpl, resolved, d.Branch, d.Commit, "", TriggerReplay, "")
	if cerr != nil {
		writePipeErr(w, cerr)
		return
	}
	prevStatus, prevReason := d.Status, d.Reason
	now := time.Now()
	d.Replays++
	d.LastReplayAt = &now
	d.RunID = created.ID
	d.Status = DeliveryAccepted
	d.Reason = fmt.Sprintf("人工重放（原状态 %s：%s）", prevStatus, prevReason)
	if _, uerr := h.deliveries.UpdateDelivery(r.Context(), d); uerr != nil {
		// 记录更新失败不影响已创建的 run：记日志，前端由响应体回显本次结果。
		log.Printf("pipeline webhook 投递记录重放更新失败（%s/%s）: %v", pid, did, uerr) //nolint:gosec // pid/did 非敏感
	}
	h.recordAudit(r, "replay", "pipeline_run", created.ID, fmt.Sprintf("pipeline=%s delivery=%s branch=%s", pid, did, d.Branch))
	httputil.WriteDataCreated(w, map[string]any{"run": created, "delivery": d, "replayed": true})
}

// createPipeline POST /api/applications/{id}/pipelines。
// 支持从模板创建（templateId 非空时校验模板存在 + 继承 Kind；stages 运行时从模板解析，不复制）。
func (h *Handler) createPipeline(w http.ResponseWriter, r *http.Request, appID string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	// 应用级权限（权限审计 I3）：建流水线可改 ParamOverrides 指向 prod，受限应用需 write。
	if !h.allowApp(w, r, appID, application.AppActionWrite) {
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
		writePipeErr(w, err)
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
	// 应用级权限（权限审计 I3）：改流水线可换 envId/branch glob，受限应用需 write。
	if !h.allowApp(w, r, appID, application.AppActionWrite) {
		return
	}
	var p Pipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	existing, err := h.pipes.GetPipeline(r.Context(), pid)
	if err != nil {
		writePipeErr(w, err)
		return
	}
	// 归属校验（同 get 分支注释）：不符 404，防跨应用改挂 AppID「移花接木」。
	if existing.AppID != appID {
		httputil.WriteError(w, http.StatusNotFound, "pipeline 不存在")
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
		writePipeErr(w, err)
		return
	}
	// 写操作全记审计（与 create/delete 同源）：改 ParamOverrides 可换目标环境/分支 glob，
	// 漏记会断「谁改的流水线配置」溯源链。
	h.recordAudit(r, "update", "pipeline", pid, "")
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

// serveAdminPipelines 处理 GET /api/admin/pipelines（平台管理员跨租户 Pipeline 列表）。
// 用途：admin 模板删除前的引用预检（前端按 templateId 过滤展示绑定流水线）。只读，
// 支持 ?tenantId= 浏览范围过滤（D2a 统一语义）。
func (h *Handler) serveAdminPipelines(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
		return
	}
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/pipelines"), "/") != "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	list, err := h.pipes.ListAllPipelines(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, adminutil.FilterTenant(r, list, func(p Pipeline) string { return p.TenantID }))
}

// serveAdminRuns 处理 GET /api/admin/pipelineruns（平台管理员跨租户 PipelineRun 总览）。
// 返回全租户最近运行（带 TenantID，LIMIT 1000），可选 ?status= 过滤。只读。
func (h *Handler) serveAdminRuns(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
		return
	}
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 管理详情按ID读取，不受最近运行窗口影响；鉴权仍在上方统一执行。
	if rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/pipelineruns"), "/"); rest != "" {
		run, err := h.runs.GetRunAny(r.Context(), rest)
		if err != nil {
			writePipeErr(w, err)
			return
		}
		httputil.WriteData(w, run)
		return
	}
	list, err := h.runs.ListAdminRuns(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("tenantId"))
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, adminutil.FilterTenant(r, list, func(run PipelineRun) string { return run.TenantID }))
}

// serveAdminTemplates 处理 /api/admin/pipeline-templates[/{id}]（平台管理员 CRUD 公共模板）。
// 保护：仅 super_admin；create 强制 Builtin=false（防伪造 builtin）；update/delete 走 Repository，
// builtin 模板由 store 层拒（ErrTemplateBuiltin→409）。模板为平台级（tenant_id=NULL）+ 全部租户自定义。
func (h *Handler) serveAdminTemplates(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
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
				writePipeErr(w, err)
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
			writePipeErr(w, err)
			return
		}
		h.recordAudit(r, "update", "pipeline_template", id, "")
		httputil.WriteData(w, updated)
	case http.MethodDelete:
		if err := h.templates.DeleteTemplate(r.Context(), id); err != nil {
			writePipeErr(w, err)
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
			writePipeErr(w, err)
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
		// GetRun 提为主路径：查询失败直接返回（fail-closed，防 appGuard 在瞬时故障下跳过——错误契约审计 I6）。
		run, err := h.runs.GetRun(r.Context(), id)
		if err != nil {
			writePipeErr(w, err)
			return
		}
		// 应用级权限：审批门禁是生产发布动作，受限应用需 release（app-maintainer+）。
		if !h.allowApp(w, r, run.AppID, application.AppActionRelease) {
			return
		}
		// prod:write 复查（权限审计 C1）：webhook 触发的 run 只要求模板含 approve 门禁，
		// approve 动作本身若不复查，developer（无 prod:write）自建 webhook pipeline ->
		// push -> paused -> 自行 approve -> 生产部署执行。与 retry 的 allowProdFlow 重放同款：
		// approve 后剩余 stages 将继续执行，触达 prod 必须持 prod:write。
		if !h.allowProdFlow(w, r, stagesAfter(run, atoiSafe(parts[2]))) {
			return
		}
		stageIdx := atoiSafe(parts[2])
		// 结构化决策记录（run 时间线用；旧端点与 decisions 端点同款记录，双入口行为一致）。
		if dtype := decisionType(run.StageRuns[min(stageIdx, len(run.StageRuns)-1)]); dtype != "" {
			if derr := h.recordDecision(r.Context(), id, stageIdx, dtype, "confirm", "", h.actorOf(r)); derr != nil {
				writePipeErr(w, derr)
				return
			}
		}
		if err := h.engine.Resume(r.Context(), id, stageIdx); err != nil {
			writePipeErr(w, err)
			return
		}
		h.recordAudit(r, "approve", "pipeline_run", id, fmt.Sprintf("stage=%d", stageIdx))
		httputil.WriteData(w, map[string]string{"resumed": id})
		return
	}

	// POST /{id}/stages/{idx}/decisions 结构化决策（spec §7，v2 前端主入口）：
	// manual-test → confirm（确认通过）/ reject（确认不通过，run failed 质量否决）
	// approve → confirm（批准）/ reject（拒绝，run aborted）
	// canary 决策走专属 canary 端点（promote/terminate 有独立副作用与两段锁）。
	if r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "stages" && parts[3] == "decisions" {
		h.decideStage(w, r, id, atoiSafe(parts[2]))
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
		if derr := json.NewDecoder(r.Body).Decode(&in); derr != nil {
			// decode 错误显式报出（错误契约审计 I7）：吞掉会让非法 JSON 报误导性的 action 文案。
			httputil.WriteError(w, http.StatusBadRequest, "请求体非法（JSON 解析失败）")
			return
		}
		if in.Action != "promote" && in.Action != "terminate" {
			httputil.WriteError(w, http.StatusBadRequest, "action 必须是 promote 或 terminate")
			return
		}
		run, err := h.runs.GetRun(r.Context(), id)
		if err != nil {
			writePipeErr(w, err)
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
			writePipeErr(w, err)
			return
		}
		if derr := h.recordDecision(r.Context(), id, stageIdx, "canary", in.Action, "", h.actorOf(r)); derr != nil {
			log.Printf("[pipeline] canary 决策记录写入失败: %v", derr)
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
		// 应用级权限（安全审计 I2）：abort 打断 run 属写操作，受限应用非成员不得执行。
		// GetRun 失败直接返回（fail-closed，错误契约审计 I6）。
		if run, err := h.runs.GetRun(r.Context(), id); err != nil {
			writePipeErr(w, err)
			return
		} else if !h.allowApp(w, r, run.AppID, application.AppActionWrite) {
			return
		}
		if h.engine == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
			return
		}
		if err := h.engine.Abort(r.Context(), id); err != nil {
			writePipeErr(w, err)
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
		// 同时补 prod:write 复查：retry 用 run.StageRuns 原 Input 重放，若触达 prod 而
		// 当前用户无 prod:write（如 admin 之外的开发者点他人失败 run 的 retry），
		// 等于绕过 triggerRun 的 allowProdFlow 静态预演（安全审计 I3）。
		if run, err := h.runs.GetRun(r.Context(), id); err != nil {
			writePipeErr(w, err)
			return
		} else {
			if h.appGuard != nil {
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
			// StageRun.Input 即触发时固化的 resolved params，映射回 StageDef 复用 targetsProd。
			replay := make([]StageDef, 0, len(run.StageRuns))
			for _, sr := range run.StageRuns {
				replay = append(replay, StageDef{Type: sr.Type, Name: sr.Name, Params: sr.Input})
			}
			if !h.allowProdFlow(w, r, replay) {
				return
			}
		}
		if h.engine == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
			return
		}
		if err := h.engine.Retry(r.Context(), id); err != nil {
			writePipeErr(w, err)
			return
		}
		h.recordAudit(r, "retry", "pipeline_run", id, "")
		httputil.WriteData(w, map[string]string{"retried": id})
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// decisionType 按当前 stage 判定决策类型（""=不支持决策，如 canary/smoke）。
func decisionType(sr StageRun) string {
	if sr.Type == StageTest && strFromInput(sr.Input, "mode") == TestManual {
		return "manual-test"
	}
	if sr.Type == StageApprove {
		return "approve"
	}
	return ""
}

// actorOf 取决策/审计 actor（actorFn nil 返回空串）。
func (h *Handler) actorOf(r *http.Request) string {
	if h.actorFn != nil {
		return h.actorFn(r)
	}
	return ""
}

// recordDecision 结构化决策落 run.Decisions（只追加）。在 engine 动作前写：
// engine 的 Resume/Abort/RejectManual 内部重新 GetRun，读到含本记录的最新状态，不丢失。
// 仅 paused run 可决策（engine 不在推进），无写竞态。
func (h *Handler) recordDecision(ctx context.Context, id string, stageIdx int, dtype, action, reason, actor string) error {
	run, err := h.runs.GetRun(ctx, id)
	if err != nil {
		return err
	}
	run.Decisions = append(run.Decisions, DecisionRecord{
		StageIndex: stageIdx, Type: dtype, Action: action,
		Reason: reason, Actor: actor, At: time.Now(),
	})
	_, err = h.runs.UpdateRun(ctx, run)
	return err
}

// decideStage POST /api/pipelineruns/{id}/stages/{idx}/decisions。
// action=confirm：manual-test 确认通过 / approve 批准（等价旧 approve 端点的 Resume）。
// action=reject：manual-test 质量否决（run failed）；approve 拒绝上线（run aborted）。
func (h *Handler) decideStage(w http.ResponseWriter, r *http.Request, id string, stageIdx int) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	if h.engine == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
		return
	}
	var in struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if derr := json.NewDecoder(r.Body).Decode(&in); derr != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体非法（JSON 解析失败）")
		return
	}
	if in.Action != "confirm" && in.Action != "reject" {
		httputil.WriteError(w, http.StatusBadRequest, "action 必须是 confirm 或 reject")
		return
	}
	// GetRun 提为主路径：查询失败直接返回（fail-closed，防 appGuard 在瞬时故障下跳过）。
	run, err := h.runs.GetRun(r.Context(), id)
	if err != nil {
		writePipeErr(w, err)
		return
	}
	if stageIdx < 0 || stageIdx >= len(run.StageRuns) {
		httputil.WriteError(w, http.StatusBadRequest, "stage 索引越界")
		return
	}
	if run.Status != RunPaused || run.CurrentStage != stageIdx {
		httputil.WriteError(w, http.StatusConflict, "该阶段当前不在等待决策")
		return
	}
	dtype := decisionType(run.StageRuns[stageIdx])
	if dtype == "" {
		httputil.WriteError(w, http.StatusBadRequest, "该阶段不支持 confirm/reject 决策（金丝雀请走 canary 决策端点）")
		return
	}
	// 应用级权限：按决策性质分流——approve（发布门禁）需 release；manual-test 验证需 write。
	action := application.AppActionWrite
	if dtype == "approve" {
		action = application.AppActionRelease
	}
	if !h.allowApp(w, r, run.AppID, action) {
		return
	}
	// prod:write 复查（与旧 approve 端点同款）：confirm 后剩余 stages 将继续执行，
	// 触达 prod 必须持 prod:write（防 webhook 触发 + 自行 confirm 绕过生产写权限）。
	if in.Action == "confirm" && !h.allowProdFlow(w, r, stagesAfter(run, stageIdx)) {
		return
	}
	// 决策记录先落（engine 动作内部重新 GetRun 读到含记录的最新状态）。
	if derr := h.recordDecision(r.Context(), id, stageIdx, dtype, in.Action, in.Reason, h.actorOf(r)); derr != nil {
		writePipeErr(w, derr)
		return
	}
	switch {
	case in.Action == "confirm":
		if err := h.engine.Resume(r.Context(), id, stageIdx); err != nil {
			writePipeErr(w, err)
			return
		}
	case dtype == "manual-test":
		if err := h.engine.RejectManual(r.Context(), id, stageIdx, in.Reason); err != nil {
			writePipeErr(w, err)
			return
		}
	default: // approve reject = 拒绝上线，中止执行
		if err := h.engine.Abort(r.Context(), id); err != nil {
			writePipeErr(w, err)
			return
		}
	}
	h.recordAudit(r, "decision_"+dtype+"_"+in.Action, "pipeline_run", id,
		fmt.Sprintf("stage=%d reason=%.80s", stageIdx, in.Reason))
	httputil.WriteData(w, map[string]string{"decided": id})
}

// allowApp 校验受限应用的应用级权限；未注入/非受限放行，不通过回 403。
func (h *Handler) allowApp(w http.ResponseWriter, r *http.Request, appID, action string) bool {
	if h.appGuard == nil || h.appGuard.Allow(r, appID, action) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: 无该应用的应用级权限（"+action+"）")
	return false
}

// stagesAfter 把 run 从 fromIdx 起的剩余 StageRun 映射回 StageDef（Input 即触发时固化的
// resolved params），供 approve 的 allowProdFlow 复查（与 retry 重放同款）。
func stagesAfter(run PipelineRun, fromIdx int) []StageDef {
	if fromIdx < 0 {
		fromIdx = 0
	}
	out := make([]StageDef, 0, len(run.StageRuns)-min(fromIdx, len(run.StageRuns)))
	for i := fromIdx; i < len(run.StageRuns); i++ {
		sr := run.StageRuns[i]
		out = append(out, StageDef{Type: sr.Type, Name: sr.Name, Params: sr.Input})
	}
	return out
}

// triggerRun POST /api/applications/{id}/pipelines/{pid}/run。
// body {branch, commit?, version?} -> 建 PipelineRun + engine.Start。
// prod:write 横切：deploy stage 目标环境为 prod 要求调用者持 prod:write。
// 单实例串行：已有 running/paused run 拒绝（ErrActiveRunExists）。
// runTriggerInput trigger/preview 共用请求体。
type runTriggerInput struct {
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Version string `json:"version"`
	// StageParams run 级一次性 stage 参数（需求文本载体）：key 格式同 ParamOverrides（"<stageIdx>.<paramKey>"），
	// 后于 ParamOverrides 应用（run 级优先）。webhook 路径不接（机器触发无动态需求）。
	StageParams map[string]any `json:"stageParams"`
	// PlanHash 来自 preview 的执行计划指纹：非空时服务端锁定产物后重算比对，
	// 不一致 409（防预览到执行之间目标漂移，spec §6）。
	PlanHash string `json:"planHash"`
}

// resolveForRun trigger/preview 共用解析链：pipeline 归属 → 模板 → ResolveStages →
// run 级 stageParams 应用。写侧权限预演（appGuard/prod:write）由 triggerRun 做，
// preview 只读不预演（不构成越权面：解析输入均为调用方自有资源）。
func (h *Handler) resolveForRun(w http.ResponseWriter, r *http.Request, appID, pid string, body runTriggerInput) (Pipeline, PipelineTemplate, []StageDef, bool) {
	p, err := h.pipes.GetPipeline(r.Context(), pid)
	if err != nil {
		writePipeErr(w, err)
		return Pipeline{}, PipelineTemplate{}, nil, false
	}
	// 归属校验（同 get 分支注释）：不符 404，防跨应用触发他人流水线。
	if p.AppID != appID {
		httputil.WriteError(w, http.StatusNotFound, "pipeline 不存在")
		return Pipeline{}, PipelineTemplate{}, nil, false
	}
	tpl, terr := h.templates.GetTemplate(r.Context(), p.TemplateID)
	if terr != nil {
		httputil.WriteError(w, http.StatusNotFound, "pipeline template not found")
		return Pipeline{}, PipelineTemplate{}, nil, false
	}
	resolved, rerr := ResolveStages(r.Context(), tpl.Stages, p.ParamOverrides, h.paramResolver, appID, body.Branch)
	if rerr != nil {
		// WriteServiceError 分流（错误契约审计 C1）：rerr 可能内嵌 pgx 原始错误，裸 400 直通泄漏内部细节。
		httputil.WriteServiceError(w, http.StatusBadRequest, rerr)
		return Pipeline{}, PipelineTemplate{}, nil, false
	}
	// run 级 stageParams 应用：必须在 validateDeployEnvs/appGuard/allowProdFlow 预演**之前**——
	// 防经 stageParams 把 deploy envId 改指 prod 绕过 prod:write 静态预演（越权防线，review focus ②）。
	for i := range resolved {
		applyOverrides(resolved[i].Params, body.StageParams, i)
	}
	if strings.HasPrefix(tpl.ID, "tpl-delivery-") {
		for _, stage := range resolved {
			if strOr(stage.Params, "serviceId", "") == "" {
				httputil.WriteError(w, http.StatusBadRequest, "请选择目标服务")
				return Pipeline{}, PipelineTemplate{}, nil, false
			}
			if stage.Type == StageBuild {
				sha := strOr(stage.Params, "commit", "")
				_, err := hex.DecodeString(sha)
				if strOr(stage.Params, "repoId", "") == "" || (len(sha) != 40 && len(sha) != 64) || err != nil {
					httputil.WriteError(w, http.StatusBadRequest, "构建需指定仓库和完整提交 SHA")
					return Pipeline{}, PipelineTemplate{}, nil, false
				}
			}
			if stage.Type == StageDeploy && strOr(stage.Params, "lane", "") == "" {
				httputil.WriteError(w, http.StatusBadRequest, "请选择目标泳道")
				return Pipeline{}, PipelineTemplate{}, nil, false
			}
		}
	}
	return p, tpl, resolved, true
}

// lockArtifacts 产物锁定（spec §6）：imageSource=latestReady 的镜像消费 stage
// （deploy/canary/promote/release）预解析一次，写入 Params["lockedImageId"]。
// 触发时固化 → 后续执行/审批后推进/Retry 均用锁定值，审批等待期间新完成的构建
// 不再漂移实际发布产物。run.Artifact 记候选五元组（digest/commit/buildRunId 来自
// 镜像元数据真源）。幂等：已锁定的 stage 跳过（triggerRun 与 triggerRunInternal 双调用安全）。
func (h *Handler) lockArtifacts(ctx context.Context, appID string, resolved []StageDef) (*ArtifactBinding, error) {
	var binding *ArtifactBinding
	for i := range resolved {
		s := &resolved[i]
		switch s.Type {
		case StageDeploy, StageCanary, StagePromote, StageRelease:
		default:
			continue
		}
		source := strOr(s.Params, "imageSource", ImagePriorBuild)
		if source != ImageLatestReady && source != ImageSelected {
			continue
		}
		if strOr(s.Params, "lockedImageId", "") != "" {
			continue
		}
		if h.engine == nil {
			continue // 引擎未配置（测试场景）：不锁定，preview 展示「触发时锁定」语义
		}
		imageID := strOr(s.Params, "imageId", "")
		var err error
		if source == ImageLatestReady {
			imageID, err = h.engine.Releases.LatestReadyImage(ctx, appID)
		}
		if imageID == "" && err == nil {
			err = errors.New("请选择镜像")
		}
		if err != nil {
			return nil, fmt.Errorf("解析候选镜像失败: %w", err)
		}
		s.Params["lockedImageId"] = imageID
		if binding == nil {
			binding = &ArtifactBinding{ImageID: imageID, ServiceID: strOr(s.Params, "serviceId", "")}
			if h.imageMeta != nil {
				if m, ok := h.imageMeta(ctx, appID, imageID); ok {
					binding.Digest, binding.Commit, binding.BuildRunID, binding.RepoID = m.Digest, m.Commit, m.BuildRunID, m.RepoID
				} else {
					return nil, errors.New("候选镜像不存在或不属于该应用")
				}
			}
		}
	}
	return binding, nil
}

// previewRun POST /api/applications/{id}/pipelines/{pid}/preview —— 执行计划预演（只读）。
// 返回最终解析的 stages（含中文目标说明）、触发时将锁定的候选产物、缺配置项、planHash。
// 前端把 planHash 原样带回 trigger 即得「预览=执行」一致性保证。
func (h *Handler) previewRun(w http.ResponseWriter, r *http.Request, appID, pid string) {
	if !h.allow(w, r, PermPipelineRead) {
		return
	}
	var body runTriggerInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Branch == "" {
		httputil.WriteError(w, http.StatusBadRequest, "branch required")
		return
	}
	p, tpl, resolved, ok := h.resolveForRun(w, r, appID, pid, body)
	if !ok {
		return
	}
	if msg := validateDeployEnvs(resolved); msg != "" {
		httputil.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	artifact, aerr := h.lockArtifacts(r.Context(), appID, resolved)
	if aerr != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, aerr)
		return
	}
	httputil.WriteData(w, BuildPreview(p, tpl, resolved, body.Branch, artifact, missingConfigItems(resolved)))
}

func (h *Handler) triggerRun(w http.ResponseWriter, r *http.Request, appID, pid string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	var body runTriggerInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Branch == "" {
		httputil.WriteError(w, http.StatusBadRequest, "branch required")
		return
	}
	p, tpl, resolved, ok := h.resolveForRun(w, r, appID, pid, body)
	if !ok {
		return
	}
	if strings.HasPrefix(tpl.ID, "tpl-delivery-") && body.PlanHash == "" {
		httputil.WriteError(w, http.StatusBadRequest, "请先预览并确认执行计划")
		return
	}
	// deploy stage envId 必填校验（fail-fast，避免注定失败的 run 占住单实例串行槽位）。
	// 占位符解析后 envId 仍空（app 无对应环境）则拒。
	if msg := validateDeployEnvs(resolved); msg != "" {
		httputil.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	// 应用级权限：受限应用触发需成员角色——按 stage 组合判定动作（见 pipelineGuardAction）。
	if h.appGuard != nil {
		if !h.allowApp(w, r, appID, pipelineGuardAction(resolved)) {
			return
		}
	}
	// prod:write 校验：deploy stage 目标环境为 prod，或 promote stage 目标环境（前序 deploy envId
	// 的下一阶）为 prod 时，要求调用者持 prod:write（防 developer 经 [deploy test, promote] 绕过）
	if !h.allowProdFlow(w, r, resolved) {
		return
	}
	// 产物锁定 + 执行计划一致性（spec §6）在 triggerRunInternal 内单次完成：
	// 锁定值计入 hash，带 planHash（来自 preview）时比对，漂移 → 409 重新预览。
	created, err := h.triggerRunInternal(r.Context(), appID, p.ID, tpl, resolved, body.Branch, body.Commit, body.Version, TriggerManual, body.PlanHash)
	if err != nil {
		writePipeErr(w, err)
		return
	}
	h.recordAudit(r, "run", "pipeline_run", created.ID, fmt.Sprintf("pipeline=%s branch=%s", pid, body.Branch))
	httputil.WriteDataCreated(w, created)
}

// pipelineGuardAction 按含有的 stage 类型判定应用级权限动作（值=application.AppAction*）：
// 含 deploy/release/promote/approve/canary 的 CD 流水线需 release（app-maintainer+，
// 测试人员被拦），纯 CI（build/test）需 write（app-developer+）。
func pipelineGuardAction(stages []StageDef) string {
	for _, st := range stages {
		if st.Type == StageDeploy || st.Type == StageRelease || st.Type == StagePromote || st.Type == StageApprove || st.Type == StageCanary {
			return application.AppActionRelease
		}
	}
	return application.AppActionWrite
}

// agentShipSegments tpl-agent-ship 可选段（stage Name → 段标识；不在表中的 stage
// （AI 开发）恒留）。stage 改名需同步此表——TestBuiltinAgentShipSegments 锁一致性。
var agentShipSegments = map[string]string{
	"AI 代码审查": "review",
	"构建":      "ship",
	"部署到测试泳道": "ship",
	"冒烟联调":    "ship",
	"上线审批":    "release",
	"金丝雀验证":   "release",
	"发布版本":    "release",
	"合并主干":    "release",
}

// trimAgentShipStages 按勾选段裁剪模板 stages（段外 stage 如 AI 开发恒留）。
func trimAgentShipStages(stages []StageDef, review, ship, release bool) []StageDef {
	on := map[string]bool{"review": review, "ship": ship, "release": release}
	out := make([]StageDef, 0, len(stages))
	for _, st := range stages {
		if seg, ok := agentShipSegments[st.Name]; !ok || on[seg] {
			out = append(out, st)
		}
	}
	return out
}

// ensureAgentShipPipeline 幂等确保 app 绑定 tpl-agent-ship（已绑定复用；同 (app,name)
// 唯一约束兜底并发建）。返回绑定实体（ParamOverrides 供解析）。
func (h *Handler) ensureAgentShipPipeline(ctx context.Context, appID string) (Pipeline, error) {
	list, err := h.pipes.ListPipelines(ctx, appID)
	if err != nil {
		return Pipeline{}, err
	}
	for _, p := range list {
		if p.TemplateID == "tpl-agent-ship" {
			return p, nil
		}
	}
	return h.pipes.CreatePipeline(ctx, Pipeline{
		AppID: appID, Name: "AI 研发交付", Kind: KindAgent, TemplateID: "tpl-agent-ship",
		Trigger: PipelineTrigger{Type: TriggerManual},
	})
}

// triggerAgentShipRun POST /api/applications/{id}/agent-ship/run —— 智能研发一条龙（2026-10-13）：
// 需求文本 + 勾选段（review/ship/release）直触 tpl-agent-ship，免预建绑定（内部 Ensure 幂等）。
// 门禁不降级：release 段自带上线审批；deploy/canary 走 triggerRun 同款 prod 预演与 appGuard。
func (h *Handler) triggerAgentShipRun(w http.ResponseWriter, r *http.Request, appID string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	var body struct {
		Request string `json:"request"`
		Review  bool   `json:"review"`
		Ship    bool   `json:"ship"`
		Release bool   `json:"release"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(body.Request) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "request required")
		return
	}
	// 段依赖：canary/release 消费 ship 段产物（镜像），无 ship 必断链。
	if body.Release && !body.Ship {
		httputil.WriteError(w, http.StatusBadRequest, "发布上线依赖构建部署（ship），请同时勾选")
		return
	}
	p, err := h.ensureAgentShipPipeline(r.Context(), appID)
	if err != nil {
		writePipeErr(w, err)
		return
	}
	tpl, terr := h.templates.GetTemplate(r.Context(), p.TemplateID)
	if terr != nil {
		httputil.WriteError(w, http.StatusNotFound, "pipeline template not found")
		return
	}
	stages := trimAgentShipStages(tpl.Stages, body.Review, body.Ship, body.Release)
	// AI 基于 main 开发（registerChange 产出分支自动注册 Change，变更 tab 可见）。
	resolved, rerr := ResolveStages(r.Context(), stages, p.ParamOverrides, h.paramResolver, appID, "main")
	if rerr != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, rerr)
		return
	}
	// 需求文本注入首 stage（run 级 stageParams 语义，先于权限预演——防经 request 之外
	// 的参数通道改 deploy 指向；此处只注入 request 单键，校验顺序与 triggerRun 一致）。
	for i := range resolved {
		applyOverrides(resolved[i].Params, map[string]any{"0.request": body.Request}, i)
	}
	if msg := validateDeployEnvs(resolved); msg != "" {
		httputil.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	if h.appGuard != nil {
		if !h.allowApp(w, r, appID, pipelineGuardAction(resolved)) {
			return
		}
	}
	if !h.allowProdFlow(w, r, resolved) {
		return
	}
	created, err := h.triggerRunInternal(r.Context(), appID, p.ID, tpl, resolved, "main", "", "", TriggerManual, "")
	if err != nil {
		writePipeErr(w, err)
		return
	}
	h.recordAudit(r, "run", "agent_ship_run", created.ID,
		fmt.Sprintf("pipeline=%s request=%.60s review=%v ship=%v release=%v", p.ID, body.Request, body.Review, body.Ship, body.Release))
	httputil.WriteDataCreated(w, created)
}

// triggerRunInternal 触发 run 核心：resolved stages -> 产物锁定 -> 单实例校验 -> 创建 run + 启动 engine。
// perm + prod:write 校验由调用方做（manual triggerRun 做；webhook 跳过，approve 门禁兜底 CD）。
// expectPlanHash 非空时（来自 preview）锁定产物后重算比对，漂移返 ErrPlanChanged（409）。
// 错误经 toHTTPStatus 映射：validateDeployEnvs 失败 -> 400，ErrActiveRunExists/ErrPlanChanged -> 409，NotFound -> 404。
// tpl 用于模板身份快照（TemplateID/Version 固化进 run，模板升级不影响归因）与 planHash。
func (h *Handler) triggerRunInternal(ctx context.Context, appID, pid string, tpl PipelineTemplate, resolved []StageDef, branch, commit, version, trigger, expectPlanHash string) (PipelineRun, error) {
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
	for _, stage := range resolved {
		if stage.Type == StageBuild {
			repoID = strOr(stage.Params, "repoId", repoID)
			commit = strOr(stage.Params, "commit", commit)
			break
		}
	}
	// 产物锁定（spec §6）：latestReady 预解析固化进 stage Input，run.Artifact 记候选五元组。
	artifact, aerr := h.lockArtifacts(ctx, appID, resolved)
	if aerr != nil {
		return PipelineRun{}, aerr
	}
	if artifact != nil {
		if artifact.RepoID == "" {
			artifact.RepoID = repoID
		}
		if artifact.Commit == "" {
			artifact.Commit = commit // 无镜像元数据时退 run 触发 commit
		}
	}
	planHash := PlanHash(tpl.ID, tpl.Version, branch, resolved)
	if expectPlanHash != "" && planHash != expectPlanHash {
		return PipelineRun{}, ErrPlanChanged
	}
	stageRuns := make([]StageRun, len(resolved))
	for i, s := range resolved {
		stageRuns[i] = StageRun{Index: i, Type: s.Type, Name: s.Name, Status: StagePending, Input: s.Params}
	}
	run := PipelineRun{
		PipelineID: pid, AppID: appID, Branch: branch, Commit: commit,
		RepoID: repoID, Version: version, Trigger: trigger,
		Status: RunRunning, CurrentStage: 0, StageRuns: stageRuns,
		TemplateID: tpl.ID, TemplateVersion: tpl.Version,
		PlanHash: planHash, Artifact: artifact,
		Lane: deployLaneOf(resolved),
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
	// webhook 无 tenant ctx，跨租户按 ID 查 pipeline（token 鉴权提供安全性）。
	// 校验顺序（错误契约审计 C2）：token 常量时间比较前置，所有失败统一 401——原顺序
	// 404(不存在)/400(非 webhook)/401(token 错) 三态可被无凭证枚举 pipeline ID 并分类。
	p, err := h.pipes.GetPipelineAny(ctx, pid)
	if err != nil {
		httputil.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// token 常量时间比较（防时序枚举）；pipeline 未配 token、非 webhook 触发器或请求无 token 均拒
	token := r.URL.Query().Get("token")
	if p.Trigger.Type != TriggerWebhook || p.Trigger.Token == "" || token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(p.Trigger.Token)) != 1 {
		httputil.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// 派生 pipeline 租户 ctx（后续 GetTemplate/CreateRun 要求 tenant；webhook 触发归属 pipeline 租户）
	wctx := tenant.WithTenant(ctx, p.TenantID)

	// 投递记录（D5-5）：每次通过鉴权的投递都留一条可查记录，用户侧不再靠服务端日志猜。
	// 默认 failed，各分支覆盖；defer 统一落库（任何提前 return 都不会丢证据）。
	did := deliveryIDFrom(r)
	rec := WebhookDelivery{
		ID: did, PipelineID: pid, AppID: p.AppID, Event: "push",
		Status: DeliveryFailed, Reason: "处理未完成", ReceivedAt: time.Now(),
	}
	skipPersist := false
	// 幂等：同一 delivery 已处理过（webhook 源重试 / 双端重复配置）→ 不重复触发 run。
	// 复用已有结果并标记 duplicate，返回 200 防重试风暴。
	if h.deliveries != nil {
		if old, gerr := h.deliveries.GetDelivery(wctx, pid, did); gerr == nil {
			// 保留首次结果事实（原状态 / 关联 run）再标 duplicate，避免覆盖掉「本来触发成功」的证据。
			prev := old.Status
			if old.RunID != "" {
				prev += "，已生成运行 " + old.RunID
			}
			old.Status = DeliveryDuplicate
			old.Reason = fmt.Sprintf("同一投递重复到达（首次：%s），未重复触发", prev)
			if _, uerr := h.deliveries.UpdateDelivery(wctx, old); uerr != nil {
				log.Printf("pipeline webhook 投递记录更新失败（duplicate, %s/%s）: %v", pid, did, uerr) //nolint:gosec // pid/did 非敏感
			}
			skipPersist = true
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	defer func() {
		if h.deliveries == nil || skipPersist {
			return
		}
		if _, derr := h.deliveries.CreateDelivery(wctx, rec); derr != nil {
			// 并发重复投递等场景：兜底改更新；仍失败只记日志——记录失败不能反过来影响触发本身。
			if _, uerr := h.deliveries.UpdateDelivery(wctx, rec); uerr != nil {
				log.Printf("pipeline webhook 投递记录写入失败（%s/%s）: %v", pid, did, derr) //nolint:gosec // pid/did 非敏感
			}
		}
	}()

	// 已禁用流水线不触发（对照 FindPipeline 的 Disabled 检查；静默 200 避免 webhook 源重试）。
	if p.Disabled {
		rec.Status = DeliverySkipped
		rec.Reason = "流水线已禁用"
		w.WriteHeader(http.StatusOK)
		return
	}
	// 解析 Gitea push event（ref/after；Gitea 与 GitHub 字段名一致）
	var push struct {
		Ref   string `json:"ref"`
		After string `json:"after"`
	}
	if err := json.NewDecoder(r.Body).Decode(&push); err != nil {
		rec.Status = DeliveryRejected
		rec.Reason = "请求体不是合法的 push event"
		httputil.WriteError(w, http.StatusBadRequest, "invalid push payload")
		return
	}
	branch := strings.TrimPrefix(push.Ref, "refs/heads/")
	if branch == push.Ref || branch == "" {
		// 非分支引用（tag/PR 等），静默忽略（返 200 避免 webhook 源重试）
		rec.Event = push.Ref
		rec.Status = DeliverySkipped
		rec.Reason = "非分支引用（tag / PR 等），无可触发构建"
		w.WriteHeader(http.StatusOK)
		return
	}
	rec.Branch, rec.Commit = branch, push.After
	// 分支删除 push（after 全零）：无 commit 可建，静默跳过（否则建出注定失败的 BuildRun，集成审计 M2）。
	if strings.Trim(push.After, "0") == "" {
		rec.Status = DeliverySkipped
		rec.Reason = "分支删除事件，无可触发构建的 commit"
		w.WriteHeader(http.StatusOK)
		return
	}
	// 分支 glob 匹配（Trigger.Branch 空=全部分支）。跳过留痕（状态/原因入投递记录）：
	// 静默 200 对 webhook 源正确（防重试风暴），但对用户是黑盒——「push 了为什么不触发」需可查证据。
	if p.Trigger.Branch != "" && !globMatch(p.Trigger.Branch, branch) {
		log.Printf("pipeline webhook 跳过：分支 %s 不匹配 glob %q（pipeline=%s）", branch, p.Trigger.Branch, pid) //nolint:gosec // pid/branch 非敏感
		rec.Status = DeliverySkipped
		rec.Reason = fmt.Sprintf("分支 %s 不匹配触发 glob %q", branch, p.Trigger.Branch)
		w.WriteHeader(http.StatusOK)
		return
	}
	// 解析模板 stages + 占位符（webhook branch 注入 {{run.branch}}）
	tpl, terr := h.templates.GetTemplate(wctx, p.TemplateID)
	if terr != nil {
		rec.Status = DeliveryFailed
		rec.Reason = "流水线模板不存在（templateId=" + p.TemplateID + "）"
		httputil.WriteError(w, http.StatusNotFound, "pipeline template not found")
		return
	}
	resolved, rerr := ResolveStages(wctx, tpl.Stages, p.ParamOverrides, h.paramResolver, p.AppID, branch)
	if rerr != nil {
		rec.Status = DeliveryFailed
		rec.Reason = "流水线参数解析失败：" + rerr.Error()
		// WriteServiceError 分流（错误契约审计 C1）：rerr 可能内嵌 apps.Get/envs.List 的
		// 原始 pgx 错误（dial tcp/SQLSTATE），裸 400 直通会泄漏连接串/SQL 细节给前端。
		httputil.WriteServiceError(w, http.StatusBadRequest, rerr)
		return
	}
	// webhook 触发免 prod:write（无用户身份），但若 stages 触达 prod 必须含 approve 门禁兜底，
	// 防去掉 approve 的 CD 模板被 webhook 直接部署生产（manual 路径靠调用者 prod:write，webhook 靠 approve）。
	if h.targetsProd(wctx, resolved) && !hasApproveGate(resolved) {
		rec.Status = DeliveryRejected
		rec.Reason = "流水线触达生产但缺少 approve 审批门禁"
		httputil.WriteError(w, http.StatusForbidden, "webhook 触发的生产部署流水线必须包含 approve 审批门禁")
		return
	}
	created, err := h.triggerRunInternal(wctx, p.AppID, pid, tpl, resolved, branch, push.After, "", TriggerWebhook, "")
	if err != nil {
		// 已有 active run（Gitea 重发/人工 replay 窗口内重复投递）：静默 200——返 409 会
		// 触发 webhook 源重试风暴，且「已有 run 在跑」本身就是期望状态（并发审计 M6）。
		if errors.Is(err, ErrActiveRunExists) {
			rec.Status = DeliveryDuplicate
			rec.Reason = "已有运行在进行中，本次未新建（单实例串行）"
			w.WriteHeader(http.StatusOK)
			return
		}
		rec.Status = DeliveryFailed
		rec.Reason = "触发失败：" + err.Error()
		writePipeErr(w, err)
		return
	}
	rec.Status = DeliveryAccepted
	rec.RunID = created.ID
	rec.Reason = ""
	// audit：webhook 无用户身份，actor="webhook"，tenant 取 pipeline 租户
	h.recordAuditCtx(wctx, p.TenantID, "webhook", "run", "pipeline_run", created.ID, fmt.Sprintf("pipeline=%s branch=%s via webhook", pid, branch))
	w.WriteHeader(http.StatusOK)
}

// deliveryIDFrom 取 webhook 源投递 ID（幂等键）：Gitea/Gogs/GitHub 头名不同，缺失时生成。
// 生成值同样落库——至少可区分「一次都没收到」与「收到但跳过」。
func deliveryIDFrom(r *http.Request) string {
	for _, h := range []string{"X-Gitea-Delivery", "X-Gogs-Delivery", "X-GitHub-Delivery"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("gen-%d", time.Now().UnixNano())
	}
	return "gen-" + hex.EncodeToString(b)
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

// globMatch 分支 glob 匹配（支持 * 通配，如 "feature-*" 匹配 "feature-x"/"feature/login"）。
// 不用 path.Match：其 * 不跨路径分隔符，而 Git 分支名常含 /（feature/login、release/1.0），
// path.Match 语义下 "feature-*" 匹配不了 "feature/login"——webhook 静默不触发且无日志，
// 用户会误判 webhook 配置失效。此处 * 匹配任意字符（含 /）。
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	// 首段前缀锚定、末段后缀锚定、中段按序 Contains（经典扁平 glob）
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = name[len(parts[0]):]
	last := parts[len(parts)-1]
	if len(name) < len(last) || !strings.HasSuffix(name, last) {
		return false
	}
	name = name[:len(name)-len(last)]
	for _, mid := range parts[1 : len(parts)-1] {
		idx := strings.Index(name, mid)
		if idx < 0 {
			return false
		}
		name = name[idx+len(mid):]
	}
	return true
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
	case errors.Is(err, ErrPipelineNotFound), errors.Is(err, ErrRunNotFound), errors.Is(err, ErrDeliveryNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrPipelineExists), errors.Is(err, ErrActiveRunExists),
		errors.Is(err, ErrRunExists), errors.Is(err, ErrTemplateExists),
		errors.Is(err, ErrNotPaused), errors.Is(err, ErrStageNotCurrent), errors.Is(err, ErrNotRunning),
		errors.Is(err, ErrNotFailed), errors.Is(err, ErrTemplateBuiltin), errors.Is(err, ErrPlanChanged):
		return http.StatusConflict
	case errors.Is(err, ErrNoTenant):
		return http.StatusBadRequest
	}
	return http.StatusBadRequest
}

// sentinelText 仓储/模型 sentinel -> 中文文案（旅程审计 R5-I8：
// "pipeline already exists" 等英文直出对中文控制台用户不可读）。
// sentinel 是包级单例，可作 map key；未命中走 WriteServiceError 原样透传。
var sentinelText = map[error]string{
	ErrPipelineNotFound: "流水线不存在",
	ErrDeliveryNotFound: "webhook 投递记录不存在",
	ErrPipelineExists:   "同名流水线已存在",
	ErrRunNotFound:      "运行不存在",
	ErrRunExists:        "运行 ID 已存在",
	ErrActiveRunExists:  "该流水线已有进行中的运行（单实例串行），请等待完成或先中止",
	ErrNotRunning:       "运行不在进行中，无法操作",
	ErrTemplateExists:   "模板已存在",
	ErrNoTenant:         "缺少租户上下文",
	ErrPlanChanged:      "执行计划已变化（可能有新构建完成改变了候选产物），请重新预览后执行",
	ErrInvalidKind:      "流水线类型非法（仅 ci/cd）",
	ErrNoStages:         "模板缺少阶段定义",
	ErrInvalidStageType: "阶段类型非法",
	ErrNotPaused:        "运行不在等待审批状态",
	ErrStageNotCurrent:  "阶段不在当前位置，无法操作",
	ErrNotFailed:        "仅失败的运行可重试",
	ErrTemplateRequired: "缺少模板 ID",
	ErrTemplateBuiltin:  "内置模板不可修改",
	errNameRequired:     "名称不能为空",
	errAppRequired:      "应用 ID 不能为空",
}

// writePipeErr 统一错误出口：sentinel 映射中文文案；其余经 WriteServiceError
// 脱敏分流（底层技术错误 pgx/dial tcp 不外泄）。
func writePipeErr(w http.ResponseWriter, err error) {
	for sentinel, msg := range sentinelText {
		if errors.Is(err, sentinel) {
			httputil.WriteError(w, toHTTPStatus(err), msg)
			return
		}
	}
	httputil.WriteServiceError(w, toHTTPStatus(err), err)
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
