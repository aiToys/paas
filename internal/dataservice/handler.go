package dataservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermDataServiceRead  = "dataservice:read"
	PermDataServiceWrite = "dataservice:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：dataservice 不直接 import environment，由 cmd/core 注入。
type EnvTypeResolver = environment.EnvTypeResolver

// Restarter 触发数据服务实例滚动重启（patch STS template 触发 Pod 重建）。
// 依赖倒置：dataservice 不 import K8s；cmd/core 桥接 controller-runtime client。
// nil 时 restart 操作降级为 no-op 返 200（集群外/dev 模式）。
type Restarter interface {
	Restart(ctx context.Context, id string) error
}

// Handler 暴露数据服务 REST API。
//
// 路由：
//
//	GET    /api/dataservices?kind=          列表（按 kind 过滤）
//	GET    /api/dataservices/meta            Kind 元数据（前端表单）
//	POST   /api/dataservices                 创建（生产需 prod:write）
//	GET    /api/dataservices/{id}            详情
//	PUT    /api/dataservices/{id}            更新 spec/status（生产需 prod:write）
//	DELETE /api/dataservices/{id}            删除（生产需 prod:write）
//	POST   /api/dataservices/{id}/start      启动（replicas→1，生产需 prod:write）
//	POST   /api/dataservices/{id}/stop       停止（replicas→0，省资源，生产需 prod:write）
//	POST   /api/dataservices/{id}/restart    滚动重启（生产需 prod:write）
//	PUT    /api/dataservices/{id}/scale      扩缩容 replicas/cpu/memory/storageGb（生产需 prod:write）
//	PUT    /api/dataservices/{id}/upgrade    版本升级 image（生产需 prod:write）
type Handler struct {
	repo        Repository
	engineRepo  EngineRepository // 创建实例时按 engineID 解析 kind/engine/mode/connection
	envResolver EnvTypeResolver
	restarter   Restarter
	Authorize   func(r *http.Request, perm string) bool
}

// NewHandler 创建数据服务 handler。
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

// WithRestarter 注入实例重启控制器（K8s 模式 patch STS）；nil 时 restart 降级 no-op。
func WithRestarter(r Restarter) HandlerOpt {
	return func(h *Handler) { h.restarter = r }
}

// WithEngineRepo 注入引擎目录仓储，Create 时按 engineID 解析 kind/engine/mode/connection。
func WithEngineRepo(r EngineRepository) HandlerOpt {
	return func(h *Handler) { h.engineRepo = r }
}

