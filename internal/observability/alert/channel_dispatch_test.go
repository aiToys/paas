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

// fakeChannels 可控渠道源（按租户返回渠道）。
type fakeChannels struct {
	mu       sync.Mutex
	byTenant map[string][]observability.NotificationChannel
}

func (f *fakeChannels) ListNotificationChannels(ctx context.Context) ([]observability.NotificationChannel, error) {
	tid, _ := tenant.IDOrErr(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]observability.NotificationChannel{}, f.byTenant[tid]...), nil
}

// hookServer 记录收到的 webhook 请求体与请求头。
type hookServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
}

func newHookServer(t *testing.T) *hookServer {
	t.Helper()
	h := &hookServer{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.bodies = append(h.bodies, body)
		h.headers = append(h.headers, r.Header.Clone())
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hookServer) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

func (h *hookServer) last() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) == 0 {
		return nil
	}
	return h.bodies[len(h.bodies)-1]
}

// fireOnce 跑两轮评估（pending → firing），并等待异步出站完成。
func fireOnce(t *testing.T, e *Engine, metrics *fakeMetrics) {
	t.Helper()
	metrics.byTenant["t-acme"] = []observability.MetricSeries{highCPU(95)}
	e.evaluate(context.Background())
	e.evaluate(context.Background())
	time.Sleep(50 * time.Millisecond)
}

// TestChannelDispatchIgnoresDirectURL：规则引用渠道时只投递渠道，直连 WebhookURL 不再使用。
func TestChannelDispatchIgnoresDirectURL(t *testing.T) {
	direct := newHookServer(t)
	channel := newHookServer(t)
	rule := testRule()
	rule.WebhookURL = direct.srv.URL
	rule.ChannelIDs = []string{"ch-1"}

	rules := &fakeRules{rules: []observability.AlertRule{rule}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{}}
	chans := &fakeChannels{byTenant: map[string][]observability.NotificationChannel{
		"t-acme": {{ID: "ch-1", TenantID: "t-acme", Name: "值班群", Kind: "webhook",
			URL: channel.srv.URL, Enabled: true}},
	}}
	e := NewEngine(rules, metrics, time.Hour, nil, nil).WithChannels(chans)
	fireOnce(t, e, metrics)

	if channel.count() != 1 {
		t.Fatalf("渠道应收到 1 次 firing 通知, got %d", channel.count())
	}
	if direct.count() != 0 {
		t.Fatalf("引用渠道后直连 URL 不应再收通知, got %d", direct.count())
	}
	if got := channel.last()["status"]; got != observability.AlertFiring {
		t.Fatalf("通知 status 应为 firing, got %v", got)
	}
	if got := channel.last()["repeat"]; got != false {
		t.Fatalf("首次通知 repeat 应为 false, got %v", got)
	}
}

// TestChannelDispatchSkipsDisabledAndMuted：停用渠道与静默期内渠道都不投递。
func TestChannelDispatchSkipsDisabledAndMuted(t *testing.T) {
	disabled := newHookServer(t)
	muted := newHookServer(t)
	active := newHookServer(t)
	rule := testRule()
	rule.ChannelIDs = []string{"ch-off", "ch-mute", "ch-on"}

	rules := &fakeRules{rules: []observability.AlertRule{rule}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{}}
	chans := &fakeChannels{byTenant: map[string][]observability.NotificationChannel{
		"t-acme": {
			{ID: "ch-off", Name: "停用", Kind: "webhook", URL: disabled.srv.URL, Enabled: false},
			{ID: "ch-mute", Name: "静默", Kind: "webhook", URL: muted.srv.URL, Enabled: true,
				MuteUntil: time.Now().Add(time.Hour)},
			{ID: "ch-on", Name: "正常", Kind: "webhook", URL: active.srv.URL, Enabled: true},
		},
	}}
	e := NewEngine(rules, metrics, time.Hour, nil, nil).WithChannels(chans)
	fireOnce(t, e, metrics)

	if disabled.count() != 0 {
		t.Fatalf("停用渠道不应投递, got %d", disabled.count())
	}
	if muted.count() != 0 {
		t.Fatalf("静默期渠道不应投递, got %d", muted.count())
	}
	if active.count() != 1 {
		t.Fatalf("正常渠道应收到 1 次通知, got %d", active.count())
	}
}

