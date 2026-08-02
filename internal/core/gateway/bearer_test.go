package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/core/identity"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

const testBearerSecret = "bearer-test-secret"

// handlerProbe 捕获注入的 tenant/userID/roles。
func handlerProbe(t *testing.T, got *capture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.tenant, _ = tenant.TenantFrom(r.Context())
		got.userID = UserIDFrom(r.Context())
		rs, _ := RolesFrom(r.Context())
		if len(rs) > 0 {
			got.role = rs[0]
		}
		w.WriteHeader(http.StatusOK)
	})
}

type capture struct{ tenant, userID, role string }

func TestBearerAuthJWTChannel(t *testing.T) {
	idb := idmemory.NewStore()
	tok, _ := auth.Sign(auth.Claims{
		Sub: "u1", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: auth.TokenAccess, Exp: time.Now().Add(time.Minute).Unix(),
	}, testBearerSecret)

	var got capture
	h := BearerAuth(idb, testBearerSecret)(handlerProbe(t, &got))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "t-acme", got.tenant)
	assert.Equal(t, "u1", got.userID)
	assert.Equal(t, "tenant-admin", got.role)
}

func TestBearerAuthAPIKeyChannel(t *testing.T) {
	idb := idmemory.NewStore()
	require.NoError(t, idb.CreateTenant(context.Background(), identity.Tenant{ID: "t-acme", Name: "Acme", CreatedAt: time.Now()}))
	require.NoError(t, idb.CreateAPIKey(context.Background(), identity.APIKey{
		ID: "k1", TenantID: "t-acme", UserID: "u1", Roles: []string{"developer"}, Key: "sk-acme-dev",
	}))

	var got capture
	h := BearerAuth(idb, testBearerSecret)(handlerProbe(t, &got))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-acme-dev")
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "t-acme", got.tenant)
	assert.Equal(t, "u1", got.userID)
	assert.Equal(t, "developer", got.role)
}

func TestBearerAuthMissingToken(t *testing.T) {
	h := BearerAuth(idmemory.NewStore(), testBearerSecret)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("不应进入下游")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBearerAuthInvalidJWT(t *testing.T) {
	h := BearerAuth(idmemory.NewStore(), testBearerSecret)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("不应进入下游")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer aaa.bbb.ccc")
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBearerAuthInvalidAPIKey(t *testing.T) {
	h := BearerAuth(idmemory.NewStore(), testBearerSecret)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("不应进入下游")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-unknown")
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBearerAuthCookieChannel(t *testing.T) {
	idb := idmemory.NewStore()
	tok, _ := auth.Sign(auth.Claims{
		Sub: "u2", Tenant: "t-globex", Roles: []string{"tenant-admin"},
		Typ: auth.TokenAccess, Exp: time.Now().Add(time.Minute).Unix(),
	}, testBearerSecret)

	var got capture
	h := BearerAuth(idb, testBearerSecret)(handlerProbe(t, &got))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: tok}) //nolint:gosec // G124 测试 cookie 无需安全属性
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "t-globex", got.tenant)
	assert.Equal(t, "u2", got.userID)
}

// cookie + header 同时存在时，cookie 通道优先（浏览器会话场景）。
func TestBearerAuthCookiePrecedence(t *testing.T) {
	idb := idmemory.NewStore()
	tok, _ := auth.Sign(auth.Claims{
		Sub: "u-cookie", Tenant: "t-cookie", Roles: []string{"tenant-admin"},
		Typ: auth.TokenAccess, Exp: time.Now().Add(time.Minute).Unix(),
	}, testBearerSecret)

	var got capture
	h := BearerAuth(idb, testBearerSecret)(handlerProbe(t, &got))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: tok}) //nolint:gosec // G124 测试 cookie 无需安全属性
	req.Header.Set("Authorization", "Bearer sk-unknown")                 // 即使 header 无效，cookie 优先
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "t-cookie", got.tenant, "cookie 通道应优先于 header")
}

// cookie 有但无效 -> 401，不降级到 header（防混淆）。
func TestBearerAuthInvalidCookieRejects(t *testing.T) {
	h := BearerAuth(idmemory.NewStore(), testBearerSecret)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("无效 cookie 不应进入下游")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: "invalid.jwt.value"}) //nolint:gosec // G124 测试 cookie 无需安全属性
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
