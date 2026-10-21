package billing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/billing"
	billingmemory "github.com/aitoys/paas/internal/billing/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fixedPrice 是测试用定价表（元/百万 token）。
func fixedPrice(m map[string]float64) billing.ModelPrice {
	return func(model string) (float64, bool) {
		p, ok := m[model]
		return p, ok
	}
}

func fixedVendor(m map[string]string) billing.ModelVendor {
	return func(model string) (string, bool) {
		v, ok := m[model]
		return v, ok
	}
}

// TestBudgetValidate 校验范围/金额/阈值的服务端规则（memory 与 PG 同源调用）。
func TestBudgetValidate(t *testing.T) {
	cases := []struct {
		name string
		b    billing.Budget
		ok   bool
	}{
		{"tenant 合法", billing.Budget{Scope: "tenant", MonthlyAmount: 100}, true},
		{"tenant 带 appId 拒绝", billing.Budget{Scope: "tenant", AppID: "a1", MonthlyAmount: 100}, false},
		{"app 缺 appId 拒绝", billing.Budget{Scope: "app", MonthlyAmount: 100}, false},
		{"app 合法", billing.Budget{Scope: "app", AppID: "a1", MonthlyAmount: 100}, true},
		{"未知 scope 拒绝", billing.Budget{Scope: "env", MonthlyAmount: 100}, false},
		{"金额为 0 拒绝", billing.Budget{Scope: "tenant", MonthlyAmount: 0}, false},
		{"阈值 >1 拒绝", billing.Budget{Scope: "tenant", MonthlyAmount: 1, WarnRatio: 1.5}, false},
		{"阈值 <0 拒绝", billing.Budget{Scope: "tenant", MonthlyAmount: 1, WarnRatio: -0.1}, false},
	}
	for _, c := range cases {
		err := c.b.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: 期望通过，得到 %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: 期望拒绝，实际通过", c.name)
		}
	}
}

// TestComputeBudgetStatus 状态机：ok → warn（达阈值）→ exceeded（超 100%）。
func TestComputeBudgetStatus(t *testing.T) {
	price := fixedPrice(map[string]float64{"m1": 10}) // 10 元/百万 token
	b := billing.Budget{Scope: "tenant", MonthlyAmount: 100, WarnRatio: 0.8}
	// 100 万 token × 10 元/百万 = 10 元 → 10%
	st := billing.ComputeBudgetStatus(b, "2026-10", []billing.TokenUsageRow{{Model: "m1", Tokens: 1_000_000}}, price)
	if st.State != billing.BudgetOK || st.EstimatedCost != 10 || st.Ratio != 0.1 {
		t.Fatalf("10%% 用量应 ok，得到 %+v", st)
	}
	// 850 万 token → 85 元 → warn
	st = billing.ComputeBudgetStatus(b, "2026-10", []billing.TokenUsageRow{{Model: "m1", Tokens: 8_500_000}}, price)
	if st.State != billing.BudgetWarn {
		t.Fatalf("85%% 应 warn，得到 %s", st.State)
	}
	// 1200 万 → 120 元 → exceeded，ratio >1
	st = billing.ComputeBudgetStatus(b, "2026-10", []billing.TokenUsageRow{{Model: "m1", Tokens: 12_000_000}}, price)
	if st.State != billing.BudgetExceeded || st.Ratio <= 1 {
		t.Fatalf("120%% 应 exceeded，得到 %+v", st)
	}
}

// TestComputeBudgetStatusUnpriced 未定价模型：tokens 照计，成本不臆造，模型名如实列出。
func TestComputeBudgetStatusUnpriced(t *testing.T) {
	price := fixedPrice(map[string]float64{"m1": 10})
	b := billing.Budget{Scope: "tenant", MonthlyAmount: 100}
	st := billing.ComputeBudgetStatus(b, "2026-10", []billing.TokenUsageRow{
		{Model: "m1", Tokens: 1_000_000},
		{Model: "ghost", Tokens: 500_000},
	}, price)
	if st.EstimatedCost != 10 {
		t.Fatalf("未定价模型不应计入成本，得到 %.2f", st.EstimatedCost)
	}
	if st.Tokens != 1_500_000 {
		t.Fatalf("未定价模型 tokens 仍应计入，得到 %d", st.Tokens)
	}
	if len(st.UnpricedModels) != 1 || st.UnpricedModels[0] != "ghost" {
		t.Fatalf("应列出未定价模型 ghost，得到 %v", st.UnpricedModels)
	}
	if st.State != billing.BudgetOK {
		t.Fatalf("未定价不应触发超限，得到 %s", st.State)
	}
}

// TestBuildVendorSettlements 供应商聚合：未归属模型不丢弃、仅登记记录的供应商可见、diff 方向正确。
func TestBuildVendorSettlements(t *testing.T) {
	price := fixedPrice(map[string]float64{"m1": 10, "m2": 20})
	vendor := fixedVendor(map[string]string{"m1": "v-a", "m2": "v-a"})
	rows := []billing.TokenUsageRow{
		{Model: "m1", Tokens: 1_000_000}, // v-a: 10 元
		{Model: "m2", Tokens: 1_000_000}, // v-a: 20 元
		{Model: "m3", Tokens: 2_000_000}, // 目录未收录 → vendor="" 行
	}
	records := []billing.SettlementRecord{
		{Vendor: "v-a", Period: "2026-10", Amount: 35}, // 供应商更贵 5 元
		{Vendor: "v-b", Period: "2026-10", Amount: 8},  // 当期无用量，仍需可见
	}
	out := billing.BuildVendorSettlements(rows, records, vendor, price)
	if len(out) != 3 {
		t.Fatalf("应聚合出 3 个供应商行（含未归属与仅对账），得到 %d: %+v", len(out), out)
	}
	byVendor := map[string]billing.VendorSettlement{}
	for _, v := range out {
		byVendor[v.Vendor] = v
	}
	if va := byVendor["v-a"]; va.EstimatedCost != 30 || va.RecordedAmount != 35 || va.Diff != 5 || len(va.Models) != 2 {
		t.Fatalf("v-a 聚合错误: %+v", va)
	}
	if un := byVendor[""]; un.Tokens != 2_000_000 || len(un.UnpricedModels) != 1 || un.UnpricedModels[0] != "m3" {
		t.Fatalf("未归属供应商行应保留且标注未定价: %+v", un)
	}
	if vb := byVendor["v-b"]; vb.RecordCount != 1 || vb.RecordedAmount != 8 || vb.Diff != 8 {
		t.Fatalf("仅对账供应商行错误: %+v", vb)
	}
}

