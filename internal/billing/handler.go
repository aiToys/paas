package billing

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
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
}

// NewHandler 创建配额计费 handler。
func NewHandler(repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// HandlerOpt 配置 Handler（当前无选项，预留扩展，如单价注入）。
type HandlerOpt func(*Handler)

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	writeErr(w, http.StatusForbidden, "forbidden: missing "+perm)
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
		writeErr(w, http.StatusNotFound, "not found")
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
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(q)
	case http.MethodPut:
		if !h.allow(w, r, PermBillingWrite) {
			return
		}
		var q ResourceQuota
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.SetQuota(r.Context(), q)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(saved)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBillingRead) {
		return
	}
	q, err := h.repo.GetQuota(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u, err := h.repo.GetUsage(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(BuildUsageView(q, u))
}

func (h *Handler) serveRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBillingRead) {
		return
	}
	list, err := h.repo.ListBills(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
}

func (h *Handler) serveGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
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
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(rec)
}

func (h *Handler) servePay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBillingWrite) {
		return
	}
	// /api/billing/records/{id}/pay -> 取中间 id 段
	rest := strings.TrimPrefix(r.URL.Path, "/api/billing/records/")
	id := strings.TrimSuffix(rest, "/pay")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	rec, err := h.repo.PayBill(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(rec)
}

// currentPeriod 返回当前 YYYY-MM。
// 注意：此处用 time.Now 仅在 handler 请求路径调用（非内存 Store 的可恢复路径），可接受。
func currentPeriod() string {
	t := time.Now()
	return t.Format("2006-01")
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
