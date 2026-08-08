package dataplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// Handler 暴露数据面 SDK 接入 API（/dp/），供 zeus 等数据面 SDK 发现服务。
//
// 鉴权经 gateway.APIKeyAuth（dp token = 专用 API Key，绑 tenant），tenant 由 ctx 注入。
// Instance 真源 = K8s Endpoints（reader）；服务元信息来自 governance 表（控制面声明）。
//
// 路由：
//
//	GET    /dp/services                 列服务（zeus Discovery 用）
//	GET    /dp/instances?service=<name> 列实例（从 K8s Endpoints 读，含 signature 供对比）
//	POST   /dp/register                 声明服务元信息（幂等）
//	DELETE /dp/register?id=             反注册（仅删 governance 元信息，K8s Endpoints 自管）
//	PUT    /dp/heartbeat                心跳（兼容保留；K8s readiness 是存活真源）
type Handler struct {
	reader   EndpointsReader         // K8s Endpoints reader（nil=非集群降级）
	services governance.ServiceStore // 服务元信息（控制面声明）
}

// NewHandler 创建数据面 handler。reader 可为 nil（非集群，instances 返空）。
// 数据面 ns 按租户派生（paas-<tenant>），从请求 ctx 取 tenant，无需注入固定 ns。
func NewHandler(reader EndpointsReader, services governance.ServiceStore) *Handler {
	return &Handler{reader: reader, services: services}
}

// ServeHTTP 按 method+path 分发 /dp/ 子路由。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.Trim(r.URL.Path, "/")
	switch {
	case path == "dp/services" && r.Method == http.MethodGet:
		h.listServices(w, r)
	case path == "dp/instances" && r.Method == http.MethodGet:
		h.listInstances(w, r)
	case path == "dp/register" && r.Method == http.MethodPost:
		h.register(w, r)
	case path == "dp/register" && r.Method == http.MethodDelete:
		h.deregister(w, r)
	case path == "dp/heartbeat" && r.Method == http.MethodPut:
		httputil.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) listServices(w http.ResponseWriter, r *http.Request) {
	// 非集群部署（无 reader）：从 governance 表返服务元信息（无 K8s 实例）。
	if h.reader == nil {
		svcs, err := h.services.ListServices(r.Context(), "", "")
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		out := make([]ServiceInfo, 0, len(svcs))
		for _, s := range svcs {
			out = append(out, ServiceInfo{Name: s.Name, Protocol: s.Protocol})
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"services": out})
		return
	}
	tid, _ := tenant.TenantFrom(r.Context())
	svcs, err := h.reader.Services(r.Context(), tenant.Namespace(tid))
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"services": svcs})
}

func (h *Handler) listInstances(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("service")
	if name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing service")
		return
	}
	insts := []Instance{}
	if h.reader != nil {
		tid, _ := tenant.TenantFrom(r.Context())
		is, err := h.reader.Instances(r.Context(), tenant.Namespace(tid), name)
		if err != nil {
			// Endpoints 不存在等错误降级返空（不 5xx，数据面 SDK 容错）。
			insts = []Instance{}
		} else {
			insts = is
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"service":   name,
		"instances": insts,
		"signature": signature(insts),
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var s governance.Service
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	created, err := h.services.CreateService(r.Context(), s)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) deregister(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := h.services.DeleteService(r.Context(), id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// signature 对实例集做确定性签名（供 zeus Watcher 对比变化触发重发现）。
// 排序后 sha256，避免实例顺序波动导致伪变更。
func signature(insts []Instance) string {
	keys := make([]string, 0, len(insts))
	for _, in := range insts {
		keys = append(keys, fmt.Sprintf("%s:%s:%d", in.ID, in.IP, in.Port))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "|")))
	return hex.EncodeToString(sum[:])
}
