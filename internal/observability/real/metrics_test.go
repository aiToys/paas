package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetricsStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("路径应为 /api/v1/query_range，实际 %s", r.URL.Path)
		}
		q := r.URL.Query().Get("query")
		if q != `paas_cpu_usage{target_type="app",target_id="app-cs"}` {
			t.Fatalf("PromQL 不符: %s", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{
					{
						"metric": map[string]any{"__name__": "paas_cpu_usage", "target_type": "app", "target_id": "app-cs"},
						"values": [][]any{{1719500000, "62"}, {1719500060, "64"}},
					},
				},
			},
		})
	}))
	defer srv.Close()
	s := NewMetricsStore(srv.URL)
	out, err := s.ListMetrics(context.Background(), "app", "app-cs", "cpu")
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if len(out) != 1 || out[0].Name != "cpu" || out[0].Current != 64 {
		t.Fatalf("解析错误: %+v", out)
	}
	if len(out[0].Points) != 2 {
		t.Fatalf("应解析 2 个点，实际 %d", len(out[0].Points))
	}
}

func TestMetricsStoreBackendDown(t *testing.T) {
	// 指向不存在的端口 → 降级返空切片，不报错。
	s := NewMetricsStore("http://127.0.0.1:1")
	out, err := s.ListMetrics(context.Background(), "app", "", "cpu")
	if err != nil {
		t.Fatalf("后端不可达应降级返空非报错: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("后端不可达应返空切片，实际 %d 条", len(out))
	}
}
