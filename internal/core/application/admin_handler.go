package application

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 application->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// QuotaCheckFunc 配额检查-递增（横切）。ctx 必须带目标租户；delta=+1 创建/-1 删除。
type QuotaCheckFunc func(ctx context.Context, delta int) error

// CascadeDeleter 跨 store 关联资源级联清理（可选）；由 cmd/core 桥接
// workload/appconfig/devops 等 store。删除应用前清该 appID 下孤儿资源（best-effort）。
type CascadeDeleter interface {
	CascadeDelete(ctx context.Context, appID string) error
}

// AdminHandler 暴露应用 admin REST API（/api/admin/applications*）。
//
// 路由：
//
//	GET    /api/admin/applications          跨租户列表
//	GET    /api/admin/applications/{id}     跨租户详情
//	DELETE /api/admin/applications/{id}     强制删除（级联清理 + 回收配额）
//
// 仅 L1 详情 + L2 删除（基线 spec 明确不代建业务编排类应用）。
//
// 注意：application.Repository.Get 强制 ctx tenant 过滤（缺失即拒），
// admin 路径取单条用 ListAll filter by id（与 workload 同款）。
type AdminHandler struct {
	repo    Repository
	quota   QuotaCheckFunc
	cascade CascadeDeleter
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

// WithAdminQuota 注入配额检查（消耗目标租户 applications 维度）。
func WithAdminQuota(f QuotaCheckFunc) AdminHandlerOpt { return func(h *AdminHandler) { h.quota = f } }

// WithAdminCascade 注入跨 store 级联清理（删应用前清关联资源）。
func WithAdminCascade(c CascadeDeleter) AdminHandlerOpt { return func(h *AdminHandler) { h.cascade = c } }

// WithAdminAudit 注入审计 recorder。
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt { return func(h *AdminHandler) { h.audit = a } }

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

// ServeHTTP 按路径分发 admin 请求。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/admin/applications" && r.Method == http.MethodGet:
		h.serveList(w, r)
	case strings.HasPrefix(path, "/api/admin/applications/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// tenantCtx 派生资源所属租户 ctx（admin 跨租户操作以资源租户身份执行下游）。
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
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "application", resourceID, detail)
}

// findByID 跨租户取单条应用（ListAll filter by id）。
// 用 ListAll 而非 Get：Repository.Get 强制 ctx tenant，admin 跨租户路径需绕过。
func (h *AdminHandler) findByID(ctx context.Context, id string) (Application, error) {
	list, err := h.repo.ListAll(ctx)
	if err != nil {
		return Application{}, err
	}
	for _, a := range list {
		if a.ID == id {
			return a, nil
		}
	}
	return Application{}, fmt.Errorf("应用不存在: %s", id)
}

// serveList 跨租户列表。
func (h *AdminHandler) serveList(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListAll(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// serveItem 按路径细分：/{id}（GET 详情 / DELETE 删除）。
func (h *AdminHandler) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/applications/")
	rest = strings.TrimRight(rest, "/")
	id := rest
	if id == "" {
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

// serveDetail 详情。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, id string) {
	a, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, a)
}

// serveDelete 强制删除（级联清理 + 回收配额 + 审计）。绕过 prod:write。
func (h *AdminHandler) serveDelete(w http.ResponseWriter, r *http.Request, id string) {
	a, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminTenantCtx(r, a.TenantID)
	// 级联清理关联资源（best-effort，失败仅记日志不阻断；与租户侧 handler 一致）。
	if h.cascade != nil {
		_ = h.cascade.CascadeDelete(ctx, id)
	}
	if err := h.repo.Delete(ctx, id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	// 回收配额 best-effort（删除主操作不因配额回滚失败而阻断）。
	if h.quota != nil {
		_ = h.quota(ctx, -1)
	}
	h.recordAudit(rr, a.TenantID, "admin:delete", id, "删除应用")
	httputil.WriteData(w, map[string]string{"deleted": id})
}
