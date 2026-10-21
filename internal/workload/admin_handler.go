package workload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 workload->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder = adminutil.AuditRecorder // admin 写操作审计（依赖倒置，统一真源 internal/web/admin）

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
	applier  Applier                                    // 可选；注入后支持 drift 修复（POST /reconcile 补投影 PG 有行无 CRD 的 Workload）
	ops      OperationsRepo                             // 可选；注入后支持滚动重启与操作记录查询
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
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt {
	return func(h *AdminHandler) { h.audit = a }
}

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

// WithAdminOps 注入运维操作仓储，启用后台滚动重启与操作记录查询
// （POST /{id}/restart、GET /{id}/operations；与租户侧同一状态机，202 只表示受理，
// 生效看操作记录 status）。
func WithAdminOps(o OperationsRepo) AdminHandlerOpt { return func(h *AdminHandler) { h.ops = o } }

// WithAdminApplier 注入 K8s 数据面 applier，启用 drift 修复端点（POST /api/admin/workloads/reconcile）。
// 注入后 super_admin 可手动触发「PG 有行无 CRD」的 Workload 补投影（CreateOrUpdate 幂等）。
func WithAdminApplier(a Applier) AdminHandlerOpt { return func(h *AdminHandler) { h.applier = a } }

// ServeHTTP 按路径分发 admin 请求。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/admin/workloads" && r.Method == http.MethodGet:
		h.serveList(w, r)
	case path == "/api/admin/workloads/reconcile" && r.Method == http.MethodPost:
		h.serveReconcile(w, r)
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

// actor 取审计操作者（super_admin UserID，未注入兜底 "admin"）。
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

// reconcilePreviewLimit 预览清单单类上限（防全平台大规模漂移时响应体膨胀）。
const reconcilePreviewLimit = 200

// ReconcileMissing 预览中「PG 有行、CRD 缺失」的条目（仅供人读，不含内部字段）。
type ReconcileMissing struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	TenantID string `json:"tenantId"`
}

