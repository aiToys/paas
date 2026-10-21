package billing

import (
	"testing"
	"time"
)

// TestEstimateAmountMatchesBillFormula 锁定「估算金额」与「生成账单」同一公式：
// 两处各写一份单价循环必然漂移（账单少算/多算而估算看不出来）。
func TestEstimateAmountMatchesBillFormula(t *testing.T) {
	counts := map[string]int{
		ResApplications: 3,
		ResWorkloads:    5,
		ResDataservices: 2,
		ResTokens:       123456,
	}
	want := 0.0
	for _, res := range ResourceOrder {
		want += float64(counts[res]) * PriceTable[res]
	}
	if got := EstimateAmount(counts); got != want {
		t.Fatalf("EstimateAmount=%v want %v", got, want)
	}
	if got := EstimateAmount(nil); got != 0 {
		t.Fatalf("空用量估算应为 0，got %v", got)
	}
}

// TestBuildAdminUsageRow 覆盖超限标记、配额缺省与 map 隔离。
func TestBuildAdminUsageRow(t *testing.T) {
	u := ResourceUsage{
		TenantID: "t1",
		Counts:   map[string]int{ResApplications: 60, ResWorkloads: 10, ResTokens: 1000},
		ByApp:    map[string]map[string]int{"app-1": {ResTokens: 1000}},
	}
	q := ResourceQuota{
		TenantID: "t1",
		Limits: map[string]int{
			ResApplications: 50,  // 超限
			ResWorkloads:    100, // 未超
			ResTokens:       Unlimited,
		},
	}
	row := BuildAdminUsageRow(u, q)
	if row.TenantID != "t1" {
		t.Fatalf("tenantId=%q", row.TenantID)
	}
	if len(row.Over) != 1 || row.Over[0] != ResApplications {
		t.Fatalf("over=%v，应仅标记 applications", row.Over)
	}
	if row.ByApp["app-1"][ResTokens] != 1000 {
		t.Fatalf("byApp 归因丢失: %v", row.ByApp)
	}
	// 修改行内 limits 不得影响传入配额（防 admin 视图污染仓储快照）
	row.Limits[ResApplications] = 999
	if q.Limits[ResApplications] != 50 {
		t.Fatalf("limits 未深拷贝，源配额被改写: %v", q.Limits)
	}
}

// TestBuildAdminUsageRowNoQuotaRecord 无配额记录时不臆造默认值（Limits 为空），
// 与运行时「按默认配额」的差异交由页面标注，不在此假装已配置。
func TestBuildAdminUsageRowNoQuotaRecord(t *testing.T) {
	row := BuildAdminUsageRow(ResourceUsage{
		TenantID:  "t2",
		Counts:    map[string]int{ResApplications: 1},
		UpdatedAt: time.Now(),
	}, ResourceQuota{})
	if len(row.Limits) != 0 {
		t.Fatalf("无配额记录时 Limits 应为空，got %v", row.Limits)
	}
	if len(row.Over) != 0 {
		t.Fatalf("无上限不应标记超限，got %v", row.Over)
	}
}

// TestAdminUsageDimensionsHonesty 维度可用性必须如实反映采集现状：
// 模型 / API Key / 周期三维平台未采集，不得声称为可用。
func TestAdminUsageDimensionsHonesty(t *testing.T) {
	got := map[string]AdminUsageDimension{}
	for _, d := range AdminUsageDimensions() {
		got[d.Key] = d
	}
	for _, k := range []string{"tenant", "app", "resource"} {
		if d, ok := got[k]; !ok || !d.Available {
			t.Fatalf("维度 %s 应为可用: %+v", k, d)
		}
	}
	for _, k := range []string{"model", "apikey", "period"} {
		d, ok := got[k]
		if !ok {
			t.Fatalf("缺少维度 %s 说明", k)
		}
		if d.Available {
			t.Fatalf("维度 %s 尚未采集，不应标记可用", k)
		}
		if d.Note == "" {
			t.Fatalf("维度 %s 不可用时必须给出原因说明", k)
		}
	}
}
