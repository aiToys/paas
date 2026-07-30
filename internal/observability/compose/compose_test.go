package compose

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/observability"
)

func acmeCtx() context.Context { return context.WithValue(context.Background(), ctxKey{}, "t-acme") }

type ctxKey struct{}

// fakeMetrics 是可控的 MetricsReader（注入 series 供 ListAlerts 评估）。
type fakeMetrics struct{ series []observability.MetricSeries }

func (f *fakeMetrics) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	return f.series, nil
}

// fakeRules 是可控的 RuleStore（无 seed，完全隔离）。
type fakeRules struct{ rules []observability.AlertRule }

func (f *fakeRules) ListAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	return f.rules, nil
}
func (f *fakeRules) CreateAlertRule(ctx context.Context, rule observability.AlertRule) (observability.AlertRule, error) {
	rule.ID = "rule-1"
	f.rules = append(f.rules, rule)
	return rule, nil
}
func (f *fakeRules) DeleteAlertRule(ctx context.Context, id string) error {
	for i, r := range f.rules {
		if r.ID == id {
			f.rules = append(f.rules[:i], f.rules[i+1:]...)
			return nil
		}
	}
	return nil
}

func TestListAlertsEvaluatesAgainstMetrics(t *testing.T) {
	rules := &fakeRules{rules: []observability.AlertRule{
		{ID: "r1", Name: "CPU 高", MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
			Operator: observability.OpGT, Threshold: 50, Severity: observability.SeverityWarning, Enabled: true},
	}}
	metrics := &fakeMetrics{series: []observability.MetricSeries{
		{TargetType: observability.TargetApp, TargetID: "app-cs", Name: observability.MetricCPU, Current: 80},
	}}
	r := New(rules, metrics, nil, nil)
	alerts, err := r.ListAlerts(acmeCtx())
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Value != 80 || alerts[0].RuleID != "r1" {
		t.Fatalf("应基于 metrics 评估出 1 条告警，实际: %+v", alerts)
	}
}

func TestListAlertsSkipsDisabledAndNonBreached(t *testing.T) {
	rules := &fakeRules{rules: []observability.AlertRule{
		{ID: "disabled", Name: "禁用", MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
			Operator: observability.OpGT, Threshold: 50, Severity: observability.SeverityWarning, Enabled: false},
		{ID: "ok", Name: "未超阈值", MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
			Operator: observability.OpGT, Threshold: 90, Severity: observability.SeverityWarning, Enabled: true},
	}}
	metrics := &fakeMetrics{series: []observability.MetricSeries{
		{TargetType: observability.TargetApp, TargetID: "app-cs", Name: observability.MetricCPU, Current: 80},
	}}
	r := New(rules, metrics, nil, nil)
	alerts, _ := r.ListAlerts(acmeCtx())
	if len(alerts) != 0 {
		t.Fatalf("禁用规则与未超阈值规则不应触发，实际: %+v", alerts)
	}
}

func TestDelegatesRulesCRUD(t *testing.T) {
	rules := &fakeRules{}
	r := New(rules, &fakeMetrics{}, nil, nil)
	rule, _ := r.CreateAlertRule(acmeCtx(), observability.AlertRule{Name: "x"})
	list, _ := r.ListAlertRules(acmeCtx())
	if len(list) != 1 || list[0].ID != rule.ID {
		t.Fatalf("Create/ListAlertRules 委托错误: %+v", list)
	}
	if err := r.DeleteAlertRule(acmeCtx(), rule.ID); err != nil {
		t.Fatalf("DeleteAlertRule 委托失败: %v", err)
	}
	list, _ = r.ListAlertRules(acmeCtx())
	if len(list) != 0 {
		t.Fatalf("删除后应空，实际: %+v", list)
	}
}

// 编译期断言：Repo 实现完整 Repository 接口。
var _ observability.Repository = (*Repo)(nil)
