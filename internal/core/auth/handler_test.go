package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/core/identity"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
)

const hSecret = "handler-test-secret"

func newAuthHandler(t *testing.T) *Handler {
	t.Helper()
	idb := idmemory.NewStore()
	hash, err := HashPassword("123456")
	require.NoError(t, err)
	require.NoError(t, idb.CreateUser(context.Background(), identity.User{
		ID: "u-admin", TenantID: "t-acme", Name: "admin",
		PasswordHash: hash, IsAdmin: true, Roles: []string{"tenant-admin"}, Status: identity.StatusActive,
	}))
	return NewHandler(idb, hSecret, false)
}

func doJSON(t *testing.T, h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeData(t *testing.T, body []byte, dst any) {
	t.Helper()
	var wrap struct {
		Data any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &wrap))
	require.NotNil(t, wrap.Data)
	raw, _ := json.Marshal(wrap.Data)
	require.NoError(t, json.Unmarshal(raw, dst))
}

func TestLoginSuccess(t *testing.T) {
	h := newAuthHandler(t)
	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions",
		`{"username":"admin","password":"123456"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var res AuthResult
	decodeData(t, rec.Body.Bytes(), &res)
	assert.NotEmpty(t, res.AccessToken)
	assert.NotEmpty(t, res.RefreshToken)
	assert.Equal(t, int64(900), res.ExpiresIn)
}

func TestLoginWrongPassword(t *testing.T) {
	h := newAuthHandler(t)
	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions",
		`{"username":"admin","password":"wrong"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLoginUnknownUser(t *testing.T) {
	h := newAuthHandler(t)
	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions",
		`{"username":"nobody","password":"x"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLoginDisabled(t *testing.T) {
	idb := idmemory.NewStore()
	hash, _ := HashPassword("123456")
	require.NoError(t, idb.CreateUser(context.Background(), identity.User{
		ID: "u1", TenantID: "t", Name: "disabled", PasswordHash: hash,
		Roles: []string{"developer"}, Status: identity.StatusDisabled,
	}))
	h := NewHandler(idb, hSecret, false)
	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions",
		`{"username":"disabled","password":"123456"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMeMapsSuperAdmin(t *testing.T) {
	h := newAuthHandler(t)
	tok, _ := Sign(Claims{
		Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenAccess, Exp: 9999999999,
	}, hSecret)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var p UserProfile
	decodeData(t, rec.Body.Bytes(), &p)
	assert.Equal(t, "admin", p.Username)
	assert.Contains(t, p.Roles, "super_admin") // IsAdmin → super_admin
	assert.Contains(t, p.Permissions, "*")
}

func TestMeDeveloperScoped(t *testing.T) {
	idb := idmemory.NewStore()
	require.NoError(t, idb.CreateUser(context.Background(), identity.User{
		ID: "u-dev", TenantID: "t", Name: "dev", IsAdmin: false,
		Roles: []string{"developer"}, Status: identity.StatusActive,
	}))
	h := NewHandler(idb, hSecret, false)
	tok, _ := Sign(Claims{Sub: "u-dev", Tenant: "t", Roles: []string{"developer"},
		Typ: TokenAccess, Exp: 9999999999}, hSecret)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.Me(rec, req)

	var p UserProfile
	decodeData(t, rec.Body.Bytes(), &p)
	assert.Equal(t, []string{"developer"}, p.Roles)
	assert.Contains(t, p.Permissions, "application:read")
	assert.NotContains(t, p.Permissions, "*")
}

func TestRefresh(t *testing.T) {
	h := newAuthHandler(t)
	// Sub 必须是真实用户 ID（GetUser 按查；旧实现误用 GetUserByName 导致 fallback 跳过状态校验）。
	rt, _ := Sign(Claims{Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenRefresh, Exp: 9999999999}, hSecret)
	rec := doJSON(t, h.Refresh, http.MethodPost, "/api/auth/tokens/refresh",
		`{"refreshToken":"`+rt+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var res AuthResult
	decodeData(t, rec.Body.Bytes(), &res)
	assert.NotEmpty(t, res.AccessToken)
}

// TestRefreshDisabledUserRejected 验证安全修复：被禁用用户持未过期 refresh token
// 不能续期（旧实现因 GetUserByName(userID) 误查失败、fallback 跳过 Status 校验而放行）。
func TestRefreshDisabledUserRejected(t *testing.T) {
	h := newAuthHandler(t)
	rt, _ := Sign(Claims{Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenRefresh, Exp: 9999999999}, hSecret)
	// 禁用该用户后用其旧 token 续期，应被拒（403）。
	require.NoError(t, h.idb.UpdateUser(context.Background(), identity.User{
		ID: "u-admin", TenantID: "t-acme", Status: identity.StatusDisabled,
	}))
	rec := doJSON(t, h.Refresh, http.MethodPost, "/api/auth/tokens/refresh",
		`{"refreshToken":"`+rt+`"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMenusNonEmpty(t *testing.T) {
	h := newAuthHandler(t)
	rec := doJSON(t, h.Menus, http.MethodGet, "/api/system/menus", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var menus []Menu
	decodeData(t, rec.Body.Bytes(), &menus)
	assert.NotEmpty(t, menus)
	assert.Equal(t, "home", menus[0].Name)
}

func TestLoginSetsCookies(t *testing.T) {
	h := newAuthHandler(t)
	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions",
		`{"username":"admin","password":"123456"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var hasAccess, hasRefresh bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessCookieName {
			hasAccess = true
		}
		if c.Name == RefreshCookieName {
			hasRefresh = true
		}
	}
	assert.True(t, hasAccess, "登录成功应下发 access cookie")
	assert.True(t, hasRefresh, "登录成功应下发 refresh cookie")
}

func TestRefreshReadsCookie(t *testing.T) {
	h := newAuthHandler(t)
	rt, _ := Sign(Claims{Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenRefresh, Exp: 9999999999}, hSecret)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/tokens/refresh", nil)
	req.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: rt})
	rec := httptest.NewRecorder()
	h.Refresh(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var hasAccess bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessCookieName {
			hasAccess = true
		}
	}
	assert.True(t, hasAccess, "refresh 应重发 access cookie")
}

func TestRefreshBodyFallback(t *testing.T) {
	h := newAuthHandler(t)
	rt, _ := Sign(Claims{Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenRefresh, Exp: 9999999999}, hSecret)
	// 不带 cookie，走 body 兼容（SDK 调用场景）
	rec := doJSON(t, h.Refresh, http.MethodPost, "/api/auth/tokens/refresh",
		`{"refreshToken":"`+rt+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestLogoutClearsCookies(t *testing.T) {
	h := newAuthHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)
	rec := httptest.NewRecorder()
	h.Logout(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	cs := rec.Result().Cookies()
	require.NotEmpty(t, cs, "应下发清 cookie 指令")
	for _, c := range cs {
		assert.Less(t, c.MaxAge, 0, "cookie %s 应过期清除", c.Name)
	}
}
