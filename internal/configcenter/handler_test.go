package configcenter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	ccmemory "github.com/aitoys/paas/internal/configcenter/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func newHandler() *configcenter.Handler {
	h := configcenter.NewHandler(ccmemory.NewStore())
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	return h
}

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// decodeData 解包 {data:T} 信封后反序列化到 v（单资源响应，handler 统一 WriteData 契约）。
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

// createNsViaHTTP 经 HTTP 创建命名空间，返回分配的 ID（测试不再依赖 seed）。
func createNsViaHTTP(t *testing.T, h *configcenter.Handler, ctx context.Context, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces", configcenter.Namespace{Name: name}))
	if w.Code != http.StatusCreated {
		t.Fatalf("创建命名空间 %s 应 201，got %d: %s", name, w.Code, w.Body.String())
	}
	var n configcenter.Namespace
	decodeData(t, w.Body.Bytes(), &n)
	return n.ID
}

// publishViaHTTP 经 HTTP 发布，返回新发布。
func publishViaHTTP(t *testing.T, h *configcenter.Handler, ctx context.Context, nsID string) configcenter.Publish {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/"+nsID+"/publish", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("发布应 201，got %d: %s", w.Code, w.Body.String())
	}
	var pub configcenter.Publish
	decodeData(t, w.Body.Bytes(), &pub)
	return pub
}

