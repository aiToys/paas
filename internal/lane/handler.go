package lane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
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

// RunLister 按泳道列 run 摘要（Detail 聚合 + Close 前置校验用）。
// 关联=真实运行目标：run.Lane（触发时从解析 stages 提取的显式部署泳道）∪ run.Branch
// == lane.Name（裸分支隐式泳道存量语义）——此前仅 branch 匹配，遗漏显式命名泳道
// （spec 2026-10-17 L5）。
type RunLister interface {
	ListByLane(ctx context.Context, laneName string) ([]pipeline.RunSummary, error)
}

// LaneDetail 是泳道详情聚合（changes/trace 留前端聚合，spec 3.1）。
type LaneDetail struct {
	Lane       Lane                  `json:"lane"`
	Workloads  []workload.Workload   `json:"workloads"`
	RecentRuns []pipeline.RunSummary `json:"recentRuns"`
}

// 访问地址分类（spec 2026-10-17 §7.1 按访问意图分类，非按名字猜测）。
const (
	AccessBusiness = "business" // 业务入口：本环境基线的正式/测试对外域名
	AccessPreview  = "preview"  // 泳道联调入口：泳道版本的对外域名（浏览器直达，链路染色待接入）
	AccessInternal = "internal" // 集群内部地址：FQDN 派生，仅集群内可达
)

// 地址来源（诚实边界：声明域名=reconciler 已下发 Ingress；FQDN=命名约定派生未验证）。
const (
	AccessSourceDomain = "domain" // workload.Domain 声明（SetDomain 写回=路由已下发）
	AccessSourceFQDN   = "fqdn"   // 集群内 FQDN 派生（仅集群内，未做连通验证）
)

// AccessEndpoint 分类后的访问地址（统一读模型：精确 target + 来源，不按名字猜测）。
type AccessEndpoint struct {
	Kind   string `json:"kind"`           // business|preview|internal
	URL    string `json:"url"`            // 完整 URL（协议+host[+port]；当前入口仅 HTTP，如实标注）
	Target string `json:"target"`         // 精确目标 workload ID
	Name   string `json:"name,omitempty"` // workload 名（展示）
	Lane   string `json:"lane"`           // 归属泳道（default=基线）
	Port   int    `json:"port,omitempty"` // 端口（internal FQDN 显式带；80 省略）
	Source string `json:"source"`         // domain|fqdn
	Note   string `json:"note,omitempty"` // 诚实边界说明（如「仅集群内」「浏览器直达不带泳道染色」）
}

// LaneAccess 泳道访问地址读模型（GET /api/lanes/{id}/access）。
type LaneAccess struct {
	Lane      Lane             `json:"lane"`
	Endpoints []AccessEndpoint `json:"endpoints"`
}

// AuditRecorder 记审计（依赖倒置，cmd/core 桥接 security.AuditStore；与 identity 同源模式）。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// ReclaimResult 泳道资源回收结构化结果（依赖倒置载体；cmd/core 桥接 workload 包同形类型）。
type ReclaimResult struct {
	Reclaimed int      `json:"reclaimed"`           // 成功回收工作负载数
	FailedIDs []string `json:"failedIds,omitempty"` // 回收失败的工作负载 ID（遗留，可重试）
}

// Reclaimer 泳道资源回收（依赖倒置：lane 不 import workload 的 LaneGC 具体类型，
// cmd/core 桥接 workload.LaneGC.ReclaimLane，防关闭路径与 GC 两处删除逻辑漂移——spec 风险 7.4）。
// 幂等：已删工作负载不在结果中，重复调用只处理遗留（DELETE 重试安全）。
type Reclaimer interface {
	ReclaimLane(ctx context.Context, tenantID, envID, laneName string) (ReclaimResult, error)
}

