package dataservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// InstanceInfo 是 admin 详情暴露的运行实例（轻量，dataservice 包自定义，cmd/core 桥接 dataplane）。
type InstanceInfo struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Port int32  `json:"port"`
}

// InstanceReader 读数据服务运行实例（admin 详情用）。cmd/core 桥接 dataplane.EndpointsReader。
// nil 或读不到时返空（集群外降级），不报错。
type InstanceReader interface {
	Instances(ctx context.Context, namespace, serviceName string) ([]InstanceInfo, error)
}

// TenantChecker 校验租户存在（admin 代建 body tenantId 必填校验）。cmd/core 桥接 identity.Repository。
type TenantChecker interface {
	Exists(ctx context.Context, tenantID string) error
}

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 dataservice->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// QuotaCheckFunc 配额检查-递增（横切）。ctx 必须带目标租户；delta=+1 创建/-1 删除。
type QuotaCheckFunc func(ctx context.Context, delta int) error

// AdminHandler 暴露数据服务 admin REST API（/api/admin/dataservices*）。
//
// 路由：
//
//	GET    /api/admin/dataservices          跨租户列表（掩码 Connection）
//	GET    /api/admin/dataservices/{id}     跨租户详情（含运行实例）
//	POST   /api/admin/dataservices          代建（body tenantId 必填，消耗目标租户配额）
//	DELETE /api/admin/dataservices/{id}     强制删除（回收配额）
//	POST   /api/admin/dataservices/{id}/{stop|start|restart}
//	PUT    /api/admin/dataservices/{id}/scale
//
// 全挂 adminGuard(super_admin)（cmd/core 装配），handler 内不重复 Authorize；
// 绕过 prod:write（super_admin 有权干预生产），但写操作必记审计。
type AdminHandler struct {
	repo       Repository
	engineRepo EngineRepository
	instances  InstanceReader
	restarter  Restarter
	quota      QuotaCheckFunc
	audit      AdminAuditRecorder
	tenants    TenantChecker
	namespace  string // K8s 命名空间（读实例），空则不读
	actorOf    func(*http.Request) string
}

// AdminHandlerOpt admin handler 配置。
type AdminHandlerOpt func(*AdminHandler)

// NewAdminHandler 创建 admin handler。
func NewAdminHandler(repo Repository, opts ...AdminHandlerOpt) *AdminHandler {
	h := &AdminHandler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// WithAdminInstances 注入实例读取器（读 K8s Endpoints）。
func WithAdminInstances(r InstanceReader) AdminHandlerOpt { return func(h *AdminHandler) { h.instances = r } }

// WithAdminRestarter 注入实例滚动重启器。
func WithAdminRestarter(r Restarter) AdminHandlerOpt { return func(h *AdminHandler) { h.restarter = r } }

// WithAdminQuota 注入配额检查（消耗目标租户 dataservices 维度）。
func WithAdminQuota(f QuotaCheckFunc) AdminHandlerOpt { return func(h *AdminHandler) { h.quota = f } }

// WithAdminAudit 注入审计 recorder。
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt { return func(h *AdminHandler) { h.audit = a } }

// WithAdminTenants 注入租户校验器。
func WithAdminTenants(c TenantChecker) AdminHandlerOpt { return func(h *AdminHandler) { h.tenants = c } }

// WithAdminNamespace 注入 K8s namespace（读实例）。
func WithAdminNamespace(ns string) AdminHandlerOpt { return func(h *AdminHandler) { h.namespace = ns } }

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

// WithAdminEngineRepo 注入引擎目录（代建按 engineId 解析）。
func WithAdminEngineRepo(r EngineRepository) AdminHandlerOpt { return func(h *AdminHandler) { h.engineRepo = r } }

// ServeHTTP 按路径分发 admin 请求。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/admin/dataservices" && r.Method == http.MethodGet:
		h.serveList(w, r)
	case path == "/api/admin/dataservices" && r.Method == http.MethodPost:
		h.serveCreate(w, r)
	case strings.HasPrefix(path, "/api/admin/dataservices/"):
		h.serveItem(w, r)
	default:
		// 已知列表路径但方法非 GET/POST -> 405；其余未注册路径 -> 404。
		if path == "/api/admin/dataservices" {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	}
}

