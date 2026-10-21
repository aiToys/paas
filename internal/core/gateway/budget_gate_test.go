package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/pkg/tenant"
)

// chatBody 是最小合法 chat 请求体。
const chatBody = `{"model":"ghost","messages":[{"role":"user","content":"hi"}]}`

func doChat(t *testing.T, gw *gateway.Gateway, tid string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
	if tid != "" {
		r = r.WithContext(tenant.WithTenant(r.Context(), tid))
	}
	w := httptest.NewRecorder()
	gateway.ChatCompletions(gw, nil, nil)(w, r)
	return w
}

// TestBudgetGateRejects 超限时在路由解析之前拦下：即便模型不存在，也返回 429 而非 404。
func TestBudgetGateRejects(t *testing.T) {
	gw := gateway.New()
	var gotTenant, gotApp string
	gw.BudgetGate = func(_ context.Context, tid, app string) error {
		gotTenant, gotApp = tid, app
		return gateway.ErrBudgetExceeded
	}
	w := doChat(t, gw, "t-acme")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("超预算应 429，得到 %d: %s", w.Code, w.Body.String())
	}
	if gotTenant != "t-acme" {
		t.Fatalf("闸门应收到租户 t-acme，得到 %q", gotTenant)
	}
	if gotApp != "" {
		t.Fatalf("无应用级 Key 时 appID 应为空，得到 %q", gotApp)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 应带 Retry-After 提示重试窗口")
	}
}

// TestBudgetGateFailOpen 闸门内部错误（非超限）放行，请求继续走正常解析（模型不存在 → 404）。
func TestBudgetGateFailOpen(t *testing.T) {
	gw := gateway.New()
	gw.BudgetGate = func(context.Context, string, string) error {
		return errors.New("billing store unavailable")
	}
	w := doChat(t, gw, "t-acme")
	if w.Code != http.StatusNotFound {
		t.Fatalf("闸门故障应放行并落到模型解析（404），得到 %d: %s", w.Code, w.Body.String())
	}
}

// TestBudgetGateSkipsUnknownTenant 无租户上下文（平台内部调用）不查预算。
func TestBudgetGateSkipsUnknownTenant(t *testing.T) {
	gw := gateway.New()
	called := false
	gw.BudgetGate = func(context.Context, string, string) error {
		called = true
		return nil
	}
	doChat(t, gw, "")
	if called {
		t.Fatal("无租户上下文不应调用预算闸门")
	}
}
