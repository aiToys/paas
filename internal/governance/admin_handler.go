// Package governance admin_handler.go 提供服务治理 admin REST API（服务跨租户管理）。
//
// 路由（全挂 adminGuard(super_admin)，cmd/core 装配；handler 内不重复 Authorize）：
//
//	GET    /api/admin/services/{id}                  跨租户服务详情（含实例列表）
//	DELETE /api/admin/services/{id}                  强制删服务（级联清实例，绕过 prod:write）
//	DELETE /api/admin/services/{id}/instances/{iid}  注销实例（绕过 prod:write）
//
// 只做服务 admin；路由 Route / 熔断 Breaker 无 ListAll 接口（基线 spec 允许跳过，留后续）。
// 跨租户单条读：Repository.GetService/ListInstances 强制 ctx tenant，admin 用 ListAllServices
// filter by id 取服务，再以目标租户 ctx 读实例（与 workload admin handler 同款模式）。
package governance

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 governance->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// AdminHandler 暴露服务治理 admin REST API（/api/admin/services/{id}*）。
//
// 注入：Repository（含 ListAllServices/ListInstances/DeregisterInstance/DeleteService）+
// AdminAuditRecorder + actor 提取器。
//
// 绕过 prod:write（super_admin 有权干预生产），但写操作必记审计。
type AdminHandler struct {
	repo     Repository
	audit    AdminAuditRecorder
	actorOf  func(*http.Request) string
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
// 注意：/api/admin/services（无尾斜杠）GET 列表由 cmd/core reg.Register 直接处理；
// 这里只处理 /api/admin/services/（有尾斜杠，{id} 路径）。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	if !strings.HasPrefix(path, "/api/admin/services/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(path, "/api/admin/services/"), "/")
	if rest == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	parts := strings.SplitN(rest, "/", 3)
	id := parts[0]
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	// /{id} 或 /{id}/instances/{iid}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			h.serveDetail(w, r, id)
		case http.MethodDelete:
			h.serveDelete(w, r, id)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	// /{id}/instances/{iid}
	if len(parts) == 3 && parts[1] == "instances" {
		if r.Method != http.MethodDelete {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveDeregister(w, r, id, parts[2])
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// adminTenantCtx 派生资源所属租户 ctx（admin 跨租户操作以资源租户身份执行下游）。
func adminTenantCtx(r *http.Request, tenantID string) (context.Context, *http.Request) {
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
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "service", resourceID, detail)
}

// findByID 跨租户取单条服务（ListAllServices filter by id）。
// 用 ListAll 而非 GetService：Repository.GetService 强制 ctx tenant，admin 跨租户路径需绕过。
func (h *AdminHandler) findByID(ctx context.Context, id string) (Service, error) {
	list, err := h.repo.ListAllServices(ctx)
	if err != nil {
		return Service{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return Service{}, fmt.Errorf("服务不存在: %s", id)
}

// serveDetail 详情：服务 + 实例列表（以目标租户 ctx 读实例，ListInstances 强制 ctx tenant）。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, id string) {
	s, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, _ := adminTenantCtx(r, s.TenantID)
	instances, err := h.repo.ListInstances(ctx, id)
	if err != nil {
		instances = []Instance{}
	}
	httputil.WriteData(w, ServiceDetail{Service: s, Instances: instances})
}

// serveDelete 强制删服务（级联清实例），以目标租户 ctx 执行，绕过 prod:write。
func (h *AdminHandler) serveDelete(w http.ResponseWriter, r *http.Request, id string) {
	s, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminTenantCtx(r, s.TenantID)
	if err := h.repo.DeleteService(ctx, id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.recordAudit(rr, s.TenantID, "admin:delete", id, "删除服务治理服务")
	httputil.WriteData(w, map[string]string{"deleted": id})
}

// serveDeregister 注销实例，以目标租户 ctx 执行，绕过 prod:write。
// 先校验实例归属该服务（防越权路径），再注销。
func (h *AdminHandler) serveDeregister(w http.ResponseWriter, r *http.Request, serviceID, instID string) {
	s, err := h.findByID(r.Context(), serviceID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminTenantCtx(r, s.TenantID)
	// 校验实例归属该服务（与租户侧 handler 一致，防越权路径）。
	sid, err := h.repo.InstanceServiceID(ctx, instID)
	if err != nil || sid != serviceID {
		httputil.WriteError(w, http.StatusNotFound, "实例不存在或不属于该服务")
		return
	}
	if err := h.repo.DeregisterInstance(ctx, instID); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.recordAudit(rr, s.TenantID, "admin:deregister", instID, "注销服务实例")
	httputil.WriteData(w, map[string]string{"deleted": instID})
}
