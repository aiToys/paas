package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// TestMetricsLazilyAdvance 验证惰性补点：查询后点数增长。
func TestMetricsLazilyAdvance(t *testing.T) {
	s := NewStore()
	key := observability.TargetApp + "|app-old|" + observability.MetricCPU
	s.series[key] = observability.MetricSeries{
		ID: "ms-old", TenantID: "t-acme", TargetType: observability.TargetApp,
		TargetID: "app-old", Name: observability.MetricCPU, Unit: "%", Current: 40,
		Points: []observability.MetricPoint{{TS: time.Now().Add(-2 * time.Minute), Value: 40}},
	}
	before := len(s.series[key].Points)
	list, err := s.ListMetrics(acmeCtx(), observability.TargetApp, "app-old", observability.MetricCPU)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应返回 1 条，got %d", len(list))
	}
	if len(list[0].Points) <= before {
		t.Fatalf("惰性补点后点数应增长，before=%d after=%d", before, len(list[0].Points))
	}
	if len(list[0].Points) > observability.MaxPoints {
		t.Fatalf("点数不应超过上限 %d，got %d", observability.MaxPoints, len(list[0].Points))
	}
}

// TestTenantIsolation 验证指标/规则按租户隔离。
func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, _ := s.ListMetrics(acmeCtx(), "", "", "")
	globex, _ := s.ListMetrics(globexCtx(), "", "", "")
	for _, m := range acme {
		if m.TenantID != "t-acme" {
			t.Fatalf("泄漏其他租户指标: %s", m.ID)
		}
	}
	for _, m := range globex {
		if m.TenantID != "t-globex" {
			t.Fatalf("泄漏其他租户指标: %s", m.ID)
		}
	}
}

// TestAlertEvaluation 验证告警即时评估（cpu>50 命中 acme app）。
func TestAlertEvaluation(t *testing.T) {
	s := NewStore()
	alerts, err := s.ListAlerts(acmeCtx())
	if err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("应至少有 1 条 firing 告警（cpu>50）")
	}
	for _, a := range alerts {
		if a.RuleID != "rule-acme-cpu" {
			t.Fatalf("告警来源规则错误，got %s", a.RuleID)
		}
	}
	// globex 无规则 -> 空告警
	gAlerts, _ := s.ListAlerts(globexCtx())
	if len(gAlerts) != 0 {
		t.Fatalf("globex 无规则不应有告警，got %d", len(gAlerts))
	}
}

