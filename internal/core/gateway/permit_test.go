package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequireAllowsAdmin(t *testing.T) {
	h := Require("model:infer")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(WithRoles(req.Context(), []string{"tenant-admin"}))
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireAllowsDeveloperInfer(t *testing.T) {
	h := Require("model:infer")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(WithRoles(req.Context(), []string{"developer"}))
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireRejectsViewerInfer(t *testing.T) {
	h := Require("model:infer")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("viewer 不应能推理")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(WithRoles(req.Context(), []string{"viewer"}))
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireRejectsMissingRoles(t *testing.T) {
	h := Require("application:read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("无角色不应放行")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/applications", nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
