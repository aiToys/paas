package dataservice_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
	dsmemory "github.com/aitoys/paas/internal/dataservice/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeInstances 测试 InstanceReader。
type fakeInstances struct{ list []dataservice.InstanceInfo }

func (f fakeInstances) Instances(ctx context.Context, ns, svc string) ([]dataservice.InstanceInfo, error) {
	return f.list, nil
}

// fakeAudit 记录最后一次审计 action。
type fakeAudit struct{ last string }

func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}

// fakeQuota 累计 quota delta。
type fakeQuota struct{ n int }

func (q *fakeQuota) check(ctx context.Context, d int) error {
	q.n += d
	return nil
}

// fakeTenants 校验租户存在。
type fakeTenants struct{}

func (fakeTenants) Exists(ctx context.Context, id string) error {
	if id == "t-acme" {
		return nil
	}
	return fmt.Errorf("not found")
}

// newAdminForTest 构造一个 admin handler + 已 seed 的 ds-1（属 t-acme）。
func newAdminForTest(t *testing.T) (*dataservice.AdminHandler, *fakeAudit, *fakeQuota) {
	t.Helper()
	repo := dsmemory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	_, err := repo.Create(ctx, dataservice.DataService{
		ID: "ds-1", Kind: dataservice.KindDB, Name: "m1", Source: dataservice.SourceManaged,
		Spec: map[string]string{"engine": "postgres"}, EnvID: "env-acme-test",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	au := &fakeAudit{}
	q := &fakeQuota{}
	h := dataservice.NewAdminHandler(repo,
		dataservice.WithAdminInstances(fakeInstances{list: []dataservice.InstanceInfo{{Name: "ds-1-0", IP: "10.0.0.1", Port: 5432}}}),
		dataservice.WithAdminNamespace("paas"),
		dataservice.WithAdminAudit(au),
		dataservice.WithAdminQuota(q.check),
	)
	return h, au, q
}

func TestAdminDetailReturnsInstancesAndMasksConnection(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/dataservices/ds-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	res := data["resource"].(map[string]any)
	if conn, ok := res["connection"].(map[string]any); ok && conn != nil {
		if v, has := conn["password"]; has && v != "" && v != dataservice.SecretMask {
			t.Fatalf("connection.password should be masked, got %v", v)
		}
	}
	ins := data["instances"].([]any)
	if len(ins) != 1 {
		t.Fatalf("instances=%v", ins)
	}
}

func TestAdminStopUpdatesAndAudits(t *testing.T) {
	h, au, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices/ds-1/stop", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if au.last != "admin:stop" {
		t.Fatalf("audit=%s", au.last)
	}
}

func TestAdminDeleteRecoversQuotaAndAudits(t *testing.T) {
	h, au, q := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/dataservices/ds-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if q.n != -1 {
		t.Fatalf("quota delta=%d want -1", q.n)
	}
	if au.last != "admin:delete" {
		t.Fatalf("audit=%s", au.last)
	}
}

func TestAdminScaleMergesFields(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"replicas":3,"cpu":"2"}`))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/dataservices/ds-1/scale", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminItemMethodNotAllowed 验证 /{id} 非 GET/DELETE 方法返 405。
// 修复前：serveDetail 未检查方法，POST /{id} 落入 serveDetail 返 200 详情（契约违反）。
func TestAdminItemMethodNotAllowed(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices/ds-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d body=%s（POST /{id} 应 405）", rec.Code, rec.Body.String())
	}
}

// TestAdminCreateConsumesQuotaAndRollsBackOnFailure 代建缺 engineId 期望 400 + 配额不消耗。
func TestAdminCreateConsumesQuotaAndRollsBackOnFailure(t *testing.T) {
	repo := dsmemory.NewStore()
	au := &fakeAudit{}
	q := &fakeQuota{}
	h := dataservice.NewAdminHandler(repo,
		dataservice.WithAdminTenants(fakeTenants{}),
		dataservice.WithAdminAudit(au),
		dataservice.WithAdminQuota(q.check),
	)
	body := bytes.NewReader([]byte(`{"tenantId":"t-acme","id":"ds-new","name":"pg1","engineId":""}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (missing engineId), got %d", rec.Code)
	}
	if q.n != 0 {
		t.Fatalf("quota should not be consumed on resolve failure, got %d", q.n)
	}
}

// TestAdminCreateRejectsMissingTenant body 缺 tenantId 应直接 400。
func TestAdminCreateRejectsMissingTenant(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"id":"ds-x","name":"x"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 missing tenantId, got %d", rec.Code)
	}
}

// TestAdminCreateRejectsUnknownTenant 未知租户应 400。
func TestAdminCreateRejectsUnknownTenant(t *testing.T) {
	repo := dsmemory.NewStore()
	h := dataservice.NewAdminHandler(repo, dataservice.WithAdminTenants(fakeTenants{}))
	body := bytes.NewReader([]byte(`{"tenantId":"t-ghost","name":"x"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 unknown tenant, got %d", rec.Code)
	}
}
