package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogsStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Fatalf("路径不符: %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("query"); q != `{app="app-cs",level="error",} |= "timeout"` {
			t.Fatalf("LogQL 不符: %s", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "logs",
				"result": []map[string]any{
					{"stream": map[string]any{"app": "app-cs", "level": "error"},
						"values": [][]string{{"1719500000000000000", "upstream timeout"}}},
				},
			},
		})
	}))
	defer srv.Close()
	s := NewLogsStore(srv.URL)
	out, err := s.ListLogs(context.Background(), "app-cs", "error", "timeout", 50)
	if err != nil || len(out) != 1 {
		t.Fatalf("解析错误: %v len=%d", err, len(out))
	}
	if out[0].Message != "upstream timeout" || out[0].AppID != "app-cs" {
		t.Fatalf("字段错误: %+v", out[0])
	}
}

func TestLogsStoreBackendDown(t *testing.T) {
	s := NewLogsStore("http://127.0.0.1:1")
	out, err := s.ListLogs(context.Background(), "", "", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("后端不可达应降级返空非报错: %v len=%d", err, len(out))
	}
}
