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

// TestTenantIsolation 验证指标/规则按租户隔离。
// 降级模式 series 空（ListMetrics 返空不泄漏）；rules 保留 seed，按租户过滤。
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
	// 规则按租户隔离：seed rule-acme-cpu 属 t-acme，globex 看不到。
	acmeRules, _ := s.ListAlertRules(acmeCtx())
	if len(acmeRules) == 0 {
		t.Fatal("acme 应有 seed 规则")
	}
	for _, r := range acmeRules {
		if r.TenantID != "t-acme" {
			t.Fatalf("泄漏其他租户规则: %s", r.ID)
		}
	}
	globexRules, _ := s.ListAlertRules(globexCtx())
	if len(globexRules) != 0 {
		t.Fatalf("globex 不应见 acme 规则，got %d", len(globexRules))
	}
}

// TestAlertEvaluation 降级模式 series 空 -> ListAlerts 无 firing 告警。
// 接真实后端时 series 由 real store 提供，告警评估在 real 模式测。
func TestAlertEvaluation(t *testing.T) {
	s := NewStore()
	alerts, err := s.ListAlerts(acmeCtx(), "", "")
	if err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("降级模式 series 空应无 firing 告警，got %d", len(alerts))
	}
	// globex 无规则 -> 空告警
	gAlerts, _ := s.ListAlerts(globexCtx(), "", "")
	if len(gAlerts) != 0 {
		t.Fatalf("globex 无规则不应有告警，got %d", len(gAlerts))
	}
}

