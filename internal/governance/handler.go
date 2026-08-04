package governance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermGovernanceRead  = "governance:read"
	PermGovernanceWrite = "governance:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：governance 不直接 import environment，由 cmd/core 注入。
type EnvTypeResolver interface {
	EnvType(ctx context.Context, envID string) (string, error)
}

// Handler 暴露服务治理（注册中心）REST API。
//
// 路由：
//
//	GET    /api/services?envId=&appId=               服务列表
//	POST   /api/services                              注册服务（生产需 prod:write）
//	GET    /api/services/{id}                         服务详情（含实例）
//	DELETE /api/services/{id}                         注销服务（生产需 prod:write，级联清实例）
//	POST   /api/services/{id}/instances               注册实例（生产需 prod:write）
//	DELETE /api/services/{id}/instances/{iid}         注销实例（生产需 prod:write）
//	PUT    /api/instances/{iid}/heartbeat             心跳
//	GET    /api/routes?serviceId=                     路由列表（API 网关）
//	POST   /api/routes                                创建路由（governance:write）
//	PUT    /api/routes/{id}                           更新路由（governance:write）
//	DELETE /api/routes/{id}                           删除路由（governance:write）
//	GET    /api/breakers?serviceId=                   熔断器列表（含即时评估 state + stats）
//	POST   /api/breakers                              创建熔断器（governance:write）
//	PUT    /api/breakers/{id}                         更新熔断器（governance:write）
//	DELETE /api/breakers/{id}                         删除熔断器（governance:write）
type Handler struct {
	repo        Repository
	envResolver EnvTypeResolver
	Authorize   func(r *http.Request, perm string) bool
}

