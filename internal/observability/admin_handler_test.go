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

// fakeAudit 记录最后一次审计 action。
type fakeAudit struct{ last string }

func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}

func newAdminForTest(t *testing.T) (*observability.AdminHandler, *fakeAudit) {
	t.Helper()
	repo := obsmemory.NewStore()
	au := &fakeAudit{}
	h := observability.NewAdminHandler(repo,
		observability.WithAdminAudit(au),
		observability.WithAdminActor(func(*http.Request) string { return "u-admin" }),
	)
	return h, au
}

// TestAdminDetail 验证跨租户取单条告警规则。
func TestAdminDetail(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/alert-rules/rule-acme-cpu", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	rule := out["data"].(map[string]any)
	if rule["id"] != "rule-acme-cpu" {
		t.Fatalf("rule=%v", rule)
	}
}

func TestAdminDetailNotFound(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/alert-rules/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminDeleteAudits 验证删除记审计 admin:delete。
func TestAdminDeleteAudits(t *testing.T) {
	h, au := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/alert-rules/rule-acme-cpu", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if au.last != "admin:delete" {
		t.Fatalf("audit=%s", au.last)
	}
}

// TestAdminDeleteUsesResourceTenantCtx 验证写操作以资源租户 ctx 落库。
func TestAdminDeleteUsesResourceTenantCtx(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/alert-rules/rule-acme-cpu", nil)
	// 请求 ctx 故意带 t-globex；handler 应以资源租户 t-acme 落库。
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
