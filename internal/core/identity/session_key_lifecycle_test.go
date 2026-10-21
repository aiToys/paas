package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D5-3：会话失效与 Key 生命周期（撤销 / 到期 / 轮换）回归锁。

func newRepoHandler(t *testing.T) (*Handler, *fakeRepo) {
	t.Helper()
	repo := newFake()
	require.NoError(t, repo.CreateUser(context.Background(), User{
		ID: "u-1", TenantID: "t-acme", Name: "alice", Status: StatusActive,
		Roles: []string{"developer"},
	}))
	h := NewHandler(repo).HashPassword(func(plain string) (string, error) { return "hash:" + plain, nil })
	h.IsPlatformAdmin = func(*http.Request) bool { return true }
	return h, repo
}

func dataMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var wrap struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &wrap))
	return wrap.Data
}

// 敏感变更（角色/密码/状态/超管标记）自动撤销会话；无实质变化的更新不撤销。
func TestUpdateUserRevokesSessionsOnSensitiveChange(t *testing.T) {
	h, repo := newRepoHandler(t)

	// 1) 角色/状态/密码全不变：不撤销。
	rec := doReq(t, h.UpdateUser, http.MethodPut, "/api/admin/users/u-1",
		`{"name":"alice","email":"a@example.com","roles":["developer"],"status":"active"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, dataMap(t, rec.Body.Bytes()), "sessionVersion", "无敏感变更不应撤销会话")
	assert.Equal(t, 0, repo.users["u-1"].SessionVersion)

	// 2) 角色变更（集合不同）：撤销，版本 +1。
	rec = doReq(t, h.UpdateUser, http.MethodPut, "/api/admin/users/u-1",
		`{"name":"alice","email":"a@example.com","roles":["developer","viewer"],"status":"active"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.EqualValues(t, 1, dataMap(t, rec.Body.Bytes())["sessionVersion"])
	assert.Equal(t, 1, repo.users["u-1"].SessionVersion)

	// 3) 改密：再 +1。
	rec = doReq(t, h.UpdateUser, http.MethodPut, "/api/admin/users/u-1",
		`{"name":"alice","email":"a@example.com","roles":["developer","viewer"],"status":"active","password":"newpass123"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.EqualValues(t, 2, dataMap(t, rec.Body.Bytes())["sessionVersion"])

	// 4) 停用：再 +1。
	rec = doReq(t, h.UpdateUser, http.MethodPut, "/api/admin/users/u-1",
		`{"name":"alice","email":"a@example.com","roles":["developer","viewer"],"status":"disabled"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.EqualValues(t, 3, dataMap(t, rec.Body.Bytes())["sessionVersion"])

	// 5) 更新不得把代次写回 0（仓库层保留）。
	assert.Equal(t, 3, repo.users["u-1"].SessionVersion)
}

// 显式撤销会话端点：返回新代次；跨租户（非超管）404 不泄漏存在性。
func TestRevokeUserSessionsEndpoint(t *testing.T) {
	h, repo := newRepoHandler(t)

	rec := doReq(t, h.RevokeUserSessions, http.MethodPost, "/api/admin/users/u-1/sessions/revoke", "")
	require.Equal(t, http.StatusOK, rec.Code)
	data := dataMap(t, rec.Body.Bytes())
	assert.Equal(t, "u-1", data["userId"])
	assert.EqualValues(t, 1, data["sessionVersion"])
	assert.Equal(t, 1, repo.users["u-1"].SessionVersion)

	rec = doReq(t, h.RevokeUserSessions, http.MethodPost, "/api/admin/users/u-ghost/sessions/revoke", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// 非超管：只能撤销本租户用户。
	h.IsPlatformAdmin = func(*http.Request) bool { return false }
	r := withTenantReq(httptest.NewRequest(http.MethodPost, "/api/admin/users/u-1/sessions/revoke", nil), "t-globex")
	rec = httptest.NewRecorder()
	h.RevokeUserSessions(rec, r)
	assert.Equal(t, http.StatusNotFound, rec.Code, "跨租户撤销应 404 不泄漏")
}

// 轮换：新 Key 继承租户/用户/应用/角色，旧 Key 立即吊销；明文只返一次；对已吊销 Key 轮换 409。
func TestRotateAPIKey(t *testing.T) {
	ctx := context.Background()
	h, repo := newRepoHandler(t)
	require.NoError(t, repo.CreateAPIKey(ctx, APIKey{
		ID: "k-old", TenantID: "t-acme", UserID: "u-1", AppID: "app-1",
		Roles: []string{"developer"}, Key: "sk-old-key-1234567890",
	}))

	rec := doReq(t, h.RotateAPIKey, http.MethodPost, "/api/admin/api-keys/k-old/rotate", "")
	require.Equal(t, http.StatusOK, rec.Code)
	data := dataMap(t, rec.Body.Bytes())
	nk := data["key"].(string)
	assert.True(t, strings.HasPrefix(nk, "sk-"))
	assert.Equal(t, "t-acme", data["tenantId"])
	assert.Equal(t, "u-1", data["userId"])
	assert.Equal(t, "app-1", data["appId"])
	assert.Equal(t, "k-old", data["rotatedFrom"])
	roles, _ := data["roles"].([]any)
	assert.Equal(t, []any{"developer"}, roles)
	assert.Nil(t, data["expiresAt"], "轮换默认不继承到期时间")

	old, err := repo.GetAPIKey(ctx, "k-old")
	require.NoError(t, err)
	assert.NotNil(t, old.RevokedAt, "旧 Key 应立即吊销")
	assert.ErrorIs(t, old.Usable(time.Now()), ErrAPIKeyRevoked)

	// 对已吊销 Key 再轮换：409（轮换不等于复活）。
	rec = doReq(t, h.RotateAPIKey, http.MethodPost, "/api/admin/api-keys/k-old/rotate", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// 到期：过去时间被拒（应走吊销），未来时间生效，空串取消到期。
func TestSetAPIKeyExpiry(t *testing.T) {
	ctx := context.Background()
	h, repo := newRepoHandler(t)
	require.NoError(t, repo.CreateAPIKey(ctx, APIKey{
		ID: "k-exp", TenantID: "t-acme", UserID: "u-1", Key: "sk-exp-key-1234567890",
	}))

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	rec := doReq(t, h.SetAPIKeyExpiry, http.MethodPut, "/api/admin/api-keys/k-exp/expiry",
		`{"expiresAt":"`+past+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	rec = doReq(t, h.SetAPIKeyExpiry, http.MethodPut, "/api/admin/api-keys/k-exp/expiry",
		`{"expiresAt":"`+future+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	k, err := repo.GetAPIKey(ctx, "k-exp")
	require.NoError(t, err)
	require.NotNil(t, k.ExpiresAt)
	assert.Equal(t, "expiring", k.KeyState(time.Now()))

	rec = doReq(t, h.SetAPIKeyExpiry, http.MethodPut, "/api/admin/api-keys/k-exp/expiry", `{"expiresAt":""}`)
	require.Equal(t, http.StatusOK, rec.Code)
	k, err = repo.GetAPIKey(ctx, "k-exp")
	require.NoError(t, err)
	assert.Nil(t, k.ExpiresAt, "空串取消到期")
}

// doReqTenant 在指定租户 ctx 下发请求（自助端点区分调用者租户）。
func doReqTenant(t *testing.T, h http.HandlerFunc, method, target, body, tenantID string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	h(rec, withTenantReq(r, tenantID))
	return rec
}

// 同租户越权防护：非 tenant-admin 只能操作自己名下的 Key。
// 动机：轮换返回的新 Key 继承旧 Key 的用户与角色——若允许轮换他人 Key，
// 任何同租户成员都能凭一次轮换拿到管理员凭证明文（提权链）。
func TestAPIKeyWriteRequiresOwnership(t *testing.T) {
	ctx := context.Background()
	repo := newFake()
	require.NoError(t, repo.CreateAPIKey(ctx, APIKey{
		ID: "k-admin", TenantID: "t-acme", UserID: "u-admin",
		Roles: []string{"tenant-admin"}, Key: "sk-admin-key-1234567890",
	}))
	require.NoError(t, repo.CreateAPIKey(ctx, APIKey{
		ID: "k-dev", TenantID: "t-acme", UserID: "u-dev",
		Roles: []string{"developer"}, Key: "sk-dev-key-1234567890",
	}))
	require.NoError(t, repo.CreateAPIKey(ctx, APIKey{
		ID: "k-other", TenantID: "t-globex", UserID: "u-dev",
		Roles: []string{"developer"}, Key: "sk-other-key-1234567890",
	}))

	h := NewHandler(repo)
	h.CallerUserID = func(*http.Request) string { return "u-dev" }
	h.CallerRoles = func(*http.Request) []string { return []string{"developer"} }

	// 1) 开发者对管理员 Key 做任意写操作：403（不返回明文，不改状态）。
	cases := []struct {
		name   string
		h      http.HandlerFunc
		method string
		target string
		body   string
	}{
		{"rotate", h.RotateAPIKey, http.MethodPost, "/api/api-keys/k-admin/rotate", ""},
		{"revoke", h.RevokeAPIKey, http.MethodPost, "/api/api-keys/k-admin/revoke", ""},
		{"expiry", h.SetAPIKeyExpiry, http.MethodPut, "/api/api-keys/k-admin/expiry", `{"expiresAt":""}`},
		{"delete", h.DeleteAPIKey, http.MethodDelete, "/api/api-keys/k-admin", ""},
	}
	for _, c := range cases {
		rec := doReqTenant(t, c.h, c.method, c.target, c.body, "t-acme")
		assert.Equal(t, http.StatusForbidden, rec.Code, c.name)
	}
	assert.Nil(t, repo.keys["k-admin"].RevokedAt, "越权写操作不得改动目标 Key")
	assert.Len(t, repo.keys, 3, "越权轮换不得创建新 Key")

	// 2) 自己的 Key：可轮换（新 Key 继承自身角色），旧 Key 被吊销。
	rec := doReqTenant(t, h.RotateAPIKey, http.MethodPost, "/api/api-keys/k-dev/rotate", "", "t-acme")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotNil(t, repo.keys["k-dev"].RevokedAt)

	// 3) 跨租户（同用户 ID）：404 不泄漏存在性。
	rec = doReqTenant(t, h.RevokeAPIKey, http.MethodPost, "/api/api-keys/k-other/revoke", "", "t-acme")
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// 4) tenant-admin 可管理本租户他人 Key（保留管理员流程）。
	h.CallerRoles = func(*http.Request) []string { return []string{"tenant-admin"} }
	h.CallerUserID = func(*http.Request) string { return "u-admin" }
	rec = doReqTenant(t, h.RevokeAPIKey, http.MethodPost, "/api/api-keys/k-admin/revoke", "", "t-acme")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// 吊销：软吊销保留记录，重复吊销幂等，列表返回掩码。
func TestRevokeAPIKeyIdempotent(t *testing.T) {
	ctx := context.Background()
	h, repo := newRepoHandler(t)
	require.NoError(t, repo.CreateAPIKey(ctx, APIKey{
		ID: "k-rev", TenantID: "t-acme", UserID: "u-1", Key: "sk-rev-key-1234567890",
	}))

	for i := 0; i < 2; i++ {
		rec := doReq(t, h.RevokeAPIKey, http.MethodPost, "/api/admin/api-keys/k-rev/revoke", "")
		require.Equal(t, http.StatusOK, rec.Code)
		data := dataMap(t, rec.Body.Bytes())
		assert.NotNil(t, data["revokedAt"])
		assert.Contains(t, data["key"], "*")
	}
	k, err := repo.GetAPIKey(ctx, "k-rev")
	require.NoError(t, err)
	assert.NotNil(t, k.RevokedAt)
	assert.Equal(t, "revoked", k.KeyState(time.Now()))
	assert.ErrorIs(t, k.Usable(time.Now()), ErrAPIKeyRevoked)
}