// resolveFromEngine 按 d.EngineID 从引擎目录解析 kind/engine/mode/connection，回填到 DataService。
// 引擎目录是真源：用户不自由组合 kind+engine，而是选一个 Engine。mode 决定连接来源：
//   - managed：清空 Connection，store.FillConnection 平台生成凭证。
//   - external-shared：复制 Engine.Connection（共享集群，全租户复用）。
//   - external-dedicated：保留用户填的 Connection（body 传入，校验 host 必填）。
//
// disabled 引擎拒绝（用户只能用 admin 启用的引擎）。
func (h *Handler) resolveFromEngine(ctx context.Context, d *DataService) (DataService, error) {
	if d.EngineID == "" {
		return *d, fmt.Errorf("missing engineId")
	}
	eng, err := h.engineRepo.GetEngine(ctx, d.EngineID)
	if err != nil {
		return *d, fmt.Errorf("引擎不存在: %s", d.EngineID)
	}
	if !eng.Enabled {
		return *d, fmt.Errorf("引擎未启用: %s", d.EngineID)
	}
	d.Kind = eng.Kind
	d.Source = eng.Mode
	if d.Spec == nil {
		d.Spec = map[string]string{}
	}
	d.Spec["engine"] = eng.Engine // EngineOf 读 spec.engine，与 FillConnection 一致
	switch eng.Mode {
	case EngineModeExternalShared:
		// 复用 admin 配的共享集群连接（深拷避免改 engine 缓存）。
		d.Connection = map[string]string{}
		for k, v := range eng.Connection {
			d.Connection[k] = v
		}
	case EngineModeExternalDedicated:
		// 用户自填：校验 host 必填（外部独占实例至少要有地址）。
		if d.Connection == nil || d.Connection["host"] == "" {
			return *d, fmt.Errorf("external-dedicated 模式需填写连接 host")
		}
	default: // managed
		d.Connection = nil // store.FillConnection 平台生成
	}
	return *d, nil
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

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/dataservices":
		h.serveCollection(w, r)
	case path == "/api/dataservices/meta":
		httputil.WriteData(w, KindMetas)
	case strings.HasPrefix(path, "/api/dataservices/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermDataServiceRead) {
			return
		}
		list, err := h.repo.List(r.Context(), r.URL.Query().Get("kind"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		// list 掩码敏感字段（password/secretKey/token）；详情才返明文（read 权限者）。
		for i := range list {
			list[i].Connection = MaskConnection(list[i].Connection)
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		var d DataService
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 按 engineID 解析 kind/engine/mode/connection（引擎目录是真源，用户不自由组合 kind+engine）。
		if h.engineRepo != nil {
			resolved, err := h.resolveFromEngine(r.Context(), &d)
			if err != nil {
				httputil.WriteServiceError(w, http.StatusBadRequest, err)
				return
			}
			d = resolved
		}
		// 生产环境创建需 prod:write（developer 被拦，生产只读）
		if !h.allowProd(w, r, d.EnvID) {
			return
		}
		saved, err := h.repo.Create(r.Context(), d)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		saved.Connection = MaskConnection(saved.Connection) // 返回前掩码（与 List/Detail 一致，明文仅内部绑定注入用）
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/dataservices/")
	rest = strings.TrimRight(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	switch action {
	case "":
		h.serveItemCRUD(w, r, id)
	case "start", "stop":
		h.serveLifecycle(w, r, id, action)
	case "restart":
		h.serveRestart(w, r, id)
	case "scale":
		h.serveScale(w, r, id)
	case "upgrade":
		h.serveUpgrade(w, r, id)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveItemCRUD 处理 {id} 的 GET/PUT/DELETE（基础资源 CRUD）。
func (h *Handler) serveItemCRUD(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermDataServiceRead) {
			return
		}
		d, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		// detail 也掩码敏感字段（与 security 模块一致：read 权限者含 viewer 不可见明文；
		// 应用绑定经 repo.Get 拿明文注入，不受 handler 掩码影响）。
		d.Connection = MaskConnection(d.Connection)
		httputil.WriteData(w, d)
	case http.MethodPut:
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		// 先取现有（确认存在 + 拿 envID 校验生产权限）
		ex, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, ex.EnvID) {
			return
		}
		var d DataService
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		d.ID = id
		updated, err := h.repo.Update(r.Context(), d)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		updated.Connection = MaskConnection(updated.Connection) // 返回前掩码
		httputil.WriteData(w, updated)
	case http.MethodDelete:
		if !h.allow(w, r, PermDataServiceWrite) {
			return
		}
		ex, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, ex.EnvID) {
			return
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveLifecycle 处理 start（replicas→1，status→running）/stop（replicas→0，status→stopped）。
// 经 Update 投影 CRD spec.replicas，reconciler 调 STS 副本数（集群内真实生效）。
func (h *Handler) serveLifecycle(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermDataServiceWrite) {
		return
	}
	ex, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowProd(w, r, ex.EnvID) {
		return
	}
	rep := 1
	if action == "stop" {
		rep = 0
		ex.Status = StatusStopped
	} else {
		ex.Status = StatusRunning
	}
	ex.Replicas = &rep
	updated, err := h.repo.Update(r.Context(), ex)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	updated.Connection = MaskConnection(updated.Connection)
	httputil.WriteData(w, updated)
}

// serveRestart 滚动重启（patch STS template annotation 触发 Pod 重建）。
// restarter nil（集群外）时 no-op 返 200（best-effort，与 K8s 数据面降级模式一致）。
func (h *Handler) serveRestart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermDataServiceWrite) {
		return
	}
	ex, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowProd(w, r, ex.EnvID) {
		return
	}
	if h.restarter != nil {
		if err := h.restarter.Restart(r.Context(), id); err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
	}
	httputil.WriteData(w, map[string]string{"restarted": id})
}

// scaleInput 扩缩容请求体（字段全可选，非空/非零覆盖）。
type scaleInput struct {
	Replicas  *int   `json:"replicas,omitempty"`
	CPU       string `json:"cpu,omitempty"`
	Memory    string `json:"memory,omitempty"`
	StorageGB int    `json:"storageGb,omitempty"`
}

// serveScale 扩缩容：replicas/cpu/memory/storageGb 任一非空覆盖，经 Update 投影 CRD，
// reconciler 调 STS 副本/容器 resources/PVC size。storage 仅扩容（K8s PVC 不可缩）。
func (h *Handler) serveScale(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermDataServiceWrite) {
		return
	}
	ex, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowProd(w, r, ex.EnvID) {
		return
	}
	var in scaleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ex.Replicas = in.Replicas // nil 不覆盖（store 合并语义）
	if in.CPU != "" {
		ex.CPU = in.CPU
	}
	if in.Memory != "" {
		ex.Memory = in.Memory
	}
	if in.StorageGB > 0 {
		ex.StorageGB = in.StorageGB
	}
	updated, err := h.repo.Update(r.Context(), ex)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	updated.Connection = MaskConnection(updated.Connection)
	httputil.WriteData(w, updated)
}

// upgradeInput 版本升级请求体（image 含 tag，覆盖默认镜像）。
type upgradeInput struct {
	Image string `json:"image"`
}

// serveUpgrade 版本升级：覆盖 image，reconciler 用 spec.Image 重建 STS（新 tag）。
func (h *Handler) serveUpgrade(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermDataServiceWrite) {
		return
	}
	ex, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowProd(w, r, ex.EnvID) {
		return
	}
	var in upgradeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Image == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing image")
		return
	}
	ex.Image = in.Image
	updated, err := h.repo.Update(r.Context(), ex)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	updated.Connection = MaskConnection(updated.Connection)
	httputil.WriteData(w, updated)
}
