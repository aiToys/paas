package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/service"
	"github.com/aitoys/paas/pkg/tenant"
)

func ctxOf(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

func TestCreateGetRoundTrip(t *testing.T) {
	s := NewStore()
	in := service.Service{ID: "svc-1", AppID: "app-1", Name: "bff", Type: service.TypeBackend, Port: 8080, RepoPath: "services/bff"}
	if err := s.Create(ctxOf("t-acme"), in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctxOf("t-acme"), "app-1", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "bff" || got.Type != service.TypeBackend {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	_ = s.Create(ctxOf("t-acme"), service.Service{ID: "svc-1", AppID: "app-1", Name: "bff", Type: service.TypeBackend})
	if _, err := s.Get(ctxOf("t-globex"), "app-1", "svc-1"); err != service.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestNameUniquePerApp(t *testing.T) {
	s := NewStore()
	_ = s.Create(ctxOf("t-acme"), service.Service{ID: "svc-1", AppID: "app-1", Name: "bff", Type: service.TypeBackend})
	err := s.Create(ctxOf("t-acme"), service.Service{ID: "svc-2", AppID: "app-1", Name: "bff", Type: service.TypeWeb})
	if err != service.ErrExists {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestValidateRejectsBadType(t *testing.T) {
	err := service.Service{ID: "s", AppID: "a", Name: "x", Type: "nope"}.Validate()
	if err == nil {
		t.Fatal("want error for invalid type")
	}
}
