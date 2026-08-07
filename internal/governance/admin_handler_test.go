package governance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/governance"
	govmemory "github.com/aitoys/paas/internal/governance/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeAudit 记录最后一次审计 action。
type fakeAudit struct{ last string }

func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}

// newAdminForTest 构造 admin handler + 已创建的测试服务/实例（属 t-acme）。
func newAdminForTest(t *testing.T) (*governance.AdminHandler, *fakeAudit, string, string) {
	t.Helper()
	repo := govmemory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	svc, err := repo.CreateService(ctx, governance.Service{
		Name: "test-svc", EnvID: "env-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	inst, err := repo.RegisterInstance(ctx, governance.Instance{
		ServiceID: svc.ID, Addr: "10.0.0.1:8080", Status: governance.StatusHealthy, LaneID: governance.LaneDefault,
	})
	if err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	au := &fakeAudit{}
	h := governance.NewAdminHandler(repo,
		governance.WithAdminAudit(au),
		governance.WithAdminActor(func(*http.Request) string { return "u-admin" }),
	)
	return h, au, svc.ID, inst.ID
}

// TestAdminDetailReturnsInstances 验证详情返回 service + instances。
func TestAdminDetailReturnsInstances(t *testing.T) {
	h, _, svcID, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/services/"+svcID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	svc := data["service"].(map[string]any)
	if svc["id"] != svcID {
		t.Fatalf("svc=%v", svc)
	}
	ins := data["instances"].([]any)
	if len(ins) != 1 {
		t.Fatalf("instances=%v", ins)
	}
}

func TestAdminDetailNotFound(t *testing.T) {
	h, _, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/services/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminDeleteAudits 验证删服务记审计 admin:delete。
func TestAdminDeleteAudits(t *testing.T) {
	h, au, svcID, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/services/"+svcID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if au.last != "admin:delete" {
		t.Fatalf("audit=%s", au.last)
	}
}

// TestAdminDeregisterAudits 验证注销实例记审计 admin:deregister。
func TestAdminDeregisterAudits(t *testing.T) {
	h, au, svcID, instID := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/services/"+svcID+"/instances/"+instID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if au.last != "admin:deregister" {
		t.Fatalf("audit=%s", au.last)
	}
}

// TestAdminDeregisterWrongService 验证实例不属于该服务时 404（防越权路径）。
func TestAdminDeregisterWrongService(t *testing.T) {
	h, _, svcID, _ := newAdminForTest(t)
	// 注销一个不存在的实例 ID，应 404。
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/services/"+svcID+"/instances/nope-inst", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminDeleteUsesResourceTenantCtx 验证写操作以资源租户 ctx 落库（DeleteService 内部按 ctx tenant 过滤）。
func TestAdminDeleteUsesResourceTenantCtx(t *testing.T) {
	h, _, svcID, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/services/"+svcID, nil)
	// 请求 ctx 故意带 t-globex（非资源所属租户）；handler 应以资源租户 t-acme 落库。
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
