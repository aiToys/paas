package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermWorkloadRead  = "workload:read"
	PermWorkloadWrite = "workload:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无此权限 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：workload 不直接 import environment，由 cmd/core 注入实现。
type EnvTypeResolver = environment.EnvTypeResolver

// Handler 暴露工作负载 REST API。
// 路由：
//
//	GET    /api/applications/{id}/workloads   应用下工作负载
//	POST   /api/applications/{id}/workloads   创建
//	GET    /api/workloads?type=               跨应用列表（按类型）
//	PUT    /api/workloads/{id}                扩缩容/状态
//	DELETE /api/workloads/{id}                删除
type Handler struct {
	repo        Repository
	envResolver EnvTypeResolver // 可选；注入后写操作校验生产权限
	// Authorize 校验当前请求是否持有权限；nil 跳过（测试场景）。
	Authorize func(r *http.Request, perm string) bool
	// QuotaCheck 工作负载数配额检查（横切，可选）；nil 跳过。由 cmd/core 桥接 billing.CheckAndInc，
	// 创建工作负载前拦截超配额。返回 error 时 Create 中止并回 429。
	QuotaCheck func(ctx context.Context, delta int) error
	// statusReader 数据面状态读取器（可选）；注入后 List 回填 K8s 真实 Ready/Status，
	// 覆盖 store 静态值（reconciler 只回写 CRD status 不回写 store，故读路径需主动回填）。
	// nil 透传 store 原值（降级：纯内存模式无真实状态）。
	statusReader StatusReader
}

// NewHandler 创建工作负载 handler。可选 envResolver 注入启用生产写校验。
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

// WithQuotaCheck 注入工作负载数配额检查（横切配额拦截）。
func WithQuotaCheck(f func(ctx context.Context, delta int) error) HandlerOpt {
	return func(h *Handler) { h.QuotaCheck = f }
}

// WithStatusReader 注入数据面状态读取器，List 时回填 K8s 真实 Ready/Status
// （非 store 静态值）。nil 降级透传 store 原值。
func WithStatusReader(r StatusReader) HandlerOpt {
	return func(h *Handler) { h.statusReader = r }
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// allowProd 校验目标环境的生产写权限。
// 未注入 envResolver 时跳过（兼容旧测试/未启用 prod 防护）；
// envID 为空时 fail-closed（无法判定环境类型，保守按生产处理，要求 prod:write）——
// 防 developer 提交无 EnvID 的工作负载绕过 prod:write（正常路径 Validate 已强制 EnvID 必填，此处为纵深防御）；
// 否则查询环境类型：生产或查不到（不存在/跨租户）均要求 prod:write（fail-closed，
// developer 被拦 -> 生产只读）；非生产放行。
func (h *Handler) allowProd(w http.ResponseWriter, r *http.Request, envID string) bool {
	if h.envResolver == nil {
		return true
	}
	if envID == "" {
		return h.allow(w, r, PermProdWrite)
	}
	etype, err := h.envResolver.EnvType(r.Context(), envID)
	// fail-closed：环境查不到（不存在/跨租户）保守按生产处理，需 prod:write。
	if err != nil || etype == "prod" {
		return h.allow(w, r, PermProdWrite)
	}
	return true
}

// fillStatus 回填 K8s 真实运行状态（注入 statusReader 时覆盖 store 静态值）。
// 读路径降级：失败仅忽略不阻塞（真实状态不可得时返 store 原值，优于 5xx）。
func (h *Handler) fillStatus(ctx context.Context, list []Workload) {
	if h.statusReader != nil {
		_ = h.statusReader.FillStatus(ctx, list)
	}
}

// instances 加载工作负载运行实例（Pod 级）。降级：无 statusReader 或查询失败返空切片
// （详情页仍可展示期望态，实例区空提示「无运行实例/非集群部署」）。
func (h *Handler) instances(ctx context.Context, id string) []Instance {
	if h.statusReader == nil {
		return []Instance{}
	}
	ins, err := h.statusReader.Instances(ctx, id)
	if err != nil {
		return []Instance{}
	}
	return ins
}

// podLogs 取实例日志流。降级：无 statusReader 返友好错误（handler 映射 404 提示）。
func (h *Handler) podLogs(ctx context.Context, workloadID, podName string, tail int64, previous bool) (io.ReadCloser, error) {
	if h.statusReader == nil {
		return nil, fmt.Errorf("日志不可用（非集群部署）")
	}
	return h.statusReader.PodLogs(ctx, workloadID, podName, tail, previous)
}

// ServeHTTP 按路径前缀分发到应用子路由或跨应用工作负载路由。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/applications/"):
		h.serveApp(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/workloads"):
		h.serveCross(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveApp 处理 /api/applications/{id}/workloads。
func (h *Handler) serveApp(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "workloads" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]

	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermWorkloadRead) {
			return
		}
		envID := r.URL.Query().Get("envId")
		service := r.URL.Query().Get("service")
		list, err := h.repo.List(r.Context(), envID, appID, "", "", service)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		h.fillStatus(r.Context(), list)
		httputil.WriteData(w, list)
		return
	}

	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermWorkloadWrite) {
			return
		}
		var w0 Workload
		if err := json.NewDecoder(r.Body).Decode(&w0); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		w0.AppID = appID // 以路径为准
		if err := w0.Validate(); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		// 生产环境创建工作负载需 prod:write（developer 被拦，生产只读）
		if !h.allowProd(w, r, w0.EnvID) {
			return
		}
		// 横切配额拦截：创建前检查工作负载数配额，超限回 429（不创建）。
		if h.QuotaCheck != nil {
			if err := h.QuotaCheck(r.Context(), 1); err != nil {
				httputil.WriteServiceError(w, http.StatusTooManyRequests, err)
				return
			}
		}
		// handler 负责生成 ID + CreatedAt（store 仅校验非空/不重复）。
		if w0.ID == "" {
			w0.ID = fmt.Sprintf("wl-%d", time.Now().UnixNano())
		}
		if w0.CreatedAt.IsZero() {
			w0.CreatedAt = time.Now()
		}
		if err := h.repo.Create(r.Context(), w0); err != nil {
			if h.QuotaCheck != nil {
				_ = h.QuotaCheck(r.Context(), -1) // Create 失败回滚已递增的配额
			}
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		// 统一 {data:T} 契约（与其他端点一致；原裸对象致 fetchJSON 解包失败）。
		httputil.WriteDataCreated(w, w0)
		return
	}

	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveCross 处理 /api/workloads 与 /api/workloads/{id}。
