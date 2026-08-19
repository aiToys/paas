package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/service"
	svcmemory "github.com/aitoys/paas/internal/service/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

type stubAudit struct {
	actions []string
}

func (a *stubAudit) Record(_ context.Context, _, _, action, _, _, _ string) error {
	a.actions = append(a.actions, action)
	return nil
}

func newHandler(repo service.Repository, audit service.AuditRecorder) *service.Handler {
	h := service.NewHandler(repo, service.WithAudit(audit), service.WithActor(func(_ *http.Request) string { return "u-1" }))
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	return h
}

func acmeReq(method, url, body string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	return r.WithContext(tenant.WithTenant(r.Context(), "t-acme"))
}

func globexReq(method, url, body string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	return r.WithContext(tenant.WithTenant(r.Context(), "t-globex"))
}

func TestHandlerList(t *testing.T) {
	repo := svcmemory.NewStore()
	require.NoError(t, repo.Create(tenant.WithTenant(context.Background(), "t-acme"),
		service.Service{ID: "svc-1", TenantID: "t-acme", AppID: "app-1", Name: "web", Type: service.TypeWeb}))
	h := newHandler(repo, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/applications/app-1/services", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string][]service.Service
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Len(t, out["data"], 1)
}

func TestHandlerCreate(t *testing.T) {
	repo := svcmemory.NewStore()
	audit := &stubAudit{}
	h := newHandler(repo, audit)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-1/services",
		`{"id":"svc-x","name":"api","type":"backend","port":8080}`))
	require.Equal(t, http.StatusCreated, rec.Code)
	var out struct {
		Data service.Service `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, "svc-x", out.Data.ID)
	assert.Equal(t, "t-acme", out.Data.TenantID, "Create 以 ctx 租户为准")
	assert.Equal(t, []string{"service_create"}, audit.actions)
}

func TestHandlerCreateRejectsInvalidType(t *testing.T) {
	repo := svcmemory.NewStore()
	h := newHandler(repo, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-1/services",
		`{"id":"svc-x","name":"api","type":"weird"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandlerGet(t *testing.T) {
	repo := svcmemory.NewStore()
	require.NoError(t, repo.Create(tenant.WithTenant(context.Background(), "t-acme"),
		service.Service{ID: "svc-1", TenantID: "t-acme", AppID: "app-1", Name: "web", Type: service.TypeWeb}))
	h := newHandler(repo, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/applications/app-1/services/svc-1", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Data service.Service `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, "svc-1", out.Data.ID)
}

func TestHandlerGetCrossTenant404(t *testing.T) {
	repo := svcmemory.NewStore()
	require.NoError(t, repo.Create(tenant.WithTenant(context.Background(), "t-acme"),
		service.Service{ID: "svc-1", TenantID: "t-acme", AppID: "app-1", Name: "web", Type: service.TypeWeb}))
	h := newHandler(repo, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, globexReq(http.MethodGet, "/api/applications/app-1/services/svc-1", ""))
	assert.Equal(t, http.StatusNotFound, rec.Code, "跨租户访问 not found 不泄漏")
}

func TestHandlerUpdate(t *testing.T) {
	repo := svcmemory.NewStore()
	require.NoError(t, repo.Create(tenant.WithTenant(context.Background(), "t-acme"),
		service.Service{ID: "svc-1", TenantID: "t-acme", AppID: "app-1", Name: "web", Type: service.TypeWeb, Port: 80}))
	audit := &stubAudit{}
	h := newHandler(repo, audit)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPut, "/api/applications/app-1/services/svc-1",
		`{"name":"web","type":"web","port":8081}`))
	require.Equal(t, http.StatusOK, rec.Code)
	got, err := repo.Get(tenant.WithTenant(context.Background(), "t-acme"), "app-1", "svc-1")
	require.NoError(t, err)
	assert.Equal(t, 8081, got.Port)
	assert.Equal(t, []string{"service_update"}, audit.actions)
}

func TestHandlerDelete(t *testing.T) {
	repo := svcmemory.NewStore()
	require.NoError(t, repo.Create(tenant.WithTenant(context.Background(), "t-acme"),
		service.Service{ID: "svc-1", TenantID: "t-acme", AppID: "app-1", Name: "web", Type: service.TypeWeb}))
	audit := &stubAudit{}
	h := newHandler(repo, audit)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodDelete, "/api/applications/app-1/services/svc-1", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, "svc-1", out.Data["deleted"])
	assert.Equal(t, []string{"service_delete"}, audit.actions)
}

func TestHandlerCreateDuplicateName409(t *testing.T) {
	repo := svcmemory.NewStore()
	require.NoError(t, repo.Create(tenant.WithTenant(context.Background(), "t-acme"),
		service.Service{ID: "svc-1", TenantID: "t-acme", AppID: "app-1", Name: "api", Type: service.TypeBackend}))
	h := newHandler(repo, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-1/services",
		`{"id":"svc-2","name":"api","type":"backend"}`))
	assert.Equal(t, http.StatusConflict, rec.Code, "同应用重名服务应返回 409 而非 400")
}

func TestHandlerAuthorizeRejectsWrite(t *testing.T) {
	repo := svcmemory.NewStore()
	h := service.NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return false }
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-1/services",
		`{"id":"svc-x","name":"api","type":"backend"}`))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
