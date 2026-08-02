package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/tenant"
)

// fakeRepo 是最小内存 Repository（测试专用，避免 import memory 子包导致循环）。
type fakeRepo struct {
	tenants map[string]Tenant
	users   map[string]User
	keys    map[string]APIKey // 按 ID 索引
}

func newFake() *fakeRepo {
	return &fakeRepo{tenants: map[string]Tenant{}, users: map[string]User{}, keys: map[string]APIKey{}}
}

func (f *fakeRepo) CreateTenant(_ context.Context, t Tenant) error {
	if _, ok := f.tenants[t.ID]; ok {
		return errConflict("租户")
	}
	f.tenants[t.ID] = t
	return nil
}
func (f *fakeRepo) GetTenant(_ context.Context, id string) (Tenant, error) {
	t, ok := f.tenants[id]
	if !ok {
		return Tenant{}, errNotFound("租户")
	}
	return t, nil
}
func (f *fakeRepo) CreateUser(_ context.Context, u User) error {
	if _, ok := f.users[u.ID]; ok {
		return errConflict("用户")
	}
	f.users[u.ID] = u
	return nil
}
func (f *fakeRepo) UsersByTenant(_ context.Context, tenantID string) ([]User, error) {
	var out []User
	for _, u := range f.users {
		if u.TenantID == tenantID {
			out = append(out, u)
		}
	}
	return out, nil
}
func (f *fakeRepo) GetUserByName(_ context.Context, name string) (*User, error) {
	for _, u := range f.users {
		if u.Name == name {
			uu := u
			return &uu, nil
		}
	}
	return nil, errNotFound("用户")
}
func (f *fakeRepo) GetUser(_ context.Context, tenantID, userID string) (*User, error) {
	u, ok := f.users[userID]
	if !ok || u.TenantID != tenantID {
		return nil, errNotFound("用户")
	}
	uu := u
	return &uu, nil
}
func (f *fakeRepo) CreateAPIKey(_ context.Context, k APIKey) error {
	if _, ok := f.keys[k.ID]; ok {
		return errConflict("API Key")
	}
	f.keys[k.ID] = k
	return nil
}
func (f *fakeRepo) LookupAPIKey(_ context.Context, key string) (APIKey, error) {
	for _, k := range f.keys {
		if k.Key == key {
			return k, nil
		}
	}
	return APIKey{}, errNotFound("API Key")
}
func (f *fakeRepo) ListTenants(_ context.Context) ([]Tenant, error) {
	out := make([]Tenant, 0, len(f.tenants))
	for _, t := range f.tenants {
		out = append(out, t)
	}
	return out, nil
}
func (f *fakeRepo) DeleteTenant(_ context.Context, id string) error {
	if _, ok := f.tenants[id]; !ok {
		return errNotFound("租户")
	}
	// 非空保护：有用户拒绝（与 memory/pg 同源）
	for _, u := range f.users {
		if u.TenantID == id {
			return fmt.Errorf("%w: %s", ErrTenantNotEmpty, id)
		}
	}
	delete(f.tenants, id)
	return nil
}
func (f *fakeRepo) ListUsers(_ context.Context, tenantID string) ([]User, error) {
	var out []User
	for _, u := range f.users {
		if tenantID == "" || u.TenantID == tenantID {
			out = append(out, u)
		}
	}
	return out, nil
}
func (f *fakeRepo) UpdateUser(_ context.Context, u User) error {
	cur, ok := f.users[u.ID]
	if !ok {
		return errNotFound("用户")
	}
	if u.PasswordHash == "" {
		u.PasswordHash = cur.PasswordHash
	}
	u.CreatedAt = cur.CreatedAt
	f.users[u.ID] = u
	return nil
}
func (f *fakeRepo) DeleteUser(_ context.Context, _, userID string) error {
	if _, ok := f.users[userID]; !ok {
		return errNotFound("用户")
	}
	delete(f.users, userID)
	return nil
}
func (f *fakeRepo) ListAPIKeys(_ context.Context, tenantID string) ([]APIKey, error) {
	var out []APIKey
	for _, k := range f.keys {
		if tenantID == "" || k.TenantID == tenantID {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeRepo) DeleteAPIKey(_ context.Context, id string) error {
	if _, ok := f.keys[id]; !ok {
		return errNotFound("API Key")
	}
	delete(f.keys, id)
	return nil
}

func errConflict(s string) error { return &testErr{s + " 已存在"} }
func errNotFound(s string) error { return &testErr{s + " 不存在"} }

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

// —— 测试 ——

func newAdminHandler() *Handler {
	h := NewHandler(newFake()).HashPassword(func(plain string) (string, error) {
		return "hash:" + plain, nil
	})
	// 现有管理测试模拟平台超管视角（跨租户）。
	h.IsPlatformAdmin = func(*http.Request) bool { return true }
	return h
}

// withTenantReq 注入租户上下文（pkg/tenant，无循环依赖）。
func withTenantReq(r *http.Request, tenantID string) *http.Request {
	return r.WithContext(tenant.WithTenant(r.Context(), tenantID))
}

func doReq(t *testing.T, h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

func dataSlice(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var wrap struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &wrap))
	return wrap.Data
}

func TestTenantsCRUD(t *testing.T) {
	h := newAdminHandler()
	rec := doReq(t, h.CreateTenant, http.MethodPost, "/api/admin/tenants", `{"id":"t-new","name":"NewCo"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = doReq(t, h.ListTenants, http.MethodGet, "/api/admin/tenants", "")
	require.Equal(t, http.StatusOK, rec.Code)
	names := []string{}
	for _, tnt := range dataSlice(t, rec.Body.Bytes()) {
		names = append(names, tnt["name"].(string))
	}
	assert.Contains(t, names, "NewCo")

	rec = doReq(t, h.DeleteTenant, http.MethodDelete, "/api/admin/tenants/t-new", "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDeleteTenantNonEmptyRejected(t *testing.T) {
	h := newAdminHandler()
	// 建租户 + 用户
	require.Equal(t, http.StatusOK, doReq(t, h.CreateTenant, http.MethodPost, "/api/admin/tenants", `{"id":"t-ne","name":"NonEmpty"}`).Code)
	require.Equal(t, http.StatusOK, doReq(t, h.CreateUser, http.MethodPost, "/api/admin/users",
		`{"id":"u-ne","tenantId":"t-ne","name":"ne-user","password":"secret","roles":["developer"]}`).Code)
	// 删有用户的租户 -> 409
	rec := doReq(t, h.DeleteTenant, http.MethodDelete, "/api/admin/tenants/t-ne", "")
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "仍有用户")
	// 清用户后删 -> 204
	require.Equal(t, http.StatusNoContent, doReq(t, h.DeleteUser, http.MethodDelete, "/api/admin/users/u-ne", "").Code)
	assert.Equal(t, http.StatusNoContent, doReq(t, h.DeleteTenant, http.MethodDelete, "/api/admin/tenants/t-ne", "").Code)
}

func TestUsersCRUDNoPasswordLeak(t *testing.T) {
	h := newAdminHandler()
	rec := doReq(t, h.CreateUser, http.MethodPost, "/api/admin/users",
		`{"id":"u1","tenantId":"t1","name":"alice","password":"secret","roles":["developer"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = doReq(t, h.ListUsers, http.MethodGet, "/api/admin/users", "")
	require.Equal(t, http.StatusOK, rec.Code)
	us := dataSlice(t, rec.Body.Bytes())
	require.Len(t, us, 1)
	_, hasHash := us[0]["passwordHash"]
	assert.False(t, hasHash, "列表不应回传 passwordHash")

	rec = doReq(t, h.UpdateUser, http.MethodPut, "/api/admin/users/u1",
		`{"name":"alice2","password":"newpw","roles":["viewer"],"isAdmin":false,"status":"active"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = doReq(t, h.DeleteUser, http.MethodDelete, "/api/admin/users/u1", "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestAPIKeyCreatePlaintextListMasks(t *testing.T) {
	h := newAdminHandler()
	rec := doReq(t, h.CreateAPIKey, http.MethodPost, "/api/admin/api-keys",
		`{"tenantId":"t1","userId":"u1","roles":["developer"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var wrap struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &wrap))
	key := wrap.Data["key"].(string)
	assert.True(t, strings.HasPrefix(key, "sk-"))

	rec = doReq(t, h.ListAPIKeys, http.MethodGet, "/api/admin/api-keys", "")
	require.Equal(t, http.StatusOK, rec.Code)
	ks := dataSlice(t, rec.Body.Bytes())
	require.Len(t, ks, 1)
	masked := ks[0]["key"].(string)
	assert.Contains(t, masked, "*")
	assert.NotEqual(t, key, masked)
}

// TestAPIKeySelfServiceCapsRoles 验证自助 API Key（/api/api-keys，非超管）：
// 创建时强制本租户 + 绑定调用者用户 + roles 封顶（请求超自身角色被剔除→镜像自身，零提权）；
// 删除经 lastID 兼容自助路径，跨租户删 404 不泄漏。
func TestAPIKeySelfServiceCapsRoles(t *testing.T) {
	repo := newFake()
	// 预置 developer 用户作自助调用者。
	require.NoError(t, repo.CreateUser(context.Background(), User{ID: "u-dev", TenantID: "t-acme", Name: "dev", Status: StatusActive}))

	h := NewHandler(repo)
	h.IsPlatformAdmin = func(*http.Request) bool { return false } // 自助/tenant-admin
	h.CallerUserID = func(*http.Request) string { return "u-dev" }
	h.CallerRoles = func(*http.Request) []string { return []string{"developer"} }

	// developer 尝试创建 tenant-admin 密钥（提权尝试），并伪造他租户归属。
	r := withTenantReq(httptest.NewRequest(http.MethodPost, "/api/api-keys",
		strings.NewReader(`{"tenantId":"t-globex","userId":"u-x","roles":["tenant-admin"]}`)), "t-acme")
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.CreateAPIKey(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)

	var wrap struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &wrap))
	// 强制本租户 + 绑定调用者：body 的 t-globex/u-x 被覆盖。
	assert.Equal(t, "t-acme", wrap.Data["tenantId"])
	assert.Equal(t, "u-dev", wrap.Data["userId"])
	// roles 封顶：请求 tenant-admin 被剔除 → 求交为空 → 镜像调用者 [developer]。
	roles, _ := wrap.Data["roles"].([]any)
	require.Len(t, roles, 1)
	assert.Equal(t, "developer", roles[0])
	// 明文 key 返回一次。
	assert.True(t, strings.HasPrefix(wrap.Data["key"].(string), "sk-"))

	// 自助删除经 lastID（/api/api-keys/{id}）。
	kid := wrap.Data["id"].(string)
	r = withTenantReq(httptest.NewRequest(http.MethodDelete, "/api/api-keys/"+kid, nil), "t-acme")
	rec = httptest.NewRecorder()
	h.DeleteAPIKey(rec, r)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// 删除他租户密钥 → 404 不泄漏（apiKeyInCallerTenant 拒绝）。
	require.NoError(t, repo.CreateAPIKey(context.Background(), APIKey{ID: "k-globex", TenantID: "t-globex", UserID: "u-g", Key: "sk-x"}))
	r = withTenantReq(httptest.NewRequest(http.MethodDelete, "/api/api-keys/k-globex", nil), "t-acme")
	rec = httptest.NewRecorder()
	h.DeleteAPIKey(rec, r)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// 自助列表仅本租户（看不到 t-globex 的 key）。
	r = withTenantReq(httptest.NewRequest(http.MethodGet, "/api/api-keys", nil), "t-acme")
	rec = httptest.NewRecorder()
	h.ListAPIKeys(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
	for _, k := range dataSlice(t, rec.Body.Bytes()) {
		assert.Equal(t, "t-acme", k["tenantId"], "自助列表不应泄漏他租户密钥")
	}

	// 空 body 自助创建（前端真实路径，不传 tenantId/userId）：从 ctx 补全，不 400。
	r = withTenantReq(httptest.NewRequest(http.MethodPost, "/api/api-keys",
		strings.NewReader(`{}`)), "t-acme")
	r.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.CreateAPIKey(rec, r)
	require.Equal(t, http.StatusOK, rec.Code, "空 body 自助创建应从 ctx 补全归属")
}

func TestListRoles(t *testing.T) {
	h := newAdminHandler()
	rec := doReq(t, h.ListRoles, http.MethodGet, "/api/admin/roles", "")
	require.Equal(t, http.StatusOK, rec.Code)
	names := []string{}
	for _, r := range dataSlice(t, rec.Body.Bytes()) {
		names = append(names, r["name"].(string))
	}
	assert.Contains(t, names, "tenant-admin")
	assert.Contains(t, names, "developer")
	assert.Contains(t, names, "viewer")
}

func TestMaskKeyShort(t *testing.T) {
	assert.Equal(t, "***", maskKey("abc"))
	assert.Contains(t, maskKey("sk-abcd1234567890"), "*")
}

// TestTenantAdminCannotCrossTenant 验证安全修复：普通 tenant-admin（非平台超管）
// 不能删除/查看其他租户的用户与 API Key（旧实现无租户作用域，可跨租户越权）。
func TestTenantAdminCannotCrossTenant(t *testing.T) {
	repo := newFake()
	require.NoError(t, repo.CreateUser(context.Background(), User{ID: "u-acme", TenantID: "t-acme", Name: "a", Status: StatusActive}))
	require.NoError(t, repo.CreateUser(context.Background(), User{ID: "u-globex", TenantID: "t-globex", Name: "g", Status: StatusActive}))

	h := NewHandler(repo)
	h.IsPlatformAdmin = func(*http.Request) bool { return false } // 普通 tenant-admin

	// t-acme 的 tenant-admin 尝试删 t-globex 的用户 → 应 404（userInCallerTenant 拒绝）。
	r := withTenantReq(httptest.NewRequest(http.MethodDelete, "/api/admin/users/u-globex", nil), "t-acme")
	rec := httptest.NewRecorder()
	h.DeleteUser(rec, r)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// 列表：tenant-admin 只看到本租户用户（1 条），看不到 t-globex。
	r = withTenantReq(httptest.NewRequest(http.MethodGet, "/api/admin/users", nil), "t-acme")
	rec = httptest.NewRecorder()
	h.ListUsers(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
	users := dataSlice(t, rec.Body.Bytes())
	assert.Len(t, users, 1)
	assert.Equal(t, "u-acme", users[0]["id"])

	// 租户枚举对 tenant-admin 禁止（仅平台超管）。
	r = withTenantReq(httptest.NewRequest(http.MethodGet, "/api/admin/tenants", nil), "t-acme")
	rec = httptest.NewRecorder()
	h.ListTenants(rec, r)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// tenant-admin 创建用户时不可授予超管（防提权）。
	r = withTenantReq(httptest.NewRequest(http.MethodPost, "/api/admin/users",
		strings.NewReader(`{"id":"u2","tenantId":"t-globex","name":"x","isAdmin":true}`)), "t-acme")
	r.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.CreateUser(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
	created, _ := repo.GetUser(context.Background(), "t-acme", "u2") // 强制落到调用者租户
	assert.Equal(t, "t-acme", created.TenantID, "tenant-admin 创建必须归属本租户")
	assert.False(t, created.IsAdmin, "tenant-admin 不可授予超管")
}

// TestPlatformAdminCrossTenant 验证平台超管仍可跨租户管理（不破坏平台运维能力）。
func TestPlatformAdminCrossTenant(t *testing.T) {
	repo := newFake()
	require.NoError(t, repo.CreateUser(context.Background(), User{ID: "u-globex", TenantID: "t-globex", Name: "g", Status: StatusActive}))
	h := NewHandler(repo)
	h.IsPlatformAdmin = func(*http.Request) bool { return true }

	r := withTenantReq(httptest.NewRequest(http.MethodDelete, "/api/admin/users/u-globex", nil), "t-acme")
	rec := httptest.NewRecorder()
	h.DeleteUser(rec, r)
	assert.Equal(t, http.StatusNoContent, rec.Code, "平台超管可跨租户删除")
}
