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

// 平台停用类拒绝走 403（Key 写错才是 401）：运维需要能区分「抄错 Key」与「被冻结/到期/吊销」。
func TestBearerAuthCredentialDeniedStates(t *testing.T) {
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	revoked := time.Now()

	cases := []struct {
		name  string
		setup func(t *testing.T, idb *idmemory.Store)
		want  int
	}{
		{"frozen tenant", func(t *testing.T, idb *idmemory.Store) {
			_, err := idb.SetTenantStatus(ctx, "t-acme", identity.TenantStatusFrozen, "欠费")
			require.NoError(t, err)
		}, http.StatusForbidden},
		{"expired key", func(t *testing.T, idb *idmemory.Store) {
			require.NoError(t, idb.SetAPIKeyExpiry(ctx, "k1", past))
		}, http.StatusForbidden},
		{"revoked key", func(t *testing.T, idb *idmemory.Store) {
			require.NoError(t, idb.RevokeAPIKey(ctx, "k1", revoked))
		}, http.StatusForbidden},
		{"unknown key", func(t *testing.T, _ *idmemory.Store) {}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idb := idmemory.NewStore()
			require.NoError(t, idb.CreateTenant(ctx, identity.Tenant{ID: "t-acme", Name: "Acme"}))
			require.NoError(t, idb.CreateAPIKey(ctx, identity.APIKey{
				ID: "k1", TenantID: "t-acme", UserID: "u1", Key: "sk-acme-dev",
			}))
			tc.setup(t, idb)

			h := BearerAuth(idb, testBearerSecret)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Fatal("停用的凭证不应进入下游")
			}))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.want == http.StatusUnauthorized {
				req.Header.Set("Authorization", "Bearer sk-other")
			} else {
				req.Header.Set("Authorization", "Bearer sk-acme-dev")
			}
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

// 鉴权成功时回写「最近使用」，且节流不阻断请求。
func TestBearerAuthTouchesUsage(t *testing.T) {
	ctx := context.Background()
	idb := idmemory.NewStore()
	require.NoError(t, idb.CreateTenant(ctx, identity.Tenant{ID: "t-acme", Name: "Acme"}))
	require.NoError(t, idb.CreateAPIKey(ctx, identity.APIKey{
		ID: "k-usage", TenantID: "t-acme", UserID: "u1", Key: "sk-usage",
	}))

	h := BearerAuth(idb, testBearerSecret)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer sk-usage")
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}
	k, err := idb.GetAPIKey(ctx, "k-usage")
	require.NoError(t, err)
	assert.NotNil(t, k.LastUsedAt, "成功鉴权后应回写最近使用时间")
}

// —— 会话代次（D5-3）——

// 携带旧代次的 access token 在撤销会话后即失效；代次一致则放行。
func TestBearerAuthRejectsStaleSessionVersion(t *testing.T) {
	ctx := context.Background()
	idb := idmemory.NewStore()
	require.NoError(t, idb.CreateUser(ctx, identity.User{
		ID: "u1", TenantID: "t-acme", Name: "alice", Status: identity.StatusActive,
	}))
	ver, err := idb.RevokeUserSessions(ctx, "t-acme", "u1")
	require.NoError(t, err)
	require.Equal(t, 1, ver)

	ok := BearerAuth(idb, testBearerSecret)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	sign := func(v int) string {
		tok, serr := auth.Sign(auth.Claims{Sub: "u1", Tenant: "t-acme", Roles: []string{"developer"},
			Typ: auth.TokenAccess, Exp: time.Now().Add(time.Minute).Unix(), Ver: v}, testBearerSecret)
		require.NoError(t, serr)
		return tok
	}
	call := func(tok string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		ok.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusUnauthorized, call(sign(5)), "旧代次应被拒")
	assert.Equal(t, http.StatusOK, call(sign(ver)), "当前代次应放行")
	assert.Equal(t, http.StatusOK, call(sign(0)), "存量无代次 token 按约定不比对")
}

// 代次缓存窗口：撤销后 TTL 内仍可能放行（既定取舍），过期后必须拒绝。
func TestSessionGateCacheWindow(t *testing.T) {
	ctx := context.Background()
	idb := idmemory.NewStore()
	require.NoError(t, idb.CreateUser(ctx, identity.User{ID: "u1", TenantID: "t-acme", Name: "bob"}))
	g := newSessionGate(idb)
	base := time.Now()
	g.now = func() time.Time { return base }

	require.True(t, g.valid(ctx, "t-acme", "u1", 0), "代次 0 不比对（且不写缓存）")
	_, err := idb.RevokeUserSessions(ctx, "t-acme", "u1")
	require.NoError(t, err)

	// 首次比对立即读库：失配即拒，同时把当前代次（1）写入缓存。
	require.False(t, g.valid(ctx, "t-acme", "u1", 2))
	require.True(t, g.valid(ctx, "t-acme", "u1", 1))

	// TTL 内再次撤销，缓存仍按 1 判定（既定的最坏 30s 生效延迟）。
	_, err = idb.RevokeUserSessions(ctx, "t-acme", "u1")
	require.NoError(t, err)
	require.True(t, g.valid(ctx, "t-acme", "u1", 1), "TTL 内按缓存判定")

	// 过期后重新加载：旧代次被拒，新代次放行。
	base = base.Add(sessionCacheTTL + time.Second)
	require.False(t, g.valid(ctx, "t-acme", "u1", 1))
	require.True(t, g.valid(ctx, "t-acme", "u1", 2))
}
