package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermObservabilityRead  = "observability:read"
	PermObservabilityWrite = "observability:write"
)

// Handler 暴露可观测 REST API。
//
// 路由：
//
//	GET    /api/observability/metrics?targetType=&targetId=&name=  指标时序（惰性补点）
//	GET    /api/observability/alert-rules                           规则列表
//	POST   /api/observability/alert-rules                           创建规则
//	DELETE /api/observability/alert-rules/{id}                      删除规则
//	GET    /api/observability/alerts                                当前告警（即时评估）
//	GET    /api/observability/alerts/events?limit=                 告警历史事件（PG 路径）
//	GET    /api/observability/logs?appId=&level=&q=&limit=         应用日志（惰性补点）
//	GET    /api/observability/traces?appId=&status=&limit=         链路追踪（惰性补点）
type Handler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
	events    EventLister // 可选：告警历史查询（PG 路径注入；nil 时 events 端点 501）
}

// EventLister 告警历史事件查询（pg Store 实现）。
type EventLister interface {
	ListAlertEvents(ctx context.Context, limit int) ([]AlertEvent, error)
}

// NewHandler 创建可观测 handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithEvents 注入告警历史查询（PG 路径；不注入则 history 端点 501 降级）。
func (h *Handler) WithEvents(e EventLister) *Handler { h.events = e; return h }

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/observability/metrics":
		h.serveMetrics(w, r)
	case path == "/api/observability/alert-rules":
		h.serveRuleCollection(w, r)
	case strings.HasPrefix(path, "/api/observability/alert-rules/"):
		h.serveRuleItem(w, r)
	case path == "/api/observability/alerts":
		h.serveAlerts(w, r)
	case path == "/api/observability/alerts/events":
		h.serveAlertEvents(w, r)
	case path == "/api/observability/logs":
		h.serveLogs(w, r)
	case path == "/api/observability/traces":
		h.serveTraces(w, r)
	case strings.HasPrefix(path, "/api/observability/traces/"):
		h.serveTraceItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveTraces 处理 GET /api/observability/traces?appId=&status=&limit=（惰性补点）。
func (h *Handler) serveTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	q := r.URL.Query()
	limit := 0
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	traces, err := h.repo.ListTraces(WithRange(r.Context(), ParseRange(q.Get("range"))), q.Get("appId"), q.Get("status"), limit)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	httputil.WriteData(w, traces)
}

// serveTraceItem 处理 GET /api/observability/traces/{traceID}（traceId 精确直查）。
// 排障入口：日志/告警/响应头拿到 traceId 后直接定位完整链路（含全部 span 树）。
func (h *Handler) serveTraceItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/observability/traces/"), "/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "traceId 不能为空")
		return
	}
	tr, err := h.repo.GetTrace(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, tr)
}

// serveLogs 处理 GET /api/observability/logs?appId=&level=&q=&limit=（惰性补点）。
func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	q := r.URL.Query()
	limit := 0
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	logs, err := h.repo.ListLogs(WithRange(r.Context(), ParseRange(q.Get("range"))), q.Get("appId"), q.Get("targetType"), q.Get("targetId"), q.Get("level"), q.Get("q"), q.Get("lane"), q.Get("traceId"), limit)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	httputil.WriteData(w, logs)
}

func (h *Handler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	q := r.URL.Query()
	list, err := h.repo.ListMetrics(WithRange(r.Context(), ParseRange(q.Get("range"))), q.Get("targetType"), q.Get("targetId"), q.Get("name"))
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

func (h *Handler) serveRuleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermObservabilityRead) {
			return
		}
		list, err := h.repo.ListAlertRules(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		// targetType 过滤（前端按 dataservice 等维度查规则）；空则返全部。
		if tt := r.URL.Query().Get("targetType"); tt != "" {
			filtered := make([]AlertRule, 0, len(list))
			for _, rule := range list {
				if rule.TargetType == tt {
					filtered = append(filtered, rule)
				}
			}
			list = filtered
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermObservabilityWrite) {
			return
		}
		var rule AlertRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.CreateAlertRule(r.Context(), rule)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveRuleItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityWrite) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/observability/alert-rules/")
	id = strings.TrimRight(id, "/")
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if err := h.repo.DeleteAlertRule(r.Context(), id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, map[string]string{"deleted": id})
}

func (h *Handler) serveAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	q := r.URL.Query()
	alerts, err := h.repo.ListAlerts(r.Context(), q.Get("targetType"), q.Get("targetId"))
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, alerts)
}

// serveAlertEvents 告警历史事件（firing/resolved 转变落库，时间倒序）。
// 内存路径未注入 EventLister：501 明示不可用（与降级语义一致，不返假空列表）。
func (h *Handler) serveAlertEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	if h.events == nil {
		httputil.WriteError(w, http.StatusNotImplemented, "告警历史需启用 PG 持久化")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	events, err := h.events.ListAlertEvents(r.Context(), limit)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, events)
}
