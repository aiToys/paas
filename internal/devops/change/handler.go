// handler.go 变更管理 HTTP handler：变更/集成批次 REST + 权限 + 审计。
//
// 路由（composite 按路径分发，参照 pipeline handler 模式）：
//
//	GET/POST  /api/applications/{id}/changes[?status=]   变更列表/创建
//	GET/DELETE /api/applications/{id}/changes/{cid}      详情/放弃
//	GET/POST  /api/applications/{id}/batches[?status=]   批次列表/创建
//	GET/DELETE /api/applications/{id}/batches/{bid}      详情（惰性推进）/放弃
//	POST/DELETE /api/applications/{id}/batches/{bid}/changes[/{cid}]  入批/出批
//	POST      /api/applications/{id}/batches/{bid}/integrate|approve|release
//
// 响应契约统一 {data:T}/{error:msg}；权限 pipeline:read/write（approve 额外 prod:write）；
// 审计经 AuditRecorder 注入（依赖倒置，cmd/core 桥接 security.AuditStore）。
package change

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"

	"github.com/aitoys/paas/internal/core/application"
)

// 权限常量（复用流水线权限域：变更管理是流水线的上游编排）。
const (
	PermPipelineRead  = "pipeline:read"
	PermPipelineWrite = "pipeline:write"
	// PermProdWrite 生产上线审批权限（approve 门禁；与 identity.PermProdWrite 同值，
	// 本地定义避免 change->identity import）。
	PermProdWrite = "prod:write"
)

// AuditRecorder 审计记录（依赖倒置，与 identity/pipeline 同源签名，cmd/core 装配桥接）。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// Handler 变更管理 HTTP 入口（composite 按路径分发）。
// svc 承载编排动作（创建/入批/集成/审批/发布）；repo 承载只读列表与批次创建/放弃
// （Service 不暴露这些直通操作，handler 经 Repository 完成）。
type Handler struct {
	svc  *Service
	repo Repository
	// Authorize 权限校验（nil 跳过，测试场景）。
	Authorize func(r *http.Request, perm string) bool
	// prodWrite 生产上线审批权限判定（nil 跳过，测试场景）。
	prodWrite func(r *http.Request) bool
	// repoLookup 解析应用内置仓库（批次创建时定位 RepoID，与变更同源）。
	repoLookup RepoLookup
	// audit 审计记录器（nil 跳过）。
	audit   AuditRecorder
	actorFn func(r *http.Request) string
	// runLister 通知聚合读 run 状态（nil 降级只通知批次侧）。
	runLister RunLister
	// alertLister 通知聚合读告警状态（nil 降级不含告警源）。
	alertLister AlertLister
	// appGuard 应用级权限 enforcement（受限应用 integrate/approve/release 需成员角色）；nil 跳过。
	appGuard *application.AppGuard
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithAuthorize 注入权限校验。
func WithAuthorize(f func(r *http.Request, perm string) bool) HandlerOpt {
	return func(h *Handler) { h.Authorize = f }
}

// WithAppGuard 注入应用级权限 enforcement（受限应用批次动作需成员角色）。
func WithAppGuard(g *application.AppGuard) HandlerOpt {
	return func(h *Handler) { h.appGuard = g }
}

// WithAudit 注入审计记录器。
func WithAudit(a AuditRecorder) HandlerOpt { return func(h *Handler) { h.audit = a } }

// WithActorFn 注入 actor 解析（从 r 取 userID，审计用）。
func WithActorFn(f func(r *http.Request) string) HandlerOpt {
	return func(h *Handler) { h.actorFn = f }
}

// WithProdWrite 注入生产上线审批权限判定（approve 门禁）。
func WithProdWrite(f func(r *http.Request) bool) HandlerOpt {
	return func(h *Handler) { h.prodWrite = f }
}

// WithHandlerRepoLookup 注入内置仓库解析（批次创建定位 RepoID）。
// 命名区别于 Service 同名选项（同包共存，避免 redeclare）。
func WithHandlerRepoLookup(f RepoLookup) HandlerOpt { return func(h *Handler) { h.repoLookup = f } }

// WithRunLister 注入 run 状态列表（通知聚合用）。
func WithRunLister(rl RunLister) HandlerOpt { return func(h *Handler) { h.runLister = rl } }

// WithAlertLister 注入告警状态列表（通知聚合用，评估引擎桥接）。
func WithAlertLister(al AlertLister) HandlerOpt { return func(h *Handler) { h.alertLister = al } }

// NewHandler 构造 Handler。
func NewHandler(svc *Service, repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{svc: svc, repo: repo}
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

// ownChange 校验变更归属 URL appID（同租户跨应用串读串写防护），不匹配按 not found 不泄漏。
func (h *Handler) ownChange(w http.ResponseWriter, r *http.Request, appID, cid string) (Change, bool) {
	c, err := h.repo.GetChange(r.Context(), cid)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return Change{}, false
	}
	if c.AppID != appID {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return Change{}, false
	}
	return c, true
}

// ownBatch 同款（批次）。
func (h *Handler) ownBatch(w http.ResponseWriter, r *http.Request, appID, bid string) (IntegrationBatch, bool) {
	b, err := h.repo.GetBatch(r.Context(), bid)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return IntegrationBatch{}, false
	}
	if b.AppID != appID {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return IntegrationBatch{}, false
	}
	return b, true
}

