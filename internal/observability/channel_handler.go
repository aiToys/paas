package observability

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// 通知渠道路由：
//
//	GET    /api/observability/channels        渠道列表（URL/Headers 掩码）
//	POST   /api/observability/channels        创建渠道
//	PUT    /api/observability/channels/{id}   更新渠道（掩码占位=保留原值）
//	DELETE /api/observability/channels/{id}   删除渠道

// serveChannelCollection 处理渠道集合端点。
func (h *Handler) serveChannelCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermObservabilityRead) {
			return
		}
		list, err := h.repo.ListNotificationChannels(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		masked := make([]NotificationChannel, 0, len(list))
		for _, c := range list {
			masked = append(masked, c.Masked())
		}
		httputil.WriteData(w, masked)
	case http.MethodPost:
		if !h.allow(w, r, PermObservabilityWrite) {
			return
		}
		var ch NotificationChannel
		if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 创建时不允许回传掩码占位（没有「原值」可保留，静默忽略会得到不可投递的渠道）。
		if IsMasked(ch.URL) {
			httputil.WriteError(w, http.StatusBadRequest, "创建渠道时必须提供真实通知地址")
			return
		}
		saved, err := h.repo.CreateNotificationChannel(r.Context(), ch)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		// 渠道决定告警发往何处，写操作全留痕（地址不进审计详情，防明文泄漏）。
		h.recordAudit(r, "create", saved.ID, "创建通知渠道 name="+saved.Name+" kind="+saved.Kind)
		httputil.WriteDataCreated(w, saved.Masked())
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveChannelItem 处理单渠道端点（更新/删除）。
func (h *Handler) serveChannelItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityWrite) {
		return
	}
	id := strings.TrimRight(strings.TrimPrefix(r.URL.Path, "/api/observability/channels/"), "/")
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.repo.DeleteNotificationChannel(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.recordAudit(r, "delete", id, "删除通知渠道")
		httputil.WriteData(w, map[string]string{"deleted": id})
		return
	}
	var ch NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	saved, err := h.repo.UpdateNotificationChannel(r.Context(), id, ch)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, "update", id, "更新通知渠道 name="+saved.Name)
	httputil.WriteData(w, saved.Masked())
}
