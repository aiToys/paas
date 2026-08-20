package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTracesStoreAppLevelByServiceName：应用级查询经 lister 解析 app→工作负载名，按 service 查 Jaeger。
// Jaeger /api/traces 一次返完整 span 树（spans + processes），无需二次拉详情。
func TestTracesStoreAppLevelByServiceName(t *testing.T) {
	var gotService string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/traces" {
			t.Fatalf("未预期的路径: %s", r.URL.Path)
		}
		gotService = r.URL.Query().Get("service")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"traceID": "3fb7533a3292802d71d84ee00995cc3f",
					"spans": []map[string]any{
						// bff SERVER（根 span）：HTTP method/path/status=200 + client.address。
						{"traceID": "3fb7533a3292802d71d84ee00995cc3f", "spanID": "d4a1a429157c4c2f", "operationName": "GET /api/recommend", "startTime": 1719500000000000, "duration": 120000, "processID": "p1", "tags": []map[string]any{{"key": "http.request.method", "type": "string", "value": "GET"}, {"key": "url.path", "type": "string", "value": "/api/recommend"}, {"key": "http.response.status_code", "type": "int64", "value": 200}, {"key": "client.address", "type": "string", "value": "10.0.0.1"}}},
						// product CLIENT（CHILD_OF 根）。
						{"traceID": "3fb7533a3292802d71d84ee00995cc3f", "spanID": "201cc2e8def7bb2f", "operationName": "HTTP GET", "references": []map[string]any{{"refType": "CHILD_OF", "traceID": "3fb7533a3292802d71d84ee00995cc3f", "spanID": "d4a1a429157c4c2f"}}, "startTime": 1719500000010000, "duration": 100000, "processID": "p2", "tags": []map[string]any{{"key": "url.full", "type": "string", "value": "http://paas-shop-product:8081/products"}}},
						// recommend CLIENT（CHILD_OF 根，503 出错 + 异常）。
						{"traceID": "3fb7533a3292802d71d84ee00995cc3f", "spanID": "a0cde9f7bd6e5c4d", "operationName": "HTTP GET", "references": []map[string]any{{"refType": "CHILD_OF", "traceID": "3fb7533a3292802d71d84ee00995cc3f", "spanID": "d4a1a429157c4c2f"}}, "startTime": 1719500000020000, "duration": 80000, "processID": "p3", "tags": []map[string]any{{"key": "http.response.status_code", "type": "int64", "value": 503}, {"key": "exception.type", "type": "string", "value": "upstream unavailable"}, {"key": "exception.message", "type": "string", "value": "connection refused"}}},
					},
					"processes": map[string]any{
						"p1": map[string]any{"serviceName": "paas-shop-bff"},
						"p2": map[string]any{"serviceName": "paas-shop-product"},
						"p3": map[string]any{"serviceName": "paas-shop-recommend"},
					},
				},
			},
		})
	}))
	defer srv.Close()

	s := NewTracesStore(srv.URL, &fakeLister{names: []string{"paas-shop-bff"}})
	out, err := s.ListTraces(context.Background(), "app-shop", "", 20)
	if err != nil || len(out) != 1 {
		t.Fatalf("解析错误: %v len=%d", err, len(out))
	}
	if gotService != "paas-shop-bff" {
		t.Fatalf("应按 service 过滤查询, got service=%s", gotService)
	}
	if out[0].ID != "3fb7533a3292802d71d84ee00995cc3f" || out[0].DurationMs != 120 || out[0].Operation != "GET /api/recommend" || out[0].Service != "paas-shop-bff" {
		t.Fatalf("字段错误: %+v", out[0])
	}
	if out[0].AppID != "app-shop" {
		t.Fatalf("应用级 trace 应回填 appID: %s", out[0].AppID)
	}
	// 含错误 span 的 trace 应标 error。
	if out[0].Status != "error" {
		t.Fatalf("含 503 span 的 trace 应标 error: %s", out[0].Status)
	}
	// span 树应一次填充：3 个 span（bff SERVER + product CLIENT + 出错 recommend CLIENT）。
	if len(out[0].Spans) != 3 {
		t.Fatalf("应解析 3 个 span, got %d: %+v", len(out[0].Spans), out[0].Spans)
	}
	var foundRoot, foundErr bool
	for _, sp := range out[0].Spans {
		if sp.ParentID == "" {
			foundRoot = true
			if sp.ID != "d4a1a429157c4c2f" || sp.Service != "paas-shop-bff" || sp.StartMs != 0 {
				t.Fatalf("根 span 解析错误: %+v", sp)
			}
			// 全属性透传：HTTP method/path/status + client.address 均应进 Tags。
			if sp.Tags["http.request.method"] != "GET" || sp.Tags["url.path"] != "/api/recommend" || sp.Tags["http.response.status_code"] != "200" {
				t.Fatalf("根 span Tags 缺 http 属性: %+v", sp.Tags)
			}
			if sp.Tags["client.address"] != "10.0.0.1" {
				t.Fatalf("根 span Tags 缺 client.address(IP): %+v", sp.Tags)
			}
		}
		// 出错的 recommend span（503）：IsError=true + 异常信息提取。
		if sp.Service == "paas-shop-recommend" {
			foundErr = true
			if !sp.IsError {
				t.Fatalf("503 span 应 IsError=true: %+v", sp)
			}
			if sp.ErrorMessage != "connection refused" || sp.ErrorType != "upstream unavailable" {
				t.Fatalf("错误 span 异常信息提取错误: type=%q msg=%q", sp.ErrorType, sp.ErrorMessage)
			}
		}
	}
	if !foundRoot {
		t.Fatalf("未找到根 span（parent 空）: %+v", out[0].Spans)
	}
	if !foundErr {
		t.Fatalf("未找到错误 span(recommend): %+v", out[0].Spans)
	}
}

