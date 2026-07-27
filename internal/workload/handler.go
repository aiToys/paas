package workload

import (
	"encoding/json"
	"net/http"
	"strings"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermWorkloadRead  = "workload:read"
	PermWorkloadWrite = "workload:write"
)

// Handler 暴露工作负载 REST API。
// 路由：
//
//	GET    /api/applications/{id}/workloads   应用下工作负载
//	POST   /api/applications/{id}/workloads   创建
//	GET    /api/workloads?type=               跨应用列表（按类型）
//	PUT    /api/workloads/{id}                扩缩容/状态
//	DELETE /api/workloads/{id}                删除
type Handler struct {
	repo Repository
	// Authorize 校验当前请求是否持有权限；nil 跳过（测试场景）。
	Authorize func(r *http.Request, perm string) bool
}

// NewHandler 创建工作负载 handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	writeErr(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
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
