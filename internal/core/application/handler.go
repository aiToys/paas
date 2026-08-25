package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"

	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermApplicationRead  = "application:read"
	PermApplicationWrite = "application:write"
	PermBindingWrite     = "binding:write"
)

// ErrQuotaExceededMarker 配额超限的 HTTP 语义：429（资源创建被配额拦截）。
const StatusQuotaExceeded = http.StatusTooManyRequests

// BindingInjector 在应用绑定/解绑资源时触发副作用注入（依赖倒置：application 不依赖具体资源模块）。
// 典型：绑定 dataservice 时自动向 appconfig 注入连接信息（DATABASE_URL/REDIS_URL/...），工作负载重启即生效。
// 失败仅记录日志、不阻断绑定本身（绑定是主操作，注入是增强）。
type BindingInjector interface {
	OnBind(ctx context.Context, appID, bindingType, bindingName string) error
	OnUnbind(ctx context.Context, appID, bindingType, bindingName string) error
}

// Handler 暴露应用 REST API：列表、详情、创建、绑定资源。
type Handler struct {
	repo Repository
	// Authorize 校验当前请求是否持有权限；nil 时跳过（测试场景）。
	// 由 cmd/core 注入 gateway 的请求级权限校验，避免本包依赖身份/网关实现。
	Authorize func(r *http.Request, perm string) bool
	// QuotaCheck 应用数配额检查（横切，可选）；nil 跳过。由 cmd/core 桥接 billing.CheckAndInc，
	// 创建应用前拦截超配额。返回 error 时 Create 中止并回 429。
	QuotaCheck func(ctx context.Context, delta int) error
	// Binder 绑定/解绑副作用注入器（可选）；nil 跳过。由 cmd/core 桥接 dataservice+appconfig。
	Binder BindingInjector
	// CascadeDelete 跨 store 关联资源级联清理（可选）；nil 跳过。由 cmd/core 桥接
	// workload/appconfig/devops 等 store，删应用前清该 appID 下孤儿资源（best-effort，失败仅记日志不阻断）。
	CascadeDelete func(ctx context.Context, appID string) error
	// OnAppCreate 应用创建成功后置 hook（可选）；nil 跳过。由 cmd/core 桥接建默认资源
	// （如默认流水线绑定 tpl-ci/tpl-cd）。best-effort，失败仅记日志不阻断应用创建。
	OnAppCreate func(ctx context.Context, appID string) error
	// stats 工作负载聚合统计（可选）；注入后 List 派生真实 Replicas/Status（覆盖 seed 假值）。
	// nil 透传 seed 原值（降级：无 workload repo 可查）。
	stats WorkloadStats
	// Guard 应用级权限 enforcement（受限应用 restrict 端点校验）；nil 跳过。
	Guard *AppGuard
	// Audit restrict 开关审计（权限模型变更高敏感）；nil 跳过。
	Audit adminutil.AuditRecorder
	// ActorFn 从请求取操作者（审计用）；nil 则空。
	ActorFn func(r *http.Request) string
}