// TestTracesStoreAppLevelMultiServiceMerge：多工作负载逐个查合并去重。
func TestTracesStoreAppLevelMultiServiceMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service := r.URL.Query().Get("service")
		var tid string
		switch service {
		case "s1":
			tid = "trace-1"
		case "s2":
			tid = "trace-2"
		default:
			t.Fatalf("未预期的 service: %s", service)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"traceID": tid, "spans": []map[string]any{
					{"spanID": "span-" + tid, "operationName": "GET /", "startTime": 1719500000000000, "duration": 50000, "processID": "p1", "tags": []map[string]any{}},
				}, "processes": map[string]any{"p1": map[string]any{"serviceName": service}}},
			},
		})
	}))
	defer srv.Close()

	s := NewTracesStore(srv.URL, &fakeLister{names: []string{"s1", "s2"}})
	out, err := s.ListTraces(context.Background(), "app-x", "", 20)
	if err != nil || len(out) != 2 {
		t.Fatalf("多服务应合并 2 条: err=%v len=%d", err, len(out))
	}
}

// TestTracesStoreAppLevelNoListerDegrades：lister 为 nil（非集群部署）→ 应用级降级返空，不查 Jaeger。
func TestTracesStoreAppLevelNoListerDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("lister 为 nil 时不应查 Jaeger")
	}))
	defer srv.Close()
	s := NewTracesStore(srv.URL, nil)
	out, err := s.ListTraces(context.Background(), "app-x", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("lister=nil 应用级应降级返空: err=%v len=%d", err, len(out))
	}
}