// TestCreateAndDeleteRule 验证规则 CRUD（不依赖告警 firing，降级模式 series 空）。
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
	// 创建后列表应含新规则
	list, _ := s.ListAlertRules(acmeCtx())
	found := false
	for _, x := range list {
		if x.ID == r.ID {
			found = true
			if x.Severity != observability.SeverityCritical {
				t.Fatalf("严重级别应透传，got %s", x.Severity)
			}
		}
	}
	if !found {
		t.Fatal("创建后列表应含新规则")
	}
	if err := s.DeleteAlertRule(acmeCtx(), r.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	// 删除后列表不含
	list2, _ := s.ListAlertRules(acmeCtx())
	for _, x := range list2 {
		if x.ID == r.ID {
			t.Fatal("删除后列表不应含该规则")
		}
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

// TestLogsFilterByLevel 验证级别过滤（注入日志后按 level 过滤 + 倒序）。
func TestLogsFilterByLevel(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.logs["t-acme"] = []observability.LogEntry{
		{ID: "l1", TenantID: "t-acme", AppID: "app-cs", Level: observability.LevelInfo, Message: "启动", Timestamp: now},
		{ID: "l2", TenantID: "t-acme", AppID: "app-cs", Level: observability.LevelError, Message: "失败", Timestamp: now.Add(time.Second)},
		{ID: "l3", TenantID: "t-acme", AppID: "app-cs", Level: observability.LevelError, Message: "再次失败", Timestamp: now.Add(2 * time.Second)},
	}
	all, _ := s.ListLogs(acmeCtx(), "", "", "", "", "", 100)
	if len(all) != 3 {
		t.Fatalf("全部应 3 条，got %d", len(all))
	}
	errs, _ := s.ListLogs(acmeCtx(), "", "", "", observability.LevelError, "", 100)
	if len(errs) != 2 {
		t.Fatalf("error 应 2 条，got %d", len(errs))
	}
	for _, l := range errs {
		if l.Level != observability.LevelError {
			t.Fatalf("级别过滤应只返回 error，got %s", l.Level)
		}
	}
	// 倒序：最新在前
	if !errs[0].Timestamp.After(errs[len(errs)-1].Timestamp) {
		t.Fatal("日志应按时间倒序")
	}
}

// TestLogsInvalidLevel 验证非法级别报错。
func TestLogsInvalidLevel(t *testing.T) {
	s := NewStore()
	if _, err := s.ListLogs(acmeCtx(), "", "", "", "fatal", "", 10); err == nil {
		t.Fatal("非法级别应报错")
	}
}

// TestLogsKeywordSearch 验证关键字搜索（注入日志后按关键字过滤）。
func TestLogsKeywordSearch(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.logs["t-acme"] = []observability.LogEntry{
		{ID: "l1", TenantID: "t-acme", AppID: "app-cs", Level: observability.LevelInfo, Message: "路由表更新", Timestamp: now},
		{ID: "l2", TenantID: "t-acme", AppID: "app-cs", Level: observability.LevelInfo, Message: "健康检查", Timestamp: now.Add(time.Second)},
	}
	hits, _ := s.ListLogs(acmeCtx(), "", "", "", "", "路由", 100)
	if len(hits) != 1 {
		t.Fatalf("关键字 '路由' 应命中 1 条，got %d", len(hits))
	}
	for _, l := range hits {
		if !strings.Contains(l.Message, "路由") {
			t.Fatalf("关键字过滤应只返回含关键字的: %s", l.Message)
		}
	}
}

// TestLogsAppFilter 验证按 appId 过滤。
func TestLogsAppFilter(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.logs["t-acme"] = []observability.LogEntry{
		{ID: "l1", TenantID: "t-acme", AppID: "app-cs", Level: observability.LevelInfo, Message: "a", Timestamp: now},
		{ID: "l2", TenantID: "t-acme", AppID: "app-etl", Level: observability.LevelInfo, Message: "b", Timestamp: now.Add(time.Second)},
	}
	cs, _ := s.ListLogs(acmeCtx(), "app-cs", "", "", "", "", 100)
	if len(cs) != 1 {
		t.Fatalf("app-cs 应 1 条，got %d", len(cs))
	}
	if cs[0].AppID != "app-cs" {
		t.Fatalf("appId 过滤错误，got %s", cs[0].AppID)
	}
}

// TestLogsCrossTenantHidden 验证日志按租户隔离（注入 acme 日志，globex 看不到）。
func TestLogsCrossTenantHidden(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.logs["t-acme"] = []observability.LogEntry{
		{ID: "l1", TenantID: "t-acme", AppID: "app-cs", Level: observability.LevelInfo, Message: "a", Timestamp: now},
	}
	acme, _ := s.ListLogs(acmeCtx(), "", "", "", "", "", 50)
	if len(acme) != 1 {
		t.Fatalf("acme 应见 1 条，got %d", len(acme))
	}
	globex, _ := s.ListLogs(globexCtx(), "", "", "", "", "", 50)
	if len(globex) != 0 {
		t.Fatalf("globex 不应见 acme 日志，got %d", len(globex))
	}
}

// TestListLogsDataserviceTarget 验证日志按 targetType 维度路由：
// targetType=dataservice 走 TargetType/TargetID 过滤；否则走原 appID 维度（向后兼容）。
func TestListLogsDataserviceTarget(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	s.logs["t-acme"] = []observability.LogEntry{
		{ID: "l1", TargetType: observability.TargetDataservice, TargetID: "ds-1", Level: observability.LevelError, Message: "conn refused", Timestamp: time.Now()},
		{ID: "l2", AppID: "app-x", Level: observability.LevelInfo, Message: "app log", Timestamp: time.Now()},
	}
	// dataservice 维度查：只命中 ds-1 的 l1
	got, err := s.ListLogs(ctx, "", observability.TargetDataservice, "ds-1", "", "", 10)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "l1" {
		t.Fatalf("dataservice 维度期望命中 l1，实际 %+v", got)
	}
	// app 维度查（appID 非空，targetType 空）：只命中 app-x 的 l2
	got2, _ := s.ListLogs(ctx, "app-x", "", "", "", "", 10)
	if len(got2) != 1 || got2[0].ID != "l2" {
		t.Fatalf("app 维度期望命中 l2，实际 %+v", got2)
	}
}

// TestTracesFilterByStatus 验证状态过滤（注入 trace 后按 status 过滤 + 倒序）。
func TestTracesFilterByStatus(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.traces["t-acme"] = []observability.Trace{
		{ID: "t1", TenantID: "t-acme", AppID: "app-cs", Operation: "GET /v1/models", Status: observability.TraceSuccess, DurationMs: 50, StartedAt: now, Spans: []observability.Span{{ID: "s1", Operation: "db", Service: "svc"}}},
		{ID: "t2", TenantID: "t-acme", AppID: "app-cs", Operation: "POST /v1/chat", Status: observability.TraceError, DurationMs: 120, StartedAt: now.Add(time.Second), Spans: []observability.Span{{ID: "s2", Operation: "upstream", Service: "svc"}}},
	}
	all, _ := s.ListTraces(acmeCtx(), "", "", 50)
	if len(all) != 2 {
		t.Fatalf("全部应 2 条，got %d", len(all))
	}
	errs, err := s.ListTraces(acmeCtx(), "", observability.TraceError, 50)
	if err != nil {
		t.Fatalf("ListTraces 失败: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("error 应 1 条，got %d", len(errs))
	}
	for _, tr := range errs {
		if tr.Status != observability.TraceError {
			t.Fatalf("状态过滤应只返回 error，got %s", tr.Status)
		}
	}
	// 倒序：最新在前
	if !all[0].StartedAt.After(all[len(all)-1].StartedAt) {
		t.Fatal("trace 应按开始时间倒序")
	}
}

// TestTracesInvalidStatus 验证非法状态报错。
func TestTracesInvalidStatus(t *testing.T) {
	s := NewStore()
	if _, err := s.ListTraces(acmeCtx(), "", "failed", 10); err == nil {
		t.Fatal("非法状态应报错")
	}
}

// TestTracesCrossTenantHidden 验证 trace 按租户隔离（注入 acme trace，globex 看不到）。
func TestTracesCrossTenantHidden(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.traces["t-acme"] = []observability.Trace{
		{ID: "t1", TenantID: "t-acme", AppID: "app-cs", Operation: "GET /v1/models", Status: observability.TraceSuccess, DurationMs: 50, StartedAt: now, Spans: []observability.Span{{ID: "s1", Operation: "db", Service: "svc"}}},
	}
	acme, _ := s.ListTraces(acmeCtx(), "", "", 20)
	if len(acme) != 1 {
		t.Fatalf("acme 应见 1 条，got %d", len(acme))
	}
	globex, _ := s.ListTraces(globexCtx(), "", "", 20)
	if len(globex) != 0 {
		t.Fatalf("globex 不应见 acme trace，got %d", len(globex))
	}
}
