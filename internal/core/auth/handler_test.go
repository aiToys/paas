package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	// 登录闸门会读租户状态（冻结即拒），租户行必须存在。
	require.NoError(t, idb.CreateTenant(context.Background(), identity.Tenant{
		ID: "t-acme", Name: "Acme", Status: identity.TenantStatusActive,
	}))
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

// TestMeFromCookie 覆盖浏览器会话路径：access cookie（无 Authorization header）也能取到 profile。
// 回归防护：原 Me 只读 header，cookie 会话 401。
func TestMeFromCookie(t *testing.T) {
	h := newAuthHandler(t)
	tok, _ := Sign(Claims{
		Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenAccess, Exp: 9999999999,
	}, hSecret)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/users/me", nil)
	req.AddCookie(&http.Cookie{Name: AccessCookieName, Value: tok}) //nolint:gosec // 测试场景，Secure 由 PAAS_COOKIE_SECURE 控制
	rec := httptest.NewRecorder()
	h.Me(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var p UserProfile
	decodeData(t, rec.Body.Bytes(), &p)
	assert.Equal(t, "admin", p.Username)
	assert.Contains(t, p.Roles, "super_admin")
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
// TestRefreshRejectsStaleSessionVersion 验证会话代次（D5-3）：撤销会话后旧 refresh token 不得续期。
// 携带当前代次的 token 正常续期；不带代次（0）的存量 token 按约定不比对。
func TestRefreshRejectsStaleSessionVersion(t *testing.T) {
	h := newAuthHandler(t)
	ver, err := h.idb.RevokeUserSessions(context.Background(), "t-acme", "u-admin")
	require.NoError(t, err)
	require.Equal(t, 1, ver)

	// 显式旧代次（非 0）：0 会走「存量 token 不比对」，构造不出失配。
	stale, _ := Sign(Claims{Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenRefresh, Exp: 9999999999, Ver: 5}, hSecret)
	rec := doJSON(t, h.Refresh, http.MethodPost, "/api/auth/tokens/refresh", `{"refreshToken":"`+stale+`"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	current, _ := Sign(Claims{Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenRefresh, Exp: 9999999999, Ver: ver}, hSecret)
	rec = doJSON(t, h.Refresh, http.MethodPost, "/api/auth/tokens/refresh", `{"refreshToken":"`+current+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	legacy, _ := Sign(Claims{Sub: "u-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"},
		Typ: TokenRefresh, Exp: 9999999999}, hSecret)
	rec = doJSON(t, h.Refresh, http.MethodPost, "/api/auth/tokens/refresh", `{"refreshToken":"`+legacy+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code, "存量无代次 token 按约定不比对")
}

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

	// 2026-10-19 IA 重构（spec §5）：平台总览 + 六职责域两层菜单。
	groups := map[string][]string{} // 分组名 -> 子入口名
	paths := map[string]Menu{}      // 全量叶子（含隐藏/兼容 redirect）
	var walk func(list []Menu)
	walk = func(list []Menu) {
		for i := range list {
			m := &list[i]
			if len(m.Children) > 0 {
				groups[m.Name] = nil
				for _, c := range m.Children {
					groups[m.Name] = append(groups[m.Name], c.Name)
				}
				walk(m.Children)
				continue
			}
			paths[m.Path] = *m
		}
	}
	walk(menus)

	for _, g := range []string{"access", "aiops", "delivery", "infra", "usage", "secops"} {
		require.Contains(t, groups, g, "应含职责域分组 %s", g)
		assert.NotEmpty(t, groups[g], "分组 %s 不应为空", g)
	}
	// 租户管理归入「租户与访问」域。
	require.Contains(t, groups["access"], "systemTenant", "access 下应含租户管理")

	// 兼容跳转（spec §5.1）：旧路径必须带 redirect 目标，防死链回归。
	for path, target := range map[string]string{
		"/system/role":           "/system/user?tab=roles",
		"/resources/buildruns":   "/resources/pipelineruns?tab=builds",
		"/resources/images":      "/resources/pipelineruns?tab=images",
		"/resources/releases":    "/resources/pipelineruns?tab=releases",
		"/resources/engines":     "/resources/dataservices?tab=engines",
		"/resources/alert-rules": "/operations/observability?tab=rules",
	} {
		m, ok := paths[path]
		require.True(t, ok, "应保留兼容入口 %s", path)
		assert.Equal(t, target, m.Redirect, "兼容入口 %s 应重定向到 %s", path, target)
	}
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
	req.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: rt}) //nolint:gosec // G124 测试 cookie 无需安全属性
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

func TestLoginRateLimitedAfter5Fails(t *testing.T) {
	h := newAuthHandler(t)
	body := `{"username":"admin","password":"wrong"}`
	for i := 0; i < 5; i++ {
		rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions", body)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "前 5 次应 401")
	}
	// 第 6 次应 429（per-IP+per-username 锁定）
	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions", body)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "5 次失败后应 429")
}

type mockAuditEntry struct{ tenant, actor, action, detail string }

type mockAudit struct {
	mu      sync.Mutex
	records []mockAuditEntry
}

func (m *mockAudit) Record(_ context.Context, tenantID, actor, action, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, mockAuditEntry{tenantID, actor, action, detail})
	return nil
}

func (m *mockAudit) hasAction(action string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records {
		if r.action == action {
			return true
		}
	}
	return false
}

func TestLoginRecordsAudit(t *testing.T) {
	h := newAuthHandler(t)
	auditor := &mockAudit{}
	h.WithAudit(auditor)
	// 成功登录
	doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions", `{"username":"admin","password":"123456"}`)
	assert.True(t, auditor.hasAction("login"), "成功登录应记 login 审计")
	// 失败登录
	doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions", `{"username":"admin","password":"wrong"}`)
	assert.True(t, auditor.hasAction("login_failed"), "失败登录应记 login_failed 审计")
}

func TestLogoutRecordsAudit(t *testing.T) {
	h := newAuthHandler(t)
	auditor := &mockAudit{}
	h.WithAudit(auditor)
	// 先登录拿 cookie，再 logout
	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions", `{"username":"admin","password":"123456"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	req := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	h.Logout(httptest.NewRecorder(), req)
	assert.True(t, auditor.hasAction("logout"), "登出应记 logout 审计")
}

// 冻结租户不能登录、也不能续期：控制面准入关闭，存量 access token 到期后自然失效。
func TestLoginAndRefreshRejectedForFrozenTenant(t *testing.T) {
	idb := idmemory.NewStore()
	hash, err := HashPassword("123456")
	require.NoError(t, err)
	require.NoError(t, idb.CreateTenant(context.Background(), identity.Tenant{
		ID: "t-frozen", Name: "Frozen", Status: identity.TenantStatusFrozen,
	}))
	require.NoError(t, idb.CreateUser(context.Background(), identity.User{
		ID: "u-frozen", TenantID: "t-frozen", Name: "frozen",
		PasswordHash: hash, Roles: []string{"developer"}, Status: identity.StatusActive,
	}))
	h := NewHandler(idb, hSecret, false)

	rec := doJSON(t, h.Login, http.MethodPost, "/api/auth/sessions",
		`{"username":"frozen","password":"123456"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "冻结")

	refresh, err := Sign(Claims{
		Sub: "u-frozen", Tenant: "t-frozen", Roles: []string{"developer"},
		Typ: TokenRefresh, Exp: 9999999999,
	}, hSecret)
	require.NoError(t, err)
	rec = doJSON(t, h.Refresh, http.MethodPost, "/api/auth/tokens/refresh",
		`{"refreshToken":"`+refresh+`"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
}