// CloseResult DELETE 关闭泳道的结构化响应（spec 2026-10-17 B：生命周期与清理状态分离——
// 部分回收失败不得假报「全部回收」，前端据 failedIds/error 呈现剩余与重试入口）。
type CloseResult struct {
	Lane      Lane     `json:"lane"`
	Reclaimed int      `json:"reclaimed"`           // 本次成功回收数
	FailedIDs []string `json:"failedIds,omitempty"` // 回收失败遗留的工作负载 ID
	Error     string   `json:"error,omitempty"`     // 回收整体错误（重试 DELETE 或等 GC 兜底）
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
	reclaimer   Reclaimer
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

// WithReclaimer 注入泳道资源回收器（DELETE 关闭时同步回收该泳道全部工作负载；
// 未注入保持仅标记 closed 的旧语义——向后兼容）。回收失败不回滚 closed（Sweep 对
// closed 泳道立即清扫兜底）；幂等可重试（已删不重复出现在结果中）。
func WithReclaimer(r Reclaimer) HandlerOpt {
	return func(h *Handler) { h.reclaimer = r }
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
	if err := h.auditLog.Record(r.Context(), tid, actor, action, "lane", resourceID, detail); err != nil {
		log.Printf("lane 审计记录失败 %s: %v", action, err)
	}
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
		// Status/Weight 服务端定值：Status 恒 active（closed 是生命周期终点不可直建）；
		// Weight 留位恒 0（终审 I3：防请求体注入将来被切流实现消费的权重值）。
		in.ID = ""
		in.TenantID = ""
		in.Status = StatusActive
		in.Weight = 0
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

// serveItem 处理 /api/lanes/{id}（GET 详情聚合 / PUT 更新 / DELETE 关闭）
// 与子资源 /api/lanes/{id}/access（GET 地址分类读模型）。
func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/lanes/")
	if strings.HasSuffix(id, "/access") {
		h.serveAccess(w, r, strings.TrimSuffix(id, "/access"))
		return
	}
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
		// 生产泳道改 mode（如 standard→permanent 使 GC 永久跳过）属生产写，需 prod:write
		// （终审 C1：与 Create 对称，防 developer 改生产泳道属性）。
		if !h.allowProd(w, r, cur.EnvID) {
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
		// 生产泳道关闭会级联回收生产工作负载（ReclaimLane 删 Deployment/Service），
		// 属生产写需 prod:write（终审 C1：比「生产建泳道」更危险的操作必须有对称护栏）。
		if !h.allowProd(w, r, cur.EnvID) {
			return
		}
		// 基线保护（spec L1 关闭层）：遗留 default 实体（保护生效前建成）也拒绝关闭——
		// 只读保留待数据修复，不自动删除。
		if cur.Name == "default" {
			httputil.WriteError(w, http.StatusForbidden, "基线 default 是系统保留泳道，不允许关闭或回收")
			return
		}
		// 幂等重试（spec 2026-10-17 B 可恢复回收）：已 closed 不直接返回——若首关回收
		// 部分失败（failedIds 非空），重试 DELETE 只重扫遗留（ReclaimLane 幂等）；完全
		// 清干净时重试返回 reclaimed=0 的等价结果。
		alreadyClosed := cur.Status == StatusClosed
		var result CloseResult
		if !alreadyClosed {
			// 关闭前置：有进行中 run（真实目标关联 lane∪branch 且非终态）拒绝——部署/联调中关不掉。
			if h.runs != nil {
				summaries, err := h.runs.ListByLane(r.Context(), cur.Name)
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
			cur = closed
		}
		result.Lane = cur
		// 关闭即回收：同步删除该泳道全部工作负载（ReclaimLane 内部逐 workload 派生租户
		// ctx + Delete + 配额回退 + 审计；幂等可重入）。部分失败不回滚 closed——failedIds
		// 上报前端呈现遗留，Sweep 对 closed 泳道立即清扫兜底（不等 TTL）。
		detail := "env=" + cur.EnvID + " name=" + cur.Name
		if alreadyClosed {
			detail += " 重试"
		}
		if h.reclaimer != nil {
			tid, _ := tenant.TenantFrom(r.Context())
			res, err := h.reclaimer.ReclaimLane(r.Context(), tid, cur.EnvID, cur.Name)
			if err != nil {
				result.Error = err.Error()
				detail += " 资源回收失败: " + err.Error() + "（遗留由 GC 兜底清扫）"
			} else {
				result.Reclaimed = res.Reclaimed
				result.FailedIDs = res.FailedIDs
				detail += " 回收工作负载数=" + strconv.Itoa(res.Reclaimed)
				if len(res.FailedIDs) > 0 {
					detail += " 失败遗留=" + strconv.Itoa(len(res.FailedIDs))
				}
			}
		}
		h.audit(r, "lane_close", id, detail)
		httputil.WriteData(w, result)
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
		if runs, err := h.runs.ListByLane(ctx, l.Name); err == nil && runs != nil {
			d.RecentRuns = runs
		}
	}
	return d
}

// serveAccess 处理 GET /api/lanes/{id}/access：地址分类读模型（spec 2026-10-17 §7）。
// 从泳道聚合的工作负载提取地址并分类（business/preview/internal），带精确 target 与
// 来源（声明域名 vs FQDN 派生）——不按名字猜测，不虚构未验证的可达状态。
func (h *Handler) serveAccess(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermGovernanceRead) {
		return
	}
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	l, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeLaneError(w, err)
		return
	}
	access := LaneAccess{Lane: l, Endpoints: []AccessEndpoint{}}
	if h.workloads != nil { // 未注入 lister 返空列表（与 buildDetail 同款降级）
		if wls, err := h.workloads.ListByLane(r.Context(), l.EnvID, l.Name); err == nil {
			tid, _ := tenant.TenantFrom(r.Context())
			for _, wl := range wls {
				access.Endpoints = append(access.Endpoints, classifyEndpoint(wl, tid))
			}
		}
	}
	httputil.WriteData(w, access)
}

