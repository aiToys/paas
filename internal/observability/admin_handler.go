// Package observability admin_handler.go 提供告警规则 admin REST API（跨租户管理）。
//
// 路由（全挂 adminGuard(super_admin)，cmd/core 装配；handler 内不重复 Authorize）：
//
//	GET    /api/admin/alert-rules/{id}   跨租户告警规则详情
//	DELETE /api/admin/alert-rules/{id}   强制删除（绕过 prod:write）
//
// Repository 无 UpdateAlertRule -> 启停跳过（无 Update 接口，留后续），只做删。
// 跨租户单条读：Repository 无 GetAny，统一 ListAllAlertRules filter by id
// （与 workload/devops admin handler 同款）。
package observability

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 observability->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder = adminutil.AuditRecorder // admin 写操作审计（依赖倒置，统一真源 internal/web/admin）

// AdminHandler 暴露告警规则 admin REST API（/api/admin/alert-rules/{id}*）。
//
// 注入：Repository（含 ListAllAlertRules/DeleteAlertRule）+ AdminAuditRecorder + actor 提取器。
//
// 绕过 prod:write（super_admin 有权干预），但写操作必记审计。
type AdminHandler struct {
	repo    Repository
	audit   AdminAuditRecorder
	actorOf func(*http.Request) string
}

// AdminHandlerOpt admin handler 配置。
type AdminHandlerOpt func(*AdminHandler)

// NewAdminHandler 创建 admin handler。
func NewAdminHandler(repo Repository, opts ...AdminHandlerOpt) *AdminHandler {
	h := &AdminHandler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// WithAdminAudit 注入审计 recorder。
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt {
	return func(h *AdminHandler) { h.audit = a }
}

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

// ServeHTTP 按路径分发 admin 请求。
// 注意：/api/admin/alert-rules（无尾斜杠）GET 列表由 cmd/core reg.Register 直接处理；
// 这里只处理 /api/admin/alert-rules/（有尾斜杠，{id} 路径）。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	if !strings.HasPrefix(path, "/api/admin/alert-rules/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := strings.Trim(strings.TrimPrefix(path, "/api/admin/alert-rules/"), "/")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.serveDetail(w, r, id)
	case http.MethodDelete:
		h.serveDelete(w, r, id)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// adminTenantCtx 派生资源所属租户 ctx（admin 跨租户操作以资源租户身份执行下游）。
func (h *AdminHandler) actor(r *http.Request) string {
	if h.actorOf != nil {
		return h.actorOf(r)
	}
	return "admin"
}

// recordAudit best-effort 记审计（错误不影响主流程）。
func (h *AdminHandler) recordAudit(r *http.Request, tenantID, action, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "alert-rule", resourceID, detail)
}

// findByID 跨租户取单条告警规则（ListAllAlertRules filter by id）。
// Repository 无 GetAlertRuleAny，用 ListAll filter（DeleteAlertRule 强制 ctx tenant，admin 需绕过）。
func (h *AdminHandler) findByID(ctx context.Context, id string) (AlertRule, error) {
	list, err := h.repo.ListAllAlertRules(ctx)
	if err != nil {
		return AlertRule{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return AlertRule{}, fmt.Errorf("告警规则不存在: %s", id)
}

// serveDetail 详情：跨租户取单条告警规则。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, id string) {
	rule, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, rule)
}

// serveDelete 强制删除，以目标租户 ctx 执行，绕过 prod:write。
func (h *AdminHandler) serveDelete(w http.ResponseWriter, r *http.Request, id string) {
	rule, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminutil.TenantCtx(r, rule.TenantID)
	if err := h.repo.DeleteAlertRule(ctx, id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.recordAudit(rr, rule.TenantID, "admin:delete", id, "删除告警规则")
	httputil.WriteData(w, map[string]string{"deleted": id})
}
