package lane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限标识（复用 governance 读写，泳道属环境维度运行态管理）。
const (
	PermGovernanceRead  = "governance:read"
	PermGovernanceWrite = "governance:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：lane 不直接 import environment 具体实现，由 cmd/core 注入。
type EnvTypeResolver = environment.EnvTypeResolver

// WorkloadLister 按泳道列工作负载（Detail 聚合用，依赖倒置避免 lane -> workload 反向控制）。
type WorkloadLister interface {
	ListByLane(ctx context.Context, envID, laneID string) ([]workload.Workload, error)
}

// RunLister 按分支列 run 摘要（Detail 聚合 + Close 前置校验用；
// run.Branch == lane.Name 是 spec 锁定的关联约定）。
type RunLister interface {
	ListByBranch(ctx context.Context, branch string) ([]pipeline.RunSummary, error)
}

// LaneDetail 是泳道详情聚合（changes/trace 留前端聚合，spec 3.1）。
type LaneDetail struct {
	Lane       Lane                  `json:"lane"`
	Workloads  []workload.Workload   `json:"workloads"`
	RecentRuns []pipeline.RunSummary `json:"recentRuns"`
}

// AuditRecorder 记审计（依赖倒置，cmd/core 桥接 security.AuditStore；与 identity 同源模式）。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// Handler 暴露泳道 REST API。
//
// 路由：
//
//	GET    /api/lanes?envId=        泳道列表（envId 空不过滤）
//	POST   /api/lanes               创建泳道（生产需 prod:write——联调泳道只在测试环境）
//	GET    /api/lanes/{id}          详情（聚合该泳道工作负载 + 最近 run）
//	PUT    /api/lanes/{id}          更新 mode/description/externalLink
//	DELETE /api/lanes/{id}          关闭泳道（仅标记 Status=closed；有进行中 run 409；幂等）
//
// DELETE 本期语义 = 仅标记 closed（资源回收在关闭钩子接通前不启动）。
type Handler struct {
	repo        Repository
	envResolver EnvTypeResolver
	workloads   WorkloadLister
	runs        RunLister
	auditLog    AuditRecorder
	Authorize   func(r *http.Request, perm string) bool
	// CallerUserID 取调用者用户 ID（main.go 注入 gateway.UserIDFrom，依赖倒置避免 lane -> gateway import）。
	CallerUserID func(ctx context.Context) string
}

// NewHandler 创建泳道 handler。
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

// WithWorkloadLister 注入泳道工作负载列表器（Detail 聚合；未注入返空切片）。
func WithWorkloadLister(l WorkloadLister) HandlerOpt {
	return func(h *Handler) { h.workloads = l }
}

// WithRunLister 注入 run 摘要列表器（Detail 聚合 + Close 前置校验；未注入跳过校验）。
func WithRunLister(l RunLister) HandlerOpt {
	return func(h *Handler) { h.runs = l }
}

// WithAudit 注入审计记录器（泳道生命周期变更记审计：lane_create/lane_update/lane_close）。
func WithAudit(a AuditRecorder) HandlerOpt {
	return func(h *Handler) { h.auditLog = a }
}

// audit best-effort 记审计（失败仅日志不阻断；与 governance 同款）。
func (h *Handler) audit(r *http.Request, action, resourceID, detail string) {
	if h.auditLog == nil {
		return
	}
	tid, _ := tenant.TenantFrom(r.Context())
	actor := "user"
	if h.CallerUserID != nil {
		if uid := h.CallerUserID(r.Context()); uid != "" {
			actor = uid
		}
	}
	h.auditLog.Record(r.Context(), tid, actor, action, "lane", resourceID, detail)
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// allowProd 校验目标环境的生产写权限。fail-closed：环境查不到（不存在/跨租户）
// 保守按生产处理；未注入 resolver 或 envID 空时跳过（测试场景兜底）。
func (h *Handler) allowProd(w http.ResponseWriter, r *http.Request, envID string) bool {
	if h.envResolver == nil || envID == "" {
		return true
	}
	etype, err := h.envResolver.EnvType(r.Context(), envID)
	if err != nil || etype == environment.TypeProd {
		return h.allow(w, r, PermProdWrite)
	}
	return true
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case path == "/api/lanes" || path == "/api/lanes/":
		h.serveCollection(w, r)
	case strings.HasPrefix(path, "/api/lanes/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveCollection 处理 /api/lanes（GET 列表 / POST 创建）。
func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermGovernanceRead) {
			return
		}
		list, err := h.repo.List(r.Context(), r.URL.Query().Get("envId"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
	case http.MethodPost:
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		var in Lane
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// 联调泳道只在测试环境：生产（含环境查不到 fail-closed）拒建。
		if !h.allowProd(w, r, in.EnvID) {
			return
		}
		// Create 以 ctx 租户为准忽略请求体；ID 由仓储生成。
		in.ID = ""
		in.TenantID = ""
		if in.Mode == "" {
			in.Mode = ModeStandard
		}
		created, err := h.repo.Create(r.Context(), in)
		if err != nil {
			writeLaneError(w, err)
			return
		}
		h.audit(r, "lane_create", created.ID, "env="+created.EnvID+" name="+created.Name)
		httputil.WriteDataCreated(w, created)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveItem 处理 /api/lanes/{id}（GET 详情聚合 / PUT 更新 / DELETE 关闭）。
func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/lanes/")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermGovernanceRead) {
			return
		}
		l, err := h.repo.Get(r.Context(), id)
		if err != nil {
			writeLaneError(w, err)
			return
		}
		httputil.WriteData(w, h.buildDetail(r.Context(), l))
	case http.MethodPut:
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		cur, err := h.repo.Get(r.Context(), id)
		if err != nil {
			writeLaneError(w, err)
			return
		}
		// mode/description/externalLink 可改；归属环境不可改（不重验 prod——环境未变）。
		var in Lane
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		updated, err := h.repo.Update(r.Context(), id, in)
		if err != nil {
			writeLaneError(w, err)
			return
		}
		h.audit(r, "lane_update", id, "env="+cur.EnvID+" name="+cur.Name)
		httputil.WriteData(w, updated)
	case http.MethodDelete:
		if !h.allow(w, r, PermGovernanceWrite) {
			return
		}
		cur, err := h.repo.Get(r.Context(), id)
		if err != nil {
			writeLaneError(w, err)
			return
		}
		// 幂等：已 closed 直接 200。
		if cur.Status == StatusClosed {
			httputil.WriteData(w, cur)
			return
		}
		// 关闭前置：有进行中 run（branch==name 且非终态）拒绝——部署/联调中关不掉。
		if h.runs != nil {
			summaries, err := h.runs.ListByBranch(r.Context(), cur.Name)
			if err != nil {
				// fail-closed：查询失败不静默关闭。
				httputil.WriteInternalError(w, err)
				return
			}
			for _, s := range summaries {
				if s.FinishedAt.IsZero() {
					httputil.WriteError(w, http.StatusConflict,
						"泳道有进行中的流水线运行（run "+s.ID+"），待其结束后再关闭")
					return
				}
			}
		}
		closed, err := h.repo.Close(r.Context(), id)
		if err != nil {
			writeLaneError(w, err)
			return
		}
		h.audit(r, "lane_close", id, "env="+cur.EnvID+" name="+cur.Name)
		httputil.WriteData(w, closed)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// buildDetail 聚合泳道详情（lister 未注入或查询失败降级空切片，不阻断详情）。
func (h *Handler) buildDetail(ctx context.Context, l Lane) LaneDetail {
	d := LaneDetail{Lane: l, Workloads: []workload.Workload{}, RecentRuns: []pipeline.RunSummary{}}
	if h.workloads != nil {
		if wls, err := h.workloads.ListByLane(ctx, l.EnvID, l.Name); err == nil && wls != nil {
			d.Workloads = wls
		}
	}
	if h.runs != nil {
		if runs, err := h.runs.ListByBranch(ctx, l.Name); err == nil && runs != nil {
			d.RecentRuns = runs
		}
	}
	return d
}

// writeLaneError 领域 sentinel -> HTTP 状态映射。
func writeLaneError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrLaneNotFound):
		httputil.WriteError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrLaneExists):
		httputil.WriteError(w, http.StatusConflict, "同环境下已存在同名泳道")
	case errors.Is(err, ErrLaneNameInvalid):
		httputil.WriteError(w, http.StatusBadRequest, "泳道名非法（需 DNS-1035：小写字母数字与连字符）")
	default:
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
	}
}
