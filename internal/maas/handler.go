package maas

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/provider"
)

// Handler 暴露模型管理 REST API（平台级，super_admin 由 cmd/core adminGuard 兜底，handler 内不判权限）。
//
// 路由：
//
//	GET    /api/admin/models                列表（含通道）
//	POST   /api/admin/models                创建模型（id 必填，标量；channels 经通道 API 单独建）
//	GET    /api/admin/models/{id}           详情（含通道）
//	PUT    /api/admin/models/{id}           更新模型标量（channels 不变）
//	DELETE /api/admin/models/{id}           删除模型（级联清通道 + 从 gateway 注销）
//	GET    /api/admin/models/{id}/channels  通道列表
//	POST   /api/admin/models/{id}/channels  创建通道（+ 刷新 gateway）
//	PUT    /api/admin/models/{id}/channels/{cid}   更新通道（+ 刷新 gateway）
//	DELETE /api/admin/models/{id}/channels/{cid}   删除通道（+ 刷新 gateway）
type Handler struct {
	repo     Repository
	gw       provider.GatewayRegistrar // CRUD 后刷新路由表（同 ID 覆盖/注销）；nil 时跳过刷新
	resolver provider.CredentialResolver
}

// NewHandler 创建模型管理 handler。gw 用于 CRUD 后增量刷新 gateway 路由表。
func NewHandler(repo Repository, gw provider.GatewayRegistrar, resolver provider.CredentialResolver) *Handler {
	return &Handler{repo: repo, gw: gw, resolver: resolver}
}

// ServeHTTP 按路径分发 model / channel 子资源。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/admin/models")
	if rest == "" {
		h.serveModels(w, r)
		return
	}
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		h.serveModels(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	modelID := parts[0]
	if modelID == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if len(parts) == 1 {
		h.serveModel(w, r, modelID)
		return
	}
	if parts[1] == "channels" {
		if len(parts) == 2 {
			h.serveChannels(w, r, modelID)
			return
		}
		h.serveChannel(w, r, modelID, parts[2])
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// reloadModel 从 store 重载模型，BuildProvider+SetImpl 重建通道 impl，RegisterModel 覆盖刷新 gateway。
// gw 为 nil 时跳过（仅 store 操作）。写操作成功后调用，保证路由表与存储一致。
func (h *Handler) reloadModel(w http.ResponseWriter, r *http.Request, modelID string) bool {
	if h.gw == nil {
		return true // 无 gateway（如纯 store 测试），跳过刷新
	}
	m, err := h.repo.GetModel(r.Context(), modelID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return false
	}
	for _, c := range m.Channels {
		c.SetImpl(BuildProvider(c, h.resolver))
	}
	if err := h.gw.RegisterModel(m); err != nil {
		httputil.WriteInternalError(w, err)
		return false
	}
	return true
}

func (h *Handler) serveModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := h.repo.ListModels(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": list})
	case http.MethodPost:
		var m provider.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if m.ID == "" {
			httputil.WriteError(w, http.StatusBadRequest, "model id 不能为空")
			return
		}
		if err := h.repo.CreateModel(r.Context(), &m); err != nil {
			writeMaasErr(w, err)
			return
		}
		// 新建模型通常无通道，但若有（一次性创建带通道）也刷新。
		if !h.reloadModel(w, r, m.ID) {
			return
		}
		saved, _ := h.repo.GetModel(r.Context(), m.ID)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(saved)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveModel(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		m, err := h.repo.GetModel(r.Context(), id)
		if err != nil {
			writeMaasErr(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(m)
	case http.MethodPut:
		var m provider.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		m.ID = id
		if err := h.repo.UpdateModel(r.Context(), &m); err != nil {
			writeMaasErr(w, err)
			return
		}
		if !h.reloadModel(w, r, id) {
			return
		}
		saved, _ := h.repo.GetModel(r.Context(), id)
		_ = json.NewEncoder(w).Encode(saved)
	case http.MethodDelete:
		if err := h.repo.DeleteModel(r.Context(), id); err != nil {
			writeMaasErr(w, err)
			return
		}
		if h.gw != nil {
			h.gw.UnregisterModel(id)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveChannels(w http.ResponseWriter, r *http.Request, modelID string) {
	switch r.Method {
	case http.MethodGet:
		list, err := h.repo.ListChannels(r.Context(), modelID)
		if err != nil {
			writeMaasErr(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": list})
	case http.MethodPost:
		var c provider.Channel
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if c.ID == "" {
			httputil.WriteError(w, http.StatusBadRequest, "channel id 不能为空")
			return
		}
		if c.Status == "" {
			c.Status = provider.StatusHealthy
		}
		if err := h.repo.CreateChannel(r.Context(), modelID, &c); err != nil {
			writeMaasErr(w, err)
			return
		}
		if !h.reloadModel(w, r, modelID) {
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(c)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveChannel(w http.ResponseWriter, r *http.Request, modelID, channelID string) {
	switch r.Method {
	case http.MethodPut:
		var c provider.Channel
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		c.ID = channelID
		if err := h.repo.UpdateChannel(r.Context(), modelID, &c); err != nil {
			writeMaasErr(w, err)
			return
		}
		if !h.reloadModel(w, r, modelID) {
			return
		}
		_ = json.NewEncoder(w).Encode(c)
	case http.MethodDelete:
		if err := h.repo.DeleteChannel(r.Context(), modelID, channelID); err != nil {
			writeMaasErr(w, err)
			return
		}
		if !h.reloadModel(w, r, modelID) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"deleted": channelID})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// writeMaasErr 把仓储错误映射为 HTTP 状态：not found→404，exists→409，其余→400。
func writeMaasErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrModelNotFound), errors.Is(err, ErrChannelNotFound):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrModelExists), errors.Is(err, ErrChannelExists):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	default:
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	}
}
