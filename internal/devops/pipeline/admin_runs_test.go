package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

func TestAdminRunWindowAndExactDetail(t *testing.T) {
	s := NewMemoryStore()
	old := PipelineRun{ID: "old-failure", TenantID: "t-acme", Status: RunFailed, CreatedAt: time.Unix(1, 0), StageRuns: []StageRun{{Name: "old-stage"}}}
	s.runs[old.ID] = old
	for i := 0; i < 1001; i++ {
		id := fmt.Sprintf("new-%04d", i)
		s.runs[id] = PipelineRun{ID: id, TenantID: "t-globex", Status: RunFailed, CreatedAt: time.Unix(int64(i+2), 0)}
	}
	testAdminRunQuery(t, s)
	h := NewHandler(s, s, s, nil, WithPlatformAdmin(func(*http.Request) bool { return true }))
	for _, path := range []string{"/api/admin/pipelineruns?tenantId=t-acme&status=failed", "/api/admin/pipelineruns/old-failure"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var body struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if path == "/api/admin/pipelineruns/old-failure" {
			var run PipelineRun
			if err := json.Unmarshal(body.Data, &run); err != nil {
				t.Fatal(err)
			}
			if run.ID != old.ID || len(run.StageRuns) != 1 {
				t.Fatalf("wrong detail: %+v", run)
			}
		} else {
			var runs []PipelineRun
			if err := json.Unmarshal(body.Data, &runs); err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 || runs[0].ID != old.ID {
				t.Fatalf("tenant filter applied too late: %+v", runs)
			}
		}
	}
	denied := NewHandler(s, s, s, nil)
	w := httptest.NewRecorder()
	denied.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/pipelineruns/old-failure", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin detail bypass: %d", w.Code)
	}
}

// memory与PG共享同一边界断言；集成用例使用相同测试数据。
func testAdminRunQuery(t *testing.T, s RunRepository) {
	t.Helper()
	ctx := context.Background()
	runs, err := s.ListAdminRuns(ctx, RunFailed, "t-acme")
	if err != nil || len(runs) != 1 || runs[0].ID != "old-failure" {
		t.Fatalf("filter before LIMIT: %v %+v", err, runs)
	}
	all, err := s.ListAdminRuns(ctx, RunFailed, "")
	if err != nil || len(all) != 1000 {
		t.Fatalf("bounded list: %v len=%d", err, len(all))
	}
	got, err := s.GetRunAny(ctx, "old-failure")
	if err != nil || got.ID != "old-failure" {
		t.Fatalf("old detail: %v %+v", err, got)
	}
	if _, err := s.GetRun(tenant.WithTenant(ctx, "t-globex"), "old-failure"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("tenant isolation changed: %v", err)
	}
	if _, err := s.GetRunAny(ctx, "missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing: %v", err)
	}
}
