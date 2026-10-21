package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/ai/marketplace"
	"github.com/aitoys/paas/pkg/tenant"
)

// stubMarket 最小 marketplace.Repository 桩（仅需下架链路用到的 Get/Delete/ListAll）。
type stubMarket struct {
	items []marketplace.Item
	gotID string
}

func (s *stubMarket) List(ctx context.Context, entityType, category, q string) ([]marketplace.Item, error) {
	return s.items, nil
}
func (s *stubMarket) Get(ctx context.Context, id string) (marketplace.Item, error) {
	for _, it := range s.items {
		if it.ID == id {
			return it, nil
		}
	}
	return marketplace.Item{}, marketplace.ErrItemNotFound
}
func (s *stubMarket) Create(ctx context.Context, i marketplace.Item) (marketplace.Item, error) {
	return i, nil
}
func (s *stubMarket) Delete(ctx context.Context, id string) error {
	s.gotID = id
	for i, it := range s.items {
		if it.ID == id {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return nil
		}
	}
	return marketplace.ErrItemNotFound
}
func (s *stubMarket) IncInstalls(ctx context.Context, id string) error { return nil }
func (s *stubMarket) ListByPublisher(ctx context.Context, tenantID string) ([]marketplace.Item, error) {
	return s.items, nil
}
func (s *stubMarket) ListAll(ctx context.Context) ([]marketplace.Item, error) {
	return s.items, nil
}

type auditLog struct{ action, resourceID, detail string }

func TestUnpublishMarketItem(t *testing.T) {
	mkt := &stubMarket{items: []marketplace.Item{{
		ID: "mk-1", EntityType: "skill", Name: "违规技能", PublisherTenant: "t-acme", Installs: 3,
	}}}
	var audits []auditLog
	h := NewHandler(nil, nil, nil, nil, nil, mkt, nil,
		WithAudit(funcRecorder(func(action, rid, detail string) {
			audits = append(audits, auditLog{action, rid, detail})
		}), func(*http.Request) string { return "u-super-admin" }),
		WithPlatformAdmin(func(*http.Request) bool { return true }))

	// reason 缺失 → 400
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/ai/marketplace/mk-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 reason 应 400，got %d", rec.Code)
	}

	// 非超管 → 403
	h2 := NewHandler(nil, nil, nil, nil, nil, mkt, nil,
		WithPlatformAdmin(func(*http.Request) bool { return false }))
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/ai/marketplace/mk-1?reason=违规", nil)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非超管应 403，got %d", rec.Code)
	}

	// 正常下架 → 200 + 审计 + 影响字段
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/ai/marketplace/mk-1?reason=内容违规", nil)
	req = req.WithContext(tenant.WithTenant(req.Context(), "t-acme"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("下架应 200，got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "installedNote") {
		t.Fatalf("响应应含影响说明（已安装副本不受影响），got %s", rec.Body.String())
	}
	if len(audits) != 1 || audits[0].action != "admin:marketplace_unpublish" || !strings.Contains(audits[0].detail, "reason=内容违规") {
		t.Fatalf("审计缺失或不完整: %+v", audits)
	}

	// 不存在的条目 → 404
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/ai/marketplace/mk-404?reason=x", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在条目应 404，got %d", rec.Code)
	}
}

// funcRecorder 把 AuditRecorder 收口到单测闭包（避免引入 identity 依赖）。
type funcRecorder func(action, resourceID, detail string)

func (f funcRecorder) Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error {
	f(action, resourceID, detail)
	return nil
}
