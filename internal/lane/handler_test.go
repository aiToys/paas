package lane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops/pipeline"
	envmemory "github.com/aitoys/paas/internal/environment/memory"
	"github.com/aitoys/paas/internal/lane"
	lanememory "github.com/aitoys/paas/internal/lane/memory"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// newHandler 构造集成 handler：真实 lane/env 内存仓储 + stub 鉴权。
// prodWrite=true 模拟 admin，false 模拟 developer（生产只读）。
func newHandler(prodWrite bool, opts ...lane.HandlerOpt) *lane.Handler {
	h := lane.NewHandler(lanememory.NewStore(),
		append([]lane.HandlerOpt{lane.WithEnvResolver(envmemory.NewStore())}, opts...)...)
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == lane.PermProdWrite {
			return prodWrite
		}
		return true
	}
	return h
}

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func decodeData(t *testing.T, body []byte, v interface{}) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解码信封失败: %v (body=%s)", err, string(body))
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("解码 data 失败: %v (data=%s)", err, string(env.Data))
	}
}

func req(ctx context.Context, method, path string, body interface{}) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	return rq.WithContext(ctx)
}

// fakeAudit 收集审计记录（断言写操作记审计）。
type fakeAudit struct{ records []string }

func (f *fakeAudit) Record(_ context.Context, _, actor, action, _, resourceID, detail string) error {
	f.records = append(f.records, action+" "+resourceID+" "+detail)
	return nil
}

// fakeRuns 桩 RunLister（Detail 聚合 + Close 前置校验）。
type fakeRuns struct {
	summaries []pipeline.RunSummary
	err       error
}

func (f *fakeRuns) ListByLane(_ context.Context, laneName string) ([]pipeline.RunSummary, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []pipeline.RunSummary
	for _, s := range f.summaries {
		if s.Lane == laneName || s.Branch == laneName {
			out = append(out, s)
		}
	}
	return out, nil
}

func validLane() map[string]any {
	return map[string]any{"envId": "env-acme-test", "name": "feature-x", "description": "联调"}
}

// TestHandlerCRUD 验证基础 CRUD 往返。
func TestHandlerCRUD(t *testing.T) {
	h := newHandler(true)
	// 创建
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d: %s", w.Code, w.Body.String())
	}
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)
	if created.ID == "" || created.Status != lane.StatusActive || created.Mode != lane.ModeStandard {
		t.Fatalf("创建默认值不符: %+v", created)
	}
	// 列表（按 envId 过滤）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/lanes?envId=env-acme-test", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", w.Code)
	}
	var list []lane.Lane
	decodeData(t, w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "feature-x" {
		t.Fatalf("列表不符: %+v", list)
	}
	// 更新（mode/description）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "PUT", "/api/lanes/"+created.ID, map[string]any{
		"mode": lane.ModePermanent, "description": "常驻", "externalLink": "PROJ-1",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("更新应 200，got %d: %s", w.Code, w.Body.String())
	}
	var updated lane.Lane
	decodeData(t, w.Body.Bytes(), &updated)
	if updated.Mode != lane.ModePermanent || updated.ExternalLink != "PROJ-1" {
		t.Fatalf("更新不符: %+v", updated)
	}
}

// TestHandlerCreateForbidden 验证普通权限（governance:write）拒绝时 403。
func TestHandlerCreateForbidden(t *testing.T) {
	h := newHandler(true)
	h.Authorize = func(r *http.Request, perm string) bool { return false }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusForbidden {
		t.Fatalf("无权限应 403，got %d", w.Code)
	}
}