// TestCreateAndDeleteRule 验证规则 CRUD + 评估生效。
func TestCreateAndDeleteRule(t *testing.T) {
	s := NewStore()
	r, err := s.CreateAlertRule(acmeCtx(), observability.AlertRule{
		Name: "高延迟", MetricName: observability.MetricLatency, TargetType: observability.TargetApp,
		TargetID: "app-cs", Operator: observability.OpGT, Threshold: 50,
		Severity: observability.SeverityCritical, Enabled: true,
	})
	if err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	if r.ID == "" {
		t.Fatal("应分配 ID")
	}
	// 该规则应触发（latency 120 > 50）
	alerts, _ := s.ListAlerts(acmeCtx())
	found := false
	for _, a := range alerts {
		if a.RuleID == r.ID {
			found = true
			if a.Severity != observability.SeverityCritical {
				t.Fatalf("严重级别应透传，got %s", a.Severity)
			}
		}
	}
	if !found {
		t.Fatal("新规则应触发告警")
	}
	if err := s.DeleteAlertRule(acmeCtx(), r.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
}

// TestRuleValidate 验证规则校验。
func TestRuleValidate(t *testing.T) {
	s := NewStore()
	_, err := s.CreateAlertRule(acmeCtx(), observability.AlertRule{
		Name: "bad", MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
		Operator: "!=" /*非法*/, Threshold: 10, Severity: observability.SeverityWarning,
	})
	if err == nil {
		t.Fatal("非法 operator 应被拒")
	}
}

// TestCrossTenantRuleHidden 验证跨租户删除规则不泄漏。
func TestCrossTenantRuleHidden(t *testing.T) {
	s := NewStore()
	if err := s.DeleteAlertRule(globexCtx(), "rule-acme-cpu"); err == nil {
		t.Fatal("跨租户删除应失败（不泄漏 acme 规则存在）")
	}
}

// TestLogsLazilyAppended 验证日志惰性补点：首次查询返回日志。
func TestLogsLazilyAppended(t *testing.T) {
	s := NewStore()
	logs, err := s.ListLogs(acmeCtx(), "", "", "", 50)
	if err != nil {
		t.Fatalf("ListLogs 失败: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("首次查询应有惰性补点的日志")
	}
	// 倒序：最新在前
	if !logs[0].Timestamp.After(logs[len(logs)-1].Timestamp) {
		t.Fatal("日志应按时间倒序")
	}
}

// TestLogsFilterByLevel 验证级别过滤。
func TestLogsFilterByLevel(t *testing.T) {
	s := NewStore()
	all, _ := s.ListLogs(acmeCtx(), "", "", "", 100)
	errs, _ := s.ListLogs(acmeCtx(), "", observability.LevelError, "", 100)
	for _, l := range errs {
		if l.Level != observability.LevelError {
			t.Fatalf("级别过滤应只返回 error，got %s", l.Level)
		}
	}
	// error 数应 <= 全部
	if len(errs) > len(all) {
		t.Fatalf("过滤后数 %d 应 <= 全部 %d", len(errs), len(all))
	}
}

// TestLogsInvalidLevel 验证非法级别报错。
func TestLogsInvalidLevel(t *testing.T) {
	s := NewStore()
	if _, err := s.ListLogs(acmeCtx(), "", "fatal", "", 10); err == nil {
		t.Fatal("非法级别应报错")
	}
}

// TestLogsTenantIsolation 验证日志按租户隔离。
func TestLogsTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, _ := s.ListLogs(acmeCtx(), "", "", "", 50)
	for _, l := range acme {
		if l.TenantID != "t-acme" {
			t.Fatalf("acme 日志不应含其它租户: %+v", l)
		}
	}
	// globex 也有日志（logApps 配置了）
	globex, _ := s.ListLogs(globexCtx(), "", "", "", 50)
	if len(globex) == 0 {
		t.Fatal("globex 应有日志")
	}
	for _, l := range globex {
		if l.TenantID != "t-globex" {
			t.Fatalf("globex 日志不应含 acme: %+v", l)
		}
	}
}

// TestLogsKeywordSearch 验证关键字搜索。
func TestLogsKeywordSearch(t *testing.T) {
	s := NewStore()
	_, _ = s.ListLogs(acmeCtx(), "", "", "", 100) // 触发补点
	// 搜索固定关键字（模板里 "路由"）
	hits, _ := s.ListLogs(acmeCtx(), "", "", "路由", 100)
	for _, l := range hits {
		if !strings.Contains(l.Message, "路由") {
			t.Fatalf("关键字过滤应只返回含关键字的: %s", l.Message)
		}
	}
}

// TestTracesLazilyAppended 验证 trace 惰性补点 + span 结构。
func TestTracesLazilyAppended(t *testing.T) {
	s := NewStore()
	traces, err := s.ListTraces(acmeCtx(), "", "", 20)
	if err != nil {
		t.Fatalf("ListTraces 失败: %v", err)
	}
	if len(traces) == 0 {
		t.Fatal("首次查询应有惰性补点的 trace")
	}
	for _, tr := range traces {
		if len(tr.Spans) == 0 {
			t.Fatalf("trace %s 应含 span", tr.ID)
		}
		if tr.DurationMs <= 0 {
			t.Fatalf("trace %s 总时长应 >0", tr.ID)
		}
		if !observability.ValidTraceStatus(tr.Status) {
			t.Fatalf("trace %s 非法状态 %s", tr.ID, tr.Status)
		}
	}
	// 倒序
	if !traces[0].StartedAt.After(traces[len(traces)-1].StartedAt) {
		t.Fatal("trace 应按开始时间倒序")
	}
}

// TestTracesFilterByStatus 验证状态过滤。
func TestTracesFilterByStatus(t *testing.T) {
	s := NewStore()
	_, _ = s.ListTraces(acmeCtx(), "", "", 50) // 触发补点
	errs, err := s.ListTraces(acmeCtx(), "", observability.TraceError, 50)
	if err != nil {
		t.Fatalf("ListTraces 失败: %v", err)
	}
	for _, tr := range errs {
		if tr.Status != observability.TraceError {
			t.Fatalf("状态过滤应只返回 error，got %s", tr.Status)
		}
	}
}

// TestTracesInvalidStatus 验证非法状态报错。
func TestTracesInvalidStatus(t *testing.T) {
	s := NewStore()
	if _, err := s.ListTraces(acmeCtx(), "", "failed", 10); err == nil {
		t.Fatal("非法状态应报错")
	}
}

// TestTracesTenantIsolation 验证 trace 按租户隔离。
func TestTracesTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, _ := s.ListTraces(acmeCtx(), "", "", 20)
	for _, tr := range acme {
		if tr.TenantID != "t-acme" {
			t.Fatalf("acme trace 不应含其它租户: %+v", tr)
		}
	}
	globex, _ := s.ListTraces(globexCtx(), "", "", 20)
	if len(globex) == 0 {
		t.Fatal("globex 应有 trace")
	}
}
