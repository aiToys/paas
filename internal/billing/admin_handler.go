// Package billing admin_handler.go 提供配额+账单 admin REST API（跨租户管理）。
//
// 路由（全挂 adminGuard(super_admin)，cmd/core 装配；handler 内不重复 Authorize）：
//
//	GET    /api/admin/quotas           跨租户配额列表（原 reg.Register 并入此 handler）
//	PUT    /api/admin/quotas           调整指定租户配额（body {tenantId, limits}，绕过 prod:write）
//	GET    /api/admin/bills            跨租户账单列表（原 reg.Register 并入此 handler）
//	GET    /api/admin/bills/{id}       跨租户账单详情
//	POST   /api/admin/bills/{id}/pay   标记账单已付（绕过 prod:write）
//
// billing admin 是调整配额本身（SetQuota 改 Limits），不消耗配额，无 QuotaCheck。
// 跨租户单条读：quota 无 id 概念（按 tenantId），bill 用 ListAllBills filter by id。
// Repository.GetBill 强制 ctx tenant，admin 跨租户路径用 ListAllBills filter 绕过。
package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 billing->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder = adminutil.AuditRecorder // admin 写操作审计（依赖倒置，统一真源 internal/web/admin）

// AdminTenantChecker 校验租户存在（admin 调整配额 body tenantId 校验，防孤儿配额记录）。cmd/core 桥接 identity.Repository。
type AdminTenantChecker interface {
	Exists(ctx context.Context, tenantID string) error
}

// AdminHandler 暴露配额+账单 admin REST API（/api/admin/quotas* + /api/admin/bills*）。
//
// 注入：Repository（含 ListAllQuotas/SetQuota/ListAllBills/PayBill）+
// AdminAuditRecorder + actor 提取器 + TenantChecker（调整配额校验租户存在）。
//
// 绕过 prod:write（super_admin 有权干预），但写操作必记审计。
type AdminHandler struct {
	repo    Repository
	audit   AdminAuditRecorder
	tenants AdminTenantChecker
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

// WithAdminTenants 注入租户校验器（调整配额校验租户存在，防给不存在租户设配额污染数据）。
func WithAdminTenants(c AdminTenantChecker) AdminHandlerOpt {
	return func(h *AdminHandler) { h.tenants = c }
}

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

// ServeHTTP 按路径分发 admin 请求。
// quotas/bills 列表 + item 路径统一此 handler 分发（PUT /quotas 与 GET /quotas 同路径需同 handler）。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/admin/quotas":
		switch r.Method {
		case http.MethodGet:
			h.serveQuotaList(w, r)
		case http.MethodPut:
			h.serveSetQuota(w, r)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case strings.HasPrefix(path, "/api/admin/bills"):
		h.serveBills(w, r, path)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
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
func (h *AdminHandler) recordAudit(r *http.Request, tenantID, action, resourceType, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, resourceType, resourceID, detail)
}

// ---------- 配额 ----------

// serveQuotaList 跨租户配额列表。
func (h *AdminHandler) serveQuotaList(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListAllQuotas(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// adminSetQuotaInput 调整配额请求体。tenantId 必填（目标租户）；limits 覆盖。
type adminSetQuotaInput struct {
	TenantID string         `json:"tenantId"`
	Limits   map[string]int `json:"limits"`
}

// serveSetQuota 调整指定租户配额，以目标租户 ctx 执行 SetQuota，绕过 prod:write。
func (h *AdminHandler) serveSetQuota(w http.ResponseWriter, r *http.Request) {
	var in adminSetQuotaInput
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
	q := ResourceQuota{TenantID: in.TenantID, Limits: in.Limits}
	ctx, rr := adminutil.TenantCtx(r, in.TenantID)
	saved, err := h.repo.SetQuota(ctx, q)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(rr, in.TenantID, "admin:set-quota", "quota", in.TenantID, "调整配额")
	httputil.WriteData(w, saved)
}

// ---------- 账单 ----------

// serveBills 处理 /api/admin/bills（列表）与 /api/admin/bills/{id}[/{action}]。
func (h *AdminHandler) serveBills(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.Trim(strings.TrimPrefix(path, "/api/admin/bills"), "/")
	// /api/admin/bills（无 {id}）
	if rest == "" {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveBillList(w, r)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	switch action {
	case "":
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveBillDetail(w, r, id)
	case "pay":
		h.servePay(w, r, id)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveBillList 跨租户账单列表。
func (h *AdminHandler) serveBillList(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListAllBills(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// findBillByID 跨租户取单条账单（ListAllBills filter by id）。
// Repository.GetBill 强制 ctx tenant，admin 跨租户路径需绕过。
func (h *AdminHandler) findBillByID(ctx context.Context, id string) (BillingRecord, error) {
	list, err := h.repo.ListAllBills(ctx)
	if err != nil {
		return BillingRecord{}, err
	}
	for _, b := range list {
		if b.ID == id {
			return b, nil
		}
	}
	return BillingRecord{}, fmt.Errorf("账单不存在: %s", id)
}

// serveBillDetail 跨租户账单详情。
func (h *AdminHandler) serveBillDetail(w http.ResponseWriter, r *http.Request, id string) {
	b, err := h.findBillByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, b)
}

// servePay 标记账单已付，以目标租户 ctx 执行 PayBill，绕过 prod:write。
func (h *AdminHandler) servePay(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	b, err := h.findBillByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminutil.TenantCtx(r, b.TenantID)
	paid, err := h.repo.PayBill(ctx, id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(rr, b.TenantID, "admin:pay", "bill", id, "标记账单已付")
	httputil.WriteData(w, paid)
}
