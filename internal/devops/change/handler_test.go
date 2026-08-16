package change

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/pkg/tenant"
)

// ---------- 测试辅助（HTTP 层端到端：真实 Service + memory store + fakes） ----------

func acmeReq(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(tenant.WithTenant(context.Background(), "t-acme"))
}

func allowAll(*http.Request, string) bool { return true }

// globexReq 跨租户请求（隔离断言用）。
func globexReq(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(tenant.WithTenant(context.Background(), "t-globex"))
}

// fakeAuditRecorder 收集审计记录（断言 action/resourceID）。
type fakeAuditRecorder struct {
	actions []string // 已记录 action 序列
	entries []auditEntry
}

type auditEntry struct {
	tenantID, actor, action, resourceType, resourceID, detail string
}

func (f *fakeAuditRecorder) Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error {
	f.actions = append(f.actions, action)
	f.entries = append(f.entries, auditEntry{tenantID, actor, action, resourceType, resourceID, detail})
	return nil
}

// newTestHandler 构造真实 Service（memory store + fakes）+ Handler。
// prodWrite 可注入（nil=跳过 approve 门禁）。
func newTestHandler(t *testing.T, prodWrite func(*http.Request) bool, audit AuditRecorder) (*Handler, *MemoryStore, *fakeBrancher, *fakeRuns) {
	t.Helper()
	store := NewMemoryStore()
	g, runs := &fakeBrancher{}, &fakeRuns{pipeline: "pipe-ci", status: "succeeded"}
	_ = prodWrite // 供调用方语义化注入（默认场景用 nil=跳过 approve 门禁）
	svc := NewService(store, WithGitea(g), WithRunTrigger(runs), WithRunReader(runs),
		WithRepoLookup(func(ctx context.Context, appID string) (string, string, string, error) {
			return "paas-bot", "app-1", "repo-1", nil
		}))
	h := NewHandler(svc, store,
		WithAuthorize(allowAll),
		WithProdWrite(prodWrite),
		WithAudit(audit),
		WithActorFn(func(r *http.Request) string { return "user-1" }),
		// 批次创建 RepoID 与变更同源（lookup fake 同款）
		WithHandlerRepoLookup(func(ctx context.Context, appID string) (string, string, string, error) {
			return "paas-bot", "app-1", "repo-1", nil
		}),
	)
	return h, store, g, runs
}

// doJSON 执行请求并解包 {data:T}。
func doJSON[T any](t *testing.T, h *Handler, method, target, body string, wantCode int) T {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(method, target, body))
	if rec.Code != wantCode {
		t.Fatalf("%s %s 期望 %d，got %d body %s", method, target, wantCode, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data T      `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("反序列化失败: %v body %s", err, rec.Body.String())
	}
	return resp.Data
}

// doRaw 执行请求返 recorder（错误路径断言用）。
func doRaw(h *Handler, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(method, target, body))
	return rec
}

// ---------- 测试 ----------

func TestHandlerChangeLifecycle(t *testing.T) {
	h, _, g, _ := newTestHandler(t, nil, nil)
	// POST 建变更（平台代建分支）
	created := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"导出","type":"feat","branch":"feat/export","createBranch":true}`, http.StatusCreated)
	if created.ID == "" || created.Status != ChangeOpen {
		t.Fatalf("创建应返 id + open: %+v", created)
	}
	if len(g.created) != 1 {
		t.Fatalf("应调 Gitea CreateBranch")
	}
	// GET 列表
	list := doJSON[[]Change](t, h, http.MethodGet, "/api/applications/app-1/changes", "", http.StatusOK)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("列表应含 1 条: %+v", list)
	}
	// GET 详情
	got := doJSON[Change](t, h, http.MethodGet, "/api/applications/app-1/changes/"+created.ID, "", http.StatusOK)
	if got.Branch != "feat/export" {
		t.Fatalf("详情 branch 不符: %+v", got)
	}
	// DELETE 放弃
	abandoned := doJSON[Change](t, h, http.MethodDelete, "/api/applications/app-1/changes/"+created.ID, "", http.StatusOK)
	if abandoned.Status != ChangeAbandoned {
		t.Fatalf("放弃后应 abandoned: %s", abandoned.Status)
	}
}