// serveList 跨租户列表，逐条掩码 Connection。
func (h *AdminHandler) serveList(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListAll(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	for i := range list {
		list[i].Connection = MaskConnection(list[i].Connection)
	}
	httputil.WriteData(w, list)
}

// tenantCtx 派生资源所属租户 ctx（admin 跨租户操作以资源租户身份执行下游）。
func tenantCtx(r *http.Request, tenantID string) (context.Context, *http.Request) {
	ctx := tenant.WithTenant(r.Context(), tenantID)
	return ctx, r.WithContext(ctx)
}

func (h *AdminHandler) actor(r *http.Request) string {
	if h.actorOf != nil {
		return h.actorOf(r)
	}
	return "admin"
}

// recordAudit best-effort 记审计（错误不影响主流程）。
func (h *AdminHandler) recordAudit(r *http.Request, tenantID, action, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "dataservice", resourceID, detail)
}

func (h *AdminHandler) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/dataservices/")
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
	d, err := h.repo.GetAny(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	switch action {
	case "":
		if r.Method == http.MethodDelete {
			h.serveDelete(w, r, d)
			return
		}
		h.serveDetail(w, r, d)
	case "stop", "start":
		h.serveLifecycle(w, r, d, action)
	case "restart":
		h.serveRestart(w, r, d)
	case "scale":
		h.serveScale(w, r, d)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveDetail 详情：资源（掩码 Connection）+ 运行实例（目标租户 ctx 读 Endpoints）。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, d DataService) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	out := map[string]any{
		"resource": maskDS(d),
	}
	if h.instances != nil && h.namespace != "" && !IsExternal(d.Source) {
		ctx, _ := tenantCtx(r, d.TenantID)
		ins, err := h.instances.Instances(ctx, h.namespace, d.ID)
		if err == nil {
			out["instances"] = ins
		} else {
			out["instances"] = []InstanceInfo{}
		}
	} else {
		out["instances"] = []InstanceInfo{}
	}
	httputil.WriteData(w, out)
}

// serveLifecycle start(replicas→1,running)/stop(replicas→0,stopped)，绕过 prod:write。
func (h *AdminHandler) serveLifecycle(w http.ResponseWriter, r *http.Request, d DataService, action string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rep := 1
	if action == "stop" {
		rep = 0
		d.Status = StatusStopped
	} else {
		d.Status = StatusRunning
	}
	d.Replicas = &rep
	ctx, rr := tenantCtx(r, d.TenantID)
	updated, err := h.repo.Update(ctx, d)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(rr, d.TenantID, "admin:"+action, d.ID, action+" dataservice")
	httputil.WriteData(w, maskDS(updated))
}

// serveRestart 滚动重启（集群外降级 no-op）。
func (h *AdminHandler) serveRestart(w http.ResponseWriter, r *http.Request, d DataService) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.restarter == nil {
		httputil.WriteServiceError(w, http.StatusNotFound, fmt.Errorf("重启不可用（非集群部署）"))
		return
	}
	ctx, rr := tenantCtx(r, d.TenantID)
	if err := h.restarter.Restart(ctx, d.ID); err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	h.recordAudit(rr, d.TenantID, "admin:restart", d.ID, "restart dataservice")
	httputil.WriteData(w, map[string]string{"restarted": d.ID})
}

// adminScaleInput 扩缩容请求体（字段全可选，非空/非零覆盖）。
type adminScaleInput struct {
	Replicas  *int   `json:"replicas,omitempty"`
	CPU       string `json:"cpu,omitempty"`
	Memory    string `json:"memory,omitempty"`
	StorageGB int    `json:"storageGb,omitempty"`
}

