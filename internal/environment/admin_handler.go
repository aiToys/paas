package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// AdminTenantChecker / AdminAuditRecorder 与 dataservice 同款（依赖倒置，避免 environment->identity/security）。
type AdminTenantChecker interface {
	Exists(ctx context.Context, tenantID string) error
}

// AdminAuditRecorder admin 写操作审计（与 dataservice.AdminAuditRecorder 同源）。
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

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
func WithAdminTenants(c AdminTenantChecker) AdminHandlerOpt { return func(h *AdminHandler) { h.tenants = c } }

// WithAdminAudit 注入审计 recorder。
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt { return func(h *AdminHandler) { h.audit = a } }

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
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
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