// upsertViaHTTP 经 HTTP upsert 配置项（改值驱动新版本——空发布被 ErrNoChanges 拒）。
func upsertViaHTTP(t *testing.T, h *configcenter.Handler, ctx context.Context, nsID, key, value string) {
	t.Helper()
	// req() 会对 body json.Marshal——传 struct（传 io.Reader 会被序列化成 JSON 字符串）
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/"+nsID+"/items", configcenter.ConfigItem{
		Key: key, Value: value, Type: configcenter.TypeText,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("upsert 应 201，got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandlerList 验证命名空间列表（租户隔离）。
func TestHandlerList(t *testing.T) {
	h := newHandler()
	createNsViaHTTP(t, h, acmeCtx(), "acme-app")
	createNsViaHTTP(t, h, globexCtx(), "globex-app")

	r := req(acmeCtx(), "GET", "/api/configcenter/namespaces", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", w.Code)
	}
	var out struct {
		Data []configcenter.Namespace `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, n := range out.Data {
		if n.TenantID != "t-acme" {
			t.Fatalf("泄漏其他租户: %s", n.Name)
		}
	}
}

// TestHandlerPublishAndDiscover 验证发布 + 发现闭环。
func TestHandlerPublishAndDiscover(t *testing.T) {
	h := newHandler()
	nsID := createNsViaHTTP(t, h, acmeCtx(), "acme-app")
	// 先建一项并发布 v1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces/"+nsID+"/items", configcenter.ConfigItem{
		Key: "feature.newui", Value: "off", Type: configcenter.TypeText,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("upsert 应 201，got %d: %s", w.Code, w.Body.String())
	}
	publishViaHTTP(t, h, acmeCtx(), nsID)

	// 改 draft（新增一项 + 改值）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces/"+nsID+"/items", configcenter.ConfigItem{
		Key: "rate.limit", Value: "300", Type: configcenter.TypeText,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("upsert 应 201，got %d", w.Code)
	}
	// 发布 v2
	pub := publishViaHTTP(t, h, acmeCtx(), nsID)
	if pub.Version != 2 {
		t.Fatalf("版本应 2，got %d", pub.Version)
	}
	// 发现
	r := req(acmeCtx(), "GET", "/api/configcenter/namespaces/"+nsID+"/published", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var disc struct {
		Published bool              `json:"published"`
		Version   int               `json:"version"`
		Snapshot  map[string]string `json:"snapshot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &disc); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !disc.Published || disc.Version != 2 || disc.Snapshot["rate.limit"] != "300" {
		t.Fatalf("发现应返回 active v2 含新值，got %+v", disc)
	}
}

// TestHandlerRollback 验证回滚。
func TestHandlerRollback(t *testing.T) {
	h := newHandler()
	nsID := createNsViaHTTP(t, h, acmeCtx(), "acme-app")
	// 建一项，发布 v1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces/"+nsID+"/items", configcenter.ConfigItem{
		Key: "feature.newui", Value: "off", Type: configcenter.TypeText,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("upsert 应 201，got %d", w.Code)
	}
	v1 := publishViaHTTP(t, h, acmeCtx(), nsID)
	// 改值再发布 v2（v1 转 rolled-back；空发布被 ErrNoChanges 拒）
	upsertViaHTTP(t, h, acmeCtx(), nsID, "feature.newui", "on")
	publishViaHTTP(t, h, acmeCtx(), nsID)

	// 回滚 v1
	r := req(acmeCtx(), "POST", "/api/configcenter/publishes/"+v1.ID+"/rollback", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("回滚应 200，got %d: %s", w.Code, w.Body.String())
	}
	// 发现应为 v1
	r = req(acmeCtx(), "GET", "/api/configcenter/namespaces/"+nsID+"/published", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var disc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &disc); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if disc.Version != 1 {
		t.Fatalf("回滚后 active 应 v1，got v%d", disc.Version)
	}
}

// TestHandlerCrossTenantHidden 验证跨租户访问不泄漏。
func TestHandlerCrossTenantHidden(t *testing.T) {
	h := newHandler()
	nsID := createNsViaHTTP(t, h, acmeCtx(), "acme-app")
	// globex 不应能 GET 到 acme 命名空间
	r := req(globexCtx(), "GET", "/api/configcenter/namespaces/"+nsID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户应 404，got %d", w.Code)
	}
}

// TestHandlerItemDeleteCrossTenantHidden 锁住 I5 修复：跨租户/跨 ns DELETE item 必须先校验
// item 归属该 namespace（ListItems 按租户+nsID 过滤），不归属则 404，杜绝越权删除。
// 回归点：serveItemDelete 的 belongs 校验若被误删，globex 可删掉 acme 的 item。
func TestHandlerItemDeleteCrossTenantHidden(t *testing.T) {
	h := newHandler()
	nsID := createNsViaHTTP(t, h, acmeCtx(), "acme-app")
	// acme 在命名空间下建一项，拿到 itemID。
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces/"+nsID+"/items", configcenter.ConfigItem{
		Key: "del.target", Value: "1", Type: configcenter.TypeText,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("建 item 应 201，got %d: %s", w.Code, w.Body.String())
	}
	var created configcenter.ConfigItem
	decodeData(t, w.Body.Bytes(), &created)
	// globex 尝试删 acme 的 item：ListItems(nsID) 在 globex ctx 下为空 → 404。
	r := req(globexCtx(), "DELETE", "/api/configcenter/namespaces/"+nsID+"/items/"+created.ID, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户删 item 应 404，got %d（越权删除成功=回归）", w.Code)
	}
}

// stubServiceLookup 测试用 ServiceLookup 桩：可控存在集合。
type stubServiceLookup struct {
	exists map[string]bool
}

func (s stubServiceLookup) ServiceExists(_ context.Context, serviceID string) (bool, error) {
	return s.exists[serviceID], nil
}