func TestHandlerBatchFlow(t *testing.T) {
	// prodWrite 返 false：验证 approve 无 prod:write 被 403（后续用 h2 放行）
	h, store, _, runs := newTestHandler(t, func(*http.Request) bool { return false }, nil)
	// 建 2 变更 + 批次
	c1 := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"a","type":"feat","branch":"feat/a","createBranch":true}`, http.StatusCreated)
	c2 := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"b","type":"feat","branch":"feat/b","createBranch":true}`, http.StatusCreated)
	b := doJSON[IntegrationBatch](t, h, http.MethodPost, "/api/applications/app-1/batches",
		`{"title":"集成","branch":"integration/x"}`, http.StatusCreated)
	if b.Status != BatchCollecting {
		t.Fatalf("批次应 collecting: %s", b.Status)
	}
	// 入批 ×2
	_ = doJSON[IntegrationBatch](t, h, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/changes", `{"changeId":"`+c1.ID+`"}`, http.StatusOK)
	b = doJSON[IntegrationBatch](t, h, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/changes", `{"changeId":"`+c2.ID+`"}`, http.StatusOK)
	if len(b.ChangeIDs) != 2 {
		t.Fatalf("批内应 2 变更: %v", b.ChangeIDs)
	}
	// integrate → testing
	b = doJSON[IntegrationBatch](t, h, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/integrate", "", http.StatusOK)
	if b.Status != BatchTesting {
		t.Fatalf("integrate 后应 testing: %s", b.Status)
	}
	// fake run succeeded → GET 详情惰性推进到 tested
	runs.status = "succeeded"
	b = doJSON[IntegrationBatch](t, h, http.MethodGet,
		"/api/applications/app-1/batches/"+b.ID, "", http.StatusOK)
	if b.Status != BatchTested {
		t.Fatalf("GET 详情应惰性推进到 tested: %s", b.Status)
	}
	// approve：无 prod:write → 403
	rec := doRaw(h, http.MethodPost, "/api/applications/app-1/batches/"+b.ID+"/approve", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无 prod:write approve 应 403，got %d body %s", rec.Code, rec.Body.String())
	}
	// 重新构造带 prodWrite 的 handler（共享 store，保持状态）
	h2 := NewHandler(NewService(store,
		WithGitea(&fakeBrancher{}), WithRunTrigger(runs), WithRunReader(runs),
		WithRepoLookup(func(ctx context.Context, appID string) (string, string, string, error) {
			return "paas-bot", "app-1", "repo-1", nil
		})), store, WithAuthorize(allowAll),
		WithProdWrite(func(*http.Request) bool { return true }))
	b = doJSON[IntegrationBatch](t, h2, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/approve", "", http.StatusOK)
	if b.Status != BatchReleasing {
		t.Fatalf("approve 后应 releasing: %s", b.Status)
	}
	// release → 200（仍 releasing，等 CD 终态）
	b = doJSON[IntegrationBatch](t, h2, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/release", "", http.StatusOK)
	if b.Status != BatchReleasing {
		t.Fatalf("release 后应 releasing（等 CD 终态）: %s", b.Status)
	}
	// CD succeeded → GET 惰性推进到 released
	runs.status = "succeeded"
	b = doJSON[IntegrationBatch](t, h2, http.MethodGet,
		"/api/applications/app-1/batches/"+b.ID, "", http.StatusOK)
	if b.Status != BatchReleased {
		t.Fatalf("应 released: %s", b.Status)
	}
}

func TestHandlerConflict409(t *testing.T) {
	h, _, g, _ := newTestHandler(t, nil, nil)
	c1 := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"a","type":"feat","branch":"feat/a","createBranch":true}`, http.StatusCreated)
	c2 := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"b","type":"feat","branch":"feat/b","createBranch":true}`, http.StatusCreated)
	b := doJSON[IntegrationBatch](t, h, http.MethodPost, "/api/applications/app-1/batches",
		`{"title":"x","branch":"integration/x"}`, http.StatusCreated)
	_ = doJSON[IntegrationBatch](t, h, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/changes", `{"changeId":"`+c1.ID+`"}`, http.StatusOK)
	_ = doJSON[IntegrationBatch](t, h, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/changes", `{"changeId":"`+c2.ID+`"}`, http.StatusOK)
	// 注入 merge 冲突（feat/b）
	g.mergeErrs = map[string]error{"feat/b": gitea.ErrMergeConflict}

	rec := doRaw(h, http.MethodPost, "/api/applications/app-1/batches/"+b.ID+"/integrate", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("冲突应 409，got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Error, "冲突") {
		t.Fatalf("error 应含「冲突」: %q", resp.Error)
	}
}

func TestHandlerUnauthorized(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	h := NewHandler(svc, store, WithAuthorize(func(*http.Request, string) bool { return false }))
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/api/applications/app-1/changes"},
		{http.MethodPost, "/api/applications/app-1/changes"},
		{http.MethodGet, "/api/applications/app-1/batches"},
		{http.MethodPost, "/api/applications/app-1/batches/b-1/integrate"},
	} {
		rec := doRaw(h, tc.method, tc.target, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s 期望 403，got %d", tc.method, tc.target, rec.Code)
		}
	}
}

func TestHandlerAudit(t *testing.T) {
	audit := &fakeAuditRecorder{}
	h, _, _, _ := newTestHandler(t, nil, audit)
	c1 := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"a","type":"feat","branch":"feat/a","createBranch":true}`, http.StatusCreated)
	b := doJSON[IntegrationBatch](t, h, http.MethodPost, "/api/applications/app-1/batches",
		`{"title":"x","branch":"integration/x"}`, http.StatusCreated)
	_ = doJSON[IntegrationBatch](t, h, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/changes", `{"changeId":"`+c1.ID+`"}`, http.StatusOK)
	_ = doJSON[IntegrationBatch](t, h, http.MethodPost,
		"/api/applications/app-1/batches/"+b.ID+"/integrate", "", http.StatusOK)

	want := []string{"change_create", "batch_create", "batch_add_change", "batch_integrate"}
	if len(audit.actions) != len(want) {
		t.Fatalf("审计序列不符: %v", audit.actions)
	}
	for i, a := range want {
		if audit.actions[i] != a {
			t.Fatalf("审计[%d] 期望 %s，got %s", i, a, audit.actions[i])
		}
	}
	if audit.entries[3].resourceType != "integration_batch" || audit.entries[3].resourceID != b.ID {
		t.Fatalf("integrate 审计应记批次: %+v", audit.entries[3])
	}
	if audit.entries[0].actor != "user-1" || audit.entries[0].tenantID != "t-acme" {
		t.Fatalf("审计 actor/tenant 不符: %+v", audit.entries[0])
	}
}

// TestHandlerCrossAppDenied 回归（final review I3）：URL appID 与资源归属不一致返 404
// （同租户跨应用串读串写防护）。
func TestHandlerCrossAppDenied(t *testing.T) {
	h, _, _, _ := newTestHandler(t, nil, nil)
	c := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"t","type":"feat","branch":"feat/x","createBranch":true}`, http.StatusCreated)
	// 用 app-2 的 URL 访问 app-1 的变更
	for _, target := range []string{
		"/api/applications/app-2/changes/" + c.ID,
	} {
		rec := doRaw(h, http.MethodGet, target, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 跨应用应 404，got %d", target, rec.Code)
		}
	}
}

// TestServeGlobalChangesBatches 跨应用列表：/api/changes 与 /api/batches 聚合 tenant 内
// 全部应用的变更/批次（appId 过滤 + status 过滤 + 跨租户隔离返空）。
func TestServeGlobalChangesBatches(t *testing.T) {
	h, store, _, _ := newTestHandler(t, nil, nil)
	// 两个应用各建一条变更 + 一条批次
	c1 := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"t1","type":"feat","branch":"feat/a","createBranch":true}`, http.StatusCreated)
	c2 := doJSON[Change](t, h, http.MethodPost, "/api/applications/app-1/changes",
		`{"title":"t2","type":"feat","branch":"feat/b","createBranch":true}`, http.StatusCreated)
	_ = c2
	b1 := doJSON[IntegrationBatch](t, h, http.MethodPost, "/api/applications/app-1/batches",
		`{"title":"批1","branch":"integration/g1"}`, http.StatusCreated)

	// 直接给 store 塞一条其他应用的变更（绕过 repoLookup fake 只认 app-1）
	other, err := store.CreateChange(acmeCtx(), Change{AppID: "app-2", RepoID: "repo-1", Title: "t3", Type: "feat", Branch: "feat/c"})
	if err != nil {
		t.Fatal(err)
	}

	// 全局列表：3 条变更（app-1 两条 + app-2 一条）、1 条批次
	listAll := doGlobal[[]Change](t, h, "/api/changes")
	if len(listAll) != 3 {
		t.Fatalf("全局变更应 3 条，got %d", len(listAll))
	}
	// appId 过滤
	listApp2 := doGlobal[[]Change](t, h, "/api/changes?appId=app-2")
	if len(listApp2) != 1 || listApp2[0].ID != other.ID {
		t.Fatalf("appId=app-2 应只有 1 条，got %+v", listApp2)
	}
	// status 过滤
	listOpen := doGlobal[[]Change](t, h, "/api/changes?status=open")
	if len(listOpen) != 3 {
		t.Fatalf("status=open 应 3 条，got %d", len(listOpen))
	}
	// 批次全局列表
	batches := doGlobal[[]IntegrationBatch](t, h, "/api/batches")
	if len(batches) != 1 || batches[0].ID != b1.ID {
		t.Fatalf("全局批次应 1 条，got %+v", batches)
	}
	// 跨租户隔离：globex ctx 查询返空（不泄漏 acme 数据）
	rec := httptest.NewRecorder()
	h.ServeGlobal(rec, globexReq(http.MethodGet, "/api/changes", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，got %d", rec.Code)
	}
	var resp struct {
		Data []Change `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("跨租户应返空，got %d", len(resp.Data))
	}
	// 未授权路径
	denyAll := NewHandler(h.svc, h.repo, WithAuthorize(func(*http.Request, string) bool { return false }))
	rec2 := httptest.NewRecorder()
	denyAll.ServeGlobal(rec2, acmeReq(http.MethodGet, "/api/changes", ""))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("未授权应 403，got %d", rec2.Code)
	}
	_ = c1
}

