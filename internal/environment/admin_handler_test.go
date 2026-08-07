package environment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/environment/memory"
)

type errT struct{}

func (errT) Error() string { return "not found" }

var errStub = errT{}

type stubTenants struct{ allow string }

func (s stubTenants) Exists(_ context.Context, id string) error {
	if id == s.allow {
		return nil
	}
	return errStub
}

type stubAudit struct{ action string }

func (a *stubAudit) Record(_ context.Context, _, _, action, _, _, _ string) error {
	a.action = action
	return nil
}

func TestAdminCreateEnvironmentAttachesTenant(t *testing.T) {
	repo := memory.NewStore()
	au := &stubAudit{}
	h := environment.NewAdminHandler(repo,
		environment.WithAdminTenants(stubTenants{allow: "t-acme"}),
		environment.WithAdminAudit(au),
	)
	body := bytes.NewReader([]byte(`{"tenantId":"t-acme","id":"env-1","name":"prod","type":"prod","cluster":"prod-bj"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/environments", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data environment.Environment `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Data.TenantID != "t-acme" {
		t.Fatalf("tenant=%s want t-acme", out.Data.TenantID)
	}
	if au.action != "admin:create" {
		t.Fatalf("audit action=%s want admin:create", au.action)
	}
}

func TestAdminCreateEnvironmentMissingTenant(t *testing.T) {
	h := environment.NewAdminHandler(memory.NewStore())
	body := bytes.NewReader([]byte(`{"id":"e","name":"x","type":"prod"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/environments", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", rec.Code)
	}
}

func TestAdminCreateEnvironmentUnknownTenant(t *testing.T) {
	h := environment.NewAdminHandler(memory.NewStore(), environment.WithAdminTenants(stubTenants{allow: "t-acme"}))
	body := bytes.NewReader([]byte(`{"tenantId":"t-ghost","id":"e","name":"x","type":"prod"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/environments", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", rec.Code)
	}
}
