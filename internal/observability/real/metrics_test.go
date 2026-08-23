package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestMetricsStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("路径应为 /api/v1/query_range，实际 %s", r.URL.Path)
		}
		q := r.URL.Query().Get("query")
		// 应用级：cAdvisor 不带 pod label，按 namespace(paas-<tenant>) + 工作负载 pod 名正则聚合 CPU。
		want := `sum(rate(container_cpu_usage_seconds_total{namespace="paas-t-acme",pod=~"wl-wl1-.*|wl-wl2-.*",container!="POD",container!=""}[5m]))`
		if q != want {
			t.Fatalf("PromQL 不符:\n got: %s\nwant: %s", q, want)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{
					{
						"metric": map[string]any{"__name__": "container_cpu_usage_seconds_total"},
						"values": [][]any{{1719500000, "0.62"}, {1719500060, "0.64"}},
					},
				},
			},
		})
	}))
	defer srv.Close()
	s := NewMetricsStore(srv.URL, &fakeLister{ids: []string{"wl-wl1", "wl-wl2"}}, nil)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	out, err := s.ListMetrics(ctx, "app", "app-cs", "cpu")
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if len(out) != 1 || out[0].Name != "cpu" || out[0].Current != 0.64 {
		t.Fatalf("解析错误: %+v", out)
	}
	if out[0].TargetType != "app" || out[0].TargetID != "app-cs" || out[0].Unit != "cores" {
		t.Fatalf("series 元数据错误: %+v", out[0])
	}
	if len(out[0].Points) != 2 {
		t.Fatalf("应解析 2 个点，实际 %d", len(out[0].Points))
	}
}

func TestMetricsStoreBackendDown(t *testing.T) {
	// 指向不存在的端口 → 降级返空切片，不报错。
	s := NewMetricsStore("http://127.0.0.1:1", nil, nil)
	out, err := s.ListMetrics(context.Background(), "app", "", "cpu")
	if err != nil {
		t.Fatalf("后端不可达应降级返空非报错: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("后端不可达应返空切片，实际 %d 条", len(out))
	}
}

// TestMetricsStoreDataservicePodQuery 验证 dataservice 走 cAdvisor pod 标签查询（pod="<id>-0"）。
func TestMetricsStoreDataservicePodQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{
					{
						"metric": map[string]any{"pod": "ds-mysql-0"},
						"values": [][]any{{1719500000, "0.5"}, {1719500060, "0.6"}},
					},
				},
			},
		})
	}))
	defer srv.Close()
	s := NewMetricsStore(srv.URL, nil, nil)
	out, err := s.ListMetrics(context.Background(), "dataservice", "ds-mysql", "cpu")
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if len(out) != 1 || out[0].TargetType != "dataservice" || out[0].TargetID != "ds-mysql" {
		t.Fatalf("series 错误: %+v", out)
	}
	if out[0].Unit != "cores" || out[0].Current != 0.6 {
		t.Fatalf("cpu 单位/值错误: unit=%s current=%v", out[0].Unit, out[0].Current)
	}
	want := `sum(rate(container_cpu_usage_seconds_total{namespace="paas-x",pod=~"ds-mysql-[\\d]+",container="main"}[5m]))`
	if gotQuery != want {
		t.Fatalf("dataservice PromQL 应按 pod 标签查 cAdvisor:\n got: %s\nwant: %s", gotQuery, want)
	}
}

// TestMetricsStoreDataserviceMemoryScale 验证内存 bytes->MiB 缩放。
func TestMetricsStoreDataserviceMemoryScale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{
					{"metric": map[string]any{"pod": "ds-r-0"}, "values": [][]any{{1719500000, "1048576"}}}, // 1 MiB
				},
			},
		})
	}))
	defer srv.Close()
	s := NewMetricsStore(srv.URL, nil, nil)
	out, _ := s.ListMetrics(context.Background(), "dataservice", "ds-r", "mem")
	if len(out) != 1 {
		t.Fatalf("应 1 条 series，got %d", len(out))
	}
	if out[0].Unit != "MiB" || out[0].Current != 1 {
		t.Fatalf("内存应缩放为 MiB: unit=%s current=%v", out[0].Unit, out[0].Current)
	}
}

// TestDataserviceDefsContainsAllMetrics 验证 dataserviceDefs 含 PVC 用量 + 全部引擎业务指标，
// 且 PromQL 字符串构造正确（按 ns+pod 限定多租户隔离）。
func TestDataserviceDefsContainsAllMetrics(t *testing.T) {
	defs := dataserviceDefs("paas-t-acme", "ds-1-0", "ds-1")
	// PVC 用量：查 kubelet volume stats，PVC 名正则 data-ds-1-\d+（多副本 max）。
	disk, ok := defs[observability.MetricDiskUsage]
	if !ok {
		t.Fatal("缺 disk_usage")
	}
	if !strings.Contains(disk.promQL, "kubelet_volume_stats_used_bytes") {
		t.Fatalf("disk_usage 应查 kubelet_volume_stats_used_bytes：%s", disk.promQL)
	}
	if !strings.Contains(disk.promQL, `data-ds-1-[\\d]+`) {
		t.Fatalf("disk_usage 应限定 PVC=data-ds-1 数字后缀正则：%s", disk.promQL)
	}
	// 引擎业务指标全集。
	for _, key := range []string{
		observability.MetricConnections, observability.MetricQPS,
		observability.MetricHitRate, observability.MetricLag, observability.MetricVectors,
	} {
		if _, ok := defs[key]; !ok {
			t.Fatalf("缺引擎业务指标 %s", key)
		}
	}
	// connections PromQL 应按 paas_aitoys_dataservice label 过滤（exporter 指标不带 pod label）。
	conn := defs[observability.MetricConnections].promQL
	if !strings.Contains(conn, "paas_aitoys_dataservice") || !strings.Contains(conn, "ds-1") {
		t.Fatalf("connections PromQL 应按 paas_aitoys_dataservice=ds-1 过滤：%s", conn)
	}
}
