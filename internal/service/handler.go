package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermServiceRead  = "service:read"
	PermServiceWrite = "service:write"
)

// AuditRecorder 审计记录器（依赖倒置，避免 service->security 反向依赖）。
// cmd/core 桥接 security.AuditStore 实现注入；未注入则不记审计。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// Handler 暴露应用下服务 REST API（composite 经 /api/applications/{id}/services 分发）。
//
//	GET    /api/applications/{id}/services         服务列表
//	POST   /api/applications/{id}/services         创建服务（201 + data）
//	GET    /api/applications/{id}/services/{sid}   服务详情
//	PUT    /api/applications/{id}/services/{sid}   更新服务
//	DELETE /api/applications/{id}/services/{sid}   删除服务（ack data）
type Handler struct {
	repo      Repository
	auditRec  AuditRecorder
	actorFn   func(*http.Request) string
	Authorize func(r *http.Request, perm string) bool
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithAudit 注入审计记录器（可选，未注入不记审计）。
func WithAudit(a AuditRecorder) HandlerOpt { return func(h *Handler) { h.auditRec = a } }

// WithActor 注入调用者取 ID 函数（审计 actor 用）。
func WithActor(fn func(*http.Request) string) HandlerOpt { return func(h *Handler) { h.actorFn = fn } }

// NewHandler 创建服务 handler。
func NewHandler(repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// actor 取调用者用户 ID（main.go 注入）；未注入返空。
func (h *Handler) actor(r *http.Request) string {
	if h.actorFn == nil {
		return ""
	}
	return h.actorFn(r)
}

// audit 写操作成功后记审计（best-effort，错误不影响主流程；tenant 取 ctx）。
func (h *Handler) audit(r *http.Request, action, resourceID, detail string) {
	if h.auditRec == nil {
		return
	}
	tid, _ := tenant.TenantFrom(r.Context())
	_ = h.auditRec.Record(r.Context(), tid, h.actor(r), action, "service", resourceID, detail)
}

// storeErrStatus 按 sentinel 错误分流 HTTP 状态码：
// ErrExists→409（重名冲突）、ErrNotFound→404、其余（含 ErrInvalid 包装）→400。
func storeErrStatus(err error) int {
	switch {
	case errors.Is(err, ErrExists):
		return http.StatusConflict
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

// ServeHTTP 路由到列表/创建或单资源操作（路径前缀 /api/applications/{id}/services）。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 || parts[1] != "services" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]
	if len(parts) == 2 {
		h.serveCollection(w, r, appID)
		return
	}
	h.serveItem(w, r, appID, parts[2])
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request, appID string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermServiceRead) {
			return
		}
		list, err := h.repo.List(r.Context(), appID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
	case http.MethodPost:
		if !h.allow(w, r, PermServiceWrite) {
			return
		}
		var s Service
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 归属以路径 + ctx 为准（AppID 路径段、TenantID ctx），忽略请求体防越权写。
		s.AppID = appID
		if tid, ok := tenant.TenantFrom(r.Context()); ok {
			s.TenantID = tid
		}
		if s.ID == "" {
			s.ID = fmt.Sprintf("svc-%d", time.Now().UnixNano())
		}
		if err := s.Validate(); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		if err := h.repo.Create(r.Context(), s); err != nil {
			httputil.WriteServiceError(w, storeErrStatus(err), err)
			return
		}
		h.audit(r, "service_create", s.ID, "创建服务 "+s.Name+"（应用 "+appID+"）")
		httputil.WriteDataCreated(w, s)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request, appID, sid string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermServiceRead) {
			return
		}
		s, err := h.repo.Get(r.Context(), appID, sid)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, s)
	case http.MethodPut:
		if !h.allow(w, r, PermServiceWrite) {
			return
		}
		var s Service
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		s.ID = sid
		s.AppID = appID
		s.TenantID = ""
		if err := s.Validate(); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		if err := h.repo.Update(r.Context(), s); err != nil {
			httputil.WriteServiceError(w, storeErrStatus(err), err)
			return
		}
		h.audit(r, "service_update", sid, "更新服务 "+s.Name+"（应用 "+appID+"）")
		httputil.WriteData(w, s)
	case http.MethodDelete:
		if !h.allow(w, r, PermServiceWrite) {
			return
		}
		if err := h.repo.Delete(r.Context(), appID, sid); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.audit(r, "service_delete", sid, "删除服务 "+sid+"（应用 "+appID+"）")
		httputil.WriteData(w, map[string]string{"deleted": sid})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
