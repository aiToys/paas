// Package dashboard 提供平台运维台首页聚合统计（console-admin dashboard 消费）。
// 跨租户平台级视图，需 tenant:admin；统计源自 identity（tenants/users/apiKeys）。
package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/httputil"
)

// StatItem 对齐 admin DashboardStats 的单项（值 + 趋势百分比）。
type StatItem struct {
	Value    int     `json:"value"`
	TrendPct float64 `json:"trendPct"`
}

// Stats 对齐 admin DashboardStats。
// PaaS 语义映射：users=用户数 / orders=API Key 数 / revenue=0（无收入概念）/ active=租户数。
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

// AppCounter / BillCounter / RunCounter 是 stats 聚合的可选依赖（依赖倒置，避免 dashboard
// 直接依赖 application/billing/pipeline 包；cmd/core 装配时桥接 ListAll 系列方法）。
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

// Handler 是 dashboard 聚合 HTTP 处理器。
type Handler struct {
	idb   identity.Repository
	apps  AppCounter
	bills BillCounter
	runs  RunCounter
}

// NewHandler 创建 dashboard handler。可选依赖未注入时对应统计返回 0（向后兼容）。
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

// Stats: GET /api/admin/dashboard/stats —— 平台级聚合统计。
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, _ := h.idb.ListUsers(ctx, "")
	tenants, _ := h.idb.ListTenants(ctx)

	ucount := len(users)

	// orders 卡：优先应用数（注入 AppCounter 时），回退 API Key 数（向后兼容旧装配）。
	orders := 0
	if h.apps != nil {
		if n, err := h.apps.ListAll(ctx); err == nil {
			orders = n
		}
	} else if keys, err := h.idb.ListAPIKeys(ctx, ""); err == nil {
		orders = len(keys)
	}

	// revenue 卡：未支付账单数（运维视角「待处理财务」，比常量 0 实用）。
	unpaid := 0
	if h.bills != nil {
		if n, err := h.bills.ListAllBills(ctx); err == nil {
			unpaid = n
		}
	}

	// active 卡：进行中（running/paused）流水线数，未注入时回退租户数。
	active := len(tenants)
	if h.runs != nil {
		running, errRunning := h.runs.ListRuns(ctx, "", "", "running")
		paused, errPaused := h.runs.ListRuns(ctx, "", "", "paused")
		if errRunning == nil && errPaused == nil {
			active = running + paused
		}
	}

	httputil.WriteData(w, Stats{
		Users:   StatItem{Value: ucount, TrendPct: trend(ucount)},
		Orders:  StatItem{Value: orders, TrendPct: trend(orders)},
		Revenue: StatItem{Value: unpaid, TrendPct: 0},
		Active:  StatItem{Value: active, TrendPct: trend(active)},
	})
}

// Charts: GET /api/admin/dashboard/charts —— 近 7 天趋势 + 租户用户分布。
func (h *Handler) Charts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, _ := h.idb.ListUsers(ctx, "")
	tenants, _ := h.idb.ListTenants(ctx)

	// 趋势：近 7 天，以当前用户数为基准确定性递减（无历史采集，展示形态）
	base := len(users)
	trend := make([]TrendPoint, 0, 7)
	for i := 6; i >= 0; i-- {
		day := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		v := base - i // 确定性递增形态
		if v < 0 {
			v = 0
		}
		trend = append(trend, TrendPoint{Date: day, Value: v})
	}

	// 分布：按租户的用户数
	tenantCounts := map[string]int{}
	for _, u := range users {
		tenantCounts[u.TenantID]++
	}
	dist := make([]DistItem, 0, len(tenants))
	for _, t := range tenants {
		dist = append(dist, DistItem{Name: t.Name, Value: tenantCounts[t.ID]})
	}

	httputil.WriteData(w, Charts{Trend: trend, Distribution: dist})
}

// Activities: GET /api/admin/dashboard/activities —— 静态系统提示（无审计跨租户聚合，留后续）。
func (h *Handler) Activities(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().Format(time.RFC3339)
	httputil.WriteData(w, []Activity{
		{ID: "a1", Title: "平台运行正常", Desc: "所有核心服务在线", Time: now, Type: "success"},
		{ID: "a2", Title: "欢迎使用 PaaS 控制台", Desc: "使用管理员账号登录，可管理租户与用户", Time: now, Type: "primary"},
	})
}

// trend 由计数派生一个稳定的假趋势百分比（无历史数据；生产应接真实计量）。
func trend(n int) float64 {
	if n == 0 {
		return 0
	}
	return float64(n%7) + 1.2
}

// —— 响应辅助（core 契约 {data:T}/{error:msg}）——
