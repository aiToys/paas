package security

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermSecurityRead  = "security:read"
	PermSecurityWrite = "security:write"
)

// Handler 暴露安全 REST API（密钥/证书 + 审计日志）。
//
// 路由：
//
//	GET    /api/security/secrets             密钥列表（掩码）
//	POST   /api/security/secrets             创建密钥（记审计）
//	DELETE /api/security/secrets/{id}        删除密钥（记审计）
//	GET    /api/security/audit-logs?resourceType=&action=  审计日志
type Handler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
	// UserIDFrom 从身份 ctx 取用户 ID（填审计 actor）；nil 则空。
	UserIDFrom func(ctx context.Context) string
	// IsAdmin 判断调用方是否平台管理员。平台级 Secret 写操作（Create/Delete scope=platform）
	// 仅 admin 可执行；nil 视为非管理员（fail-closed）。由 main.go 基于角色注入。
	IsAdmin func(r *http.Request) bool
}

// NewHandler 创建安全 handler。
func NewHandler(repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithUserIDFrom 注入用户 ID 解析器，填充审计 actor。
func WithUserIDFrom(f func(context.Context) string) HandlerOpt {
	return func(h *Handler) { h.UserIDFrom = f }
}

// WithIsAdmin 注入平台管理员判定（平台级 Secret 写操作仅 admin）。
func WithIsAdmin(f func(*http.Request) bool) HandlerOpt {
	return func(h *Handler) { h.IsAdmin = f }
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	writeErr(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// adminOnly 校验平台管理员权限（平台级 Secret 写操作）。fail-closed：IsAdmin 为 nil 或返回 false → 403。
func (h *Handler) adminOnly(w http.ResponseWriter, r *http.Request) bool {
	if h.IsAdmin != nil && h.IsAdmin(r) {
		return true
	}
	writeErr(w, http.StatusForbidden, "forbidden: platform admin only")
	return false
}

func (h *Handler) actor(ctx context.Context) string {
	if h.UserIDFrom == nil {
		return ""
	}
	return h.UserIDFrom(ctx)
}

// record 写操作审计（失败不阻塞主流程，仅记录）。
func (h *Handler) record(ctx context.Context, action, resourceID, detail string) {
	_ = h.repo.RecordAudit(ctx, AuditLog{
		Actor:        h.actor(ctx),
		Action:       action,
		ResourceType: ResourceSecret,
		ResourceID:   resourceID,
		Detail:       detail,
	})
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/security/secrets":
		h.serveSecretCollection(w, r)
	case strings.HasPrefix(path, "/api/security/secrets/"):
		h.serveSecretItem(w, r)
	case path == "/api/security/audit-logs":
		h.serveAudit(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveSecretCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermSecurityRead) {
			return
		}
		list, err := h.repo.ListSecrets(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermSecurityWrite) {
			return
		}
		var sec Secret
		if err := json.NewDecoder(r.Body).Decode(&sec); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 平台级 Secret（如供应商凭证）仅平台管理员可创建。
		if sec.Scope == ScopePlatform && !h.adminOnly(w, r) {
			return
		}
		saved, err := h.repo.CreateSecret(r.Context(), sec)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// 记审计（创建成功）
		h.record(r.Context(), ActionCreate, saved.ID, "创建密钥 "+sec.Name)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(saved)
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveSecretItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermSecurityWrite) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/security/secrets/")
	id = strings.TrimRight(id, "/")
	if id == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	// 先取（确认存在 + 拿 name 记审计）；跨租户/不存在走 not found 不泄漏
	sec, err := h.repo.GetSecret(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	// 平台级 Secret 仅平台管理员可删。
	if sec.Scope == ScopePlatform && !h.adminOnly(w, r) {
		return
	}
	if err := h.repo.DeleteSecret(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	h.record(r.Context(), ActionDelete, id, "删除密钥 "+sec.Name)
	_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
}

func (h *Handler) serveAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermSecurityRead) {
		return
	}
	q := r.URL.Query()
	list, err := h.repo.ListAuditLogs(r.Context(), q.Get("resourceType"), q.Get("action"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
