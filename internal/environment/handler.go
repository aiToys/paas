package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermEnvironmentRead  = "environment:read"
	PermEnvironmentWrite = "environment:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无此权限 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvUsageReport 环境依赖清单（spec 2026-10-17 专项04 §6：删除环境先展示实际依赖，
// 不用重复确认弹窗代替依赖检查；流水线目标/配置引用为元数据级，留后续）。
type EnvUsageReport struct {
	Workloads    int `json:"workloads"`    // 依赖该环境的工作负载
	Dataservices int `json:"dataservices"` // 绑定该环境的数据服务（有 K8s 实物）
	Lanes        int `json:"lanes"`        // 归属该环境的泳道
}

// Total 依赖总数（非 0 时删除被阻断）。
func (u EnvUsageReport) Total() int { return u.Workloads + u.Dataservices + u.Lanes }

// EnvUsageChecker 环境依赖查询（依赖倒置：environment 不 import workload/dataservice/lane
// ——workload 已 import 本包作 EnvTypeResolver，直接依赖会循环）。cmd/core 桥接三 store。
type EnvUsageChecker interface {
	// EnvUsage 返回该环境的依赖清单（实现内部按 ctx 租户过滤）。
	EnvUsage(ctx context.Context, envID string) (EnvUsageReport, error)
}

// Handler 暴露环境 REST API。
//
//	GET    /api/environments       环境列表（按租户）
//	POST   /api/environments       创建环境
//	GET    /api/environments/{id}  环境详情
//	DELETE /api/environments/{id}  删除环境
type Handler struct {
	repo Repository
	// Authorize 校验当前请求是否持有权限；nil 跳过（测试场景）。
	Authorize func(r *http.Request, perm string) bool
	// Usage 环境占用检查（可选）；nil 跳过。注入后删除环境前查该环境下工作负载数，
	// 非 0 拒删（409）——悬挂 envId 会让 allowProd fail-closed 按 prod 处理、发布阶序断裂。
	Usage EnvUsageChecker
	// Audit 写操作审计（Create/Delete）；nil 跳过。
	Audit adminutil.AuditRecorder
	// ActorFn 从请求取操作者（审计用）；nil 则空。
	ActorFn func(r *http.Request) string
}

// NewHandler 创建环境 handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// recordAudit best-effort 记审计（错误不影响主流程）。
func (h *Handler) recordAudit(r *http.Request, action, resourceID, detail string) {
	if h.Audit == nil {
		return
	}
	actor := ""
	if h.ActorFn != nil {
		actor = h.ActorFn(r)
	}
	// 租户从 ctx 取（空归 platform，由 adapter 统一兜底）。
	tid, _ := tenant.TenantFrom(r.Context())
	_ = h.Audit.Record(r.Context(), tid, actor, action, "environment", resourceID, detail)
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 路由到列表/创建或单资源操作。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rest := strings.TrimPrefix(r.URL.Path, "/api/environments")
	rest = strings.Trim(rest, "/")

	// GET /api/environments
	if rest == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermEnvironmentRead) {
			return
		}
		list, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}

	// POST /api/environments
	if rest == "" && r.Method == http.MethodPost {
		if !h.allow(w, r, PermEnvironmentWrite) {
			return
		}
		var e Environment
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// id 兜底：客户端未提供时生成（与 application/workload 同款），避免空 id 入库。
		if e.ID == "" {
			e.ID = fmt.Sprintf("env-%d", time.Now().UnixNano())
		}
		if err := e.Validate(); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		// 生产环境写操作需 prod:write（developer 被拦，生产只读）
		if e.Type == TypeProd && !h.allow(w, r, PermProdWrite) {
			return
		}
		// 阶序未指定（0）时按 type 填默认（test=10/prod=20），保证开箱即用参与发布流水线。
		if e.PromoteOrder == 0 {
			e.PromoteOrder = DefaultPromoteOrder(e.Type)
		}
		if err := h.repo.Create(r.Context(), e); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		h.recordAudit(r, "environment_create", e.ID, "type="+e.Type+" name="+e.Name)
		httputil.WriteDataCreated(w, e)
		return
	}

	id := strings.Split(rest, "/")[0]

	// GET /api/environments/{id}/usage 依赖预检（spec 2026-10-17 专项04 §6：删除前
	// 展示实际依赖清单；Usage 未注入返零值——非 K8s 部署语义，前端按无依赖处理）。
	// 必须先于 GET /{id}：rest 含斜杠时 Split[0] 截出的 id 会误匹配单资源分支。
	if r.Method == http.MethodGet && strings.HasSuffix(rest, "/usage") {
		if !h.allow(w, r, PermEnvironmentRead) {
			return
		}
		envID := strings.TrimSuffix(rest, "/usage")
		var usage EnvUsageReport
		if h.Usage != nil {
			var uerr error
			usage, uerr = h.Usage.EnvUsage(r.Context(), envID)
			if uerr != nil {
				httputil.WriteInternalError(w, uerr)
				return
			}
		}
		httputil.WriteData(w, usage)
		return
	}

	// GET /api/environments/{id}
	if r.Method == http.MethodGet && id != "" {
		if !h.allow(w, r, PermEnvironmentRead) {
			return
		}
		e, err := h.repo.Get(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, e)
		return
	}

	// DELETE /api/environments/{id}
	if r.Method == http.MethodDelete && id != "" {
		if !h.allow(w, r, PermEnvironmentWrite) {
			return
		}
		// 生产环境删除需 prod:write（developer 被拦）。
		// fail-closed：EnvType 查不到（不存在/跨租户）保守按生产处理，需 prod:write。
		etype, err := h.repo.EnvType(r.Context(), id)
		if err != nil || etype == TypeProd {
			if !h.allow(w, r, PermProdWrite) {
				return
			}
		}
		// 占用校验（与 identity.DeleteTenant 非空 409 同款先例）：环境下仍有依赖时拒删，
		// 防悬挂 envId 引用（allowProd fail-closed 全按生产处理 + 发布阶序断裂）。
		// 查询失败 fail-closed 不删（环境 store 抖动期间不产生孤儿引用）。
		if h.Usage != nil {
			usage, uerr := h.Usage.EnvUsage(r.Context(), id)
			if uerr != nil {
				httputil.WriteInternalError(w, uerr)
				return
			}
			if usage.Total() > 0 {
				httputil.WriteError(w, http.StatusConflict,
					fmt.Sprintf("环境仍有依赖：%d 个工作负载、%d 个数据服务、%d 个泳道，请先删除或迁移后再删除环境",
						usage.Workloads, usage.Dataservices, usage.Lanes))
				return
			}
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.recordAudit(r, "environment_delete", id, "type="+etype)
		httputil.WriteData(w, map[string]string{"deleted": id})
		return
	}

	httputil.WriteError(w, http.StatusNotFound, "not found")
}