// TestBudgetStoreLifecycle memory 实现：重复范围拒绝、归属不可变、跨租户隔离。
func TestBudgetStoreLifecycle(t *testing.T) {
	s := billingmemory.NewStore()
	ctxA := tenant.WithTenant(context.Background(), "t-a")
	ctxB := tenant.WithTenant(context.Background(), "t-b")

	b, err := s.CreateBudget(ctxA, billing.Budget{Scope: "tenant", MonthlyAmount: 100, WarnRatio: 0.8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBudget(ctxA, billing.Budget{Scope: "tenant", MonthlyAmount: 200}); err == nil {
		t.Fatal("同租户同范围重复预算应拒绝")
	}
	// 不同租户同范围允许（隔离）
	if _, err := s.CreateBudget(ctxB, billing.Budget{Scope: "tenant", MonthlyAmount: 300}); err != nil {
		t.Fatalf("跨租户同范围应允许: %v", err)
	}

	// Update 只改额度/阈值/强制/备注，归属字段以库内为准
	upd, err := s.UpdateBudget(ctxA, billing.Budget{ID: b.ID, Scope: "app", AppID: "a1", MonthlyAmount: 150, WarnRatio: 0.5, Enforce: true})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Scope != "tenant" || upd.AppID != "" {
		t.Fatalf("归属字段不可变，得到 %+v", upd)
	}
	if upd.MonthlyAmount != 150 || !upd.Enforce || upd.WarnRatio != 0.5 {
		t.Fatalf("可编辑字段未生效: %+v", upd)
	}

	if list, _ := s.ListBudgets(ctxA); len(list) != 1 {
		t.Fatalf("租户 A 应只见自己的 1 条预算，得到 %d", len(list))
	}
	if all, _ := s.ListAllBudgets(context.Background()); len(all) != 2 {
		t.Fatalf("admin 应见全部 2 条预算，得到 %d", len(all))
	}
	if err := s.DeleteBudget(ctxA, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBudget(ctxA, b.ID); err != billing.ErrBudgetNotFound {
		t.Fatalf("二次删除应 ErrBudgetNotFound，得到 %v", err)
	}
}

// TestTokenUsageAggregation 周期用量：同键累加、按周期/租户过滤。
func TestTokenUsageAggregation(t *testing.T) {
	s := billingmemory.NewStore()
	if err := s.AddTokenUsageForTenant("t-a", "2026-10", "app1", "m1", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTokenUsageForTenant("t-a", "2026-10", "app1", "m1", 50); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTokenUsageForTenant("t-a", "2026-11", "app1", "m1", 7); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTokenUsageForTenant("", "2026-10", "app1", "m1", 1); err == nil {
		t.Fatal("空租户应拒绝")
	}
	ctxA := tenant.WithTenant(context.Background(), "t-a")
	rows, err := s.ListTokenUsage(ctxA, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Tokens != 150 {
		t.Fatalf("10 月应聚合为 150，得到 %+v", rows)
	}
	all, _ := s.ListAllTokenUsage(context.Background(), "")
	if len(all) != 2 {
		t.Fatalf("跨周期应 2 行，得到 %d", len(all))
	}
}

// TestHandlerBudgetEndpoints 租户侧 HTTP 契约：列表带状态、跨租户 404、越权 403。
func TestHandlerBudgetEndpoints(t *testing.T) {
	s := billingmemory.NewStore()
	h := billing.NewHandler(s, billing.WithModelCatalog(fixedPrice(map[string]float64{"m1": 100})))
	h.Authorize = allowIf(true)

	// 建预算（租户上下文）
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodPost, "/api/billing/budgets", `{"scope":"tenant","monthlyAmount":100,"warnRatio":0.8}`, "t-a"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建预算应 201，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var created billing.Budget
	decodeData(t, rec, &created)

	// 记账 100 万 token × 100 元/百万 = 100 元 → 恰好 100% → exceeded
	now := time.Now().UTC()
	if err := s.AddTokenUsageForTenant("t-a", billing.PeriodOf(now), "", "m1", 1_000_000); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodGet, "/api/billing/budgets", "", "t-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d", rec.Code)
	}
	var ov billing.BudgetOverview
	decodeData(t, rec, &ov)
	if len(ov.Rows) != 1 || ov.Rows[0].State != billing.BudgetExceeded || ov.Rows[0].EstimatedCost != 100 {
		t.Fatalf("预算状态错误: %+v", ov)
	}

	// 跨租户更新 → 404（不泄漏存在性）
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodPut, "/api/billing/budgets/"+created.ID, `{"monthlyAmount":50}`, "t-b"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户更新应 404，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 无写权限 → 403
	h.Authorize = allowIf(false)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(http.MethodDelete, "/api/billing/budgets/"+created.ID, "", "t-a"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无权限应 403，得到 %d", rec.Code)
	}
}
