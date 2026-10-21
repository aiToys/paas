package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
	"github.com/aitoys/paas/pkg/tenant"
)

// AdminTenantChecker / AdminAuditRecorder 与 dataservice 同款（依赖倒置，避免 environment->identity/security）。
type AdminTenantChecker interface {
	Exists(ctx context.Context, tenantID string) error
}

// AdminAuditRecorder admin 写操作审计（与 dataservice.AdminAuditRecorder 同源）。
type AdminAuditRecorder = adminutil.AuditRecorder // admin 写操作审计（依赖倒置，统一真源 internal/web/admin）

// AdminHandler 暴露环境 admin REST API（/api/admin/environments*）。
//
// 路由：
//
//	GET    /api/admin/environments     跨租户列表
//	POST   /api/admin/environments     代建（body tenantId 必填）
//
// 仅代建（L3）：环境是基础设施，admin 可代某租户建环境；其余运维（删除）走租户侧。
type AdminHandler struct {
	repo    Repository
	tenants AdminTenantChecker
	audit   AdminAuditRecorder
	actorOf func(*http.Request) string
	// usage 依赖预检（只读）：与租户侧 DELETE 前检查同一实现，admin 视图不另算一套。
	usage EnvUsageChecker
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

// WithAdminTenants 注入租户校验器。
func WithAdminTenants(c AdminTenantChecker) AdminHandlerOpt {
	return func(h *AdminHandler) { h.tenants = c }
}

// WithAdminAudit 注入审计 recorder。
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt {
	return func(h *AdminHandler) { h.audit = a }
}

// WithAdminUsage 注入环境依赖预检（只读；与租户侧删除前置检查同源）。
func WithAdminUsage(u EnvUsageChecker) AdminHandlerOpt {
	return func(h *AdminHandler) { h.usage = u }
}

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

func (h *AdminHandler) actor(r *http.Request) string {
	if h.actorOf != nil {
		return h.actorOf(r)
	}
	return "admin"
}

// recordAudit best-effort 记审计。
func (h *AdminHandler) recordAudit(r *http.Request, tenantID, action, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "environment", resourceID, detail)
}

// ServeHTTP 按路径分发 admin 请求。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/admin/environments" && r.Method == http.MethodGet:
		h.serveList(w, r)
	case path == "/api/admin/environments" && r.Method == http.MethodPost:
		h.serveCreate(w, r)
	case strings.HasPrefix(path, "/api/admin/environments/"):
		h.serveItem(w, r)
	default:
		// 已知列表路径但方法非 GET/POST -> 405；其余未注册路径 -> 404。
		if path == "/api/admin/environments" {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	}
}

// serveItem 处理 /api/admin/environments/{id}（GET 详情）与 /{id}/usage（依赖预检）。
func (h *AdminHandler) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(strings.TrimRight(r.URL.Path, "/"), "/api/admin/environments/"), "/")
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// usage 分支必须先于单资源分支判断（rest 含斜杠时 Split 出的 id 会误匹配单资源）。
	if strings.HasSuffix(rest, "/usage") {
		h.serveUsage(w, r, strings.TrimSuffix(rest, "/usage"))
		return
	}
	if rest == "" || strings.Contains(rest, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	h.serveDetail(w, r, rest)
}

// serveUsage 跨租户环境依赖预检：定位环境归属租户 → 以该租户 ctx 复用租户侧检查器。
// 只读：不代租户删除环境（删除仍走租户侧，依赖存在时 409）。
func (h *AdminHandler) serveUsage(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	env, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if h.usage == nil {
		httputil.WriteError(w, http.StatusNotImplemented, "依赖预检未装配")
		return
	}
	ctx, _ := adminutil.TenantCtx(r, env.TenantID)
	usage, err := h.usage.EnvUsage(ctx, id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
		return
	}
	httputil.WriteData(w, usage)
}

// serveDetail 跨租户环境详情（ListAll filter by id）。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, id string) {
	e, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, e)
}

// findByID 跨租户取单条环境（ListAll filter by id）。
func (h *AdminHandler) findByID(ctx context.Context, id string) (Environment, error) {
	list, err := h.repo.ListAll(ctx)
	if err != nil {
		return Environment{}, err
	}
	for _, e := range list {
		if e.ID == id {
			return e, nil
		}
	}
	return Environment{}, fmt.Errorf("环境不存在: %s", id)
}

// serveList 跨租户列表（支持 ?tenantId= 浏览范围过滤）。
func (h *AdminHandler) serveList(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListAll(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, adminutil.FilterTenant(r, list, func(e Environment) string { return e.TenantID }))
}

// adminCreateInput 代建请求体。TenantID 必填（归属租户）；其余与 Environment 一致。
type adminCreateInput struct {
	TenantID string `json:"tenantId"`
	Environment
}

// serveCreate 代建：校验租户 → Validate → 以目标租户 ctx Create → 审计 admin:create。
// 环境 Create 以 ctx tenant 为准（CLAUDE.md「Create 以 ctx 租户为准忽略请求体」）。
func (h *AdminHandler) serveCreate(w http.ResponseWriter, r *http.Request) {
	var in adminCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.TenantID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing tenantId")
		return
	}
	if h.tenants != nil {
		if err := h.tenants.Exists(r.Context(), in.TenantID); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, fmt.Errorf("租户不存在: %s", in.TenantID))
			return
		}
	}
	e := in.Environment
	e.TenantID = in.TenantID
	if err := e.Validate(); err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	// 未指定 promoteOrder 时按 type 填默认（与租户侧 handler 一致）。
	if e.PromoteOrder == 0 {
		e.PromoteOrder = DefaultPromoteOrder(e.Type)
	}
	ctx := tenant.WithTenant(r.Context(), in.TenantID)
	if err := h.repo.Create(ctx, e); err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, in.TenantID, "admin:create", e.ID, "代建 environment")
	httputil.WriteDataCreated(w, e)
}