// TestChannelRepeatReminder：冷却窗口内不重发；窗口到期后重发 repeat=true 的提醒。
func TestChannelRepeatReminder(t *testing.T) {
	hook := newHookServer(t)
	rule := testRule()
	rule.ChannelIDs = []string{"ch-1"}

	rules := &fakeRules{rules: []observability.AlertRule{rule}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{}}
	chans := &fakeChannels{byTenant: map[string][]observability.NotificationChannel{
		"t-acme": {{ID: "ch-1", Name: "值班", Kind: "webhook", URL: hook.srv.URL,
			Enabled: true, CooldownMinutes: 30}},
	}}
	e := NewEngine(rules, metrics, time.Hour, nil, nil).WithChannels(chans)
	fireOnce(t, e, metrics)
	if hook.count() != 1 {
		t.Fatalf("首次 firing 应通知 1 次, got %d", hook.count())
	}

	// 冷却窗口内持续 firing：不重发。
	metrics.byTenant["t-acme"] = []observability.MetricSeries{highCPU(96)}
	e.evaluate(context.Background())
	time.Sleep(50 * time.Millisecond)
	if hook.count() != 1 {
		t.Fatalf("冷却窗口内不应重发, got %d", hook.count())
	}

	// 把上次通知时间推到窗口外：应重发一条 repeat 提醒。
	e.mu.Lock()
	for k := range e.lastNotify {
		e.lastNotify[k] = time.Now().Add(-time.Hour)
	}
	e.mu.Unlock()
	e.evaluate(context.Background())
	time.Sleep(50 * time.Millisecond)
	if hook.count() != 2 {
		t.Fatalf("冷却到期应重发, got %d", hook.count())
	}
	if got := hook.last()["repeat"]; got != true {
		t.Fatalf("重复提醒 repeat 应为 true, got %v", got)
	}
}

// TestChannelResolvedNotifies：恢复时渠道收到 status=resolved（且不受冷却抑制）。
func TestChannelResolvedNotifies(t *testing.T) {
	hook := newHookServer(t)
	rule := testRule()
	rule.ChannelIDs = []string{"ch-1"}

	rules := &fakeRules{rules: []observability.AlertRule{rule}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{}}
	chans := &fakeChannels{byTenant: map[string][]observability.NotificationChannel{
		"t-acme": {{ID: "ch-1", Name: "值班", Kind: "webhook", URL: hook.srv.URL,
			Enabled: true, CooldownMinutes: 600}},
	}}
	e := NewEngine(rules, metrics, time.Hour, nil, nil).WithChannels(chans)
	fireOnce(t, e, metrics)
	if hook.count() != 1 {
		t.Fatalf("firing 应先通知 1 次, got %d", hook.count())
	}

	metrics.byTenant["t-acme"] = []observability.MetricSeries{highCPU(10)}
	e.evaluate(context.Background())
	time.Sleep(50 * time.Millisecond)
	if hook.count() != 2 {
		t.Fatalf("恢复应再通知 1 次, got %d", hook.count())
	}
	if got := hook.last()["status"]; got != observability.AlertResolved {
		t.Fatalf("恢复通知 status 应为 resolved, got %v", got)
	}
	// 恢复后冷却跟踪键应清理。
	e.mu.RLock()
	n := len(e.lastNotify)
	e.mu.RUnlock()
	if n != 0 {
		t.Fatalf("恢复后 lastNotify 应清空, got %d", n)
	}
}

// TestChannelDanglingReference：规则引用的渠道不存在（已删/跨租户）时跳过，不 panic 且不影响直连回落。
func TestChannelDanglingReference(t *testing.T) {
	hook := newHookServer(t)
	rule := testRule()
	rule.ChannelIDs = []string{"ghost"}

	rules := &fakeRules{rules: []observability.AlertRule{rule}}
	metrics := &fakeMetrics{byTenant: map[string][]observability.MetricSeries{}}
	chans := &fakeChannels{byTenant: map[string][]observability.NotificationChannel{
		"t-acme": {{ID: "ch-1", Name: "值班", Kind: "webhook", URL: hook.srv.URL, Enabled: true}},
	}}
	e := NewEngine(rules, metrics, time.Hour, nil, nil).WithChannels(chans)
	fireOnce(t, e, metrics) // 不应 panic

	if hook.count() != 0 {
		t.Fatalf("悬空渠道 ID 不应投递到其他渠道, got %d", hook.count())
	}
	alerts, _ := e.ListAlerts(acmeCtx(), "", "")
	if len(alerts) != 1 || alerts[0].Status != observability.AlertFiring {
		t.Fatalf("告警状态机不应受渠道解析影响, got %+v", alerts)
	}
}
