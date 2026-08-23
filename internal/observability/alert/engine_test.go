package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeRules 可控规则源（跨租户全量）。
type fakeRules struct {
	mu    sync.Mutex
	rules []observability.AlertRule
}

func (f *fakeRules) ListAllAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]observability.AlertRule{}, f.rules...), nil
}

// fakeMetrics 可控 metrics reader（按租户返 series）。
type fakeMetrics struct {
	mu      sync.Mutex
	byTenant map[string][]observability.MetricSeries
}

func (f *fakeMetrics) ListMetrics(ctx context.Context, targetType, targetID, name string) ([]observability.MetricSeries, error) {
	tid, _ := tenant.IDOrErr(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]observability.MetricSeries{}, f.byTenant[tid]...), nil
}

func highCPU(value float64) observability.MetricSeries {
	return observability.MetricSeries{
		TargetType: observability.TargetApp, TargetID: "app-1",
		Name: observability.MetricCPU, Current: value,
	}
}

func testRule() observability.AlertRule {
	return observability.AlertRule{
		ID: "rule-1", TenantID: "t-acme", Name: "CPU 高",
		MetricName: observability.MetricCPU, TargetType: observability.TargetApp,
		Operator: observability.OpGT, Threshold: 80,
		Severity: observability.SeverityCritical, Enabled: true,
	}
}

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

// TestEngineStateLifecycle：首次命中 pending → 第二次命中 firing → 恢复 resolved。
func TestEngineStateLifecycle(t *testing.T) {
	rules := &fakeRules{rules: []observability.AlertRule{testRule()}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{}}
	e := NewEngine(rules, metrics, time.Hour) // 不 Start，手动 evaluate
	ctx := context.Background()

	metrics.byTenant["t-acme"] = []observability.MetricSeries{highCPU(95)}
	e.evaluate(ctx)
	alerts, _ := e.ListAlerts(acmeCtx(), "", "")
	if len(alerts) != 1 || alerts[0].Status != observability.AlertPending {
		t.Fatalf("首轮应 pending, got %+v", alerts)
	}

	e.evaluate(ctx)
	alerts, _ = e.ListAlerts(acmeCtx(), "", "")
	if len(alerts) != 1 || alerts[0].Status != observability.AlertFiring {
		t.Fatalf("第二轮应 firing, got %+v", alerts)
	}

	// 恢复：值降回阈值下。
	metrics.byTenant["t-acme"] = []observability.MetricSeries{highCPU(30)}
	e.evaluate(ctx)
	alerts, _ = e.ListAlerts(acmeCtx(), "", "")
	if len(alerts) != 1 || alerts[0].Status != observability.AlertResolved {
		t.Fatalf("恢复应 resolved, got %+v", alerts)
	}

	// resolved 再一轮后清理。
	e.evaluate(ctx)
	alerts, _ = e.ListAlerts(acmeCtx(), "", "")
	if len(alerts) != 0 {
		t.Fatalf("resolved 展示一轮后应清理, got %d", len(alerts))
	}
}

// TestEngineWebhookOnFiring：firing 转变时 webhook 收到 POST（pending 不发、持续 firing 不重发）。
func TestEngineWebhookOnFiring(t *testing.T) {
	var mu sync.Mutex
	var payloads []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		payloads = append(payloads, p)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	rule := testRule()
	rule.WebhookURL = srv.URL
	rules := &fakeRules{rules: []observability.AlertRule{rule}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{
		"t-acme": {highCPU(95)},
	}}
	e := NewEngine(rules, metrics, time.Hour)
	ctx := context.Background()

	e.evaluate(ctx) // pending，不发
	if n := len(payloads); n != 0 {
		t.Fatalf("pending 不应发 webhook, got %d", n)
	}
	e.evaluate(ctx) // firing，发
	e.evaluate(ctx) // 持续 firing，不重发
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 1 {
		t.Fatalf("应恰好 1 次 webhook（firing 转变时）, got %d", len(payloads))
	}
	if payloads[0]["status"] != observability.AlertFiring {
		t.Fatalf("webhook status 应 firing, got %v", payloads[0]["status"])
	}
}

// TestEngineDisabledRuleSkipped：disabled 规则不评估。
func TestEngineDisabledRuleSkipped(t *testing.T) {
	rule := testRule()
	rule.Enabled = false
	rules := &fakeRules{rules: []observability.AlertRule{rule}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{
		"t-acme": {highCPU(95)},
	}}
	e := NewEngine(rules, metrics, time.Hour)
	e.evaluate(context.Background())
	e.evaluate(context.Background())
	alerts, _ := e.ListAlerts(acmeCtx(), "", "")
	if len(alerts) != 0 {
		t.Fatalf("disabled 规则不应产告警, got %d", len(alerts))
	}
}

// TestEngineTenantIsolation：t-acme 告警对 t-globex 不可见。
func TestEngineTenantIsolation(t *testing.T) {
	rules := &fakeRules{rules: []observability.AlertRule{testRule()}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{
		"t-acme": {highCPU(95)},
	}}
	e := NewEngine(rules, metrics, time.Hour)
	e.evaluate(context.Background())
	e.evaluate(context.Background())
	globex := tenant.WithTenant(context.Background(), "t-globex")
	alerts, _ := e.ListAlerts(globex, "", "")
	if len(alerts) != 0 {
		t.Fatalf("跨租户应不可见, got %d", len(alerts))
	}
}

// TestEngineTargetFilter：targetType/targetId 维度过滤。
func TestEngineTargetFilter(t *testing.T) {
	rules := &fakeRules{rules: []observability.AlertRule{testRule()}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{
		"t-acme": {highCPU(95)},
	}}
	e := NewEngine(rules, metrics, time.Hour)
	e.evaluate(context.Background())
	e.evaluate(context.Background())
	ds, _ := e.ListAlerts(acmeCtx(), observability.TargetDataservice, "")
	if len(ds) != 0 {
		t.Fatalf("dataservice 维度应 0, got %d", len(ds))
	}
	app, _ := e.ListAlerts(acmeCtx(), observability.TargetApp, "app-1")
	if len(app) != 1 {
		t.Fatalf("app-1 维度应 1, got %d", len(app))
	}
}
