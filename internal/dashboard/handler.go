// Package dashboard 提供平台运维台首页聚合统计（console-admin dashboard 消费）。
// 跨租户平台级视图，需 tenant:admin；统计源自 identity（tenants/users/apiKeys）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/httputil"
)

// StatItem 对齐 admin DashboardStats 的单项。
// TrendPct 仅在接入真实历史计量后输出（omitempty：无历史数据时字段缺省，
// 前端不渲染趋势角标——不推算假百分比，spec 2026-10-19 D1）。
type StatItem struct {
	Value    int     `json:"value"`
	TrendPct float64 `json:"trendPct,omitempty"`
	// Error 非空 = 该数据源查询失败：value 不可信，前端必须显示失败态而非 0
	//（语义诚实：失败 ≠ 0，部分失败不抹掉成功分区）。
	Error string `json:"error,omitempty"`
}

// Stats 对齐 admin DashboardStats。
// PaaS 语义映射：users=用户数 / orders=应用数 / revenue=未支付账单数 / active=进行中流水线数。
type Stats struct {
	Users   StatItem `json:"users"`
	Orders  StatItem `json:"orders"`
	Revenue StatItem `json:"revenue"`
	Active  StatItem `json:"active"`
}

// TrendPoint 趋势点。
type TrendPoint struct {
	Date  string `json:"date"`
	Value int    `json:"value"`
}

// DistItem 分布项。
type DistItem struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

// Charts 对齐 admin DashboardCharts。
type Charts struct {
	Trend        []TrendPoint `json:"trend"`
	Distribution []DistItem   `json:"distribution"`
}

// Activity 对齐 admin Activity。
type Activity struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Desc  string `json:"desc"`
	Time  string `json:"time"`
	Type  string `json:"type"`
}

// AppCounter / BillCounter / RunCounter / AuditLister 是 stats/activities 聚合的可选依赖
// （依赖倒置，避免 dashboard 直接依赖 application/billing/pipeline/security 包；
// cmd/core 装配时桥接 ListAll 系列方法）。
type AppCounter interface {
	ListAll(ctx context.Context) (count int, err error)
}

type BillCounter interface {
	// ListAllBills 跨租户全部账单（只关心 status）。
	ListAllBills(ctx context.Context) (unpaidCount int, err error)
}

type RunCounter interface {
	// ListRuns 状态过滤列表（running/paused 计活跃流水线）。
	ListRuns(ctx context.Context, appID, pipelineID, status string) (count int, err error)
}

// AuditEntry 是审计记录在 dashboard 侧的投影（字段与 security.AuditLog 对齐；
// 独立声明以维持依赖倒置——dashboard 不 import security）。
type AuditEntry struct {
	ID           string
	TenantID     string
	Actor        string
	Action       string
	ResourceType string
	ResourceID   string
	Detail       string
	At           time.Time
}

// AuditLister 供首页「最近平台操作」拉取跨租户审计（按时间倒序取前 N 条）。
type AuditLister interface {
	ListRecentAudit(ctx context.Context, limit int) ([]AuditEntry, error)
}

// Handler 是 dashboard 聚合 HTTP 处理器。
type Handler struct {
	idb   identity.Repository
	apps  AppCounter
	bills BillCounter
	runs  RunCounter
	audit AuditLister
}

// NewHandler 创建 dashboard handler。可选依赖未注入时对应统计返回 0（向后兼容）；
// audit 未注入时 Activities 返回空列表（如实「暂无动态」，不伪造平台状态）。
func NewHandler(idb identity.Repository, opts ...func(*Handler)) *Handler {
	h := &Handler{idb: idb}
	for _, o := range opts {
		o(h)
	}
	return h
}

// WithAppCounter 注入应用计数（PaaS 语义：orders 卡 -> 应用数）。
func WithAppCounter(c AppCounter) func(*Handler) { return func(h *Handler) { h.apps = c } }

// WithBillCounter 注入账单计数（revenue 卡 -> 未支付账单数）。
func WithBillCounter(c BillCounter) func(*Handler) { return func(h *Handler) { h.bills = c } }

// WithRunCounter 注入流水线运行计数（active 卡 -> 进行中流水线）。
func WithRunCounter(c RunCounter) func(*Handler) { return func(h *Handler) { h.runs = c } }

// WithAuditLister 注入跨租户审计源（activities -> 最近平台操作）。
func WithAuditLister(a AuditLister) func(*Handler) { return func(h *Handler) { h.audit = a } }

