package dataservice

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermDataServiceRead  = "dataservice:read"
	PermDataServiceWrite = "dataservice:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：dataservice 不直接 import environment，由 cmd/core 注入。
type EnvTypeResolver interface {
	EnvType(ctx context.Context, envID string) (string, error)
}

// Handler 暴露数据服务 REST API。
//
// 路由：
//
//	GET    /api/dataservices?kind=          列表（按 kind 过滤）
//	GET    /api/dataservices/meta            Kind 元数据（前端表单）
//	POST   /api/dataservices                 创建（生产需 prod:write）
//	GET    /api/dataservices/{id}            详情
//	PUT    /api/dataservices/{id}            更新 spec/status（生产需 prod:write）
//	DELETE /api/dataservices/{id}            删除（生产需 prod:write）
type Handler struct {
	repo        Repository
	envResolver EnvTypeResolver
	Authorize   func(r *http.Request, perm string) bool
}

// NewHandler 创建数据服务 handler。
func NewHandler(repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithEnvResolver 注入环境类型解析器，启用生产写权限校验。
func WithEnvResolver(r EnvTypeResolver) HandlerOpt {
	return func(h *Handler) { h.envResolver = r }
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// allowProd 校验目标环境的生产写权限（未注入 resolver/envID 空时跳过；非生产放行）。
func (h *Handler) allowProd(w http.ResponseWriter, r *http.Request, envID string) bool {
	if h.envResolver == nil || envID == "" {
		return true
	}
	etype, err := h.envResolver.EnvType(r.Context(), envID)
	// fail-closed：环境查不到（不存在/跨租户）保守按生产处理，需 prod:write。
	if err != nil || etype == "prod" {
		return h.allow(w, r, PermProdWrite)
	}
	return true
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/dataservices":
		h.serveCollection(w, r)
	case path == "/api/dataservices/meta":
		httputil.WriteData(w, KindMetas)
	case strings.HasPrefix(path, "/api/dataservices/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermDataServiceRead) {
			return
		}
		list, err := h.repo.List(r.Context(), r.URL.Query().Get("kind"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		// list 掩码敏感字段（password/secretKey/token）；详情才返明文（read 权限者）。
		for i := range list {
			list[i].Connection = MaskConnection(list[i].Connection)
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		var d DataService
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 强制平台生成凭证（防用户传弱密码/自定义 password），FillConnection 重生。
		d.Connection = nil
		// 生产环境创建需 prod:write（developer 被拦，生产只读）
		if !h.allowProd(w, r, d.EnvID) {
			return
		}
		saved, err := h.repo.Create(r.Context(), d)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		saved.Connection = MaskConnection(saved.Connection) // 返回前掩码（与 List/Detail 一致，明文仅内部绑定注入用）
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/dataservices/")
	id = strings.TrimRight(id, "/")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermDataServiceRead) {
			return
		}
		d, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		// detail 也掩码敏感字段（与 security 模块一致：read 权限者含 viewer 不可见明文；
		// 应用绑定经 repo.Get 拿明文注入，不受 handler 掩码影响）。
		d.Connection = MaskConnection(d.Connection)
		httputil.WriteData(w, d)
	case http.MethodPut:
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		// 先取现有（确认存在 + 拿 envID 校验生产权限）
		ex, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, ex.EnvID) {
			return
		}
		var d DataService
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		d.ID = id
		updated, err := h.repo.Update(r.Context(), d)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		updated.Connection = MaskConnection(updated.Connection) // 返回前掩码
		httputil.WriteData(w, updated)
	case http.MethodDelete:
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		ex, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, ex.EnvID) {
			return
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