// TestHandlerCreateProdDenied 验证生产环境建泳道需 prod:write（developer 403）。
func TestHandlerCreateProdDenied(t *testing.T) {
	h := newHandler(false)
	body := validLane()
	body["envId"] = "env-acme-prod-bj"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", body))
	if w.Code != http.StatusForbidden {
		t.Fatalf("生产建泳道应 403，got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandlerCreateProdEnvUnknownFailClosed 验证环境查询失败也按生产处理（fail-closed）。
func TestHandlerCreateProdEnvUnknownFailClosed(t *testing.T) {
	h := newHandler(false)
	body := validLane()
	body["envId"] = "env-not-exist"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", body))
	if w.Code != http.StatusForbidden {
		t.Fatalf("环境查不到应 fail-closed 403，got %d", w.Code)
	}
}

// TestHandlerCreateConflict 验证同租户×环境×名唯一冲突 409。
func TestHandlerCreateConflict(t *testing.T) {
	h := newHandler(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusCreated {
		t.Fatalf("首次创建应 201，got %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusConflict {
		t.Fatalf("重复创建应 409，got %d", w.Code)
	}
}

// TestHandlerCreateNameInvalid 验证非法名（非 DNS-1035）400。
func TestHandlerCreateNameInvalid(t *testing.T) {
	h := newHandler(true)
	body := validLane()
	body["name"] = "Feature_X"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法名应 400，got %d", w.Code)
	}
}

// TestHandlerCrossTenantNotFound 验证跨租户 Get 404 不泄漏。
func TestHandlerCrossTenantNotFound(t *testing.T) {
	h := newHandler(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d", w.Code)
	}
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	globexCtx := tenant.WithTenant(context.Background(), "t-globex")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(globexCtx, "GET", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户 Get 应 404，got %d", w.Code)
	}
}

// TestHandlerClose 验证 DELETE 语义：标记 closed + 幂等 + active run 时 409。
func TestHandlerClose(t *testing.T) {
	runs := &fakeRuns{}
	h := newHandler(true, lane.WithRunLister(runs))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	// 有进行中 run（branch==name 且非终态）→ 409
	runs.summaries = []pipeline.RunSummary{{ID: "run-1", Branch: "feature-x"}}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("有进行中 run 关闭应 409，got %d: %s", w.Code, w.Body.String())
	}

	// run 终态后可关
	runs.summaries = []pipeline.RunSummary{{ID: "run-1", Branch: "feature-x", FinishedAt: time.Now()}}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("关闭应 200，got %d: %s", w.Code, w.Body.String())
	}
	var closed lane.CloseResult
	decodeData(t, w.Body.Bytes(), &closed)
	if closed.Lane.Status != lane.StatusClosed {
		t.Fatalf("关闭后状态不符: %+v", closed.Lane)
	}

	// 已 closed 再 DELETE 幂等 200（重试回收遗留，spec 2026-10-17 B）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("重复关闭应幂等 200，got %d: %s", w.Code, w.Body.String())
	}
	var retried lane.CloseResult
	decodeData(t, w.Body.Bytes(), &retried)
	if retried.Lane.Status != lane.StatusClosed || retried.Reclaimed != 0 || len(retried.FailedIDs) != 0 {
		t.Fatalf("重试应返回 closed + 零回收结果, got %+v", retried)
	}
}

// fakeReclaimer 桩 Reclaimer（结构化回收结果注入）。
type fakeReclaimer struct {
	results []lane.ReclaimResult
	err     error
	calls   int
}

func (f *fakeReclaimer) ReclaimLane(context.Context, string, string, string) (lane.ReclaimResult, error) {
	f.calls++
	if f.err != nil {
		return lane.ReclaimResult{}, f.err
	}
	if f.calls-1 < len(f.results) {
		return f.results[f.calls-1], nil
	}
	return lane.ReclaimResult{}, nil
}

// TestHandlerCloseReportsPartialFailure 部分回收失败结构化呈现（spec 2026-10-17 B：
// 部分删除失败不得假报「全部回收」）——HTTP 200 但 body 带 failedIds。
func TestHandlerCloseReportsPartialFailure(t *testing.T) {
	rc := &fakeReclaimer{results: []lane.ReclaimResult{{Reclaimed: 1, FailedIDs: []string{"wl-x"}}}}
	h := newHandler(true, lane.WithReclaimer(rc))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("部分失败仍应 200（关闭成功）, got %d: %s", w.Code, w.Body.String())
	}
	var res lane.CloseResult
	decodeData(t, w.Body.Bytes(), &res)
	if res.Lane.Status != lane.StatusClosed || res.Reclaimed != 1 || len(res.FailedIDs) != 1 || res.FailedIDs[0] != "wl-x" {
		t.Fatalf("应结构化报 1 成功 + 1 失败, got %+v", res)
	}

	// 重试 DELETE：只重扫遗留（第二次回收结果如实上报），不 409 不假成功。
	rc.results = append(rc.results, lane.ReclaimResult{Reclaimed: 1})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("重试应 200, got %d", w.Code)
	}
	var res2 lane.CloseResult // 新变量：json decode 不清缺省字段，复用会残留 failedIds
	decodeData(t, w.Body.Bytes(), &res2)
	if res2.Reclaimed != 1 || len(res2.FailedIDs) != 0 {
		t.Fatalf("重试应报补收 1 个无遗留, got %+v", res2)
	}
	if rc.calls != 2 {
		t.Fatalf("已 closed 重试 DELETE 应重执行回收, got calls=%d", rc.calls)
	}
}

