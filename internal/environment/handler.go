package environment

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermEnvironmentRead  = "environment:read"
	PermEnvironmentWrite = "environment:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无此权限 -> 生产只读。
	PermProdWrite = "prod:write"
)

// Handler 暴露环境 REST API。
//
//	GET    /api/environments       环境列表（按租户）
//	POST   /api/environments       创建环境
//	GET    /api/environments/{id}  环境详情
//	DELETE /api/environments/{id}  删除环境
type Handler struct {
	repo Repository
	// Authorize 校验当前请求是否持有权限；nil 跳过（测试场景）。
	Authorize func(r *http.Request, perm string) bool
}

// NewHandler 创建环境 handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 路由到列表/创建或单资源操作。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rest := strings.TrimPrefix(r.URL.Path, "/api/environments")
	rest = strings.Trim(rest, "/")

	// GET /api/environments
	if rest == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermEnvironmentRead) {
			return
		}
		list, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}

	// POST /api/environments
	if rest == "" && r.Method == http.MethodPost {
		if !h.allow(w, r, PermEnvironmentWrite) {
			return
		}
		var e Environment
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// id 兜底：客户端未提供时生成（与 application/workload 同款），避免空 id 入库。
		if e.ID == "" {
			e.ID = fmt.Sprintf("env-%d", time.Now().UnixNano())
		}
		if err := e.Validate(); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		// 生产环境写操作需 prod:write（developer 被拦，生产只读）
		if e.Type == TypeProd && !h.allow(w, r, PermProdWrite) {
			return
		}
		if err := h.repo.Create(r.Context(), e); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": e})
		return
	}

	id := strings.Split(rest, "/")[0]

	// GET /api/environments/{id}
	if r.Method == http.MethodGet && id != "" {
		if !h.allow(w, r, PermEnvironmentRead) {
			return
		}
		e, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(e)
		return
	}

	// DELETE /api/environments/{id}
	if r.Method == http.MethodDelete && id != "" {
		if !h.allow(w, r, PermEnvironmentWrite) {
			return
		}
		// 生产环境删除需 prod:write（developer 被拦）。
		// fail-closed：EnvType 查不到（不存在/跨租户）保守按生产处理，需 prod:write。
		etype, err := h.repo.EnvType(r.Context(), id)
		if err != nil || etype == TypeProd {
			if !h.allow(w, r, PermProdWrite) {
				return
			}
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httputil.WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
		return
	}

	httputil.WriteError(w, http.StatusNotFound, "not found")
}
