package dataservice

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// EngineHandler 暴露引擎目录 REST API。
//
// 路由：
//
//	GET    /api/engines            用户：列出 enabled 引擎（创建表单用，按 kind 分组）
//	GET    /api/admin/engines      admin：列出全部引擎（含 disabled）
//	POST   /api/admin/engines      创建引擎（super_admin）
//	PUT    /api/admin/engines/{id} 更新引擎（super_admin）
//	DELETE /api/admin/engines/{id} 删除引擎（super_admin）
type EngineHandler struct {
	repo      EngineRepository
	Authorize func(r *http.Request, perm string) bool
	audit     AdminAuditRecorder // admin 写操作审计（平台级引擎目录变更）
	actorOf   func(*http.Request) string
}

// NewEngineHandler 创建引擎 handler。
func NewEngineHandler(repo EngineRepository) *EngineHandler {
	return &EngineHandler{repo: repo}
}

// SetAdminAudit 注入审计 recorder（admin 引擎写操作记审计，平台级合规「审计只增不删」）。
// 复用 dataservice.AdminAuditRecorder 接口（同包），与 admin_handler 同源。
func (h *EngineHandler) SetAdminAudit(a AdminAuditRecorder) *EngineHandler { h.audit = a; return h }

// SetAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func (h *EngineHandler) SetAdminActor(f func(*http.Request) string) *EngineHandler {
	h.actorOf = f
	return h
}

// recordAudit best-effort 记审计（平台级引擎 tenantID=""，identityAuditAdapter 转 "platform" 落库）。
func (h *EngineHandler) recordAudit(r *http.Request, action, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	actor := "admin"
	if h.actorOf != nil {
		actor = h.actorOf(r)
	}
	_ = h.audit.Record(r.Context(), "", actor, action, "engine", resourceID, detail)
}

func (h *EngineHandler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 按路径分发。/api/engines（用户只读 enabled）vs /api/admin/engines（admin CRUD）。
func (h *EngineHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/engines":
		// 用户创建表单用：仅 enabled，全租户可读（已认证即可，无 perm 校验——引擎目录是公共配置）。
		h.servePublicList(w, r)
	case path == "/api/admin/engines":
		h.serveAdminCollection(w, r)
	case strings.HasPrefix(path, "/api/admin/engines/"):
		h.serveAdminItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// servePublicList 返回 enabled 引擎（用户创建表单下拉用）。
func (h *EngineHandler) servePublicList(w http.ResponseWriter, r *http.Request) {
	all, err := h.repo.ListEngines(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	out := make([]Engine, 0, len(all))
	for _, e := range all {
		if e.Enabled {
			// external-shared 连接含共享集群凭证，对用户列表掩码（用户实例创建后从 connection 派生，
			// 但目录浏览不暴露集群凭证细节；admin 页才看明文）。
			e.Connection = MaskConnection(e.Connection)
			out = append(out, e)
		}
	}
	httputil.WriteData(w, out)
}

func (h *EngineHandler) serveAdminCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		all, err := h.repo.ListEngines(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, all)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermDataServiceWrite) { // admin 引擎写复用 dataservice:write（super_admin 经 adminGuard 兜底）
			return
		}
		var e Engine
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.CreateEngine(r.Context(), e)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		h.recordAudit(r, "admin:create", saved.ID, "创建引擎")
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *EngineHandler) serveAdminItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/engines/")
	id = strings.TrimRight(id, "/")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		var e Engine
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		e.ID = id
		saved, err := h.repo.UpdateEngine(r.Context(), e)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		h.recordAudit(r, "admin:update", id, "更新引擎")
		httputil.WriteData(w, saved)
	case http.MethodDelete:
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		if err := h.repo.DeleteEngine(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.recordAudit(r, "admin:delete", id, "删除引擎")
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
