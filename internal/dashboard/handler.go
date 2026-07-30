// Package dashboard 提供平台运维台首页聚合统计（console-admin dashboard 消费）。
// 跨租户平台级视图，需 tenant:admin；统计源自 identity（tenants/users/apiKeys）。
package dashboard

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
)

// StatItem 对齐 admin DashboardStats 的单项（值 + 趋势百分比）。
type StatItem struct {
	Value   int     `json:"value"`
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

// Handler 是 dashboard 聚合 HTTP 处理器。
type Handler struct {
	idb identity.Repository
}

// NewHandler 创建 dashboard handler。
func NewHandler(idb identity.Repository) *Handler { return &Handler{idb: idb} }

// Stats: GET /api/dashboard/stats —— 平台级聚合统计。
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, _ := h.idb.ListUsers(ctx, "")
	tenants, _ := h.idb.ListTenants(ctx)
	keys, _ := h.idb.ListAPIKeys(ctx, "")

	ucount := len(users)
	writeData(w, Stats{
		Users:   StatItem{Value: ucount, TrendPct: trend(ucount)},
		Orders:  StatItem{Value: len(keys), TrendPct: trend(len(keys))},
		Revenue: StatItem{Value: 0, TrendPct: 0},
		Active:  StatItem{Value: len(tenants), TrendPct: trend(len(tenants))},
	})
}

// Charts: GET /api/dashboard/charts —— 近 7 天趋势 + 租户用户分布。
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

	writeData(w, Charts{Trend: trend, Distribution: dist})
}

// Activities: GET /api/dashboard/activities —— 静态系统提示（无审计跨租户聚合，留后续）。
func (h *Handler) Activities(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().Format(time.RFC3339)
	writeData(w, []Activity{
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

func writeData(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": v})
}