// Stats: GET /api/admin/dashboard/stats —— 平台级聚合统计。
// 每张卡独立结算：单源失败置 item.Error（前端显示失败态），不以 0 冒充真实计数、
// 不因单源失败拖垮其它卡（R3 审计 H-2）。
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var usersErr string
	users, err := h.idb.ListUsers(ctx, "")
	if err != nil {
		usersErr = "用户数据查询失败"
	}
	ucount := len(users)

	// orders 卡：优先应用数（注入 AppCounter 时），回退 API Key 数（向后兼容旧装配）。
	orders, ordersErr := 0, ""
	if h.apps != nil {
		n, err := h.apps.ListAll(ctx)
		if err != nil {
			ordersErr = "应用数据查询失败"
		} else {
			orders = n
		}
	} else if keys, err := h.idb.ListAPIKeys(ctx, ""); err != nil {
		ordersErr = "API Key 数据查询失败"
	} else {
		orders = len(keys)
	}

	// revenue 卡：未支付账单数（运维视角「待处理财务」，比常量 0 实用）。
	unpaid, unpaidErr := 0, ""
	if h.bills != nil {
		n, err := h.bills.ListAllBills(ctx)
		if err != nil {
			unpaidErr = "账单数据查询失败"
		} else {
			unpaid = n
		}
	}

	// active 卡：进行中（running/paused）流水线数。查询失败如实标失败——
	// 不回退租户数（那是另一张卡的语义，错值比 0 更误导，R3 审计 H-2）。
	active, activeErr := 0, ""
	if h.runs != nil {
		running, errRunning := h.runs.ListRuns(ctx, "", "", "running")
		paused, errPaused := h.runs.ListRuns(ctx, "", "", "paused")
		if errRunning != nil || errPaused != nil {
			activeErr = "运行数据查询失败"
		} else {
			active = running + paused
		}
	}

	// TrendPct 留空：平台未接入按日历史计量，无从计算环比——输出缺省让前端隐藏角标，
	// 不用计数取模伪造趋势（此前 float64(n%7)+1.2 属无依据数据，spec 2026-10-19 D1 移除）。
	httputil.WriteData(w, Stats{
		Users:   StatItem{Value: ucount, Error: usersErr},
		Orders:  StatItem{Value: orders, Error: ordersErr},
		Revenue: StatItem{Value: unpaid, Error: unpaidErr},
		Active:  StatItem{Value: active, Error: activeErr},
	})
}

// Charts: GET /api/admin/dashboard/charts —— 租户用户分布（真实）+ 趋势（无历史采集，返回空）。
func (h *Handler) Charts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, _ := h.idb.ListUsers(ctx, "")
	tenants, _ := h.idb.ListTenants(ctx)

	// 分布：按租户的用户数（真实聚合）。
	tenantCounts := map[string]int{}
	for _, u := range users {
		tenantCounts[u.TenantID]++
	}
	dist := make([]DistItem, 0, len(tenants))
	for _, t := range tenants {
		dist = append(dist, DistItem{Name: t.Name, Value: tenantCounts[t.ID]})
	}

	httputil.WriteData(w, Charts{
		Trend:        []TrendPoint{}, // 无用量/访问历史采集，返回空数组（前端显示空态），不编造 7 天曲线
		Distribution: dist,
	})
}

// Activities: GET /api/admin/dashboard/activities —— 最近跨租户审计（真实平台操作）。
func (h *Handler) Activities(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		httputil.WriteData(w, []Activity{})
		return
	}
	entries, err := h.audit.ListRecentAudit(r.Context(), 8)
	if err != nil {
		httputil.WriteInternalError(w, errors.New("加载平台动态失败"))
		return
	}
	acts := make([]Activity, 0, len(entries))
	for _, e := range entries {
		desc := e.Detail
		if desc == "" {
			desc = fmt.Sprintf("%s %s", e.ResourceType, e.ResourceID)
		}
		who := e.Actor
		if e.TenantID != "" {
			who = fmt.Sprintf("%s（租户 %s）", e.Actor, e.TenantID)
		}
		acts = append(acts, Activity{
			ID:    e.ID,
			Title: e.Action,
			Desc:  fmt.Sprintf("%s · %s", who, desc),
			Time:  e.At.Format("2006-01-02 15:04:05"),
			Type:  "primary",
		})
	}
	httputil.WriteData(w, acts)
}
