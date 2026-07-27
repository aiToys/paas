package workload

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

// stubRepo 用于 handler 测试，固定返回 acme 租户的一条 service。
type stubRepo struct {
	list      []Workload
	saved     Workload
	deleted   string
	updatedID string
}

func (s *stubRepo) List(context.Context, string, string, string) ([]Workload, error) {
	return s.list, nil
}
func (s *stubRepo) Get(_ context.Context, id string) (Workload, error) {
	for _, w := range s.list {
		if w.ID == id {
			return w, nil
		}
	}
	return Workload{}, errNotFound
}
func (s *stubRepo) Create(_ context.Context, w Workload) error { s.saved = w; return nil }
func (s *stubRepo) Update(_ context.Context, id string, r int, st string) (Workload, error) {
	s.updatedID = id
	for _, w := range s.list {
		if w.ID == id {
			w.Replicas = r
			w.Status = st
			return w, nil
		}
	}
	return Workload{}, errNotFound
}
func (s *stubRepo) Delete(_ context.Context, id string) error { s.deleted = id; return nil }

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var errNotFound = notFoundErr{}

func newHandler(repo Repository) *Handler {
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true } // 测试全放行
	return h
}

func acmeReq(method, url string, body string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	return r.WithContext(tenant.WithTenant(r.Context(), "t-acme"))
}

func TestListByAppHandler(t *testing.T) {
	repo := &stubRepo{list: []Workload{{ID: "wl-1", AppID: "app-cs", Type: TypeService, Name: "cs-api"}}}
	h := newHandler(repo)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/applications/app-cs/workloads", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string][]Workload
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Len(t, out["data"], 1)
}

func TestListCrossAppWithType(t *testing.T) {
	repo := &stubRepo{list: []Workload{{ID: "wl-1", Type: TypeService}}}
	h := newHandler(repo)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/workloads?type=service", ""))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestCreateHandler(t *testing.T) {
	repo := &stubRepo{}
	h := newHandler(repo)
	body := `{"id":"wl-x","type":"service","name":"n","image":"img","replicas":2}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-cs/workloads", body))
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "wl-x", repo.saved.ID)
	assert.Equal(t, "app-cs", repo.saved.AppID)
}

func TestCreateRejectsInvalid(t *testing.T) {
	repo := &stubRepo{}
	h := newHandler(repo)
	// 缺 image
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-cs/workloads", `{"id":"wl-x","type":"service","name":"n"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateHandler(t *testing.T) {
	repo := &stubRepo{list: []Workload{{ID: "wl-1", Type: TypeService, Replicas: 1}}}
	h := newHandler(repo)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPut, "/api/workloads/wl-1", `{"replicas":5,"status":"running"}`))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "wl-1", repo.updatedID)
}

func TestDeleteHandler(t *testing.T) {
	repo := &stubRepo{list: []Workload{{ID: "wl-1"}}}
	h := newHandler(repo)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodDelete, "/api/workloads/wl-1", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "wl-1", repo.deleted)
}

func TestAuthorizeRejectsWrite(t *testing.T) {
	repo := &stubRepo{}
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return false } // 无写权限
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-cs/workloads", `{"id":"x","type":"service","name":"n","image":"i"}`))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
