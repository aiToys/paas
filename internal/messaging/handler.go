package messaging

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// Handler 是 messaging HTTP 处理器（Topic + 消费组 CRUD）。
// 路由扁平：/api/mq-topics、/api/consumer-groups（避免深嵌套）。
type Handler struct {
	repo Repository
	// Authorize 方法级权限判定（main.go 注入 gateway.RequestAllowed）；nil 则放行。
	Authorize func(r *http.Request, perm string) bool
}

// NewHandler 创建 messaging handler。
func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

func (h *Handler) authorize(r *http.Request, perm string) bool {
	if h.Authorize == nil {
		return true
	}
	return h.Authorize(r, perm)
}

// tenantID 从 ctx 取租户；缺失即 fail-closed 写 401 并返回 ok=false（不信任请求体 TenantID）。
func tenantID(w http.ResponseWriter, r *http.Request) (string, bool) {
	tid, ok := tenant.TenantFrom(r.Context())
	if !ok || tid == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "missing tenant context")
		return "", false
	}
	return tid, true
}

// ServeHTTP 按 path 分发 Topic / ConsumerGroup 的 List/Create/Delete。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/api/mq-topics" && r.Method == http.MethodGet:
		h.listTopics(w, r)
	case p == "/api/mq-topics" && r.Method == http.MethodPost:
		h.createTopic(w, r)
	case strings.HasPrefix(p, "/api/mq-topics/") && r.Method == http.MethodDelete:
		h.deleteTopic(w, r)
	case p == "/api/consumer-groups" && r.Method == http.MethodGet:
		h.listGroups(w, r)
	case p == "/api/consumer-groups" && r.Method == http.MethodPost:
		h.createGroup(w, r)
	case strings.HasPrefix(p, "/api/consumer-groups/") && r.Method == http.MethodDelete:
		h.deleteGroup(w, r)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "不支持的操作")
	}
}

func (h *Handler) listTopics(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:read") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	mqID := r.URL.Query().Get("mqId")
	topics, err := h.repo.ListTopics(r.Context(), tid, mqID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, topics)
}

func (h *Handler) createTopic(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	var t Topic
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	t.TenantID = tid
	if t.MQID == "" || t.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "mqId 与 name 必填")
		return
	}
	if t.ID == "" {
		t.ID = "tp-" + t.Name
	}
	if t.Partitions <= 0 {
		t.Partitions = 1
	}
	if t.Status == "" {
		t.Status = StatusActive
	}
	t.CreatedAt = time.Now()
	if err := h.repo.CreateTopic(r.Context(), t); err != nil {
		httputil.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	httputil.WriteData(w, t)
}

func (h *Handler) deleteTopic(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/mq-topics/")
	if err := h.repo.DeleteTopic(r.Context(), tid, id); err != nil {
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:read") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	topicID := r.URL.Query().Get("topicId")
	groups, err := h.repo.ListConsumerGroups(r.Context(), tid, topicID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, groups)
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	var g ConsumerGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	g.TenantID = tid
	if g.TopicID == "" || g.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "topicId 与 name 必填")
		return
	}
	if g.ID == "" {
		g.ID = "cg-" + g.Name
	}
	if g.Mode == "" {
		g.Mode = ModeClustering
	}
	g.CreatedAt = time.Now()
	if err := h.repo.CreateConsumerGroup(r.Context(), g); err != nil {
		httputil.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	httputil.WriteData(w, g)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/consumer-groups/")
	if err := h.repo.DeleteConsumerGroup(r.Context(), tid, id); err != nil {
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// —— 响应辅助（core 契约 {data:T}/{error:msg}）——