// TestHandlerDetailAggregation 验证 Detail 聚合（nil listers 返空切片）。
func TestHandlerDetailAggregation(t *testing.T) {
	h := newHandler(true, lane.WithRunLister(&fakeRuns{
		summaries: []pipeline.RunSummary{{ID: "run-1", Branch: "feature-x", Status: pipeline.RunSucceeded}},
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("详情应 200，got %d", w.Code)
	}
	var d lane.LaneDetail
	decodeData(t, w.Body.Bytes(), &d)
	if d.Lane.ID != created.ID {
		t.Fatalf("详情 Lane 不符: %+v", d)
	}
	if len(d.RecentRuns) != 1 || d.RecentRuns[0].ID != "run-1" {
		t.Fatalf("RecentRuns 不符: %+v", d.RecentRuns)
	}
	if d.Workloads == nil {
		t.Fatalf("Workloads 应为空切片非 nil（前端消费一致性）")
	}
}

// TestHandlerAudit 验证 Create/Update/Close 记审计。
func TestHandlerAudit(t *testing.T) {
	audit := &fakeAudit{}
	h := newHandler(true, lane.WithAudit(audit))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	h.ServeHTTP(httptest.NewRecorder(), req(acmeCtx(), "PUT", "/api/lanes/"+created.ID, map[string]any{"description": "改"}))
	h.ServeHTTP(httptest.NewRecorder(), req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))

	joined := strings.Join(audit.records, "\n")
	for _, want := range []string{"lane_create", "lane_update", "lane_close"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("审计缺失 %s，records=%s", want, joined)
		}
	}
}

// TestHandlerCloseRunListerError 验证 RunLister 查询失败 fail-closed（500，不静默关闭）。
func TestHandlerCloseRunListerError(t *testing.T) {
	h := newHandler(true, lane.WithRunLister(&fakeRuns{err: errors.New("boom")}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("RunLister 失败应 500 fail-closed，got %d", w.Code)
	}
}

// 终审 C1 回归：DELETE 关闭（级联回收生产工作负载）与 PUT（改 mode）对生产泳道需 prod:write——
// developer（governance:write 有、prod:write 无）不可操作生产泳道。
func TestHandlerProdLaneDeletePutDenied(t *testing.T) {
	// 共享 store：admin 建（有 prod:write），developer 操作（无 prod:write）
	store := lanememory.NewStore()
	admin := lane.NewHandler(store, lane.WithEnvResolver(envmemory.NewStore()))
	admin.Authorize = func(r *http.Request, perm string) bool { return true }
	body := validLane()
	body["envId"] = "env-acme-prod-bj"
	w := httptest.NewRecorder()
	admin.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", body))
	if w.Code != 201 {
		t.Fatalf("admin 建生产泳道应 201, got %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeData(t, w.Body.Bytes(), &created)

	dev := lane.NewHandler(store, lane.WithEnvResolver(envmemory.NewStore()))
	dev.Authorize = func(r *http.Request, perm string) bool {
		return perm != lane.PermProdWrite // developer：无 prod:write
	}
	w = httptest.NewRecorder()
	dev.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("developer 关生产泳道应 403, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	dev.ServeHTTP(w, req(acmeCtx(), "PUT", "/api/lanes/"+created.ID, map[string]any{"mode": "permanent"}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("developer 改生产泳道 mode 应 403, got %d", w.Code)
	}
}

// TestHandlerBaselineProtected 基线 default 三层保护（spec 2026-10-17 L1）：
// 创建拒绝 403 + 遗留 default 实体关闭拒绝（UI 隐藏不是保护，handler 是真闸门）。
func TestHandlerBaselineProtected(t *testing.T) {
	h := newHandler(true)
	// 创建层：POST name=default 拒绝。
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", map[string]any{
		"envId": "env-acme-test", "name": "default", "mode": "standard",
	}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("创建 default 应 403，got %d: %s", w.Code, w.Body.String())
	}
	// 关闭层：保护生效前建成的遗留 default 实体也拒绝 DELETE。
	// 仓储直建不经 Validate（模拟历史遗留数据；Validate 已拒，走 API 建不出来）。
	store := lanememory.NewStore()
	created, err := store.UpsertForRepair(acmeCtx(), lane.Lane{
		EnvID: "env-acme-test", Name: "default", Mode: lane.ModeStandard, Status: lane.StatusActive,
	})
	if err != nil {
		t.Fatalf("seed 遗留实体失败: %v", err)
	}
	h = lane.NewHandler(store)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("关闭遗留 default 应 403，got %d: %s", w.Code, w.Body.String())
	}
	got, _ := store.Get(acmeCtx(), created.ID)
	if got.Status != lane.StatusActive {
		t.Fatalf("default 实体不应被关闭，got %s", got.Status)
	}
}