// ReconcileResult 对账结果。dryRun=true 时只填 missing/existing，created 恒 0（未写任何东西）。
type ReconcileResult struct {
	DryRun   bool               `json:"dryRun"`
	Scanned  int                `json:"scanned"`
	Created  int                `json:"created"`
	Skipped  int                `json:"skipped"`
	Failed   int                `json:"failed"`
	Existing int                `json:"existing,omitempty"`
	Missing  []ReconcileMissing `json:"missing,omitempty"`
	// MissingCount 缺失总数（missing 清单超上限被截断时仍准确；truncated=true 时有意义）。
	MissingCount int      `json:"missingCount,omitempty"`
	FailedIDs    []string `json:"failedIds,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

// serveReconcile POST /api/admin/workloads/reconcile[?dryRun=true] 手动触发 drift 修复：
// 扫 PG workloads 全部行，对「PG 有行无 CRD」的补投影（EnsureIfMissing：仅缺失才补建，
// 绝不覆盖 K8s 既有运行态，防 PG 陈旧状态如 replicas=0 把 service 缩到 0）。
//
// dryRun=true 是只读预览：逐行 Exists 汇总「将补建哪些」，不写数据面、不记执行审计；
// 执行（无 dryRun 参数）保持原有语义并记审计——预览与执行是两个独立动作，执行须显式发起。
// applier 未注入（K8s 未启用）返 503 友好提示；dry-run 同样 503（无数据面可查）。
func (h *AdminHandler) serveReconcile(w http.ResponseWriter, r *http.Request) {
	if h.applier == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "K8s 数据面未启用，drift 修复不可用")
		return
	}
	list, err := h.repo.ListAll(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if strings.EqualFold(r.URL.Query().Get("dryRun"), "true") {
		h.serveReconcilePreview(w, r, list)
		return
	}
	res := ReconcileResult{Scanned: len(list)}
	for i := range list {
		wl := list[i]
		if wl.TenantID == "" {
			res.Skipped++ // 无租户行无法派生数据面 ns，不计入 created/failed（与预览同口径）
			continue
		}
		created, err := h.applier.EnsureIfMissing(r.Context(), wl)
		switch {
		case err != nil:
			res.Failed++
		case created:
			res.Created++
		default:
			res.Skipped++
		}
	}
	h.recordAudit(r, "", "reconcile-drift", "",
		fmt.Sprintf("scanned=%d created=%d skipped=%d failed=%d", res.Scanned, res.Created, res.Skipped, res.Failed))
	httputil.WriteData(w, res)
}

// serveReconcilePreview dry-run：只读探测每条 CRD 是否存在，返回将补建清单。
// 探测失败（权限/连接）单独计数并列出 ID——不能当作「缺失」混进将补建清单，
// 否则预览会夸大动作范围。
func (h *AdminHandler) serveReconcilePreview(w http.ResponseWriter, r *http.Request, list []Workload) {
	res := ReconcileResult{DryRun: true, Scanned: len(list), Missing: []ReconcileMissing{}}
	for i := range list {
		wl := list[i]
		if wl.TenantID == "" {
			res.Skipped++
			continue
		}
		exists, err := h.applier.Exists(r.Context(), wl)
		if err != nil {
			res.Failed++
			if len(res.FailedIDs) < reconcilePreviewLimit {
				res.FailedIDs = append(res.FailedIDs, wl.ID)
			}
			continue
		}
		if exists {
			res.Existing++
			continue
		}
		res.MissingCount++
		if len(res.Missing) >= reconcilePreviewLimit {
			res.Truncated = true
			continue // 继续计数，仅清单截断
		}
		res.Missing = append(res.Missing, ReconcileMissing{ID: wl.ID, Name: wl.Name, TenantID: wl.TenantID})
	}
	httputil.WriteData(w, res)
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
	httputil.WriteData(w, adminutil.FilterTenant(r, list, func(w Workload) string { return w.TenantID }))
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
	case "restart":
		h.serveRestart(w, r, id)
	case "operations":
		h.serveOperations(w, r, id)
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
	ctx, _ := adminutil.TenantCtx(r, wl.TenantID)
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
		_, _ = fmt.Sscanf(t, "%d", &tail)
	}
	if tail <= 0 {
		tail = 1000
	}
	previous := r.URL.Query().Get("previous") == "true"
	ctx, _ := adminutil.TenantCtx(r, wl.TenantID)
	rc, err := h.status.PodLogs(ctx, id, podName, tail, previous)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	defer func() { _ = rc.Close() }()
	// 覆盖默认 json Content-Type（ServeHTTP 入口设的 application/json）。
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = io.Copy(w, rc)
}

// adminScaleInput 扩缩容请求体。
type adminScaleInput struct {
	Replicas *int   `json:"replicas,omitempty"`
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
	ctx, rr := adminutil.TenantCtx(r, wl.TenantID)
	// replicas 缺省时保留当前值（防 body 漏 replicas 致意外缩容到 0，与 dataservice scale 对齐）。
	replicas := wl.Replicas
	if in.Replicas != nil {
		replicas = *in.Replicas
	}
	updated, err := h.repo.Update(ctx, id, replicas, in.Status)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	detail := fmt.Sprintf("扩缩容至 %d 副本", replicas)
	if in.Replicas == nil {
		detail = "更新状态：" + in.Status
	}
	h.recordAudit(rr, wl.TenantID, "admin:scale", id, detail)
	httputil.WriteData(w, updated)
}

// serveRestart 后台滚动重启（POST /{id}/restart）。
// 与租户侧同一操作状态机：202 只表示受理，生效看操作记录 status（accepted→reconciling→running→
// succeeded|failed|unknown）。admin 必须填原因（审计留痕），不默认代办生产发布审批。
func (h *AdminHandler) serveRestart(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.ops == nil || h.status == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "重启不可用（非集群部署或运维仓储未装配）")
		return
	}
	wl, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if wl.Type != TypeService {
		httputil.WriteError(w, http.StatusBadRequest, "仅常驻服务（service）支持滚动重启")
		return
	}
	if wl.Replicas <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, "副本为 0，先恢复副本再执行重启")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		httputil.WriteError(w, http.StatusBadRequest, "请填写重启原因（后台代操作必须留痕）")
		return
	}
	ctx, rr := adminutil.TenantCtx(r, wl.TenantID)
	op, created, err := h.ops.CreateRestart(ctx, wl, reason, h.actor(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		if errors.Is(err, ErrOpConflict) {
			httputil.WriteError(w, http.StatusConflict, "该工作负载已有进行中的运维操作，请等待完成后再试")
			return
		}
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	if !created {
		// 幂等命中：复用既有操作，不重复受理也不重复审计。
		httputil.WriteData(w, op)
		return
	}
	h.recordAudit(rr, wl.TenantID, "admin:restart", id, "reason="+reason+" op="+op.ID)
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": op})
}

// serveOperations 运维操作记录查询（GET /{id}/operations?limit=&offset=）。
// 后台据此展示受理/执行/生效/失败，不把 HTTP 202 当「已生效」。
func (h *AdminHandler) serveOperations(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.ops == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "运维仓储未装配")
		return
	}
	wl, err := h.findByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	limit, offset := 50, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			offset = n
		}
	}
	ctx, _ := adminutil.TenantCtx(r, wl.TenantID)
	ops, err := h.ops.ListByWorkload(ctx, id, limit, offset)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, ops)
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
	ctx, rr := adminutil.TenantCtx(r, wl.TenantID)
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
