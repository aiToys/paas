package admin

import (
	"net/http/httptest"
	"testing"
)

type item struct{ TenantID string }

func TestFilterTenant(t *testing.T) {
	items := []item{{"t-a"}, {"t-b"}, {"t-a"}, {""}}

	t.Run("缺省返回原列表（全部租户）", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/admin/applications", nil)
		got := FilterTenant(r, items, func(i item) string { return i.TenantID })
		if len(got) != len(items) {
			t.Fatalf("缺省应原样返回，got %d want %d", len(got), len(items))
		}
	})

	t.Run("按 tenantId 收敛", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/admin/applications?tenantId=t-a", nil)
		got := FilterTenant(r, items, func(i item) string { return i.TenantID })
		if len(got) != 2 {
			t.Fatalf("t-a 应命中 2 条，got %d", len(got))
		}
		for _, g := range got {
			if g.TenantID != "t-a" {
				t.Fatalf("混入非目标租户: %+v", g)
			}
		}
	})

	t.Run("未知租户返回空集而非全量", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/admin/applications?tenantId=nope", nil)
		if got := FilterTenant(r, items, func(i item) string { return i.TenantID }); len(got) != 0 {
			t.Fatalf("未知租户应空集，got %d", len(got))
		}
	})
}
