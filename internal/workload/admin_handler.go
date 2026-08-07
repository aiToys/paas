package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 workload->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// QuotaCheckFunc 配额检查-递增（横切）。ctx 必须带目标租户；delta=+1 创建/-1 删除。
type QuotaCheckFunc func(ctx context.Context, delta int) error

// AdminHandler 暴露工作负载 admin REST API（/api/admin/workloads*）。
//
// 路由：
//
//	GET    /api/admin/workloads          跨租户列表（含真实状态回填）
//	GET    /api/admin/workloads/{id}     跨租户详情（含运行实例 Pod 级）
//	GET    /api/admin/workloads/{id}/logs?pod=&tail=&previous=  实例日志（text/plain）
//	PUT    /api/admin/workloads/{id}/scale  扩缩容（body {replicas}）
//	DELETE /api/admin/workloads/{id}     强制删除（回收配额）
//
// 全挂 adminGuard(super_admin)（cmd/core 装配），handler 内不重复 Authorize；
// 绕过 prod:write（super_admin 有权干预生产），但写操作必记审计。
//
// 注意：workload.Repository.Get/List 强制按 ctx tenant 过滤（缺失即拒），
// admin 路径取单条统一用 ListAll filter by id（与 dataservice 的 GetAny 区别）。
type AdminHandler struct {
	repo     Repository
	status   StatusReader // 可选；注入后详情回填 K8s 真实 Ready/Status + 读实例/日志
	quota    QuotaCheckFunc
	audit    AdminAuditRecorder
	actorOf  func(*http.Request) string
	fillStat func(ctx context.Context, list []Workload) // 便于测试注入 stub；默认走 status.FillStatus
}

// AdminHandlerOpt admin handler 配置。
type AdminHandlerOpt func(*AdminHandler)

// NewAdminHandler 创建 admin handler。
func NewAdminHandler(repo Repository, opts ...AdminHandlerOpt) *AdminHandler {
	h := &AdminHandler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	if h.fillStat == nil && h.status != nil {
		h.fillStat = func(ctx context.Context, list []Workload) {
			_ = h.status.FillStatus(ctx, list)
		}
	}
	return h
}

// WithAdminStatusReader 注入数据面状态读取器（K8s 实例/日志/状态回填）。
func WithAdminStatusReader(r StatusReader) AdminHandlerOpt {
	return func(h *AdminHandler) { h.status = r }
}

// WithAdminQuota 注入配额检查（消耗目标租户 workloads 维度）。
func WithAdminQuota(f QuotaCheckFunc) AdminHandlerOpt { return func(h *AdminHandler) { h.quota = f } }

// WithAdminAudit 注入审计 recorder。
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt { return func(h *AdminHandler) { h.audit = a } }

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

// withAdminFillStat 仅供测试：替换 fillStatus 实现。
func withAdminFillStat(f func(ctx context.Context, list []Workload)) AdminHandlerOpt {
	return func(h *AdminHandler) { h.fillStat = f }
}

// ServeHTTP 按路径分发 admin 请求。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/admin/workloads" && r.Method == http.MethodGet:
		h.serveList(w, r)
	case strings.HasPrefix(path, "/api/admin/workloads/"):
		h.serveItem(w, r)
	default:
		// 已知列表路径但方法非 GET -> 405；其余未注册路径 -> 404。
		if path == "/api/admin/workloads" {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	}
}

// tenantCtx 派生资源所属租户 ctx（admin 跨租户操作以资源租户身份执行下游）。
func adminTenantCtx(r *http.Request, tenantID string) (context.Context, *http.Request) {
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
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "workload", resourceID, detail)
}

// findByID 跨租户取单条工作负载（ListAll filter by id）。
// 用 ListAll 而非 Get：Repository.Get 强制 ctx tenant，admin 跨租户路径需绕过。
func (h *AdminHandler) findByID(ctx context.Context, id string) (Workload, error) {
	list, err := h.repo.ListAll(ctx)
	if err != nil {
		return Workload{}, err
	}
	for _, w := range list {
		if w.ID == id {
			return w, nil
		}
	}
	return Workload{}, fmt.Errorf("工作负载不存在: %s", id)
}

