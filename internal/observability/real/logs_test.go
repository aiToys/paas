package real

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

// fakeLister 实现 observability.AppWorkloadLister，返回固定工作负载 ID 列表（测试用）。
type fakeLister struct {
	ids   []string
	names []string
}

func (f *fakeLister) AppWorkloadIDs(ctx context.Context, appID string) ([]string, error) {
	return f.ids, nil
}

func (f *fakeLister) AppWorkloadNames(ctx context.Context, appID string) ([]string, error) {
	return f.names, nil
}

func TestLogsStoreSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Fatalf("路径不符: %s", r.URL.Path)
		}
		// 应用级查询：namespace(paas-<tenant>) + 工作负载 pod 名正则 + level/q 内容过滤。
		want := `{namespace="paas-t-acme",pod=~"wl-wl1-.*|wl-wl2-.*"} |~ "(?i)\\berror\\b" |= "timeout"`
		if q := r.URL.Query().Get("query"); q != want {
			t.Fatalf("LogQL 不符:\n got: %s\nwant: %s", q, want)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "logs",
				"result": []map[string]any{
					{"stream": map[string]any{"pod": "wl-wl1-abc"},
						"values": [][]string{{"1719500000000000000", "upstream timeout"}}},
				},
			},
		})
	}))
	defer srv.Close()
	s := NewLogsStore(srv.URL, &fakeLister{ids: []string{"wl-wl1", "wl-wl2"}})
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	out, err := s.ListLogs(ctx, "app-cs", "", "", "error", "timeout", 50)
	if err != nil || len(out) != 1 {
		t.Fatalf("解析错误: %v len=%d", err, len(out))
	}
	if out[0].Message != "upstream timeout" || out[0].AppID != "app-cs" {
		t.Fatalf("字段错误: %+v", out[0])
	}
}

func TestLogsStoreBackendDown(t *testing.T) {
	s := NewLogsStore("http://127.0.0.1:1", &fakeLister{ids: []string{"wl-x"}})
	out, err := s.ListLogs(context.Background(), "app-cs", "", "", "", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("后端不可达应降级返空非报错: %v len=%d", err, len(out))
	}
}

// TestLogsStoreNoListerReturnsEmpty 验证 lister 未注入时应用级查询降级返空（不 panic）。
func TestLogsStoreNoListerReturnsEmpty(t *testing.T) {
	s := NewLogsStore("http://127.0.0.1:1", nil)
	out, err := s.ListLogs(context.Background(), "app-cs", "", "", "", "", 10)
	if err != nil || len(out) != 0 {
		t.Fatalf("无 lister 应降级返空: %v len=%d", err, len(out))
	}
}