// ServeGlobal 分发 /api/changes、/api/batches、/api/notifications（跨应用只读，DevOps 中心用）。
// 只读列表：appId query 可选过滤，tenant 内跨应用（与 /api/buildruns 同款语义）。
func (h *Handler) ServeGlobal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermPipelineRead) {
		return
	}
	q := r.URL.Query()
	var (
		list any
		err  error
	)
	switch r.URL.Path {
	case "/api/changes":
		list, err = h.repo.ListChanges(r.Context(), q.Get("appId"), q.Get("status"))
	case "/api/batches":
		list, err = h.syncAndListBatches(r.Context(), q.Get("appId"), q.Get("status"))
	case "/api/notifications":
		// 通知依赖批次最新状态（tested 待审批等），先惰性推进再聚合。
		if _, serr := h.syncAndListBatches(r.Context(), "", ""); serr == nil {
			list, err = Notifications(r.Context(), h.repo, h.runLister, h.alertLister)
		} else {
			err = serr
		}
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	httputil.WriteData(w, list)
}

// syncAndListBatches 列表前惰性推进活跃批次（testing/releasing）终态。
// 前端列表/通知轮询不触发详情端点，若仅详情推进，批次会一直显示 testing、
// 「tested 待审批」通知永不出现。活跃批次通常 ≤2 个，开销可忽略；单批失败不影响其余。
func (h *Handler) syncAndListBatches(ctx context.Context, appID, status string) ([]IntegrationBatch, error) {
	list, err := h.repo.ListBatches(ctx, appID, status)
	if err != nil {
		return nil, err
	}
	for _, b := range list {
		if b.Status != BatchTesting && b.Status != BatchReleasing {
			continue
		}
		if _, err := h.svc.SyncBatchStatus(ctx, b.ID); err != nil {
			continue // 单批推进失败保持现状（下次再推）
		}
	}
	// 推进后状态可能变化，重读一次保证返回最新
	return h.repo.ListBatches(ctx, appID, status)
}

