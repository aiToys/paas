package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/core/application"
	appmemory "github.com/aitoys/paas/internal/core/application/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

type fakeAudit struct{ last string }

func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}

type fakeQuota struct{ n int }

func (q *fakeQuota) check(ctx context.Context, d int) error {
	q.n += d
	return nil
}

type fakeCascade struct{ called bool }

func (f *fakeCascade) CascadeDelete(ctx context.Context, appID string) error {
	f.called = true
	return nil
}

func newAdminForTest(t *testing.T) (*application.AdminHandler, *fakeAudit, *fakeQuota, *fakeCascade) {
	t.Helper()
	repo := appmemory.NewStore()
	au := &fakeAudit{}
	q := &fakeQuota{}
	cas := &fakeCascade{}
	h := application.NewAdminHandler(repo,
		application.WithAdminQuota(q.check),
		application.WithAdminCascade(cas),
		application.WithAdminAudit(au),
	)
	return h, au, q, cas
}

func TestAdminListReturnsAllTenants(t *testing.T) {
	h, _, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/applications", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	list := out["data"].([]any)
	// 两租户应用都应返回（跨租户）。
	seen := map[string]int{}
	for _, item := range list {
		m := item.(map[string]any)
		seen[m["tenantId"].(string)]++
	}
	if seen["t-acme"] == 0 || seen["t-globex"] == 0 {
		t.Fatalf("expected cross-tenant, got %v", seen)
	}
}

func TestAdminDetail(t *testing.T) {
	h, _, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/applications/app-cs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	if data["id"] != "app-cs" {
		t.Fatalf("id=%v", data["id"])
	}
}

func TestAdminDetailNotFound(t *testing.T) {
	h, _, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/applications/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminDeleteCascadeQuotaAudit(t *testing.T) {
	h, au, q, cas := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/applications/app-cs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !cas.called {
		t.Fatalf("cascade not called")
	}
	if q.n != -1 {
		t.Fatalf("quota delta=%d want -1", q.n)
	}
	if au.last != "admin:delete" {
		t.Fatalf("audit=%s", au.last)
	}
}

// 验证 admin 写操作以资源租户 ctx 落库（Delete 内部 Get 强制 ctx tenant）。
func TestAdminDeleteUsesResourceTenantCtx(t *testing.T) {
	h, _, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/applications/app-cs", nil)
	// 请求 ctx 故意带 t-globex（非资源所属租户 t-acme）；handler 应以资源租户 t-acme 落库。
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminItemMethodNotAllowed(t *testing.T) {
	h, _, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/applications/app-cs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
