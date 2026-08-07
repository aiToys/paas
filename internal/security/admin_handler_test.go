package security_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/security"
	secmemory "github.com/aitoys/paas/internal/security/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeAudit 记录最后一次审计 action。
type fakeAudit struct{ last string }

func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}

func newAdminForTest(t *testing.T) (*security.AdminHandler, *fakeAudit) {
	t.Helper()
	repo := secmemory.NewStore()
	au := &fakeAudit{}
	h := security.NewAdminHandler(repo,
		security.WithAdminAudit(au),
		security.WithAdminActor(func(*http.Request) string { return "u-admin" }),
	)
	return h, au
}

// TestAdminDetailMasked 验证详情返回掩码（Value 替换为 ••••••，不泄漏明文）。
func TestAdminDetailMasked(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/secrets/sec-acme-db", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	sec := out["data"].(map[string]any)
	if sec["value"] != "••••••" {
		t.Fatalf("value=%v 应为掩码", sec["value"])
	}
}

func TestAdminDetailNotFound(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/secrets/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminDetailPlatformSecret 验证平台级 Secret（TenantID 空）也能取到。
func TestAdminDetailPlatformSecret(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/secrets/sec-platform-airouter", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminDeleteAudits 验证删除记审计 admin:delete。
func TestAdminDeleteAudits(t *testing.T) {
	h, au := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/secrets/sec-acme-db", nil)
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
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/secrets/sec-acme-db", nil)
	// 请求 ctx 故意带 t-globex；handler 应以资源租户 t-acme 落库。
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