// fakeWorkloads 桩 WorkloadLister（access 读模型验证）。
type fakeWorkloads struct{ items []workload.Workload }

func (f *fakeWorkloads) ListByLane(context.Context, string, string) ([]workload.Workload, error) {
	return f.items, nil
}

// TestHandlerAccessClassification access 读模型分类（spec 2026-10-17 §7.1）：
// 声明域名按泳道归属分 business/preview；无域名 FQDN 兜底标 internal（仅集群内）。
func TestHandlerAccessClassification(t *testing.T) {
	h := newHandler(true, lane.WithWorkloadLister(&fakeWorkloads{items: []workload.Workload{
		{ID: "wl-b", Name: "shop-web", LaneID: "default", Domain: "shop.example.com"},
		{ID: "wl-l", Name: "shop-web-feature-x", LaneID: "feature-x", Domain: "shop-web-feature-x.preview.example.com"},
		{ID: "wl-i", Name: "orders", LaneID: "feature-x", Port: 8080},
	}}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/lanes/"+created.ID+"/access", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("access 应 200, got %d: %s", w.Code, w.Body.String())
	}
	var acc lane.LaneAccess
	decodeData(t, w.Body.Bytes(), &acc)
	if len(acc.Endpoints) != 3 {
		t.Fatalf("应 3 条地址, got %+v", acc.Endpoints)
	}
	byTarget := map[string]lane.AccessEndpoint{}
	for _, ep := range acc.Endpoints {
		byTarget[ep.Target] = ep
	}
	// 基线声明域名 → 业务入口
	if ep := byTarget["wl-b"]; ep.Kind != lane.AccessBusiness || ep.URL != "http://shop.example.com" || ep.Source != lane.AccessSourceDomain {
		t.Fatalf("基线域名应分类 business, got %+v", ep)
	}
	// 泳道声明域名 → 联调入口（带诚实边界 note）
	if ep := byTarget["wl-l"]; ep.Kind != lane.AccessPreview || ep.Lane != "feature-x" || ep.Note == "" {
		t.Fatalf("泳道域名应分类 preview 带说明, got %+v", ep)
	}
	// 无域名 → FQDN 兜底 internal（带端口）
	if ep := byTarget["wl-i"]; ep.Kind != lane.AccessInternal || ep.Source != lane.AccessSourceFQDN ||
		ep.URL != "orders.paas-t-acme.svc.cluster.local:8080" || ep.Port != 8080 {
		t.Fatalf("无域名应 FQDN internal 带端口, got %+v", ep)
	}
}

// TestHandlerAccessEmptyLister 未注入 lister 返空列表不 panic（与 Detail 同款降级）。
func TestHandlerAccessEmptyLister(t *testing.T) {
	h := newHandler(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/lanes/"+created.ID+"/access", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("access 无 lister 应 200 空列表, got %d", w.Code)
	}
	var acc lane.LaneAccess
	decodeData(t, w.Body.Bytes(), &acc)
	if acc.Endpoints == nil || len(acc.Endpoints) != 0 {
		t.Fatalf("Endpoints 应空切片非 nil, got %+v", acc.Endpoints)
	}
}
