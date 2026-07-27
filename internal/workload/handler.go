package workload

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermWorkloadRead  = "workload:read"
	PermWorkloadWrite = "workload:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无此权限 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：workload 不直接 import environment，由 cmd/core 注入实现。
type EnvTypeResolver interface {
	EnvType(ctx context.Context, envID string) (string, error)
}

// Handler 暴露工作负载 REST API。
// 路由：
//
//	GET    /api/applications/{id}/workloads   应用下工作负载
//	POST   /api/applications/{id}/workloads   创建
//	GET    /api/workloads?type=               跨应用列表（按类型）
//	PUT    /api/workloads/{id}                扩缩容/状态
//	DELETE /api/workloads/{id}                删除
type Handler struct {
	repo        Repository
	envResolver EnvTypeResolver // 可选；注入后写操作校验生产权限
	// Authorize 校验当前请求是否持有权限；nil 跳过（测试场景）。
	Authorize func(r *http.Request, perm string) bool
}

// NewHandler 创建工作负载 handler。可选 envResolver 注入启用生产写校验。
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

// allowProd 校验目标环境的生产写权限。
// 未注入 envResolver 或 envID 为空时跳过（兼容旧测试）；
// 目标环境非生产或不存在时放行（不存在由后续 repo 报错）；
// 目标环境是生产时校验 prod:write（developer 被拦 -> 生产只读）。
func (h *Handler) allowProd(w http.ResponseWriter, r *http.Request, envID string) bool {
	if h.envResolver == nil || envID == "" {
		return true
	}
	etype, err := h.envResolver.EnvType(r.Context(), envID)
	if err != nil || etype != "prod" {
		return true
	}
	return h.allow(w, r, PermProdWrite)
}

// ServeHTTP 按路径前缀分发到应用子路由或跨应用工作负载路由。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/applications/"):
		h.serveApp(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/workloads"):
		h.serveCross(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// serveApp 处理 /api/applications/{id}/workloads。
func (h *Handler) serveApp(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "workloads" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]

	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermWorkloadRead) {
			return
		}
		envID := r.URL.Query().Get("envId")
		list, err := h.repo.List(r.Context(), envID, appID, "")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}

	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermWorkloadWrite) {
			return
		}
		var w0 Workload
		if err := json.NewDecoder(r.Body).Decode(&w0); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		w0.AppID = appID // 以路径为准
		if err := w0.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// 生产环境创建工作负载需 prod:write（developer 被拦，生产只读）
		if !h.allowProd(w, r, w0.EnvID) {
			return
		}
		if err := h.repo.Create(r.Context(), w0); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(w0)
		return
	}

	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveCross 处理 /api/workloads 与 /api/workloads/{id}。
func (h *Handler) serveCross(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/workloads")
	rest = strings.Trim(rest, "/")

	// GET /api/workloads?type=
	if rest == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermWorkloadRead) {
			return
		}
		wtype := r.URL.Query().Get("type")
		envID := r.URL.Query().Get("envId")
		list, err := h.repo.List(r.Context(), envID, "", wtype)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}

	id := strings.Split(rest, "/")[0]

	if r.Method == http.MethodPut && id != "" {
		if !h.allow(w, r, PermWorkloadWrite) {
			return
		}
		// 生产环境扩缩容需 prod:write（先查 workload 所属环境）
		if existing, err := h.repo.Get(r.Context(), id); err == nil {
			if !h.allowProd(w, r, existing.EnvID) {
				return
			}
		}
		var body struct {
			Replicas int    `json:"replicas"`
			Status   string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		w0, err := h.repo.Update(r.Context(), id, body.Replicas, body.Status)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(w0)
		return
	}

	if r.Method == http.MethodDelete && id != "" {
		if !h.allow(w, r, PermWorkloadWrite) {
			return
		}
		// 生产环境删除需 prod:write（先查 workload 所属环境）
		if existing, err := h.repo.Get(r.Context(), id); err == nil {
			if !h.allowProd(w, r, existing.EnvID) {
				return
			}
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
		return
	}

	writeErr(w, http.StatusNotFound, "not found")
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