// TestCreateNamespaceRejectsDanglingServiceID 验证 CreateNamespace 校验 ServiceID 归属：
// serviceLookup 注入后，不存在的 serviceID 返 400（防悬挂引用脏数据）；
// 存在的 serviceID 正常创建；ServiceID 空（不关联）跳过校验。
func TestCreateNamespaceRejectsDanglingServiceID(t *testing.T) {
	h := newHandler()
	h.WithServiceLookup(stubServiceLookup{exists: map[string]bool{"svc-real": true}})

	// 不存在的 serviceID → 400
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces", configcenter.Namespace{
		Name: "ns-dangling", ServiceID: "svc-typo",
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("悬挂 serviceID 应 400，got %d: %s", w.Code, w.Body.String())
	}

	// 存在的 serviceID → 201
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces", configcenter.Namespace{
		Name: "ns-ok", ServiceID: "svc-real",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("存在 serviceID 应 201，got %d: %s", w.Code, w.Body.String())
	}

	// ServiceID 空（不关联）→ 201（跳过校验，向后兼容）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces", configcenter.Namespace{
		Name: "ns-noservice",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("无 serviceID 应 201，got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandlerAppScopeWriteDeniedViaNsDim 锁住 F1 修复：应用派生 ns（scope=app）经 ns 维度
// 写操作（POST items / publish / DELETE ns / item 删除 / rollback）一律 403——防绕过
// AppGuard + application 权限域；读操作（GET）放行；不存在的 ns 保持 404 不泄漏。
func TestHandlerAppScopeWriteDeniedViaNsDim(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := acmeCtx()

	// 经应用维度建 app-scope ns + 一个 item
	appH := configcenter.NewAppHandler(repo)
	appH.Authorize = h.Authorize
	w := httptest.NewRecorder()
	rq := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`)).WithContext(ctx)
	appH.ServeHTTP(w, rq)
	if w.Code != 201 {
		t.Fatalf("app upsert: %d %s", w.Code, w.Body.String())
	}
	ns, ok, err := repo.FindAppNamespace(ctx, "app-1")
	if err != nil || !ok {
		t.Fatalf("FindAppNamespace: %v ok=%v", err, ok)
	}

	// POST items → 403
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/"+ns.ID+"/items", configcenter.ConfigItem{Key: "x", Value: "1"}))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "应用派生配置请经应用详情操作") {
		t.Fatalf("ns 维度 POST items 应 403: %d %s", w.Code, w.Body.String())
	}
	// POST publish → 403
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/"+ns.ID+"/publish", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ns 维度 publish 应 403: %d", w.Code)
	}
	// DELETE ns → 403
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "DELETE", "/api/configcenter/namespaces/"+ns.ID, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ns 维度 DELETE ns 应 403: %d", w.Code)
	}
	// DELETE item → 403（先经 app 维度建一项拿 itemID）
	w = httptest.NewRecorder()
	rq = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"del","value":"v"}`)).WithContext(ctx)
	appH.ServeHTTP(w, rq)
	items, _ := repo.ListItems(ctx, ns.ID)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "DELETE", "/api/configcenter/namespaces/"+ns.ID+"/items/"+items[0].ID, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ns 维度 DELETE item 应 403: %d", w.Code)
	}

	// rollback：app ns 的 publish pid 经 ns 维度回滚 → 403
	if _, err := repo.CreatePublish(ctx, ns.ID); err != nil {
		t.Fatalf("CreatePublish: %v", err)
	}
	pubs, _ := repo.ListPublishes(ctx, ns.ID)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/publishes/"+pubs[0].ID+"/rollback", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ns 维度回滚 app 发布应 403: %d", w.Code)
	}

	// 读操作放行（GET ns/items/published/publishes 200）
	for _, p := range []string{"/api/configcenter/namespaces/" + ns.ID, "/api/configcenter/namespaces/" + ns.ID + "/items", "/api/configcenter/namespaces/" + ns.ID + "/published"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req(ctx, "GET", p, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("读应放行 %s: %d", p, w.Code)
		}
	}
	// 不存在的 ns 写操作 → 404 不泄漏
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/ns-nope/items", configcenter.ConfigItem{Key: "x", Value: "1"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在 ns 应 404: %d", w.Code)
	}
}

