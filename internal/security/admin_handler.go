// Package security admin_handler.go 提供密钥 admin REST API（跨租户管理）。
//
// 路由（全挂 adminGuard(super_admin)，cmd/core 装配；handler 内不重复 Authorize）：
//
//	GET    /api/admin/secrets/{id}   跨租户密钥详情（掩码）
//	DELETE /api/admin/secrets/{id}   强制删除（绕过 prod:write）
//
// security handler 现有 DELETE 已记审计（serveSecretItem），但 admin 跨租户需独立路径。
// 跨租户单条读：Repository.GetSecret 强制 ctx tenant，admin 用 ListAllSecrets filter by id
// （与 workload/devops admin handler 同款）。返回 Masked() 掩码（与租户侧一致）。
package security

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 security 自引用循环）。
// tenantID = 资源所属租户（target_tenant，平台级密钥为空字符串）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// AdminHandler 暴露密钥 admin REST API（/api/admin/secrets/{id}*）。
//
// 注入：Repository（含 ListAllSecrets/DeleteSecret）+ AdminAuditRecorder + actor 提取器。
//
// 绕过 prod:write（super_admin 有权干预），但写操作必记审计。
// 注意：平台级 Secret（scope=platform, TenantID 空）也经此 handler；target_tenant 记录为空字符串。
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
// 注意：/api/admin/secrets（无尾斜杠）GET 列表由 cmd/core reg.Register 直接处理；
// 这里只处理 /api/admin/secrets/（有尾斜杠，{id} 路径）。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	if !strings.HasPrefix(path, "/api/admin/secrets/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := strings.Trim(strings.TrimPrefix(path, "/api/admin/secrets/"), "/")
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
// 平台级 Secret（TenantID 空）注入 sentinel "platform"：下游 DeleteSecret 调 TenantOrErr
// 拒绝空字符串（返 "missing tenant context"），sentinel 让其通过；SQL tenant_id='platform'
// 不会误匹配租户级（NULL ≠ 'platform'），OR scope='platform' 命中平台级行（与 identityAuditAdapter 同款）。
// 审计仍记真实 sec.TenantID（空），由 identityAuditAdapter 转 "platform" 落库。
func adminTenantCtx(r *http.Request, tenantID string) (context.Context, *http.Request) {
	if tenantID == "" {
		tenantID = "platform"
	}
	ctx := tenant.WithTenant(r.Context(), tenantID)
	return ctx, r.WithContext(ctx)
}

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
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "secret", resourceID, detail)
}

// findByID 跨租户取单条密钥（ListAllSecrets filter by id）。
// Repository.GetSecret 强制 ctx tenant，admin 跨租户路径需绕过。返回明文（外部 Masked）。
func (h *AdminHandler) findByID(ctx context.Context, id string) (Secret, error) {
	list, err := h.repo.ListAllSecrets(ctx)
	if err != nil {
		return Secret{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return Secret{}, fmt.Errorf("密钥不存在: %s", id)
}

// serveDetail 详情：跨租户取单条密钥，返回 Masked() 掩码（与租户侧一致，不泄漏明文）。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, id string) {
	sec, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, sec.Masked())
}

// serveDelete 强制删除，以目标租户 ctx 执行，绕过 prod:write。
func (h *AdminHandler) serveDelete(w http.ResponseWriter, r *http.Request, id string) {
	sec, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminTenantCtx(r, sec.TenantID)
	if err := h.repo.DeleteSecret(ctx, id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.recordAudit(rr, sec.TenantID, "admin:delete", id, "删除密钥")
	httputil.WriteData(w, map[string]string{"deleted": id})
}
