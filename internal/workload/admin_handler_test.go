package workload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/workload"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

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

// fakeStatus stub StatusReader，回填 Ready + 提供实例/日志。
type fakeStatus struct {
	ready int
	logs  string
}

func (f *fakeStatus) FillStatus(ctx context.Context, wls []workload.Workload) error {
	for i := range wls {
		wls[i].Ready = f.ready
		wls[i].Status = workload.StatusRunning
	}
	return nil
}

// fakeApplier 记录 Apply/EnsureIfMissing 调用（drift 修复端点测试用）。
type fakeApplier struct {
	applied       []workload.Workload
	ensured       []workload.Workload
	ensureCreated bool // EnsureIfMissing 是否每次都报「补建」（CRD 视为不存在）
}

func (f *fakeApplier) Apply(ctx context.Context, w workload.Workload) error {
	f.applied = append(f.applied, w)
	return nil
}
func (f *fakeApplier) EnsureIfMissing(ctx context.Context, w workload.Workload) (bool, error) {
	f.ensured = append(f.ensured, w)
	return f.ensureCreated, nil
}
func (f *fakeApplier) Delete(ctx context.Context, id string) error { return nil }

func (f *fakeStatus) Instances(ctx context.Context, id string) ([]workload.Instance, error) {
	return []workload.Instance{{Name: id + "-pod", Status: "Running"}}, nil
}

func (f *fakeStatus) PodLogs(ctx context.Context, wid, pod string, tail int64, prev bool) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte(f.logs))), nil
}

// newAdminForTest 构造 admin handler + 已 seed 的 wl-cs-api（属 t-acme）。
func newAdminForTest(t *testing.T) (*workload.AdminHandler, *fakeAudit, *fakeQuota) {
	t.Helper()
	repo := wlmemory.NewStore()
	au := &fakeAudit{}
	q := &fakeQuota{}
	h := workload.NewAdminHandler(repo,
		workload.WithAdminStatusReader(&fakeStatus{ready: 2, logs: "hello-logs"}),
		workload.WithAdminQuota(q.check),
		workload.WithAdminAudit(au),
	)
	return h, au, q
}

func TestAdminListReturnsAllTenants(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/workloads", nil)
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

func TestAdminDetailReturnsInstances(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/workloads/wl-cs-api", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	wl := data["workload"].(map[string]any)
	if wl["id"] != "wl-cs-api" {
		t.Fatalf("wl=%v", wl)
	}
	ins := data["instances"].([]any)
	if len(ins) != 1 {
		t.Fatalf("instances=%v", ins)
	}
}

func TestAdminDetailNotFound(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/workloads/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminScaleUpdatesAndAudits(t *testing.T) {
	h, au, _ := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"replicas":5}`))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/workloads/wl-cs-api/scale", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if au.last != "admin:scale" {
		t.Fatalf("audit=%s", au.last)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	wl := out["data"].(map[string]any)
	if int(wl["replicas"].(float64)) != 5 {
		t.Fatalf("replicas=%v", wl["replicas"])
	}
}

func TestAdminDeleteRecoversQuotaAndAudits(t *testing.T) {
	h, au, q := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/workloads/wl-cs-api", nil)
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

func TestAdminLogsReturnsText(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/workloads/wl-cs-api/logs?pod=p1&tail=10", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello-logs" {
		t.Fatalf("body=%q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content-type=%q", ct)
	}
}

// 验证 admin handler 不依赖 ctx tenant（adminGuard 不注入租户，跨租户读全量）。
func TestAdminListWorksNoTenantCtx(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/workloads", nil)
	// 不带 tenant ctx，模拟 admin super_admin 通行场景
	req = req.WithContext(context.Background())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminLogsMissingPod(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/workloads/wl-cs-api/logs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 验证 admin 写操作以资源租户 ctx 落库（Update 内部 Get 强制 ctx tenant）。
func TestAdminScaleUsesResourceTenantCtx(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"replicas":3}`))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/workloads/wl-cs-api/scale", body)
	// 请求 ctx 故意带 t-globex（非资源所属租户）；handler 应以资源租户 t-acme 落库。
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAdminReconcileDrift 验证 POST /api/admin/workloads/reconcile 触发 drift 修复：
// 扫全部 workload，对每行调 applier.EnsureIfMissing（仅 CRD 缺失才补建，绝不覆盖既有运行态），
// 返 scanned/created/skipped 统计。
func TestAdminReconcileDrift(t *testing.T) {
	repo := wlmemory.NewStore()
	applier := &fakeApplier{ensureCreated: true}
	h := workload.NewAdminHandler(repo, workload.WithAdminApplier(applier))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/workloads/reconcile", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(applier.ensured) == 0 {
		t.Error("reconcile 应对 seed workload 调 EnsureIfMissing，实际 ensured=0")
	}
}

// TestAdminReconcileDriftNoApplier 验证未注入 applier（K8s 未启用）返 503 友好提示。
func TestAdminReconcileDriftNoApplier(t *testing.T) {
	repo := wlmemory.NewStore()
	h := workload.NewAdminHandler(repo) // 无 WithAdminApplier
	req := httptest.NewRequest(http.MethodPost, "/api/admin/workloads/reconcile", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用应 503, got code=%d", rec.Code)
	}
}
