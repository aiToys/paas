// Package admin 实现 AI 编排实体的 admin 跨租户总览（只读 + 广场下架）：
// /api/admin/ai/{agents|tools|knowledgebases|prompts|skills|workflows|workflow-runs|marketplace}。
//
// 与其他 /api/admin/* 资源总览同款模式：super_admin 只读、ListAll 跨租户、
// 不绕过租户写（资源运维仍在 console-user 租户内）。
// 例外：广场下架是平台治理动作（spec 2026-10-19 D3）——独立 admin 契约
// （DELETE /api/admin/ai/marketplace/{id}?reason=），不复用租户端点语义；
// 原因必填、写审计、响应如实标注「已安装副本不受影响」。
package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/marketplace"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/ai/workflow"
	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// AuditRecorder 审计记录（依赖倒置，cmd/core 装配桥接 identity）。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// Handler AI 编排 admin 总览。
type Handler struct {
	agents  agent.Repository
	tools   tool.Repository
	kbs     knowledgebase.Repository
	prompts prompt.Repository
	skills  skill.Repository
	market  marketplace.Repository
	wfs     workflow.Repository
	// audit 审计记录器（广场下架写审计；nil 跳过，测试场景）。
	audit AuditRecorder
	// actorFrom 从请求取操作者（审计归因；nil 时记 "platform"）。
	actorFrom func(r *http.Request) string
	// platformAdmin 判定 super_admin（广场下架 fail-closed：nil 拒绝）。
	platformAdmin func(r *http.Request) bool
}

type HandlerOpt func(*Handler)

func WithAudit(a AuditRecorder, actorFrom func(r *http.Request) string) HandlerOpt {
	return func(h *Handler) { h.audit = a; h.actorFrom = actorFrom }
}

func WithPlatformAdmin(f func(r *http.Request) bool) HandlerOpt {
	return func(h *Handler) { h.platformAdmin = f }
}

func NewHandler(
	agents agent.Repository,
	tools tool.Repository,
	kbs knowledgebase.Repository,
	prompts prompt.Repository,
	skills skill.Repository,
	market marketplace.Repository,
	wfs workflow.Repository,
	opts ...HandlerOpt,
) *Handler {
	h := &Handler{agents: agents, tools: tools, kbs: kbs, prompts: prompts, skills: skills, market: market, wfs: wfs}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")

	// 广场下架（平台治理写操作，独立契约）。
	if strings.HasPrefix(path, "/api/admin/ai/marketplace/") && r.Method == http.MethodDelete {
		h.unpublishMarketItem(w, r, strings.TrimPrefix(path, "/api/admin/ai/marketplace/"))
		return
	}

	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch path {
	case "/api/admin/ai/agents":
		list, err := h.agents.ListAll(r.Context())
		h.write(w, adminutil.FilterTenant(r, list, func(a agent.Agent) string { return a.TenantID }), err)
	case "/api/admin/ai/tools":
		list, err := h.tools.ListAll(r.Context())
		h.write(w, adminutil.FilterTenant(r, list, func(t tool.Tool) string { return t.TenantID }), err)
	case "/api/admin/ai/knowledgebases":
		list, err := h.kbs.ListAll(r.Context())
		h.write(w, adminutil.FilterTenant(r, list, func(k knowledgebase.KnowledgeBase) string { return k.TenantID }), err)
	case "/api/admin/ai/prompts":
		list, err := h.prompts.ListAll(r.Context())
		h.write(w, adminutil.FilterTenant(r, list, func(p prompt.Prompt) string { return p.TenantID }), err)
	case "/api/admin/ai/skills":
		list, err := h.skills.ListAll(r.Context())
		h.write(w, adminutil.FilterTenant(r, list, func(s skill.Skill) string { return s.TenantID }), err)
	case "/api/admin/ai/workflows":
		// 工作流定义跨租户总览（spec D3：资产详情与依赖可见）。
		list, err := h.wfs.ListAll(r.Context())
		h.write(w, adminutil.FilterTenant(r, list, func(d workflow.WorkflowDef) string { return d.TenantID }), err)
	case "/api/admin/ai/workflow-runs":
		// 工作流运行跨租户总览（spec D3：节点失败/等待确认状态随 NodeRun 原样返回）。
		// 列表瘦身：DefSnapshot 不带（体积大，详情语义在租户端点；console-v2 同款先例）。
		list, err := h.wfs.ListAllRuns(r.Context())
		if err == nil {
			for i := range list {
				list[i].DefSnapshot = nil
			}
		}
		h.write(w, adminutil.FilterTenant(r, list, func(run workflow.WorkflowRun) string { return run.TenantID }), err)
	case "/api/admin/ai/marketplace":
		// 广场总览（admin 可发现违规内容；下架走本包 unpublish 独立契约）。
		// 平台级快照不按租户过滤（发布租户是元信息而非归属边界）。
		list, err := h.market.ListAll(r.Context())
		h.write(w, list, err)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// unpublishMarketItem 广场下架（spec 2026-10-19 D3）：super_admin + reason 必填 + 审计。
// 影响：仅下架广场列表项；已安装副本不受影响（安装是 fork 到租户，无反向依赖）。
func (h *Handler) unpublishMarketItem(w http.ResponseWriter, r *http.Request, id string) {
	if h.platformAdmin == nil || !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden: super_admin required")
		return
	}
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if reason == "" {
		httputil.WriteError(w, http.StatusBadRequest, "下架原因必填（?reason=）")
		return
	}
	it, err := h.market.Get(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "广场条目不存在: "+id)
		return
	}
	if err := h.market.Delete(r.Context(), id); err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if h.audit != nil {
		actor := "platform"
		if h.actorFrom != nil {
			if a := h.actorFrom(r); a != "" {
				actor = a
			}
		}
		_ = h.audit.Record(r.Context(), "", actor, "admin:marketplace_unpublish",
			"marketplace_item", id, "entity="+it.EntityType+" name="+it.Name+" publisher="+it.PublisherTenant+" reason="+reason)
	}
	httputil.WriteData(w, map[string]any{
		"deleted":         id,
		"installs":        it.Installs,
		"installedNote":   "已安装副本不受影响（安装是 fork 到租户，无反向依赖）",
		"publisherTenant": it.PublisherTenant,
		"entityType":      it.EntityType,
	})
}

func (h *Handler) write(w http.ResponseWriter, list any, err error) {
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}