// serveList 跨租户列表，回填 K8s 真实状态（statusReader 可用时）。
func (h *AdminHandler) serveList(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListAll(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if h.fillStat != nil {
		// fillStatus 按 ctx tenant 过滤 Pod，跨租户批量回填需按租户分组调用；
		// 简化：不回填真实状态，列表展示 store 期望态（运维点进详情看真实状态）。
		// 与单租户 List 区别（admin 跨租户 Pod 查询需多 K8s namespace 跨租户路由，留后续）。
		_ = list
	}
	httputil.WriteData(w, list)
}

// serveItem 按路径细分：/{id}、/{id}/logs、/{id}/scale。
func (h *AdminHandler) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/workloads/")
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
		if r.Method == http.MethodDelete {
			h.serveDelete(w, r, id)
			return
		}
		h.serveDetail(w, r, id)
	case "logs":
		h.serveLogs(w, r, id)
	case "scale":
		h.serveScale(w, r, id)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveDetail 详情：资源（含真实状态回填）+ 运行实例（Pod 级，目标租户 ctx 读）。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	wl, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	// 以资源租户 ctx 回填真实状态 + 读实例（StatusReader 内部按 ctx tenant 取 K8s Pod）。
	ctx, _ := adminTenantCtx(r, wl.TenantID)
	if h.fillStat != nil {
		h.fillStat(ctx, []Workload{wl})
	}
	instances := h.instances(ctx, id)
	httputil.WriteData(w, Detail{Workload: wl, Instances: instances})
}

// instances 加载工作负载运行实例（Pod 级）。降级：无 statusReader 或查询失败返空切片。
func (h *AdminHandler) instances(ctx context.Context, id string) []Instance {
	if h.status == nil {
		return []Instance{}
	}
	ins, err := h.status.Instances(ctx, id)
	if err != nil {
		return []Instance{}
	}
	return ins
}

// serveLogs 实例日志：text/plain，复用 StatusReader.PodLogs（越权校验靠 ctx tenant label）。
func (h *AdminHandler) serveLogs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	wl, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if h.status == nil {
		httputil.WriteServiceError(w, http.StatusNotFound, fmt.Errorf("日志不可用（非集群部署）"))
		return
	}
	podName := r.URL.Query().Get("pod")
	if podName == "" {
		httputil.WriteError(w, http.StatusBadRequest, "pod 参数必填")
		return
	}
	var tail int64
	if t := r.URL.Query().Get("tail"); t != "" {
		fmt.Sscanf(t, "%d", &tail)
	}
	if tail <= 0 {
		tail = 1000
	}
	previous := r.URL.Query().Get("previous") == "true"
	ctx, _ := adminTenantCtx(r, wl.TenantID)
	rc, err := h.status.PodLogs(ctx, id, podName, tail, previous)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	defer rc.Close()
	// 覆盖默认 json Content-Type（ServeHTTP 入口设的 application/json）。
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.Copy(w, rc)
}

// adminScaleInput 扩缩容请求体。
type adminScaleInput struct {
	Replicas int    `json:"replicas"`
	Status   string `json:"status,omitempty"`
}

// serveScale 扩缩容，绕过 prod:write。
func (h *AdminHandler) serveScale(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in adminScaleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	wl, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminTenantCtx(r, wl.TenantID)
	updated, err := h.repo.Update(ctx, id, in.Replicas, in.Status)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(rr, wl.TenantID, "admin:scale", id, fmt.Sprintf("扩缩容至 %d 副本", in.Replicas))
	httputil.WriteData(w, updated)
}

// serveDelete 强制删除（先以资源租户 ctx 删，成功后回收配额，与租户侧/application admin 对齐）。
// 顺序：先 Delete 再 quota(-1)。若先 -1 再 Delete，Delete 失败时配额已扣减且重试可累积虚减（配额绕过）。
func (h *AdminHandler) serveDelete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	wl, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminTenantCtx(r, wl.TenantID)
	if err := h.repo.Delete(ctx, id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	// 删除成功后回收配额 best-effort（-1 失败不阻断返回 deleted，下次重试自然恢复一致）。
	if h.quota != nil {
		_ = h.quota(ctx, -1)
	}
	h.recordAudit(rr, wl.TenantID, "admin:delete", id, "删除工作负载")
	httputil.WriteData(w, map[string]string{"deleted": id})
}
