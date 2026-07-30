package application

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermApplicationRead  = "application:read"
	PermApplicationWrite = "application:write"
	PermBindingWrite     = "binding:write"
)

// ErrQuotaExceededMarker 配额超限的 HTTP 语义：429（资源创建被配额拦截）。
const StatusQuotaExceeded = http.StatusTooManyRequests

// Handler 暴露应用 REST API：列表、详情、创建、绑定资源。
type Handler struct {
	repo Repository
	// Authorize 校验当前请求是否持有权限；nil 时跳过（测试场景）。
	// 由 cmd/core 注入 gateway 的请求级权限校验，避免本包依赖身份/网关实现。
	Authorize func(r *http.Request, perm string) bool
	// QuotaCheck 应用数配额检查（横切，可选）；nil 跳过。由 cmd/core 桥接 billing.CheckAndInc，
	// 创建应用前拦截超配额。返回 error 时 Create 中止并回 429。
	QuotaCheck func(ctx context.Context, delta int) error
}

// NewHandler 创建应用 API handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// allow 统一权限校验：未注入或放行返回 true，否则写 403 返回 false。
func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	writeErr(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 路由到具体方法（Go 1.22 ServeMux 已按方法+路径分发，这里做子路由细分）。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/applications")
	path = strings.Trim(path, "/")

	// GET /api/applications
	if path == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermApplicationRead) {
			return
		}
		apps, err := h.repo.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": apps})
		return
	}

	// POST /api/applications
	if path == "" && r.Method == http.MethodPost {
		if !h.allow(w, r, PermApplicationWrite) {
			return
		}
		var a Application
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 横切配额拦截：创建前检查应用数配额，超限回 429（不创建）。
		if h.QuotaCheck != nil {
			if err := h.QuotaCheck(r.Context(), 1); err != nil {
				writeErr(w, StatusQuotaExceeded, err.Error())
				return
			}
		}
		if err := h.repo.Create(r.Context(), a); err != nil {
			// 配额已递增但 Create 失败 → 回滚（保持用量与实际一致）
			if h.QuotaCheck != nil {
				_ = h.QuotaCheck(r.Context(), -1)
			}
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
		return
	}

	// 剩余: /{id} 或 /{id}/bindings
	parts := strings.Split(path, "/")
	id := parts[0]

	if r.Method == http.MethodGet && len(parts) == 1 {
		if !h.allow(w, r, PermApplicationRead) {
			return
		}
		a, err := h.repo.Get(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(a)
		return
	}

	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "bindings" {
		if !h.allow(w, r, PermBindingWrite) {
			return
		}
		var body struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Type == "" || body.Name == "" {
			writeErr(w, http.StatusBadRequest, "missing type or name")
			return
		}
		a, err := h.repo.BindResource(r.Context(), id, body.Type, body.Name)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
		return
	}

	// DELETE /api/applications/{id}/bindings/{type}/{name}
	if r.Method == http.MethodDelete && len(parts) == 4 && parts[1] == "bindings" {
		if !h.allow(w, r, PermBindingWrite) {
			return
		}
		a, err := h.repo.Unbind(r.Context(), id, parts[2], parts[3])
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(a)
		return
	}

	writeErr(w, http.StatusNotFound, "not found")
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
