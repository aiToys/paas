package configcenter

import (
	"encoding/json"
	"net/http"
	"strings"
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
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
}

// NewHandler 创建配置中心 handler。
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

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/configcenter/namespaces":
		h.serveNamespaceCollection(w, r)
	case strings.HasPrefix(path, "/api/configcenter/namespaces/"):
		h.serveNamespaceItem(w, r)
	case strings.HasPrefix(path, "/api/configcenter/publishes/"):
		h.servePublishAction(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// serveNamespaceCollection GET 列表 / POST 创建。
func (h *Handler) serveNamespaceCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		list, err := h.repo.ListNamespaces(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		var n Namespace
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.CreateNamespace(r.Context(), n)
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

// serveNamespaceItem 处理 namespaces/{id}[/{sub}[/{iid}]]。
func (h *Handler) serveNamespaceItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/configcenter/namespaces/")
	rest = strings.TrimRight(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErr(w, http.StatusNotFound, "not found")
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
			writeErr(w, http.StatusNotFound, "not found")
		}
	case 3:
		if parts[1] == "items" {
			h.serveItemDelete(w, r, nsID, parts[2])
		} else {
			writeErr(w, http.StatusNotFound, "not found")
		}
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveNamespaceGetDelete(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		n, err := h.repo.GetNamespace(r.Context(), nsID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(n)
		return
	}
	if r.Method == http.MethodDelete {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		if err := h.repo.DeleteNamespace(r.Context(), nsID); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"deleted": nsID})
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveItemCollection GET items / POST upsert item。
func (h *Handler) serveItemCollection(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		list, err := h.repo.ListItems(r.Context(), nsID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		var item ConfigItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		item.NamespaceID = nsID
		saved, err := h.repo.UpsertItem(r.Context(), item)
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

func (h *Handler) serveItemDelete(w http.ResponseWriter, r *http.Request, nsID, itemID string) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	// 校验 item 归属该 namespace，防止 DELETE /nsA/items/{item-of-nsB} 跨 ns 越权删除。
	items, err := h.repo.ListItems(r.Context(), nsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
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
		writeErr(w, http.StatusNotFound, "配置项不存在: "+itemID)
		return
	}
	if err := h.repo.DeleteItem(r.Context(), itemID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"deleted": itemID})
}

// servePublish POST 发布（生成版本快照）。
func (h *Handler) servePublish(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	pub, err := h.repo.CreatePublish(r.Context(), nsID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(pub)
}

// servePublishHistory GET 发布历史。
func (h *Handler) servePublishHistory(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	list, err := h.repo.ListPublishes(r.Context(), nsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
}

// servePublished GET 客户端发现（active 快照 + version）。
func (h *Handler) servePublished(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	active, ok, err := h.repo.ActivePublish(r.Context(), nsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"published": false})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"published": true,
		"version":   active.Version,
		"snapshot":  active.Snapshot,
		"publishId": active.ID,
	})
}

// servePublishAction POST /api/configcenter/publishes/{pid}/rollback 回滚。
func (h *Handler) servePublishAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/configcenter/publishes/")
	parts := strings.Split(strings.TrimRight(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "rollback" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	rb, err := h.repo.RollbackPublish(r.Context(), parts[0])
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(rb)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
