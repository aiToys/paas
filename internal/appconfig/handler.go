package appconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermConfigRead  = "config:read"
	PermConfigWrite = "config:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：appconfig 不直接 import environment，由 cmd/core 注入。
type EnvTypeResolver interface {
	EnvType(ctx context.Context, envID string) (string, error)
}

// Handler 暴露应用配置 REST API。
//
// 路由：
//
//	GET    /api/applications/{id}/configs?envId=  配置列表（Secret 掩码）
//	POST   /api/applications/{id}/configs         新增/更新配置项（生产需 prod:write）
//	DELETE /api/applications/{id}/configs/{cfgId} 删除（生产需 prod:write）
type Handler struct {
	repo        Repository
	envResolver EnvTypeResolver
	Authorize   func(r *http.Request, perm string) bool
}

// NewHandler 创建应用配置 handler。
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
	writeErr(w, http.StatusForbidden, "forbidden: missing "+perm)
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

// ServeHTTP 处理 /api/applications/{id}/configs[/{cfgId}]。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 || parts[1] != "configs" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]
	switch len(parts) {
	case 2:
		h.serveCollection(w, r, appID)
	case 3:
		h.serveItem(w, r, appID, parts[2])
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigRead) {
			return
		}
		envID := r.URL.Query().Get("envId")
		list, err := h.repo.List(r.Context(), appID, envID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermConfigWrite) {
			return
		}
		var item ConfigItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		item.AppID = appID
		if item.EnvID == "" {
			writeErr(w, http.StatusBadRequest, "envId 必填")
			return
		}
		// 生产环境改配置需 prod:write（developer 被拦，生产只读）
		if !h.allowProd(w, r, item.EnvID) {
			return
		}
		saved, err := h.repo.Upsert(r.Context(), item)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(saved)
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request, appID, cfgID string) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigWrite) {
		return
	}
	// 校验配置归属该 app + 取 EnvID 校验生产权限。
	// 必须确认 cfgID 属于 appID，否则会跨应用越权删除同租户其它应用配置。
	list, err := h.repo.List(r.Context(), appID, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	belongs := false
	for _, c := range list {
		if c.ID == cfgID {
			belongs = true
			if !h.allowProd(w, r, c.EnvID) {
				return
			}
			break
		}
	}
	if !belongs {
		writeErr(w, http.StatusNotFound, "配置不存在: "+cfgID)
		return
	}
	if err := h.repo.Delete(r.Context(), cfgID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"deleted": cfgID})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