// ServeHTTP 分发 /api/applications/{id}/changes[...] 与 /api/applications/{id}/batches[...]。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]
	switch parts[1] {
	case "changes":
		h.serveChanges(w, r, appID, parts)
	case "batches":
		h.serveBatches(w, r, appID, parts)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveChanges 处理 /api/applications/{id}/changes[/{cid}]。
func (h *Handler) serveChanges(w http.ResponseWriter, r *http.Request, appID string, parts []string) {
	ctx := r.Context()
	// /changes（列表 / 创建）
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			if !h.allow(w, r, PermPipelineRead) {
				return
			}
			list, err := h.repo.ListChanges(ctx, appID, r.URL.Query().Get("status"))
			if err != nil {
				httputil.WriteInternalError(w, err)
				return
			}
			httputil.WriteData(w, list)
		case http.MethodPost:
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			var in ChangeInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				httputil.WriteError(w, http.StatusBadRequest, "invalid body")
				return
			}
			created, err := h.svc.CreateChangeWithBranch(ctx, appID, in)
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			h.recordAudit(r, "change_create", "change", created.ID, "app="+appID+" branch="+in.Branch)
			httputil.WriteDataCreated(w, created)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	// /changes/{cid}（详情 / 放弃）
	if len(parts) == 3 {
		cid := parts[2]
		switch r.Method {
		case http.MethodGet:
			if !h.allow(w, r, PermPipelineRead) {
				return
			}
			c, ok := h.ownChange(w, r, appID, cid)
			if !ok {
				return
			}
			httputil.WriteData(w, c)
		case http.MethodDelete:
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			if _, ok := h.ownChange(w, r, appID, cid); !ok {
				return
			}
			c, err := h.svc.AbandonChange(ctx, cid)
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			h.recordAudit(r, "change_abandon", "change", cid, "app="+appID)
			httputil.WriteData(w, c)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// serveBatches 处理 /api/applications/{id}/batches[...]。
func (h *Handler) serveBatches(w http.ResponseWriter, r *http.Request, appID string, parts []string) {
	ctx := r.Context()
	// /batches（列表 / 创建）
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			if !h.allow(w, r, PermPipelineRead) {
				return
			}
			list, err := h.syncAndListBatches(ctx, appID, r.URL.Query().Get("status"))
			if err != nil {
				httputil.WriteInternalError(w, err)
				return
			}
			httputil.WriteData(w, list)
		case http.MethodPost:
			h.createBatch(w, r, appID)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	bid := parts[2]
	// 批次子资源统一权限 + 归属校验（权限先行：403 优先于 404；归属不匹配 not found 不泄漏）。
	perm := PermPipelineRead
	if r.Method != http.MethodGet {
		perm = PermPipelineWrite
	}
	if !h.allow(w, r, perm) {
		return
	}
	if _, ok := h.ownBatch(w, r, appID, bid); !ok {
		return
	}
	// /batches/{bid}（详情（惰性推进）/ 放弃）
	if len(parts) == 3 {
		switch r.Method {
		case http.MethodGet:
			b, err := h.svc.SyncBatchStatus(ctx, bid) // 惰性推进（testing/releasing 终态回写；权限已在上方统一校验）
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			httputil.WriteData(w, b)
		case http.MethodDelete:
			h.abandonBatch(w, r, appID, bid)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// /batches/{bid}/changes[/{cid}]
	if parts[3] == "changes" {
		switch {
		case len(parts) == 4 && r.Method == http.MethodPost:
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			var body struct {
				ChangeID string `json:"changeId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ChangeID == "" {
				httputil.WriteError(w, http.StatusBadRequest, "changeId required")
				return
			}
			if _, ok := h.ownChange(w, r, appID, body.ChangeID); !ok {
				return
			}
			b, err := h.svc.AddChangeToBatch(ctx, bid, body.ChangeID)
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			h.recordAudit(r, "batch_add_change", "integration_batch", bid, "change="+body.ChangeID)
			httputil.WriteData(w, b)
		case len(parts) == 5 && r.Method == http.MethodDelete:
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			if _, ok := h.ownChange(w, r, appID, parts[4]); !ok {
				return
			}
			b, err := h.svc.RemoveChangeFromBatch(ctx, bid, parts[4])
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			h.recordAudit(r, "batch_remove_change", "integration_batch", bid, "change="+parts[4])
			httputil.WriteData(w, b)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// /batches/{bid}/integrate|approve|release
	if len(parts) == 4 && r.Method == http.MethodPost {
		// 应用级权限：受限应用的批次动作需成员角色（integrate=write；approve/release=release 动作）。
		if h.appGuard != nil {
			if b, err := h.repo.GetBatch(ctx, bid); err == nil {
				action := application.AppActionRelease
				if parts[3] == "integrate" {
					action = application.AppActionWrite
				}
				if !h.appGuard.Allow(r, b.AppID, action) {
					httputil.WriteError(w, http.StatusForbidden, "forbidden: 无该应用的应用级权限（"+action+"）")
					return
				}
			}
		}
		switch parts[3] {
		case "integrate":
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			b, err := h.svc.Integrate(ctx, bid)
			if err != nil {
				h.writeBatchError(w, err)
				return
			}
			h.recordAudit(r, "batch_integrate", "integration_batch", bid, "run="+b.RunID)
			httputil.WriteData(w, b)
		case "approve":
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			// 生产上线门禁：approve 即批准发布到生产，要求 prod:write。
			if h.prodWrite != nil && !h.prodWrite(r) {
				httputil.WriteError(w, http.StatusForbidden, "生产上线需要 prod:write 权限")
				return
			}
			b, err := h.svc.Approve(ctx, bid)
			if err != nil {
				httputil.WriteServiceError(w, toHTTPStatus(err), err)
				return
			}
			h.recordAudit(r, "batch_approve", "integration_batch", bid, "")
			httputil.WriteData(w, b)
		case "release":
			if !h.allow(w, r, PermPipelineWrite) {
				return
			}
			b, err := h.svc.Release(ctx, bid)
			if err != nil {
				h.writeBatchError(w, err)
				return
			}
			h.recordAudit(r, "batch_release", "integration_batch", bid, "run="+b.RunID)
			httputil.WriteData(w, b)
		default:
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// createBatch POST /api/applications/{id}/batches。body {title, branch}。
// RepoID 经 repoLookup 解析应用内置仓库（与变更同源，保证 AddChangeToBatch 同仓校验通过）。
func (h *Handler) createBatch(w http.ResponseWriter, r *http.Request, appID string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	var body struct {
		Title  string `json:"title"`
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if h.repoLookup == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "repo lookup 未配置")
		return
	}
	_, _, repoID, err := h.repoLookup(r.Context(), appID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusServiceUnavailable, err)
		return
	}
	created, err := h.repo.CreateBatch(r.Context(), IntegrationBatch{
		AppID: appID, RepoID: repoID, Title: body.Title, Branch: body.Branch,
	})
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	h.recordAudit(r, "batch_create", "integration_batch", created.ID, "app="+appID)
	httputil.WriteDataCreated(w, created)
}

// abandonBatch DELETE /api/applications/{id}/batches/{bid}。
// 仅 collecting/conflict/failed 可放弃；批内未出批变更一并回 open 出批（可重新入批）。
func (h *Handler) abandonBatch(w http.ResponseWriter, r *http.Request, appID, bid string) {
	if !h.allow(w, r, PermPipelineWrite) {
		return
	}
	ctx := r.Context()
	b, err := h.repo.GetBatch(ctx, bid)
	if err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	switch b.Status {
	case BatchCollecting, BatchConflict, BatchFailed:
	default:
		httputil.WriteServiceError(w, http.StatusConflict, errors.New("仅 collecting/conflict/failed 批次可放弃"))
		return
	}
	b.Status = BatchAbandoned
	if _, err := h.repo.UpdateBatch(ctx, b); err != nil {
		httputil.WriteServiceError(w, toHTTPStatus(err), err)
		return
	}
	// 批内变更回 open 出批（collecting/conflict 态变更仍挂批次；failed 态已由 Sync 出批，幂等）
	for _, cid := range b.ChangeIDs {
		if c, err := h.repo.GetChange(ctx, cid); err == nil {
			c.BatchID = ""
			c.ConflictWith = ""
			if c.Status == ChangeIntegrated {
				c.Status = ChangeOpen
			}
			_, _ = h.repo.UpdateChange(ctx, c)
		}
	}
	h.recordAudit(r, "batch_abandon", "integration_batch", bid, "app="+appID)
	httputil.WriteData(w, map[string]string{"deleted": bid})
}

// writeBatchError 批次编排错误分流：merge 冲突（*BatchConflictError）→ 409 + 中文冲突信息；
// 其余走 toHTTPStatus + WriteServiceError。
func (h *Handler) writeBatchError(w http.ResponseWriter, err error) {
	var ce *BatchConflictError
	if errors.As(err, &ce) {
		httputil.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	httputil.WriteServiceError(w, toHTTPStatus(err), err)
}

// toHTTPStatus sentinel -> HTTP 状态码。
func toHTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrChangeNotFound), errors.Is(err, ErrBatchNotFound),
		errors.Is(err, ErrNoCIPipeline):
		return http.StatusNotFound
	case errors.Is(err, ErrChangeExists), errors.Is(err, ErrBatchExists),
		errors.Is(err, ErrBatchState), errors.Is(err, ErrMergeConflictBatch),
		errors.Is(err, gitea.ErrBranchExists):
		return http.StatusConflict
	case errors.Is(err, ErrNoTenant):
		return http.StatusBadRequest
	case errors.Is(err, ErrGiteaNotConfigured):
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

// recordAudit 记审计（audit/actorFn nil 跳过）。tenantID 从 ctx 取。
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