// classifyEndpoint 把工作负载投影为分类地址（每 workload 一条主地址）：
//   - Domain 非空：对外域名（reconciler 已下发 Ingress）。基线=业务入口；泳道版本=
//     联调入口（诚实边界：浏览器直达不带泳道染色，链路传播依赖 SDK/Header，§8）。
//   - Domain 空：集群内 FQDN（命名约定派生，仅集群内可达，未做连通验证）。
func classifyEndpoint(wl workload.Workload, tid string) AccessEndpoint {
	ep := AccessEndpoint{Target: wl.ID, Name: wl.Name, Lane: wl.LaneID, Source: AccessSourceDomain}
	lane := wl.LaneID
	if lane == "" {
		lane = "default"
	}
	ep.Lane = lane
	if wl.Domain != "" {
		ep.URL = "http://" + wl.Domain // 当前入口栈仅 HTTP（无 TLS 配置），如实标注不由前端猜协议
		if lane == "default" {
			ep.Kind = AccessBusiness
		} else {
			ep.Kind = AccessPreview
			ep.Note = "泳道版本对外域名；浏览器直达不带泳道染色，全链路传播依赖 SDK/Header"
		}
		return ep
	}
	// FQDN 兜底：Service 名（wl.Name 优先）. 租户 ns .svc.cluster.local（与 pipeline
	// ProbeAddr 同一命名约定）。port 非 0 非 80 显式带。
	host := wl.Name
	if host == "" {
		host = wl.ID
	}
	fqdn := host + "." + tenant.Namespace(tid) + ".svc.cluster.local"
	if wl.Port > 0 && wl.Port != 80 {
		fqdn = fmt.Sprintf("%s:%d", fqdn, wl.Port)
		ep.Port = wl.Port
	}
	return AccessEndpoint{
		Kind: AccessInternal, URL: fqdn, Target: wl.ID, Name: wl.Name, Lane: lane,
		Port: ep.Port, Source: AccessSourceFQDN, Note: "仅集群内可达（命名约定派生，未做连通验证）",
	}
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
	case errors.Is(err, ErrLaneProtected):
		httputil.WriteError(w, http.StatusForbidden, "基线 default 是系统保留泳道，不允许创建或关闭")
	default:
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
	}
}
