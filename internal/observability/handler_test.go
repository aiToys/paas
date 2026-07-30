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

// TestMetrics 验证指标查询（惰性补点）。
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
	if len(out.Data) != 1 || out.Data[0].Name != observability.MetricCPU {
		t.Fatalf("应返回 cpu 单条，got %+v", out.Data)
	}
	if len(out.Data[0].Points) == 0 {
		t.Fatal("应有点数")
	}
}

// TestAlerts 验证告警评估。
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
	if len(out.Data) == 0 {
		t.Fatal("应有 firing 告警")
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
