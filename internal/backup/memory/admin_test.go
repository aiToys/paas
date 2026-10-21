package memory

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/backup"
	"github.com/aitoys/paas/pkg/tenant"
)

// TestListAllCrossTenant ListAll 不过滤租户（admin 总览），且按创建时间倒序。
func TestListAllCrossTenant(t *testing.T) {
	s := NewStore()
	base := time.Now()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	if err := s.Create(ctxA, backup.Backup{ID: "bk-a", TenantID: "t-acme", ResourceID: "ds-1", Status: backup.StatusCompleted, CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctxB, backup.Backup{ID: "bk-b", TenantID: "t-globex", ResourceID: "ds-2", Status: backup.StatusCompleted, CreatedAt: base.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 { // 含 seed bk-1
		t.Fatalf("want 3 cross-tenant rows, got %d", len(list))
	}
	if list[0].ID != "bk-b" {
		t.Fatalf("want newest first, got %s", list[0].ID)
	}
	// 租户侧 List 仍强隔离（ListAll 不得削弱租户过滤）。
	tenantList, err := s.List(ctxA, "t-acme", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range tenantList {
		if b.TenantID != "t-acme" {
			t.Fatalf("List 泄漏跨租户记录: %+v", b)
		}
	}
}