func (h *Handler) serveCross(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/workloads")
	rest = strings.Trim(rest, "/")

	// GET /api/workloads?type=
	if rest == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermWorkloadRead) {
			return
		}
		wtype := r.URL.Query().Get("type")
		envID := r.URL.Query().Get("envId")
		service := r.URL.Query().Get("service")
		list, err := h.repo.List(r.Context(), envID, "", "", wtype, service)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		h.fillStatus(r.Context(), list)
		httputil.WriteData(w, list)
		return
	}

	id := strings.Split(rest, "/")[0]
	parts := strings.Split(rest, "/")

	// GET /api/workloads/{id}/logs?pod=&tail=&previous= 实例（Pod）日志。
	if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "logs" && id != "" {
		if !h.allow(w, r, PermWorkloadRead) {
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
			tail = 1000 // 默认最近 1000 行，避免全量拉爆内存
		}
		previous := r.URL.Query().Get("previous") == "true"
		rc, err := h.podLogs(r.Context(), id, podName, tail, previous)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		// 禁缓冲：日志可能较大，边读边写。
		w.Header().Set("X-Accel-Buffering", "no")
		_, _ = io.Copy(w, rc)
		return
	}

	// GET /api/workloads/{id} 详情：期望态 + 实际运行实例（Pod 级）。
	if r.Method == http.MethodGet && id != "" {
		if !h.allow(w, r, PermWorkloadRead) {
			return
		}
		wl, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		// 回填真实 Ready/Status（与 List 一致），并加载运行实例。
		h.fillStatus(r.Context(), []Workload{wl})
		instances := h.instances(r.Context(), id)
		httputil.WriteData(w, Detail{Workload: wl, Instances: instances})
		return
	}

	// PUT /api/workloads/{id}/schedule 修改 cronjob 的 cron 表达式（仅 cronjob 类型有效）。
	// 生产环境改 schedule 需 prod:write（先查 workload 所属环境）。schedule 空对 cronjob 拒绝。
	if r.Method == http.MethodPut && len(parts) == 2 && parts[1] == "schedule" && id != "" {
		if !h.allow(w, r, PermWorkloadWrite) {
			return
		}
		existing, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, existing.EnvID) {
			return
		}
		var body struct {
			Schedule string `json:"schedule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		w0, err := h.repo.UpdateSchedule(r.Context(), id, body.Schedule)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteData(w, w0)
		return
	}

	if r.Method == http.MethodPut && id != "" {
		if !h.allow(w, r, PermWorkloadWrite) {
			return
		}
		// 生产环境扩缩容需 prod:write（先查 workload 所属环境；Get 失败直接 404，不跳过校验）
		existing, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, existing.EnvID) {
			return
		}
		var body struct {
			Replicas *int   `json:"replicas,omitempty"`
			Status   string `json:"status,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// replicas 缺省时保留当前值（防 body 漏 replicas 致意外缩容到 0，与 admin scale 对齐）。
		replicas := existing.Replicas
		if body.Replicas != nil {
			replicas = *body.Replicas
		}
		w0, err := h.repo.Update(r.Context(), id, replicas, body.Status)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, w0)
		return
	}

	if r.Method == http.MethodDelete && id != "" {
		if !h.allow(w, r, PermWorkloadWrite) {
			return
		}
		// 生产环境删除需 prod:write（先查 workload 所属环境；Get 失败直接 404，不跳过校验）
		existing, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		if !h.allowProd(w, r, existing.EnvID) {
			return
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		// 删除成功回滚配额用量（与 Create 失败回滚对称；否则用量只增不减，永久消耗触发误拦截）。
		if h.QuotaCheck != nil {
			_ = h.QuotaCheck(r.Context(), -1)
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
		return
	}

	httputil.WriteError(w, http.StatusNotFound, "not found")
}