// serveScale 扩缩容，绕过 prod:write。
func (h *AdminHandler) serveScale(w http.ResponseWriter, r *http.Request, d DataService) {
	if r.Method != http.MethodPut {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in adminScaleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	d.Replicas = in.Replicas
	if in.CPU != "" {
		d.CPU = in.CPU
	}
	if in.Memory != "" {
		d.Memory = in.Memory
	}
	if in.StorageGB > 0 {
		d.StorageGB = in.StorageGB
	}
	ctx, rr := tenantCtx(r, d.TenantID)
	updated, err := h.repo.Update(ctx, d)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(rr, d.TenantID, "admin:scale", d.ID, "scale dataservice")
	httputil.WriteData(w, maskDS(updated))
}

// serveDelete 强制删除（先以资源租户 ctx 删，成功后回收配额，与租户侧/application admin 对齐）。
// 顺序：先 Delete 再 quota(-1)。若先 -1 再 Delete，Delete 失败时配额已扣减且重试可累积虚减（配额绕过）。
func (h *AdminHandler) serveDelete(w http.ResponseWriter, r *http.Request, d DataService) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, rr := tenantCtx(r, d.TenantID)
	if err := h.repo.Delete(ctx, d.ID); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	// 删除成功后回收配额 best-effort（-1 失败不阻断返回 deleted，下次重试自然恢复一致）。
	if h.quota != nil {
		_ = h.quota(ctx, -1)
	}
	h.recordAudit(rr, d.TenantID, "admin:delete", d.ID, "delete dataservice")
	httputil.WriteData(w, map[string]string{"deleted": d.ID})
}

// adminCreateInput 代建请求体。TenantID 必填（归属租户）；其余与 DataService 一致。
type adminCreateInput struct {
	TenantID string `json:"tenantId"`
	DataService
}

// serveCreate 代建：校验租户 → 配额(+1) → 引擎解析 → Create → 审计 admin:create。
func (h *AdminHandler) serveCreate(w http.ResponseWriter, r *http.Request) {
	var in adminCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.TenantID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing tenantId")
		return
	}
	if h.tenants != nil {
		if err := h.tenants.Exists(r.Context(), in.TenantID); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, fmt.Errorf("租户不存在: %s", in.TenantID))
			return
		}
	}
	d := in.DataService
	d.TenantID = in.TenantID
	if err := h.resolveEngineForAdmin(r.Context(), &d); err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	// 以目标租户 ctx 执行（配额 + Create 都按目标租户计）
	ctx, rr := tenantCtx(r, in.TenantID)
	if h.quota != nil {
		if err := h.quota(ctx, 1); err != nil {
			httputil.WriteServiceError(w, http.StatusTooManyRequests, err)
			return
		}
	}
	saved, err := h.repo.Create(ctx, d)
	if err != nil {
		if h.quota != nil {
			_ = h.quota(ctx, -1) // 创建失败回滚配额
		}
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(rr, in.TenantID, "admin:create", saved.ID, "代建 dataservice")
	httputil.WriteDataCreated(w, maskDS(saved))
}

// resolveEngineForAdmin 与 Handler.resolveFromEngine 等价（admin 路径独立，避免耦合租户侧 Handler）。
// engineID 必填；按引擎目录回填 kind/source/connection。
func (h *AdminHandler) resolveEngineForAdmin(ctx context.Context, d *DataService) error {
	if d.EngineID == "" {
		return fmt.Errorf("missing engineId")
	}
	if h.engineRepo == nil {
		return fmt.Errorf("引擎目录未启用")
	}
	eng, err := h.engineRepo.GetEngine(ctx, d.EngineID)
	if err != nil {
		return fmt.Errorf("引擎不存在: %s", d.EngineID)
	}
	if !eng.Enabled {
		return fmt.Errorf("引擎未启用: %s", d.EngineID)
	}
	d.Kind = eng.Kind
	d.Source = eng.Mode
	if d.Spec == nil {
		d.Spec = map[string]string{}
	}
	d.Spec["engine"] = eng.Engine
	switch eng.Mode {
	case EngineModeExternalShared:
		d.Connection = map[string]string{}
		for k, v := range eng.Connection {
			d.Connection[k] = v
		}
	case EngineModeExternalDedicated:
		if d.Connection == nil || d.Connection["host"] == "" {
			return fmt.Errorf("external-dedicated 模式需填写连接 host")
		}
	default:
		d.Connection = nil // store.FillConnection 平台生成
	}
	return nil
}

// maskDS 返回 Connection 掩码副本（admin 详情/操作返回前一律掩码）。
func maskDS(d DataService) DataService {
	d.Connection = MaskConnection(d.Connection)
	return d
}
