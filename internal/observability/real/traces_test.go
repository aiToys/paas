package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTracesStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Fatalf("路径不符: %s", r.URL.Path)
		}
		if tags := r.URL.Query().Get("tags"); tags != "app=app-cs" {
			t.Fatalf("tags 过滤不符: %s", tags)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"traces": []map[string]any{
				{"traceID": "abc123", "rootTraceName": "POST /v1/chat", "durationSeconds": 0.12, "startTimeUnixNs": uint64(1719500000000000000)},
			},
		})
	}))
	defer srv.Close()
	s := NewTracesStore(srv.URL)
	out, err := s.ListTraces(context.Background(), "app-cs", "", 20)
	if err != nil || len(out) != 1 {
		t.Fatalf("解析错误: %v len=%d", err, len(out))
	}
	if out[0].ID != "abc123" || out[0].DurationMs != 120 || out[0].Operation != "POST /v1/chat" {
		t.Fatalf("字段错误: %+v", out[0])
	}
}

func TestTracesStoreBackendDown(t *testing.T) {
	s := NewTracesStore("http://127.0.0.1:1")
	out, err := s.ListTraces(context.Background(), "", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("后端不可达应降级返空非报错: %v len=%d", err, len(out))
	}
}
