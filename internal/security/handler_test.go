package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/security"
	secmemory "github.com/aitoys/paas/internal/security/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func newHandler() *security.Handler {
	h := security.NewHandler(secmemory.NewStore(), security.WithUserIDFrom(func(ctx context.Context) string {
		return "u-test"
	}))
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	return h
}

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func req(ctx context.Context, method, path string, body interface{}) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	return rq.WithContext(ctx)
}

// TestSecretMasked 验证 API 返回掩码。
func TestSecretMasked(t *testing.T) {
	h := newHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/security/secrets", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", w.Code)
	}
	var out struct {
		Data []security.Secret `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, s := range out.Data {
		if s.Value != security.SecretMask {
			t.Fatalf("值应掩码，got %q", s.Value)
		}
	}
}

// TestCreateAndAudit 验证创建密钥后自动记审计。
func TestCreateAndAudit(t *testing.T) {
	h := newHandler()
	// 创建
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/security/secrets", security.Secret{
		Name: "new-key", Type: security.TypeSecret, Value: "plaintext-value",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d: %s", w.Code, w.Body.String())
	}
	// 审计应记一条 create
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/security/audit-logs", nil))
	var out struct {
		Data []security.AuditLog `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	found := false
	for _, l := range out.Data {
		if l.Action == security.ActionCreate && l.Actor == "u-test" {
			found = true
		}
	}
	if !found {
		t.Fatal("创建后应记审计（actor=u-test）")
	}
}

// TestDeleteAudit 验证删除记审计 + 跨租户不泄漏。
func TestDeleteAudit(t *testing.T) {
	h := newHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/security/secrets/sec-acme-db", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("删除应 200，got %d", w.Code)
	}
	// 审计记了 delete
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/security/audit-logs?action=delete", nil))
	var out struct {
		Data []security.AuditLog `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0].ResourceID != "sec-acme-db" {
		t.Fatalf("删除审计应 1 条指向 sec-acme-db，got %+v", out.Data)
	}
}

// TestCrossTenantHidden 验证跨租户删除不泄漏。
func TestCrossTenantHidden(t *testing.T) {
	globex := tenant.WithTenant(context.Background(), "t-globex")
	h := newHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(globex, "DELETE", "/api/security/secrets/sec-acme-db", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户删除应 404，got %d", w.Code)
	}
}

// TestPlatformSecretAdminOnly 验证平台级 Secret 写操作仅 admin：非 admin 创建/删除 → 403，admin → 成功。
func TestPlatformSecretAdminOnly(t *testing.T) {
	mkHandler := func(isAdmin bool) *security.Handler {
		h := security.NewHandler(secmemory.NewStore(), security.WithUserIDFrom(func(context.Context) string { return "u-test" }))
		h.Authorize = func(*http.Request, string) bool { return true }
		h.IsAdmin = func(*http.Request) bool { return isAdmin }
		return h
	}

	t.Run("非 admin 创建平台级 → 403", func(t *testing.T) {
		h := mkHandler(false)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/security/secrets", security.Secret{
			Scope: security.ScopePlatform, Name: "vendor-x", Type: security.TypeSecret, Value: "sk",
		}))
		if w.Code != http.StatusForbidden {
			t.Fatalf("非 admin 创建平台级应 403，got %d", w.Code)
		}
	})
	t.Run("admin 创建平台级 → 201", func(t *testing.T) {
		h := mkHandler(true)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/security/secrets", security.Secret{
			Scope: security.ScopePlatform, Name: "vendor-y", Type: security.TypeSecret, Value: "sk",
		}))
		if w.Code != http.StatusCreated {
			t.Fatalf("admin 创建平台级应 201，got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("平台级全租户可见", func(t *testing.T) {
		h := mkHandler(true)
		// admin 建
		h.ServeHTTP(httptest.NewRecorder(), req(acmeCtx(), "POST", "/api/security/secrets", security.Secret{
			Scope: security.ScopePlatform, Name: "vendor-z", Type: security.TypeSecret, Value: "sk",
		}))
		// globex 也能列出（掩码）
		globex := tenant.WithTenant(context.Background(), "t-globex")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(globex, "GET", "/api/security/secrets", nil))
		var out struct {
			Data []security.Secret `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		found := false
		for _, s := range out.Data {
			if s.Name == "vendor-z" {
				found = true
				if s.Value != security.SecretMask {
					t.Fatalf("平台级返回应掩码，got %q", s.Value)
				}
			}
		}
		if !found {
			t.Fatal("平台级凭证应全租户可见")
		}
	})
}