// NewHandler 创建服务治理 handler。
func NewHandler(repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithEnvResolver 注入环境类型解析器，启用生产写权限校验。
func WithEnvResolver(r EnvTypeResolver) HandlerOpt {
	return func(h *Handler) { h.envResolver = r }
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// allowProd 校验目标环境的生产写权限（未注入 resolver/envID 空时跳过；非生产放行）。
func (h *Handler) allowProd(w http.ResponseWriter, r *http.Request, envID string) bool {
	if h.envResolver == nil || envID == "" {
		return true
	}
	etype, err := h.envResolver.EnvType(r.Context(), envID)
	// fail-closed：环境查不到（不存在/跨租户）保守按生产处理，需 prod:write。
	if err != nil || etype == "prod" {
		return h.allow(w, r, PermProdWrite)
	}
	return true
}

// ServeHTTP 按路径前缀分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case path == "/api/services" || path == "/api/services/":
		h.serveCollection(w, r)
	case strings.HasPrefix(path, "/api/services/"):
		h.serveService(w, r)
	case strings.HasPrefix(path, "/api/instances/"):
		h.serveInstance(w, r)
	case path == "/api/routes" || path == "/api/routes/":
		h.serveRouteCollection(w, r)
	case strings.HasPrefix(path, "/api/routes/"):
		h.serveRouteItem(w, r)
	case path == "/api/breakers" || path == "/api/breakers/":
		h.serveBreakerCollection(w, r)
	case strings.HasPrefix(path, "/api/breakers/"):
		h.serveBreakerItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveRouteCollection 处理 /api/routes（GET 列表 / POST 创建）。
// 路由是逻辑配置（不绑定物理环境），复用 governance:read/write，不接 prod:write。
func (h *Handler) serveRouteCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermGovernanceRead) {
			return
		}
		list, err := h.repo.ListRoutes(r.Context(), r.URL.Query().Get("serviceId"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		var rt Route
		if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.CreateRoute(r.Context(), rt)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveRouteItem 处理 /api/routes/{id}（PUT 更新 / DELETE 删除）。
func (h *Handler) serveRouteItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/routes/")
	id = strings.TrimRight(id, "/")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		var rt Route
		if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		rt.ID = id
		updated, err := h.repo.UpdateRoute(r.Context(), rt)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteData(w, updated)
	case http.MethodDelete:
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		if err := h.repo.DeleteRoute(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// fillBreakerState 即时评估熔断器，回填 State + Stats（非持久化）。
// 列表/详情返回前调用，使前端看到"实时"状态。
func (h *Handler) fillBreakerState(b CircuitBreaker) CircuitBreaker {
	stats, state := EvaluateBreaker(b, time.Now())
	b.Stats = stats
	b.State = state
	return b
}

// serveBreakerCollection 处理 /api/breakers（GET 列表含即时评估 / POST 创建）。
// 熔断规则是逻辑配置（不绑定物理环境），复用 governance:read/write，不接 prod:write。
func (h *Handler) serveBreakerCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermGovernanceRead) {
			return
		}
		list, err := h.repo.ListBreakers(r.Context(), r.URL.Query().Get("serviceId"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		for i := range list {
			list[i] = h.fillBreakerState(list[i])
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		var b CircuitBreaker
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.CreateBreaker(r.Context(), b)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		saved = h.fillBreakerState(saved)
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveBreakerItem 处理 /api/breakers/{id}（PUT 更新 / DELETE 删除）。
func (h *Handler) serveBreakerItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/breakers/")
	id = strings.TrimRight(id, "/")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		var b CircuitBreaker
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		b.ID = id
		updated, err := h.repo.UpdateBreaker(r.Context(), b)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteData(w, h.fillBreakerState(updated))
	case http.MethodDelete:
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		if err := h.repo.DeleteBreaker(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveCollection 处理 /api/services（GET 列表 / POST 注册）。
func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermGovernanceRead) {
			return
		}
		envID := r.URL.Query().Get("envId")
		appID := r.URL.Query().Get("appId")
		list, err := h.repo.ListServices(r.Context(), envID, appID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		var s Service
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 生产环境注册服务需 prod:write（developer 被拦，生产只读）
		if !h.allowProd(w, r, s.EnvID) {
			return
		}
		saved, err := h.repo.CreateService(r.Context(), s)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveService 处理 /api/services/{id}[/instances[/{iid}]]。
func (h *Handler) serveService(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/services/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	switch len(parts) {
	case 1:
		h.serveServiceItem(w, r, id)
	case 2:
		if parts[1] == "instances" {
			h.serveInstanceCreate(w, r, id)
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	case 3:
		if parts[1] == "instances" {
			h.serveInstanceDelete(w, r, id, parts[2])
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveServiceItem GET 详情（含实例）/ DELETE 注销（级联清实例）。
func (h *Handler) serveServiceItem(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermGovernanceRead) {
			return
		}
		s, err := h.repo.GetService(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		instances, err := h.repo.ListInstances(r.Context(), id)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, ServiceDetail{Service: s, Instances: instances})
		return
	}
	if r.Method == http.MethodDelete {
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		// 先取服务的环境类型校验生产权限（跨租户/不存在也走 not found，不泄漏）
		s, err := h.repo.GetService(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, s.EnvID) {
			return
		}
		if err := h.repo.DeleteService(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveInstanceCreate POST /api/services/{id}/instances 注册实例。
func (h *Handler) serveInstanceCreate(w http.ResponseWriter, r *http.Request, serviceID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermGovernanceWrite) {
		return
	}
	// 校验服务存在 + 取环境类型校验生产权限
	s, err := h.repo.GetService(r.Context(), serviceID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowProd(w, r, s.EnvID) {
		return
	}
	var in Instance
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.ServiceID = serviceID
	saved, err := h.repo.RegisterInstance(r.Context(), in)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	httputil.WriteDataCreated(w, saved)
}

// serveInstanceDelete DELETE /api/services/{id}/instances/{iid} 注销实例。
func (h *Handler) serveInstanceDelete(w http.ResponseWriter, r *http.Request, serviceID, instID string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermGovernanceWrite) {
		return
	}
	// 取实例所属服务的环境类型校验生产权限（先验实例确实属于该 service）
	gotSvc, err := h.repo.InstanceServiceID(r.Context(), instID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if gotSvc != serviceID {
		httputil.WriteError(w, http.StatusNotFound, "实例不属于该服务")
		return
	}
	s, err := h.repo.GetService(r.Context(), serviceID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowProd(w, r, s.EnvID) {
		return
	}
	if err := h.repo.DeregisterInstance(r.Context(), instID); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, map[string]string{"deleted": instID})
}

// serveInstance PUT /api/instances/{iid}/heartbeat 心跳。
func (h *Handler) serveInstance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermGovernanceWrite) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/instances/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "heartbeat" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	// 心跳是生产环境写操作：先解析实例归属服务的环境类型，prod 需 prod:write。
	// fail-closed：实例/服务查不到保守按生产处理，需 prod:write。
	envID := ""
	if sid, err := h.repo.InstanceServiceID(r.Context(), parts[0]); err == nil {
		if svc, err := h.repo.GetService(r.Context(), sid); err == nil {
			envID = svc.EnvID
		}
	}
	if !h.allowProd(w, r, envID) {
		return
	}
	in, err := h.repo.Heartbeat(r.Context(), parts[0])
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, in)
}
