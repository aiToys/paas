package billing

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermBillingRead  = "billing:read"
	PermBillingWrite = "billing:write"
)

// Handler 暴露配额计费 REST API（配额 + 用量 + 账单）。
//
// 路由：
//
//	GET  /api/billing/quota                        读取配额
//	PUT  /api/billing/quota                        更新配额（billing:write）
//	GET  /api/billing/usage                        用量 + 配额 + 超限标记（UsageView）
//	GET  /api/billing/records                      账单列表（倒序）
//	POST /api/billing/records/generate?period=     生成本期账单（billing:write）
//	POST /api/billing/records/{id}/pay             支付账单（billing:write）
type Handler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
	audit     AdminAuditRecorder // 租户侧写操作审计（SetQuota/GenerateBill/PayBill 敏感财务写）
	actor     func(*http.Request) string
}

// NewHandler 创建配额计费 handler。
func NewHandler(repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithAudit 注入审计 recorder（租户侧写操作记审计）。
func WithAudit(a AdminAuditRecorder) HandlerOpt {
	return func(h *Handler) { h.audit = a }
}

// WithActor 注入 actor 提取器（审计 actor 字段）。
func WithActor(f func(*http.Request) string) HandlerOpt {
	return func(h *Handler) { h.actor = f }
}

// recordAudit best-effort 记审计（错误不影响主流程）。tenant 取 ctx，缺失归 "platform"。
func (h *Handler) recordAudit(r *http.Request, action, resourceType, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	tid, ok := tenant.TenantFrom(r.Context())
	if !ok || tid == "" {
		tid = "platform"
	}
	actor := ""
	if h.actor != nil {
		actor = h.actor(r)
	}
	_ = h.audit.Record(r.Context(), tid, actor, action, resourceType, resourceID, detail)
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/billing/quota":
		h.serveQuota(w, r)
	case path == "/api/billing/usage":
		h.serveUsage(w, r)
	case path == "/api/billing/records":
		h.serveRecords(w, r)
	case path == "/api/billing/records/generate":
		h.serveGenerate(w, r)
	case strings.HasPrefix(path, "/api/billing/records/") && strings.HasSuffix(path, "/pay"):
		h.servePay(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveQuota(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermBillingRead) {
			return
		}
		q, err := h.repo.GetQuota(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, q)
	case http.MethodPut:
		if !h.allow(w, r, PermBillingWrite) {
			return
		}
		var q ResourceQuota
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.SetQuota(r.Context(), q)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		h.recordAudit(r, "set_quota", "quota", "", "调整配额")
		httputil.WriteData(w, saved)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBillingRead) {
		return
	}
	q, err := h.repo.GetQuota(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	u, err := h.repo.GetUsage(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, BuildUsageView(q, u))
}

func (h *Handler) serveRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBillingRead) {
		return
	}
	list, err := h.repo.ListBills(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

func (h *Handler) serveGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBillingWrite) {
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = currentPeriod()
	}
	rec, err := h.repo.GenerateBill(r.Context(), period)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, "generate_bill", "bill", rec.ID, "生成账单 "+period)
	httputil.WriteDataCreated(w, rec)
}

func (h *Handler) servePay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBillingWrite) {
		return
	}
	// /api/billing/records/{id}/pay -> 取中间 id 段
	rest := strings.TrimPrefix(r.URL.Path, "/api/billing/records/")
	id := strings.TrimSuffix(rest, "/pay")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	rec, err := h.repo.PayBill(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, "pay_bill", "bill", id, "支付账单 "+id)
	httputil.WriteData(w, rec)
}

// currentPeriod 返回当前 YYYY-MM。
// 注意：此处用 time.Now 仅在 handler 请求路径调用（非内存 Store 的可恢复路径），可接受。
func currentPeriod() string {
	t := time.Now()
	return t.Format("2006-01")
}