// TestEnsureByAppNameConflict409 锁住 M4 修复：手工共享 ns 占名 app-<appID> 后，
// 应用维度写路径懒建失败映射 409 + 引导改名文案。
func TestEnsureByAppNameConflict409(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := acmeCtx()
	// 手工建共享 ns 占名
	nsh := configcenter.NewHandler(repo)
	nsh.Authorize = h.Authorize
	w := httptest.NewRecorder()
	nsh.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces", configcenter.Namespace{Name: "app-app-9"}))
	if w.Code != 201 {
		t.Fatalf("占名 ns 应 201: %d %s", w.Code, w.Body.String())
	}
	// 应用维度写路径 → 409
	rq := httptest.NewRequest("POST", "/api/applications/app-9/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rq)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "命名空间名被占用") {
		t.Fatalf("占名应 409: %d %s", rec.Code, rec.Body.String())
	}
}

// fakeAppLookup 固定映射的应用名→ID 解析器（测试桩）。
type fakeAppLookup struct{ m map[string]string }

func (f fakeAppLookup) AppIDByName(ctx context.Context, name string) (string, error) {
	return f.m[name], nil
}

// TestAppPublishedByName 按应用名发现端点：有发布返快照，未知应用名 {"published":false} 不泄漏。
func TestAppPublishedByName(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	h.WithAppLookup(fakeAppLookup{m: map[string]string{"shop": "app-1"}})
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 经应用维度先发布
	ns, err := repo.EnsureByApp(ctx, "app-1")
	if err != nil {
		t.Fatalf("EnsureByApp: %v", err)
	}
	if _, err := repo.UpsertItem(ctx, configcenter.ConfigItem{NamespaceID: ns.ID, Key: "topk", Value: "3", Type: "text"}); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	if _, err := repo.CreatePublish(ctx, ns.ID); err != nil {
		t.Fatalf("CreatePublish: %v", err)
	}
	// 按名发现
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "GET", "/api/configcenter/apps/shop/published", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "topk") {
		t.Fatalf("按名发现失败: %d %s", w.Code, w.Body.String())
	}
	// 未知应用名：{"published":false}（不泄漏存在性）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "GET", "/api/configcenter/apps/nope/published", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"published":false`) {
		t.Fatalf("未知应用: %d %s", w.Code, w.Body.String())
	}
}

// stubAudit 收集审计落参（测试断言用）。
type stubAudit struct {
	mu      sync.Mutex
	records [][5]string // tenantID, action, resourceID, detail, _
}

func (s *stubAudit) record(ctx context.Context, tenantID, action, resourceID, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, [5]string{tenantID, action, resourceID, detail, ""})
}

