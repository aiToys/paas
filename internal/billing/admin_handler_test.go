package billing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/billing"
	billmemory "github.com/aitoys/paas/internal/billing/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeAudit 记录最后一次审计 action。
type fakeAudit struct{ last string }

func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}

// fakeTenants 校验租户存在（仅 t-acme 接受，与 seed 数据一致）。
type fakeTenants struct{}

func (fakeTenants) Exists(ctx context.Context, id string) error {
	if id == "t-acme" {
		return nil
	}
	return fmt.Errorf("租户不存在: %s", id)
}

// newAdminForTest 构造 admin handler + 已灌的配额+账单（属 t-acme）。
// GenerateBill 生成账单 id = bill-t-acme-1（seq=1）。
func newAdminForTest(t *testing.T) (*billing.AdminHandler, *fakeAudit) {
	t.Helper()
	repo := billmemory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if _, err := repo.SetQuota(ctx, billing.ResourceQuota{TenantID: "t-acme", Limits: map[string]int{"applications": 5}}); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	if _, err := repo.IncUsage(ctx, billing.ResApplications, 3); err != nil {
		t.Fatalf("IncUsage: %v", err)
	}
	if _, err := repo.GenerateBill(ctx, "2026-06"); err != nil {
		t.Fatalf("GenerateBill: %v", err)
	}
	au := &fakeAudit{}
	h := billing.NewAdminHandler(repo,
		billing.WithAdminAudit(au),
		billing.WithAdminTenants(fakeTenants{}),
		billing.WithAdminActor(func(*http.Request) string { return "u-admin" }),
	)
	return h, au
}

// TestAdminQuotaList 验证配额列表跨租户全量返回。
func TestAdminQuotaList(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/quotas", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	list := out["data"].([]any)
	if len(list) == 0 {
		t.Fatalf("list empty")
	}
}

// TestAdminSetQuotaAudits 验证调整配额记审计 admin:set-quota。
func TestAdminSetQuotaAudits(t *testing.T) {
	h, au := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"tenantId":"t-acme","limits":{"applications":10,"workloads":20}}`))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/quotas", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if au.last != "admin:set-quota" {
		t.Fatalf("audit=%s", au.last)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	q := out["data"].(map[string]any)
	limits := q["limits"].(map[string]any)
	if int(limits["applications"].(float64)) != 10 {
		t.Fatalf("limits=%v", limits)
	}
}

// TestAdminSetQuotaMissingTenant 验证 body 缺 tenantId 报 400。
func TestAdminSetQuotaMissingTenant(t *testing.T) {
	h, _ := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"limits":{}}`))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/quotas", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminSetQuotaRejectsUnknownTenant 验证调整配额校验租户存在（防给不存在租户设配额污染数据 + 审计）。
func TestAdminSetQuotaRejectsUnknownTenant(t *testing.T) {
	h, au := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"tenantId":"t-unknown","limits":{}}`))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/quotas", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s（未知租户应 400）", rec.Code, rec.Body.String())
	}
	if au.last != "" {
		t.Fatalf("未知租户不应记审计，audit=%s", au.last)
	}
}

// TestAdminBillList 验证账单列表跨租户全量返回。
func TestAdminBillList(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/bills", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminBillDetail 验证跨租户取单条账单。
func TestAdminBillDetail(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/bills/bill-t-acme-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminPayAudits 验证标记账单已付记审计 admin:pay。
func TestAdminPayAudits(t *testing.T) {
	h, au := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bills/bill-t-acme-1/pay", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if au.last != "admin:pay" {
		t.Fatalf("audit=%s", au.last)
	}
}

// TestAdminPayUsesResourceTenantCtx 验证写操作以资源租户 ctx 落库。
func TestAdminPayUsesResourceTenantCtx(t *testing.T) {
	h, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bills/bill-t-acme-1/pay", nil)
	// 请求 ctx 故意带 t-globex；handler 应以资源租户 t-acme 落库。
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
