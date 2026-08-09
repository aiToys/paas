package configcenter

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（复用 governance 切片已加入 BuiltinRoles 的权限）。
// 配置中心属治理四件套，独立于物理环境，不接入 prod:write。
const (
	PermConfigCenterRead  = "governance:read"
	PermConfigCenterWrite = "governance:write"
)

// Handler 暴露配置中心 REST API。
//
// 路由：
//
//	GET    /api/configcenter/namespaces                  命名空间列表
//	POST   /api/configcenter/namespaces                  创建
//	GET    /api/configcenter/namespaces/{id}             详情
//	DELETE /api/configcenter/namespaces/{id}             删除（级联）
//	GET    /api/configcenter/namespaces/{id}/items       配置项列表（draft）
//	POST   /api/configcenter/namespaces/{id}/items       upsert 配置项
//	DELETE /api/configcenter/namespaces/{id}/items/{iid} 删除配置项
//	POST   /api/configcenter/namespaces/{id}/publish     发布
//	GET    /api/configcenter/namespaces/{id}/publishes   发布历史
//	GET    /api/configcenter/namespaces/{id}/published   客户端发现（active 快照）
//	POST   /api/configcenter/publishes/{pid}/rollback    回滚
type Handler struct {
	repo          Repository
	Authorize     func(r *http.Request, perm string) bool
	serviceLookup ServiceLookup // 可选，CreateNamespace 时校验 ServiceID 归属（防悬挂引用）
}

// NewHandler 创建配置中心 handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithServiceLookup 注入 governance Service 存在性校验器（依赖倒置）。
// 非空时 CreateNamespace 的 ServiceID 需存在且属本租户，防悬挂引用脏数据。
func (h *Handler) WithServiceLookup(sl ServiceLookup) *Handler {
	h.serviceLookup = sl
	return h
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/configcenter/namespaces":
		h.serveNamespaceCollection(w, r)
	case strings.HasPrefix(path, "/api/configcenter/namespaces/"):
		h.serveNamespaceItem(w, r)
	case strings.HasPrefix(path, "/api/configcenter/publishes/"):
		h.servePublishAction(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveNamespaceCollection GET 列表 / POST 创建。
func (h *Handler) serveNamespaceCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		list, err := h.repo.ListNamespaces(r.Context(), r.URL.Query().Get("serviceId"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		var n Namespace
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// ServiceID 非空时校验关联服务存在 + 属本租户，防悬挂引用（typo/已删/跨租户脏数据）。
		if n.ServiceID != "" && h.serviceLookup != nil {
			ok, lerr := h.serviceLookup.ServiceExists(r.Context(), n.ServiceID)
			if lerr != nil {
				httputil.WriteInternalError(w, lerr)
				return
			}
			if !ok {
				httputil.WriteError(w, http.StatusBadRequest, "关联服务不存在: "+n.ServiceID)
				return
			}
		}
		saved, err := h.repo.CreateNamespace(r.Context(), n)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveNamespaceItem 处理 namespaces/{id}[/{sub}[/{iid}]]。
func (h *Handler) serveNamespaceItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/configcenter/namespaces/")
	rest = strings.TrimRight(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	nsID := parts[0]
	switch len(parts) {
	case 1:
		h.serveNamespaceGetDelete(w, r, nsID)
	case 2:
		switch parts[1] {
		case "items":
			h.serveItemCollection(w, r, nsID)
		case "publish":
			h.servePublish(w, r, nsID)
		case "publishes":
			h.servePublishHistory(w, r, nsID)
		case "published":
			h.servePublished(w, r, nsID)
		default:
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	case 3:
		if parts[1] == "items" {
			h.serveItemDelete(w, r, nsID, parts[2])
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveNamespaceGetDelete(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		n, err := h.repo.GetNamespace(r.Context(), nsID)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, n)
		return
	}
	if r.Method == http.MethodDelete {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		if err := h.repo.DeleteNamespace(r.Context(), nsID); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": nsID})
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveItemCollection GET items / POST upsert item。
func (h *Handler) serveItemCollection(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		list, err := h.repo.ListItems(r.Context(), nsID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		var item ConfigItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		item.NamespaceID = nsID
		saved, err := h.repo.UpsertItem(r.Context(), item)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItemDelete(w http.ResponseWriter, r *http.Request, nsID, itemID string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	// 校验 item 归属该 namespace，防止 DELETE /nsA/items/{item-of-nsB} 跨 ns 越权删除。
	items, err := h.repo.ListItems(r.Context(), nsID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	belongs := false
	for _, it := range items {
		if it.ID == itemID {
			belongs = true
			break
		}
	}
	if !belongs {
		httputil.WriteError(w, http.StatusNotFound, "配置项不存在: "+itemID)
		return
	}
	if err := h.repo.DeleteItem(r.Context(), itemID); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, map[string]string{"deleted": itemID})
}

// servePublish POST 发布（生成版本快照）。
func (h *Handler) servePublish(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	pub, err := h.repo.CreatePublish(r.Context(), nsID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	httputil.WriteDataCreated(w, pub)
}

// servePublishHistory GET 发布历史。
func (h *Handler) servePublishHistory(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	list, err := h.repo.ListPublishes(r.Context(), nsID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// servePublished GET 客户端发现（active 快照 + version）。
func (h *Handler) servePublished(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	active, ok, err := h.repo.ActivePublish(r.Context(), nsID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !ok {
		httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"published": false})
		return
	}
	// 发现协议：保持 {published,version,snapshot,publishId} shape（前端 published.value = await json() 直取），
	// 仅经 httputil 统一编码（Content-Type 显式）。非标准 {data:T}，因属数据面客户端发现契约。
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"published": true,
		"version":   active.Version,
		"snapshot":  active.Snapshot,
		"publishId": active.ID,
	})
}

// servePublishAction POST /api/configcenter/publishes/{pid}/rollback 回滚。
func (h *Handler) servePublishAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/configcenter/publishes/")
	parts := strings.Split(strings.TrimRight(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "rollback" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	rb, err := h.repo.RollbackPublish(r.Context(), parts[0])
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	httputil.WriteData(w, rb)
}