// TestHandlerNamespaceAudit 锁住 R1-I1 修复：ns 维度写操作全覆盖审计
// （ns 建删 + item 建改删 + publish + rollback），落参断言 action/resourceID/detail。
func TestHandlerNamespaceAudit(t *testing.T) {
	repo := ccmemory.NewStore()
	aud := &stubAudit{}
	h := configcenter.NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	h.WithAudit(aud.record)
	ctx := acmeCtx()

	// 1) CreateNamespace 审计
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces", configcenter.Namespace{Name: "audit-ns"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create ns: %d %s", w.Code, w.Body.String())
	}
	var n configcenter.Namespace
	decodeData(t, w.Body.Bytes(), &n)
	// 2) UpsertItem 审计
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/"+n.ID+"/items", configcenter.ConfigItem{Key: "k1", Value: "v1"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("upsert item: %d %s", w.Code, w.Body.String())
	}
	var it configcenter.ConfigItem
	decodeData(t, w.Body.Bytes(), &it)
	// 3) publish 审计（v1）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/"+n.ID+"/publish", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	// 改值再发布 v2（v1 转 rolled-back，供回滚；空发布被 ErrNoChanges 拒）
	upsertViaHTTP(t, h, ctx, n.ID, "audit.key", "v2")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces/"+n.ID+"/publish", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("publish v2: %d %s", w.Code, w.Body.String())
	}
	// 4) rollback 审计（回滚到 v1）
	pubs, _ := repo.ListPublishes(ctx, n.ID)
	var v1 string
	for _, p := range pubs {
		if p.Version == 1 {
			v1 = p.ID
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/publishes/"+v1+"/rollback", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", w.Code, w.Body.String())
	}
	// 5) item delete 审计
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "DELETE", "/api/configcenter/namespaces/"+n.ID+"/items/"+it.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete item: %d %s", w.Code, w.Body.String())
	}
	// 6) ns delete 审计
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "DELETE", "/api/configcenter/namespaces/"+n.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete ns: %d %s", w.Code, w.Body.String())
	}

	want := []string{
		"configcenter_ns_create", "configcenter_item_upsert", "configcenter_publish",
		"configcenter_item_upsert", "configcenter_publish", "configcenter_rollback",
		"configcenter_item_delete", "configcenter_ns_delete",
	}
	aud.mu.Lock()
	defer aud.mu.Unlock()
	if len(aud.records) != len(want) {
		t.Fatalf("审计应 %d 条, got %d: %v", len(want), len(aud.records), aud.records)
	}
	for i, wa := range want {
		if aud.records[i][1] != wa {
			t.Fatalf("审计[%d] action 应 %s, got %s", i, wa, aud.records[i][1])
		}
	}
	if aud.records[0][0] != "t-acme" || aud.records[0][2] != n.ID {
		t.Fatalf("ns_create 审计落参错误: %v", aud.records[0])
	}
	if !strings.Contains(aud.records[1][3], "key=k1") {
		t.Fatalf("item_upsert 审计 detail 应含 key: %v", aud.records[1])
	}
}

// TestHandlerNamespaceWriteForbidden 锁住 R7-F5：只读授权下 ns 维度全部写操作 403。
func TestHandlerNamespaceWriteForbidden(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return perm == configcenter.PermConfigCenterRead }
	ctx := acmeCtx()

	// 建一个共享 ns（绕过 handler，直写 repo）作为删除/发布目标。
	n, err := repo.CreateNamespace(ctx, configcenter.Namespace{Name: "ro-ns"})
	if err != nil {
		t.Fatalf("seed ns: %v", err)
	}

	writes := []struct {
		method, path string
		body         interface{}
	}{
		{"POST", "/api/configcenter/namespaces", configcenter.Namespace{Name: "x"}},
		{"DELETE", "/api/configcenter/namespaces/" + n.ID, nil},
		{"POST", "/api/configcenter/namespaces/" + n.ID + "/items", configcenter.ConfigItem{Key: "k", Value: "v"}},
		{"DELETE", "/api/configcenter/namespaces/" + n.ID + "/items/item-x", nil},
		{"POST", "/api/configcenter/namespaces/" + n.ID + "/publish", nil},
		{"POST", "/api/configcenter/publishes/pub-x/rollback", nil},
	}
	for i, wr := range writes {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(ctx, wr.method, wr.path, wr.body))
		if w.Code != http.StatusForbidden {
			t.Fatalf("写操作[%d] %s %s 应 403, got %d", i, wr.method, wr.path, w.Code)
		}
	}
	// 读放行
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "GET", "/api/configcenter/namespaces", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("读应放行: %d", w.Code)
	}
}