// doGlobal 经 ServeGlobal 执行并解包。
func doGlobal[T any](t *testing.T, h *Handler, target string) T {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeGlobal(rec, acmeReq(http.MethodGet, target, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s 期望 200，got %d body %s", target, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Data
}

// TestServeGlobalRoutingWiring 路由层接线回归：/api/changes|batches|notifications 必须派发到
// ServeGlobal。C1 教训——曾误接 Handler.ServeHTTP（按 /api/applications/ 前缀解析，/api/changes
// 被当作 appID="api" 返空列表、/api/notifications 落 404），单测直调 ServeGlobal 绕过路由层测不出。
// 本测试模拟 mux 装配（http.HandlerFunc(ServeGlobal) + 尾斜杠 404 语义），断言三路径行为。
func TestServeGlobalRoutingWiring(t *testing.T) {
	h, _, _, _ := newTestHandler(t, nil, nil)
	// 模拟 cmd/core 装配：mux.Handle("/api/changes", http.HandlerFunc(h.ServeGlobal)) 等
	mux := http.NewServeMux()
	global := http.HandlerFunc(h.ServeGlobal)
	mux.Handle("/api/changes", global)
	mux.Handle("/api/batches", global)
	mux.Handle("/api/notifications", global)

	for _, path := range []string{"/api/changes", "/api/batches", "/api/notifications"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, acmeReq(http.MethodGet, path, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s 经 mux 应 200，got %d body %s", path, rec.Code, rec.Body.String())
		}
		// POST 应 405（ServeGlobal 方法守卫）
		rec2 := httptest.NewRecorder()
		mux.ServeHTTP(rec2, acmeReq(http.MethodPost, path, "{}"))
		if rec2.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s 应 405，got %d", path, rec2.Code)
		}
	}
	// 尾斜杠/子路径不匹配精确 pattern（落兜底，防误派发到 ServeHTTP 语义）
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/changes/", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("尾斜杠应落兜底 404，got %d", rec.Code)
	}
}
