package environment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/tenant"
)

type stubRepo struct {
	list    []Environment
	saved   Environment
	deleted string
}

func (s *stubRepo) List(context.Context) ([]Environment, error) { return s.list, nil }
func (s *stubRepo) ListAll(context.Context) ([]Environment, error) { return s.list, nil }
func (s *stubRepo) Get(_ context.Context, id string) (Environment, error) {
	for _, e := range s.list {
		if e.ID == id {
			return e, nil
		}
	}
	return Environment{}, errNotFound
}
func (s *stubRepo) Create(_ context.Context, e Environment) error { s.saved = e; return nil }
func (s *stubRepo) Delete(_ context.Context, id string) error     { s.deleted = id; return nil }
func (s *stubRepo) EnvType(_ context.Context, id string) (string, error) {
	for _, e := range s.list {
		if e.ID == id {
			return e.Type, nil
		}
	}
	return "", errNotFound
}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var errNotFound = notFoundErr{}

func newHandler(repo Repository) *Handler {
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	return h
}

func acmeReq(method, url, body string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	return r.WithContext(tenant.WithTenant(r.Context(), "t-acme"))
}

func TestListHandler(t *testing.T) {
	repo := &stubRepo{list: []Environment{{ID: "env-1", Name: "测试", Type: TypeTest}}}
	h := newHandler(repo)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/environments", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string][]Environment
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Len(t, out["data"], 1)
}

func TestCreateHandler(t *testing.T) {
	repo := &stubRepo{}
	h := newHandler(repo)
	body := `{"id":"env-x","name":"新环境","type":"test"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/environments", body))
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "env-x", repo.saved.ID)
}

func TestCreateRejectsInvalid(t *testing.T) {
	repo := &stubRepo{}
	h := newHandler(repo)
	// 缺 name + 非法 type
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/environments", `{"id":"x","type":"staging"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeleteHandler(t *testing.T) {
	repo := &stubRepo{list: []Environment{{ID: "env-1"}}}
	h := newHandler(repo)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodDelete, "/api/environments/env-1", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "env-1", repo.deleted)
}

func TestAuthorizeRejectsWrite(t *testing.T) {
	repo := &stubRepo{}
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return false }
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/environments", `{"id":"x","name":"n","type":"test"}`))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestCreateProdRequiresProdWrite(t *testing.T) {
	repo := &stubRepo{}
	h := NewHandler(repo)
	// 放行除 prod:write 外的所有权限（模拟 developer 角色）
	h.Authorize = func(r *http.Request, perm string) bool { return perm != PermProdWrite }

	// Create prod 环境 -> 需要 prod:write -> 被拦 403（developer 生产只读）
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/environments", `{"id":"env-prod","name":"prod","type":"prod"}`))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, repo.saved.ID, "prod 环境不应被创建")

	// Create test 环境 -> 不需 prod:write -> 放行 201
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, acmeReq(http.MethodPost, "/api/environments", `{"id":"env-test","name":"test","type":"test"}`))
	assert.Equal(t, http.StatusCreated, rec2.Code)
}

// TestDeleteProdRequiresProdWrite 验证删除生产环境需 prod:write（developer 被拦）。
func TestDeleteProdRequiresProdWrite(t *testing.T) {
	repo := &stubRepo{list: []Environment{{ID: "env-prod", Type: TypeProd}}}
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return perm != PermProdWrite }

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodDelete, "/api/environments/env-prod", ""))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, repo.deleted, "生产环境无 prod:write 不应被删除")
}

// TestDeleteFailClosedOnUnknownEnv 验证删除不存在环境时 fail-closed：
// EnvType 返回 err 应保守按生产处理，需 prod:write（developer 被拦）。
func TestDeleteFailClosedOnUnknownEnv(t *testing.T) {
	// 空 list → EnvType 对任何 id 返回 errNotFound。
	repo := &stubRepo{list: nil}
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return perm != PermProdWrite }

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodDelete, "/api/environments/env-ghost", ""))
	assert.Equal(t, http.StatusForbidden, rec.Code, "未知环境 fail-closed 应要求 prod:write")
}
