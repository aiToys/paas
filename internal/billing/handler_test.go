package billing_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/billing"
	billingmemory "github.com/aitoys/paas/internal/billing/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newReq 构造带租户上下文的请求。
func newReq(method, target, body, tid string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	ctx := tenant.WithTenant(r.Context(), tid)
	return r.WithContext(ctx)
}

func allowIf(b bool) func(*http.Request, string) bool {
	return func(_ *http.Request, _ string) bool { return b }
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("解码响应失败: %v (body=%s)", err, w.Body.String())
	}
}

// TestHandlerUsageView 验证用量视图含超限标记。
func TestHandlerUsageView(t *testing.T) {
	h := billing.NewHandler(billingmemory.NewStore())
	r := newReq(http.MethodGet, "/api/billing/usage", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("usage 应 200，got %d", w.Code)
	}
	var view billing.UsageView
	decode(t, w, &view)
	foundOver := false
	for _, l := range view.Items {
		if l.Resource == billing.ResGPU && l.Over {
			foundOver = true
		}
	}
	if !foundOver {
		t.Fatal("GPU 应超限标记")
	}
}

// TestHandlerQuotaUpdate 验证配额更新。
func TestHandlerQuotaUpdate(t *testing.T) {
	h := billing.NewHandler(billingmemory.NewStore())
	body := `{"limits":{"gpu":16,"storage_gb":1000}}`
	r := newReq(http.MethodPut, "/api/billing/quota", body, "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("配额更新应 200，got %d body=%s", w.Code, w.Body.String())
	}
	var q billing.ResourceQuota
	decode(t, w, &q)
	if q.Limits[billing.ResGPU] != 16 {
		t.Fatalf("GPU 上限应 16，got %v", q.Limits[billing.ResGPU])
	}
	if q.TenantID != "t-acme" {
		t.Fatalf("TenantID 应由 ctx 注入，got %q", q.TenantID)
	}
}

// TestHandlerGenerateAndPay 验证账单生成 + 支付闭环。
func TestHandlerGenerateAndPay(t *testing.T) {
	h := billing.NewHandler(billingmemory.NewStore())

	// 生成
	r := newReq(http.MethodPost, "/api/billing/records/generate?period=2026-07", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("生成账单应 201，got %d body=%s", w.Code, w.Body.String())
	}
	var rec billing.BillingRecord
	decode(t, w, &rec)
	if rec.Status != billing.StatusUnpaid || rec.Total <= 0 {
		t.Fatalf("新账单 unpaid 且 total>0，got %+v", rec)
	}

	// 支付
	r2 := newReq(http.MethodPost, "/api/billing/records/"+rec.ID+"/pay", "", "t-acme")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("支付应 200，got %d body=%s", w2.Code, w2.Body.String())
	}
	var paid billing.BillingRecord
	decode(t, w2, &paid)
	if paid.Status != billing.StatusPaid || paid.PaidAt == nil {
		t.Fatalf("应已支付，got %+v", paid)
	}
}

// TestHandlerForbidden 验证 viewer（无 write）被拒。
func TestHandlerForbidden(t *testing.T) {
	h := billing.NewHandler(billingmemory.NewStore())
	h.Authorize = allowIf(false) // 模拟无 billing:write
	r := newReq(http.MethodPut, "/api/billing/quota", `{"limits":{}}`, "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("无权限应 403，got %d", w.Code)
	}
}

// TestHandlerTenantIsolation 验证账单列表租户隔离。
func TestHandlerTenantIsolation(t *testing.T) {
	h := billing.NewHandler(billingmemory.NewStore())
	// acme 生成账单
	r := newReq(http.MethodPost, "/api/billing/records/generate?period=2026-07", "", "t-acme")
	h.ServeHTTP(httptest.NewRecorder(), r)
	// globex 看不到 acme 账单
	r2 := newReq(http.MethodGet, "/api/billing/records", "", "t-globex")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r2)
	var resp struct {
		Data []billing.BillingRecord `json:"data"`
	}
	decode(t, w, &resp)
	if len(resp.Data) != 0 {
		t.Fatalf("globex 应无账单，got %d", len(resp.Data))
	}
}

// TestHandlerGenerateInvalidPeriod 验证非法周期报 400。
func TestHandlerGenerateInvalidPeriod(t *testing.T) {
	h := billing.NewHandler(billingmemory.NewStore())
	r := newReq(http.MethodPost, "/api/billing/records/generate?period=bad", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法周期应 400，got %d", w.Code)
	}
}
