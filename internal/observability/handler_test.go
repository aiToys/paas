package observability_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/observability"
	obsmemory "github.com/aitoys/paas/internal/observability/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func newHandler() *observability.Handler {
	h := observability.NewHandler(obsmemory.NewStore())
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	return h
}

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

func get(ctx context.Context, path string) *httptest.ResponseRecorder {
	h := newHandler()
	r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestMetrics 降级模式无 series，返回空列表。
// 接真实后端（PAAS_PROM_URL）时 metrics 由 real store 提供。
func TestMetrics(t *testing.T) {
	w := get(acmeCtx(), "/api/observability/metrics?targetType=app&targetId=app-cs&name=cpu")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", w.Code)
	}
	var out struct {
		Data []observability.MetricSeries `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(out.Data) != 0 {
		t.Fatalf("降级模式应返空，got %d", len(out.Data))
	}
}

// TestAlerts 降级模式 series 空，无 firing 告警。
// 接真实后端时 series 由 real store 提供，告警评估在 real 模式测。
func TestAlerts(t *testing.T) {
	w := get(acmeCtx(), "/api/observability/alerts")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", w.Code)
	}
	var out struct {
		Data []observability.Alert `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(out.Data) != 0 {
		t.Fatalf("降级模式无 series 应无 firing 告警，got %d", len(out.Data))
	}
}

// TestCrossTenant 验证 globex 无告警（无规则）。
func TestCrossTenant(t *testing.T) {
	w := get(globexCtx(), "/api/observability/alerts")
	var out struct {
		Data []observability.Alert `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(out.Data) != 0 {
		t.Fatalf("globex 无规则不应有告警，got %d", len(out.Data))
	}
}