// TestCreateNamespaceNameTaken409 锁住 R5-M3：ns 维度创建重名统一 409（原 400，
// sentinel ErrNamespaceNameTaken 判定）。
func TestCreateNamespaceNameTaken409(t *testing.T) {
	h := newHandler()
	ctx := acmeCtx()
	createNsViaHTTP(t, h, ctx, "dup-ns")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/namespaces", configcenter.Namespace{Name: "dup-ns"}))
	if w.Code != http.StatusConflict {
		t.Fatalf("重名应 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRollbackAlreadyActive409 锁住 R4-M3：回滚当前 active 版本映射 409（非 400）。
func TestRollbackAlreadyActive409(t *testing.T) {
	h := newHandler()
	ctx := acmeCtx()
	nsID := createNsViaHTTP(t, h, ctx, "rb-ns")
	pub := publishViaHTTP(t, h, ctx, nsID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "POST", "/api/configcenter/publishes/"+pub.ID+"/rollback", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("回滚 active 应 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAppPublishedByNameEnvLane 按应用名发现接 env/lane（F1 修复）：env 回退 + lane merge + overrideHash。
func TestAppPublishedByNameEnvLane(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	h.WithAppLookup(fakeAppLookup{m: map[string]string{"shop": "app-1"}})
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 基线（env=''）发布 topk=3
	ns, _ := repo.EnsureByApp(ctx, "app-1")
	_, _ = repo.UpsertItem(ctx, configcenter.ConfigItem{NamespaceID: ns.ID, Key: "topk", Value: "3", Type: "text"})
	if _, err := repo.CreatePublish(ctx, ns.ID); err != nil {
		t.Fatalf("CreatePublish: %v", err)
	}
	// lane 覆盖 topk=5（写入在 env='' 层——发现带 env 时回退命中）
	if _, err := repo.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "app-1", LaneID: "feat", Key: "topk", Value: "5"}); err != nil {
		t.Fatalf("UpsertLaneOverride: %v", err)
	}

	get := func(path string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(ctx, "GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s -> %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	// 不带 query：向后兼容（基线，无 overrideHash）
	if b := get("/api/configcenter/apps/shop/published"); strings.Contains(b, "overrideHash") || !strings.Contains(b, `"topk":"3"`) {
		t.Fatalf("无 query 应基线无 hash: %s", b)
	}
	// env 精确未命中 → 回退基线；lane=feat 命中覆盖 → merge + hash
	b := get("/api/configcenter/apps/shop/published?env=env-x&lane=feat")
	if !strings.Contains(b, `"topk":"5"`) || !strings.Contains(b, "overrideHash") {
		t.Fatalf("env 回退 + lane merge 失败: %s", b)
	}
	// lane 不带：基线值
	if b := get("/api/configcenter/apps/shop/published?env=env-x"); !strings.Contains(b, `"topk":"3"`) {
		t.Fatalf("仅 env 回退应基线: %s", b)
	}
	// 未知应用带 query 仍统一 {"published":false} 不泄漏
	if b := get("/api/configcenter/apps/nope/published?env=env-x&lane=feat"); !strings.Contains(b, `"published":false`) {
		t.Fatalf("未知应用带 query 应 unpublished: %s", b)
	}
}

// lane=default 在按应用名发现端点同样归一为基线（不报错——发现是数据面宽入口，与覆盖写 400 语义区分）。
func TestAppPublishedByNameLaneDefault(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	h.WithAppLookup(fakeAppLookup{m: map[string]string{"shop": "app-1"}})
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	ns, _ := repo.EnsureByApp(ctx, "app-1")
	_, _ = repo.UpsertItem(ctx, configcenter.ConfigItem{NamespaceID: ns.ID, Key: "k", Value: "v", Type: "text"})
	if _, err := repo.CreatePublish(ctx, ns.ID); err != nil {
		t.Fatalf("CreatePublish: %v", err)
	}
	// 覆盖挂在 lane=default 上不会被消费（归一基线后 lane=""）
	if _, err := repo.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "app-1", LaneID: "default", Key: "k", Value: "x"}); err != nil {
		t.Fatalf("UpsertLaneOverride: %v", err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(ctx, "GET", "/api/configcenter/apps/shop/published?lane=default", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"k":"v"`) || strings.Contains(w.Body.String(), "overrideHash") {
		t.Fatalf("lane=default 发现应基线无 hash: %d %s", w.Code, w.Body.String())
	}
}