// auditRestrict 记受限开关审计（best-effort）。
func (h *Handler) auditRestrict(r *http.Request, appID string, restricted bool) {
	if h.Audit == nil {
		return
	}
	actor := ""
	if h.ActorFn != nil {
		actor = h.ActorFn(r)
	}
	action := "app_restrict_off"
	if restricted {
		action = "app_restrict_on"
	}
	_ = h.Audit.Record(r.Context(), "", actor, action, "application", appID, "")
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithWorkloadStats 注入工作负载聚合统计，List 时派生应用 Replicas/Status（真实化）。
func WithWorkloadStats(s WorkloadStats) HandlerOpt {
	return func(h *Handler) { h.stats = s }
}

// WithOnAppCreate 注入应用创建后置 hook（建默认资源，如默认流水线绑定）。
func WithOnAppCreate(f func(ctx context.Context, appID string) error) HandlerOpt {
	return func(h *Handler) { h.OnAppCreate = f }
}

// NewHandler 创建应用 API handler。
func NewHandler(repo Repository, opts ...HandlerOpt) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

// allow 统一权限校验：未注入或放行返回 true，否则写 403 返回 false。
func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 路由到具体方法（Go 1.22 ServeMux 已按方法+路径分发，这里做子路由细分）。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/applications")
	path = strings.Trim(path, "/")

	// GET /api/applications
	if path == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermApplicationRead) {
			return
		}
		apps, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		// 派生真实 Replicas/Status（从工作负载聚合，覆盖 seed 假值）；stats 不可得时透传原值。
		if h.stats != nil {
			if statsMap, sErr := h.stats.StatsByTenant(r.Context()); sErr == nil {
				for i := range apps {
					apps[i].ApplyStats(statsMap[apps[i].ID])
				}
			}
		}
		httputil.WriteData(w, apps)
		return
	}

	// POST /api/applications
	if path == "" && r.Method == http.MethodPost {
		if !h.allow(w, r, PermApplicationWrite) {
			return
		}
		var a Application
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// id 兜底：客户端未提供时生成（与 workload 同款），避免空 id 入库——
		// 历史 PG 脏数据（id="" 应用）即由此产生，致前端 Applications 列表访问 statusMeta[""] 崩溃。
		if a.ID == "" {
			a.ID = fmt.Sprintf("app-%d", time.Now().UnixNano())
		}
		a.ApplyDefaults() // 补展示默认（图标/状态/环境/渐变色）
		// 横切配额拦截：创建前检查应用数配额，超限回 429（不创建）。
		if h.QuotaCheck != nil {
			if err := h.QuotaCheck(r.Context(), 1); err != nil {
				httputil.WriteServiceError(w, StatusQuotaExceeded, err)
				return
			}
		}
		if err := h.repo.Create(r.Context(), a); err != nil {
			// 配额已递增但 Create 失败 → 回滚（保持用量与实际一致）
			if h.QuotaCheck != nil {
				_ = h.QuotaCheck(r.Context(), -1)
			}
			httputil.WriteServiceError(w, http.StatusConflict, err)
			return
		}
		// 应用创建成功后置 hook：建默认资源（如默认流水线绑定），best-effort 不阻断应用创建。
		if h.OnAppCreate != nil {
			if err := h.OnAppCreate(r.Context(), a.ID); err != nil {
				log.Printf("OnAppCreate 失败（不阻断应用创建）: app=%s: %v", a.ID, err) //nolint:gosec // G706 误报：日志格式化输出，非注入
			}
		}
		w.WriteHeader(http.StatusCreated)
		httputil.WriteData(w, a)
		return
	}

	// 剩余: /{id} 或 /{id}/bindings
	parts := strings.Split(path, "/")
	id := parts[0]

	if r.Method == http.MethodGet && len(parts) == 1 {
		if !h.allow(w, r, PermApplicationRead) {
			return
		}
		a, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, a)
		return
	}

	// PUT /api/applications/{id}/restrict —— 切换应用级权限受限模式（body: {"restricted":bool}）。
	// 权限语义（防权限提升链，与 member handler allowManage 同源）：
	//   - 非受限 → 开启：仅租户管理员（开启是初始 owner 授予的前置，不能让 developer 自开自封）。
	//   - 受限 → 关闭/保持：app-owner（或租户管理员通行）。
	if r.Method == http.MethodPut && len(parts) == 2 && parts[1] == "restrict" {
		if !h.allow(w, r, PermApplicationWrite) {
			return
		}
		if h.Guard != nil {
			a, err := h.repo.Get(r.Context(), id)
			if err != nil {
				httputil.WriteServiceError(w, http.StatusNotFound, err)
				return
			}
			if !a.Restricted {
				if h.Guard.IsAdmin == nil || !h.Guard.IsAdmin(r) {
					httputil.WriteError(w, http.StatusForbidden, "forbidden: 开启受限模式需租户管理员（初始 owner 授予）")
					return
				}
			} else if !h.Guard.Allow(r, id, AppActionManage) {
				httputil.WriteError(w, http.StatusForbidden, "forbidden: 需应用所有者（app-owner）权限")
				return
			}
		}
		var body struct {
			Restricted bool `json:"restricted"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if err := h.repo.SetRestricted(r.Context(), id, body.Restricted); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.auditRestrict(r, id, body.Restricted)
		a, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, a)
		return
	}

	// DELETE /api/applications/{id} —— 删除应用（含跨 store 关联资源级联清理）。
	if r.Method == http.MethodDelete && len(parts) == 1 {
		if !h.allow(w, r, PermApplicationWrite) {
			return
		}
		// 应用级权限：受限应用删除需 owner（manage）。
		if h.Guard != nil && !h.Guard.Allow(r, id, AppActionManage) {
			httputil.WriteError(w, http.StatusForbidden, "forbidden: 需应用所有者（app-owner）权限")
			return
		}
		// 先校验存在性 + 归属（跨租户 not found 不泄漏）。
		if _, err := h.repo.Get(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		// 级联清理关联资源（工作负载/配置/DevOps，best-effort；失败记日志不阻断删除）。
		if h.CascadeDelete != nil {
			if err := h.CascadeDelete(r.Context(), id); err != nil {
				log.Printf("应用级联清理失败（best-effort，继续删应用）: app=%s: %v", id, err) //nolint:gosec // G706 误报：日志格式化输出
			}
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		// 配额回收：删除成功后递减应用数用量（best-effort，同 workload Delete）。
		// 缺失会导致配额只增不减——删除应用后用量仍占用，最终无法再创建（429 锁死）。
		if h.QuotaCheck != nil {
			_ = h.QuotaCheck(r.Context(), -1)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "bindings" {
		if !h.allow(w, r, PermBindingWrite) {
			return
		}
		// 应用级权限：受限应用绑定资源需 write（app-developer+）。
		if h.Guard != nil && !h.Guard.Allow(r, id, AppActionWrite) {
			httputil.WriteError(w, http.StatusForbidden, "forbidden: 无该应用的应用级权限（write）")
			return
		}
		var body struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Type == "" || body.Name == "" {
			httputil.WriteError(w, http.StatusBadRequest, "missing type or name")
			return
		}
		a, err := h.repo.BindResource(r.Context(), id, body.Type, body.Name)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		// 绑定资源后触发副作用注入（Binder 内部按 type 过滤，非 dataservice kind 无副作用；best-effort）。
		if h.Binder != nil {
			if err := h.Binder.OnBind(r.Context(), id, body.Type, body.Name); err != nil {
				log.Printf("binding injector OnBind 失败（不阻断绑定）: app=%s %s=%s: %v", id, body.Type, body.Name, err) //nolint:gosec // G706 误报：日志格式化输出，非注入
			}
		}
		w.WriteHeader(http.StatusCreated)
		httputil.WriteData(w, a)
		return
	}

	// DELETE /api/applications/{id}/bindings/{type}/{name}
	if r.Method == http.MethodDelete && len(parts) == 4 && parts[1] == "bindings" {
		if !h.allow(w, r, PermBindingWrite) {
			return
		}
		// 应用级权限：受限应用解绑需 write（app-developer+）。
		if h.Guard != nil && !h.Guard.Allow(r, id, AppActionWrite) {
			httputil.WriteError(w, http.StatusForbidden, "forbidden: 无该应用的应用级权限（write）")
			return
		}
		a, err := h.repo.Unbind(r.Context(), id, parts[2], parts[3])
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		// 解绑资源时清理已注入的 appconfig 连接条目（Binder 内部按 type 过滤；best-effort）。
		if h.Binder != nil {
			if err := h.Binder.OnUnbind(r.Context(), id, parts[2], parts[3]); err != nil {
				log.Printf("binding injector OnUnbind 失败（best-effort）: app=%s %s=%s: %v", id, parts[2], parts[3], err) //nolint:gosec // G706 误报：日志格式化输出，非注入
			}
		}
		httputil.WriteData(w, a)
		return
	}

	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// writeData 写成功响应（统一 {data:T} 契约，与 List 一致；下游 fetchJSON<T> 期望此包裹）。
