package observability

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
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
//	GET    /api/observability/logs?appId=&level=&q=&limit=         应用日志（惰性补点）
//	GET    /api/observability/traces?appId=&status=&limit=         链路追踪（惰性补点）
type Handler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
}

// NewHandler 创建可观测 handler。
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
	case path == "/api/observability/metrics":
		h.serveMetrics(w, r)
	case path == "/api/observability/alert-rules":
		h.serveRuleCollection(w, r)
	case strings.HasPrefix(path, "/api/observability/alert-rules/"):
		h.serveRuleItem(w, r)
	case path == "/api/observability/alerts":
		h.serveAlerts(w, r)
	case path == "/api/observability/logs":
		h.serveLogs(w, r)
	case path == "/api/observability/traces":
		h.serveTraces(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// serveTraces 处理 GET /api/observability/traces?appId=&status=&limit=（惰性补点）。
func (h *Handler) serveTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
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
	traces, err := h.repo.ListTraces(r.Context(), q.Get("appId"), q.Get("status"), limit)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": traces})
}

// serveLogs 处理 GET /api/observability/logs?appId=&level=&q=&limit=（惰性补点）。
func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
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
	logs, err := h.repo.ListLogs(r.Context(), q.Get("appId"), q.Get("level"), q.Get("q"), limit)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": logs})
}

func (h *Handler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	q := r.URL.Query()
	list, err := h.repo.ListMetrics(r.Context(), q.Get("targetType"), q.Get("targetId"), q.Get("name"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
}

func (h *Handler) serveRuleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermObservabilityRead) {
			return
		}
		list, err := h.repo.ListAlertRules(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermObservabilityWrite) {
			return
		}
		var rule AlertRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.CreateAlertRule(r.Context(), rule)
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

func (h *Handler) serveRuleItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityWrite) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/observability/alert-rules/")
	id = strings.TrimRight(id, "/")
	if id == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := h.repo.DeleteAlertRule(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
}

func (h *Handler) serveAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermObservabilityRead) {
		return
	}
	alerts, err := h.repo.ListAlerts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": alerts})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