// TestTracesStorePlatformLevelViaServices：平台级（appID 空）→ Jaeger /api/traces 要求 service 参数，
// 故先 /api/services 拿全量服务，逐个查合并（过滤 jaeger-all-in-one 自身服务）。
func TestTracesStorePlatformLevelViaServices(t *testing.T) {
	var queriedServices []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{"s1", "s2", "jaeger-all-in-one"}})
		case "/api/traces":
			svc := r.URL.Query().Get("service")
			queriedServices = append(queriedServices, svc)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"traceID": "t-" + svc, "spans": []map[string]any{
					{"spanID": "x", "operationName": "GET /", "startTime": 1719500000000000, "duration": 50000, "processID": "p1", "tags": []map[string]any{}},
				}, "processes": map[string]any{"p1": map[string]any{"serviceName": svc}}},
			}})
		default:
			t.Fatalf("未预期的路径: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	s := NewTracesStore(srv.URL, &fakeLister{})
	out, err := s.ListTraces(context.Background(), "", "", 20)
	if err != nil {
		t.Fatalf("平台级查询不应报错: %v", err)
	}
	// 应过滤 jaeger-all-in-one，只逐个查 s1/s2。
	if len(queriedServices) != 2 {
		t.Fatalf("应过滤 jaeger-all-in-one 只查 s1/s2, got queried=%v", queriedServices)
	}
	if len(out) != 2 {
		t.Fatalf("应合并 2 条 trace, got %d: %+v", len(out), out)
	}
}

// TestTracesStoreBackendDown：后端不可达降级返空非报错。
func TestTracesStoreBackendDown(t *testing.T) {
	s := NewTracesStore("http://127.0.0.1:1", &fakeLister{names: []string{"s1"}})
	out, err := s.ListTraces(context.Background(), "app-x", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("后端不可达应降级返空非报错: %v len=%d", err, len(out))
	}
}

// TestParseJaegerTraceEmptySpans：空 spans 的 trace 不 panic，返空 Spans。
func TestParseJaegerTraceEmptySpans(t *testing.T) {
	tr, hasErr := parseJaegerTrace(jaegerTrace{TraceID: "t1"})
	if tr.ID != "t1" || hasErr || len(tr.Spans) != 0 {
		t.Fatalf("空 span trace 解析错误: %+v hasErr=%v", tr, hasErr)
	}
}

// TestTracesStoreFiltersProbeNoise：单 span 且 0ms 的探活 trace（如 mcp /mcp 健康检查）
// 从默认列表排除（>=2 span 或耗时 >0 的保留），防刷屏掩盖业务链路。
func TestTracesStoreFiltersProbeNoise(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				// 探活噪音：单 span + 0 duration → 应被排除
				{"traceID": "probe-1", "spans": []map[string]any{
					{"spanID": "s-p1", "operationName": "POST /mcp", "startTime": 1719500000000000, "duration": 0, "processID": "p1", "tags": []map[string]any{}},
				}, "processes": map[string]any{"p1": map[string]any{"serviceName": "s1"}}},
				// 真实链路：多 span → 保留
				{"traceID": "real-1", "spans": []map[string]any{
					{"spanID": "s-r1", "operationName": "GET /api/products", "startTime": 1719500000000001, "duration": 4000, "processID": "p1", "tags": []map[string]any{}},
					{"spanID": "s-r2", "operationName": "SELECT products", "startTime": 1719500000000002, "duration": 2000, "processID": "p1", "tags": []map[string]any{}},
				}, "processes": map[string]any{"p1": map[string]any{"serviceName": "s1"}}},
				// 单 span 但耗时 >0 → 保留
				{"traceID": "slow-1", "spans": []map[string]any{
					{"spanID": "s-s1", "operationName": "POST /api/chat", "startTime": 1719500000000003, "duration": 3130000, "processID": "p1", "tags": []map[string]any{}},
				}, "processes": map[string]any{"p1": map[string]any{"serviceName": "s1"}}},
			},
		})
	}))
	defer srv.Close()

	s := NewTracesStore(srv.URL, &fakeLister{names: []string{"s1"}})
	out, err := s.ListTraces(context.Background(), "app-x", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("探活噪音应被排除（3 条中留 2），got %d: %+v", len(out), out)
	}
	for _, tr := range out {
		if tr.ID == "probe-1" {
			t.Fatalf("probe-1 应被排除: %+v", out)
		}
	}
}
