package configcenter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"encoding/json"
	"github.com/aitoys/paas/internal/configcenter"
	ccmemory "github.com/aitoys/paas/internal/configcenter/memory"

	"github.com/aitoys/paas/pkg/tenant"
)

// guardFunc 把 func 适配为 configcenter.GuardAdapter（测试用）。
type guardFunc func(r *http.Request, appID, action string) bool

func (f guardFunc) Allow(r *http.Request, appID, action string) bool { return f(r, appID, action) }

// resolverFunc 把 func 适配为 configcenter.EnvTypeResolver（测试用）。
type resolverFunc func(ctx context.Context, envID string) (string, error)

func (f resolverFunc) EnvType(ctx context.Context, envID string) (string, error) {
	return f(ctx, envID)
}

// TestAppDynamicConfigsCRUDAndPublish 应用维度动态配置端到端：upsert（自动 EnsureByApp）→ 列表 → 发布 → 发现。
func TestAppDynamicConfigsCRUDAndPublish(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// POST upsert（自动 EnsureByApp）
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"greeting","value":"hello","type":"text"}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("upsert: %d %s", rec.Code, rec.Body.String())
	}
	// GET 列表
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "greeting") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	// publish
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	// published 当前生效
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"published":true`) || !strings.Contains(rec.Body.String(), "greeting") {
		t.Fatalf("published: %s", rec.Body.String())
	}
}

// TestAppDynamicConfigsGuardDenied 写权限不足 403（application:write，读放行写拒绝）。
func TestAppDynamicConfigsGuardDenied(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return perm == "application:read" } // 读放行写拒绝
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("写应 403: %d", rec.Code)
	}
}

// TestAppDynamicConfigsCrossTenantNotFound 跨租户互不可见（EnsureByApp 各建各的 ns，不泄漏）。
func TestAppDynamicConfigsCrossTenantNotFound(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`))
	req = req.WithContext(ctxA)
	h.ServeHTTP(httptest.NewRecorder(), req)
	// t-globex 访问同名 app-1：EnsureByApp 建的是自己租户的 ns，互不可见（不泄漏）
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs", nil)
	req = req.WithContext(ctxB)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "k") {
		t.Fatal("跨租户泄漏")
	}
}

// TestAppDynamicConfigsAppGuard 受限应用 AppGuard write 动作拦截（依赖倒置 GuardAdapter）。
func TestAppDynamicConfigsAppGuard(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	h.WithGuard(guardFunc(func(r *http.Request, appID, action string) bool { return false })) // 全拒
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("Guard 拒绝应 403: %d", rec.Code)
	}
	// 只读路径不受 Guard 限制（Guard 只拦写）
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("读不应受 Guard 限制: %d", rec.Code)
	}
}

// TestAppDynamicConfigsPublishAudit publish 记审计（依赖倒置 AuditFunc）。
func TestAppDynamicConfigsPublishAudit(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	var audited [5]string
	h.WithAudit(func(ctx context.Context, tenantID, action, resourceID, detail string) {
		audited = [5]string{"done", tenantID, action, resourceID, detail}
	})
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`))
	req = req.WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	if audited[0] != "done" || audited[2] != "configcenter_publish" || audited[3] != "app-1" {
		t.Fatalf("审计未落: %v", audited)
	}
}

// TestAppDynamicConfigsPublishedEmpty 未发布时发现返 {"published":false}（不懒建 ns）。
func TestAppDynamicConfigsPublishedEmpty(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("GET", "/api/applications/app-9/dynamic-configs/published", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"published":false`) {
		t.Fatalf("published 空态: %s", rec.Body.String())
	}
}

// TestAppDynamicConfigsGetNoLazyCreate GET 列表只读不懒建：无 ns 返空列表（锁住 M5 修复，
// 回归点：GET 误走 EnsureByApp 会在只读路径创建 ns）。
func TestAppDynamicConfigsGetNoLazyCreate(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("GET", "/api/applications/app-none/dynamic-configs", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("无 ns 应返空列表: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok, err := repo.FindAppNamespace(ctx, "app-none"); err != nil || ok {
		t.Fatalf("GET 不应懒建 ns（ok=%v err=%v）", ok, err)
	}
}

// TestAppDynamicConfigsRollback 应用维度回滚端到端：publish v1 → v2 → 回滚 v1 +
// 审计落参 + 跨应用 pid 404（不泄漏存在性）。
func TestAppDynamicConfigsRollback(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	var audited [5]string
	h.WithAudit(func(ctx context.Context, tenantID, action, resourceID, detail string) {
		audited = [5]string{"done", tenantID, action, resourceID, detail}
	})
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	run := func(path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest("POST", path, rd)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 建项 + 发布 v1 + v2
	if rec := run("/api/applications/app-1/dynamic-configs", `{"key":"k","value":"v1"}`); rec.Code != 201 {
		t.Fatalf("upsert: %d %s", rec.Code, rec.Body.String())
	}
	if rec := run("/api/applications/app-1/dynamic-configs/publish", ""); rec.Code != 201 {
		t.Fatalf("publish v1: %d", rec.Code)
	}
	run("/api/applications/app-1/dynamic-configs", `{"key":"k","value":"v2"}`)
	if rec := run("/api/applications/app-1/dynamic-configs/publish", ""); rec.Code != 201 {
		t.Fatalf("publish v2: %d", rec.Code)
	}
	pubs, _ := repo.ListPublishes(ctx, mustNSID(t, repo, ctx, "app-1"))
	var v1 string
	for _, p := range pubs {
		if p.Version == 1 {
			v1 = p.ID
		}
	}

	// 回滚 v1（app 维度端点）
	rec := run("/api/applications/app-1/dynamic-configs/rollback/"+v1, "")
	if rec.Code != 200 {
		t.Fatalf("rollback 应 200: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("rollback 返回应 v1: %s", rec.Body.String())
	}
	// 审计落参
	if audited[0] != "done" || audited[2] != "configcenter_rollback" || audited[3] != "app-1" {
		t.Fatalf("回滚审计未落: %v", audited)
	}

	// 跨应用 pid（他人应用的发布）→ 404 不泄漏
	if rec := run("/api/applications/app-2/dynamic-configs/rollback/"+v1, ""); rec.Code != 404 {
		t.Fatalf("跨应用回滚应 404: %d", rec.Code)
	}
	// 不存在的 pid → 404
	if rec := run("/api/applications/app-1/dynamic-configs/rollback/pub-nope", ""); rec.Code != 404 {
		t.Fatalf("不存在 pid 应 404: %d", rec.Code)
	}
}

// TestRollbackResetsDraftItems 锁住「回滚同步重置草稿」：回滚后 draft items
// 对齐目标版本快照（值改回、多余 key 删、缺失 key 补），不再显示假差异待发布。
func TestRollbackResetsDraftItems(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	run := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// v1：k1=a, k2=b
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"k1","value":"a"}`)
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"k2","value":"b"}`)
	run("POST", "/api/applications/app-1/dynamic-configs/publish", "")
	// v2：k1 改值 + k2 删除 + k3 新增
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"k1","value":"a2"}`)
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"k3","value":"c"}`)
	nsID := mustNSID(t, repo, ctx, "app-1")
	items, _ := repo.ListItems(ctx, nsID)
	var k2id string
	for _, it := range items {
		if it.Key == "k2" {
			k2id = it.ID
		}
	}
	run("DELETE", "/api/applications/app-1/dynamic-configs/items/"+k2id, "")
	run("POST", "/api/applications/app-1/dynamic-configs/publish", "")

	pubs, _ := repo.ListPublishes(ctx, nsID)
	var v1id string
	for _, p := range pubs {
		if p.Version == 1 {
			v1id = p.ID
		}
	}
	if rec := run("POST", "/api/applications/app-1/dynamic-configs/rollback/"+v1id, ""); rec.Code != 200 {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}

	// 断言：draft 对齐 v1 快照——k1=a（值改回）、k2=b（补回）、k3 无（删除）
	items, _ = repo.ListItems(ctx, nsID)
	got := map[string]string{}
	for _, it := range items {
		got[it.Key] = it.Value
	}
	want := map[string]string{"k1": "a", "k2": "b"}
	if len(got) != len(want) {
		t.Fatalf("回滚后 draft 应 %d 项，实得 %v", len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key=%s 应 %s，实得 %q（全部: %v）", k, v, got[k], got)
		}
	}
}

func mustNSID(t *testing.T, repo configcenter.Repository, ctx context.Context, appID string) string {
	t.Helper()
	ns, ok, err := repo.FindAppNamespace(ctx, appID)
	if err != nil || !ok {
		t.Fatalf("FindAppNamespace: %v ok=%v", err, ok)
	}
	return ns.ID
}

// TestAppDynamicConfigsItemDelete 锁住 R7-F1：DELETE item 成功 + 审计 + 跨应用 item 404 + 405。
func TestAppDynamicConfigsItemDelete(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	var audited [5]string
	h.WithAudit(func(ctx context.Context, tenantID, action, resourceID, detail string) {
		audited = [5]string{"done", tenantID, action, resourceID, detail}
	})
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	run := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 建项
	if rec := run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"dk","value":"dv"}`); rec.Code != 201 {
		t.Fatalf("upsert: %d %s", rec.Code, rec.Body.String())
	}
	items, _ := repo.ListItems(ctx, mustNSID(t, repo, ctx, "app-1"))
	itemID := items[0].ID

	// 非 DELETE 方法 → 405
	if rec := run("POST", "/api/applications/app-1/dynamic-configs/items/"+itemID, ""); rec.Code != 405 {
		t.Fatalf("非 DELETE 应 405: %d", rec.Code)
	}
	// 跨应用 item（app-2 无任何项）→ 404
	if rec := run("DELETE", "/api/applications/app-2/dynamic-configs/items/"+itemID, ""); rec.Code != 404 {
		t.Fatalf("跨应用删 item 应 404: %d", rec.Code)
	}
	// 成功删除 + 审计
	if rec := run("DELETE", "/api/applications/app-1/dynamic-configs/items/"+itemID, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if audited[0] != "done" || audited[2] != "configcenter_item_delete" || audited[3] != "app-1" {
		t.Fatalf("删除审计未落: %v", audited)
	}
	// 再删 → 404（item 已不在）
	if rec := run("DELETE", "/api/applications/app-1/dynamic-configs/items/"+itemID, ""); rec.Code != 404 {
		t.Fatalf("重复删应 404: %d", rec.Code)
	}
}

// TestAppDynamicConfigsPublishHistory 锁住 R7-F2：发布历史两版本降序 + 无 ns 空列表。
func TestAppDynamicConfigsPublishHistory(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	run := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 无 ns → 空列表（不懒建）
	if _, ok, _ := repo.FindAppNamespace(ctx, "app-1"); ok {
		t.Fatal("前置：不应已有 ns")
	}
	rec := run("/api/applications/app-1/dynamic-configs/publishes")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("无 ns 应空列表: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := repo.FindAppNamespace(ctx, "app-1"); ok {
		t.Fatal("publishes 不应懒建 ns")
	}

	// 建项发布两版
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v1"}`))
	req = req.WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish", nil).WithContext(ctx))
	// 改值再发布 v2（空发布被 ErrNoChanges 拒）
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v2"}`)).WithContext(ctx))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish", nil).WithContext(ctx))

	rec = run("/api/applications/app-1/dynamic-configs/publishes")
	if rec.Code != 200 {
		t.Fatalf("publishes: %d", rec.Code)
	}
	var env struct {
		Data []configcenter.Publish `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if len(env.Data) != 2 || env.Data[0].Version != 2 || env.Data[1].Version != 1 {
		t.Fatalf("应两版本降序 [2,1], got %+v", env.Data)
	}
}

// TestAppDynamicConfigsGuardAllowPath 锁住 R7-F6：Guard 桩 write 动作放行 → upsert 201。
func TestAppDynamicConfigsGuardAllowPath(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	h.WithGuard(guardFunc(func(r *http.Request, appID, action string) bool { return action == "write" }))
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("Guard 放行应 201: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAppDynamicConfigsItemAudit 应用维度 item upsert/delete 审计落参（R1-I1 补全）。
func TestAppDynamicConfigsItemAudit(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	var actions []string
	h.WithAudit(func(ctx context.Context, tenantID, action, resourceID, detail string) {
		actions = append(actions, action+"|"+resourceID+"|"+detail)
	})
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	run := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"ak","value":"av"}`)
	if rec.Code != 201 {
		t.Fatalf("upsert: %d %s", rec.Code, rec.Body.String())
	}
	items, _ := repo.ListItems(ctx, mustNSID(t, repo, ctx, "app-1"))
	run("DELETE", "/api/applications/app-1/dynamic-configs/items/"+items[0].ID, "")
	if len(actions) != 2 ||
		!strings.HasPrefix(actions[0], "configcenter_item_upsert|app-1|item=") ||
		!strings.Contains(actions[0], "key=ak") ||
		!strings.HasPrefix(actions[1], "configcenter_item_delete|app-1|item=") {
		t.Fatalf("item 审计落参错误: %v", actions)
	}
}

// TestAppDynamicConfigsRollbackNoLazyCreate 锁住 R5-M4/R8-M2：rollback/itemDelete 不懒建 ns
// （无 ns 返 404，不再凭空创建派生命名空间）。
func TestAppDynamicConfigsRollbackNoLazyCreate(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	run := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := run("POST", "/api/applications/app-none/dynamic-configs/rollback/pub-x"); rec.Code != 404 {
		t.Fatalf("无 ns rollback 应 404: %d", rec.Code)
	}
	if rec := run("DELETE", "/api/applications/app-none/dynamic-configs/items/item-x"); rec.Code != 404 {
		t.Fatalf("无 ns itemDelete 应 404: %d", rec.Code)
	}
	if _, ok, _ := repo.FindAppNamespace(ctx, "app-none"); ok {
		t.Fatal("rollback/itemDelete 不应懒建 ns")
	}
}

// TestPublishEnvIsolation 环境隔离：同一应用 test/prod 各自独立发布互不可见。
func TestPublishEnvIsolation(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// test env 发布 v
	postQ := func(body, q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs?"+q, strings.NewReader(body))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := postQ(`{"key":"greeting","value":"hello-test","type":"text"}`, "envId=env-t"); rec.Code != 201 {
		t.Fatalf("test upsert: %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish?envId=env-t", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("test publish: %d %s", rec.Code, rec.Body.String())
	}
	// prod env 独立 upsert + 发布
	if rec := postQ(`{"key":"greeting","value":"hello-prod","type":"text"}`, "envId=env-p"); rec.Code != 201 {
		t.Fatalf("prod upsert: %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish?envId=env-p", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("prod publish: %d %s", rec.Code, rec.Body.String())
	}
	// 发现 env-t 只见 hello-test；env-p 只见 hello-prod
	get := func(q string) string {
		req := httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published"+q, nil)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if b := get("?envId=env-t"); !strings.Contains(b, "hello-test") || strings.Contains(b, "hello-prod") {
		t.Fatalf("env-t 发现应只见 test 值: %s", b)
	}
	if b := get("?envId=env-p"); !strings.Contains(b, "hello-prod") || strings.Contains(b, "hello-test") {
		t.Fatalf("env-p 发现应只见 prod 值: %s", b)
	}
	// 发布历史同样隔离：env-t 只有 1 个发布
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/publishes?envId=env-t", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env struct {
		Data []configcenter.Publish `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Data) != 1 {
		t.Fatalf("env-t 发布历史应 1 条: %s err=%v", rec.Body.String(), err)
	}
}

// TestProdPublishRequiresPerm 生产环境 publish/回滚/覆盖写需 prod:write（EnvTypeResolver fail-closed）。
func TestProdPublishRequiresPerm(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return perm != "prod:write" } // prod:write 拒绝
	h.WithEnvResolver(resolverFunc(func(ctx context.Context, envID string) (string, error) {
		switch envID {
		case "env-prod":
			return "prod", nil
		case "env-test":
			return "test", nil
		}
		return "", errors.New("环境不存在") // 未知 env 报错 → handler fail-closed 按生产
	}))
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish?envId=env-prod", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("prod publish 应 403: %d %s", rec.Code, rec.Body.String())
	}
	// test env 不需要 prod:write
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish?envId=env-test", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("test publish 应放行: %d %s", rec.Code, rec.Body.String())
	}
	// 未知环境 fail-closed 按生产：403
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish?envId=env-unknown", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("未知 env 应 fail-closed 403: %d", rec.Code)
	}
}

// TestLaneOverrideMerge 泳道覆盖端到端：发现时基线+覆盖 merge，overrideHash 随覆盖变化。
func TestLaneOverrideMerge(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 基线：upsert + publish（env=''）
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"a","value":"base","type":"text"}`))
	req = req.WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish", nil)
	req = req.WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	// 无覆盖发现：无 overrideHash 字段，基线值透传
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published?lane=feat-x", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"a":"base"`) || strings.Contains(body, "overrideHash") {
		t.Fatalf("无覆盖发现应透传基线且无 overrideHash: %s", body)
	}
	// 写覆盖 key a
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/lane-overrides?envId=&lane=feat-x",
		strings.NewReader(`{"key":"a","value":"override"}`))
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("覆盖 upsert: %d %s", rec.Code, rec.Body.String())
	}
	// 有覆盖发现：merge 生效 + overrideHash 出现
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published?lane=feat-x", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, `"a":"override"`) || !strings.Contains(body, "overrideHash") {
		t.Fatalf("覆盖发现应 merge+hash: %s", body)
	}
	// 无 lane 参数：不取覆盖（基线）
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `"a":"override"`) {
		t.Fatalf("无 lane 应基线: %s", rec.Body.String())
	}
	// 覆盖列表
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/lane-overrides?lane=feat-x", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "override") {
		t.Fatalf("覆盖列表: %s", rec.Body.String())
	}
}

// TestDiscoveryBackwardCompat 不带 env/lane 的发现调用行为与升级前一致（env=” 基线）。
func TestDiscoveryBackwardCompat(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v","type":"text"}`))
	req = req.WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish", nil)
	req = req.WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	// 不带任何 query：published 可见基线（env='' + lane 空路径）
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"published":true`) || !strings.Contains(rec.Body.String(), `"k":"v"`) {
		t.Fatalf("向后兼容发现失败: %s", rec.Body.String())
	}
	// env 精确 ns 不存在 → 回退 env='' 基线（Task 1 发现回退语义：存量 ns 继续可发现）。
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published?envId=env-x", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"k":"v"`) {
		t.Fatalf("无精确 env ns 应回退基线: %s", rec.Body.String())
	}
	// 全部无 ns 的应用：published:false
	req = httptest.NewRequest("GET", "/api/applications/app-none/dynamic-configs/published", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"published":false`) {
		t.Fatalf("无 ns 应用发现应 false: %s", rec.Body.String())
	}
}

// TestLaneDefaultRejected lane="default" 即基线，不作为泳道接受（F4：入口统一拒 400，
// 客户端误传 default 时显式报错而非静默当基线）。
func TestLaneDefaultRejected(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	run := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := run("GET", "/api/applications/app-1/dynamic-configs/lane-overrides?lane=default", ""); rec.Code != 400 {
		t.Fatalf("lane-overrides GET default 应 400: %d", rec.Code)
	}
	if rec := run("POST", "/api/applications/app-1/dynamic-configs/lane-overrides?lane=default", `{"key":"k","value":"v"}`); rec.Code != 400 {
		t.Fatalf("lane-overrides POST default 应 400: %d", rec.Code)
	}
}

// TestProdGateCoversAllWrites 生产闸门覆盖全部写路径（F5）：rollback/item delete/lane-overrides
// 写在 prod env（含 fail-closed 未知 env）均 403。
func TestProdGateCoversAllWrites(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return perm != "prod:write" }
	h.WithEnvResolver(resolverFunc(func(ctx context.Context, envID string) (string, error) {
		if envID == "env-t" {
			return "test", nil
		}
		return "", errors.New("环境不存在") // fail-closed
	}))
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 在 test env 建配置 + 发布，拿 itemID/publishID
	mk := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := mk("POST", "/api/applications/app-1/dynamic-configs?envId=env-t", `{"key":"k","value":"v","type":"text"}`)
	if rec.Code != 201 {
		t.Fatalf("test upsert: %d %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Data configcenter.ConfigItem `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("解包 upsert: %v", err)
	}
	rec = mk("POST", "/api/applications/app-1/dynamic-configs/publish?envId=env-t", "")
	if rec.Code != 201 {
		t.Fatalf("test publish: %d %s", rec.Code, rec.Body.String())
	}
	var pub struct {
		Data configcenter.Publish `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pub); err != nil {
		t.Fatalf("解包 publish: %v", err)
	}

	// 以下各写路径带 prod envId → 403
	cases := []struct{ name, method, path, body string }{
		{"item delete", "DELETE", "/api/applications/app-1/dynamic-configs/items/" + saved.Data.ID + "?envId=env-p", ""},
		{"rollback", "POST", "/api/applications/app-1/dynamic-configs/rollback/" + pub.Data.ID + "?envId=env-p", ""},
		{"lane override upsert", "POST", "/api/applications/app-1/dynamic-configs/lane-overrides?envId=env-p&lane=feat", `{"key":"k","value":"v"}`},
		{"lane override delete", "DELETE", "/api/applications/app-1/dynamic-configs/lane-overrides/k?envId=env-p&lane=feat", ""},
		{"未知 env item delete fail-closed", "DELETE", "/api/applications/app-1/dynamic-configs/items/" + saved.Data.ID + "?envId=env-unknown", ""},
	}
	for _, c := range cases {
		if rec := mk(c.method, c.path, c.body); rec.Code != 403 {
			t.Errorf("%s 应 403: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	// 同路径 test env 放行（对照）
	if rec := mk("DELETE", "/api/applications/app-1/dynamic-configs/items/"+saved.Data.ID+"?envId=env-t", ""); rec.Code == 403 {
		t.Errorf("test env item delete 不应 403: %d", rec.Code)
	}
}

// TestLanePromote 泳道灰度提升到基线：覆盖合并进 draft → 新版本 → 覆盖清空。
// 失败语义：空集 400；提升后再次 promote 空集（幂等边界）。
func TestLanePromote(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	mk := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// 基线：a=base 发布 v1
	mk("POST", "/api/applications/app-1/dynamic-configs?envId=env-t", `{"key":"a","value":"base","type":"text"}`)
	mk("POST", "/api/applications/app-1/dynamic-configs/publish?envId=env-t", "")
	// 泳道覆盖：a=override + 新 key b=new
	if rec := mk("POST", "/api/applications/app-1/dynamic-configs/lane-overrides?envId=env-t&lane=feat-x", `{"key":"a","value":"override"}`); rec.Code != 201 {
		t.Fatalf("覆盖 a: %d %s", rec.Code, rec.Body.String())
	}
	if rec := mk("POST", "/api/applications/app-1/dynamic-configs/lane-overrides?envId=env-t&lane=feat-x", `{"key":"b","value":"new"}`); rec.Code != 201 {
		t.Fatalf("覆盖 b: %d %s", rec.Code, rec.Body.String())
	}
	// 提升
	rec := mk("POST", "/api/applications/app-1/dynamic-configs/lane-overrides/promote?envId=env-t&lane=feat-x", "")
	if rec.Code != 201 {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body.String())
	}
	var pub struct{ Data configcenter.Publish }
	if err := json.Unmarshal(rec.Body.Bytes(), &pub); err != nil || pub.Data.Version != 2 {
		t.Fatalf("promote 应产生 v2: %s", rec.Body.String())
	}
	// 基线发现：a=override、b=new（新 key 也在快照）
	rec = mk("GET", "/api/applications/app-1/dynamic-configs/published?envId=env-t", "")
	body := rec.Body.String()
	if !strings.Contains(body, `"a":"override"`) || !strings.Contains(body, `"b":"new"`) {
		t.Fatalf("提升后基线应含覆盖值: %s", body)
	}
	if strings.Contains(body, "overrideHash") {
		t.Fatalf("无 lane 发现不应有 overrideHash: %s", body)
	}
	// 覆盖清空：列表空 + 带 lane 发现等于基线
	rec = mk("GET", "/api/applications/app-1/dynamic-configs/lane-overrides?envId=env-t&lane=feat-x", "")
	if rec.Body.String() != "null" && !strings.Contains(rec.Body.String(), `"data":[]`) && !strings.Contains(rec.Body.String(), `"data":null`) {
		t.Fatalf("提升后覆盖应清空: %s", rec.Body.String())
	}
	// 幂等边界：再次 promote 空集 400
	if rec := mk("POST", "/api/applications/app-1/dynamic-configs/lane-overrides/promote?envId=env-t&lane=feat-x", ""); rec.Code != 400 {
		t.Fatalf("空覆盖 promote 应 400: %d %s", rec.Code, rec.Body.String())
	}
	// lane=default 拒绝
	if rec := mk("POST", "/api/applications/app-1/dynamic-configs/lane-overrides/promote?envId=env-t&lane=default", ""); rec.Code != 400 {
		t.Fatalf("lane=default promote 应 400: %d", rec.Code)
	}
}

// TestSharedRefDiscoveryMerge 共享配置引用端到端：建 shared ns + 发布 → 应用引用 →
// 按应用名发现快照含 shared key；应用自身同 key 胜出（逃生门）；sharedHash 指纹存在；
// 解除引用后 snapshot 不再含 shared key、sharedHash 省略。
func TestSharedRefDiscoveryMerge(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	run := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// shared ns：skey=shared-val, overlap=shared-default；发布 v1
	nsHandler := configcenter.NewHandler(repo)
	nsHandler.Authorize = func(r *http.Request, perm string) bool { return true }
	var sharedID string
	{
		req := httptest.NewRequest("POST", "/api/configcenter/namespaces", strings.NewReader(`{"name":"common-flags"}`))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		nsHandler.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("create shared ns: %d %s", rec.Code, rec.Body.String())
		}
		var created struct{ Data configcenter.Namespace }
		_ = json.Unmarshal(rec.Body.Bytes(), &created)
		sharedID = created.Data.ID
	}
	for _, kv := range []string{
		`{"key":"skey","value":"shared-val"}`,
		`{"key":"overlap","value":"shared-default"}`,
	} {
		req := httptest.NewRequest("POST", "/api/configcenter/namespaces/"+sharedID+"/items", strings.NewReader(kv))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		nsHandler.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("shared item: %d %s", rec.Code, rec.Body.String())
		}
	}
	{
		req := httptest.NewRequest("POST", "/api/configcenter/namespaces/"+sharedID+"/publish", nil)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		nsHandler.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("shared publish: %d %s", rec.Code, rec.Body.String())
		}
	}

	// 应用侧：overlap=app-wins（与 shared 同 key 冲突）+ own-key；发布 v1
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"overlap","value":"app-wins"}`)
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"own-key","value":"v"}`)
	run("POST", "/api/applications/app-1/dynamic-configs/publish", "")

	// 未引用：快照无 shared key、无 sharedHash
	{
		rec := run("GET", "/api/applications/app-1/dynamic-configs/published", "")
		var pub struct {
			Published  bool              `json:"published"`
			Snapshot   map[string]string `json:"snapshot"`
			SharedHash string            `json:"sharedHash"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &pub)
		if !pub.Published || pub.SharedHash != "" {
			t.Fatalf("未引用不应有 sharedHash: %s", rec.Body.String())
		}
		if _, has := pub.Snapshot["skey"]; has {
			t.Fatalf("未引用快照不应含 shared key: %v", pub.Snapshot)
		}
	}

	// 建引用
	{
		rec := run("POST", "/api/applications/app-1/dynamic-configs/shared-refs", `{"sharedNsId":"`+sharedID+`"}`)
		if rec.Code != 201 {
			t.Fatalf("add ref: %d %s", rec.Code, rec.Body.String())
		}
	}

	// 引用后：三层 merge 生效
	{
		rec := run("GET", "/api/applications/app-1/dynamic-configs/published", "")
		var pub struct {
			Version    int               `json:"version"`
			Snapshot   map[string]string `json:"snapshot"`
			SharedHash string            `json:"sharedHash"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &pub); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if pub.Snapshot["skey"] != "shared-val" {
			t.Fatalf("shared key 应并入: %v", pub.Snapshot)
		}
		if pub.Snapshot["overlap"] != "app-wins" {
			t.Fatalf("应用自身 key 应胜出（逃生门）: %v", pub.Snapshot)
		}
		if pub.SharedHash == "" {
			t.Fatalf("引用后应有 sharedHash: %s", rec.Body.String())
		}
	}

	// 影响面反查
	refs, err := repo.ListNSRefUsers(ctx, sharedID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("ref users: %v %d", err, len(refs))
	}

	// 解除引用
	{
		list := run("GET", "/api/applications/app-1/dynamic-configs/shared-refs", "")
		var refs struct{ Data []configcenter.NSRef }
		_ = json.Unmarshal(list.Body.Bytes(), &refs)
		if len(refs.Data) != 1 {
			t.Fatalf("refs list: %s", list.Body.String())
		}
		rec := run("DELETE", "/api/applications/app-1/dynamic-configs/shared-refs/"+refs.Data[0].ID, "")
		if rec.Code != 200 {
			t.Fatalf("delete ref: %d %s", rec.Code, rec.Body.String())
		}
	}
	{
		rec := run("GET", "/api/applications/app-1/dynamic-configs/published", "")
		if strings.Contains(rec.Body.String(), "skey") || strings.Contains(rec.Body.String(), "sharedHash") {
			t.Fatalf("解除后快照不应含 shared key/sharedHash: %s", rec.Body.String())
		}
	}

	// 越权/非法：自引/非 shared 拒；不存在 404 语义；重复引用 409 语义
	appNSID := mustNSID(t, repo, ctx, "app-1")
	if _, err := repo.AddNSRef(ctx, appNSID, appNSID); !errors.Is(err, configcenter.ErrRefNotShared) {
		t.Fatalf("自引应 ErrRefNotShared: %v", err)
	}
	if _, err := repo.AddNSRef(ctx, appNSID, "ns-nope"); !errors.Is(err, configcenter.ErrNamespaceNotFound) {
		t.Fatalf("不存在应 NotFound: %v", err)
	}
	if _, err := repo.AddNSRef(ctx, appNSID, sharedID); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if _, err := repo.AddNSRef(ctx, appNSID, sharedID); !errors.Is(err, configcenter.ErrRefExists) {
		t.Fatalf("重复应 ErrRefExists: %v", err)
	}
}

// TestPublishedSharedSnapshotField 锁住发现响应 sharedSnapshot 字段（前端 diff
// 依赖它排除 shared 来源 key，防误显「发布后将移除」）。按应用名发现端点同款三层 merge。
func TestPublishedSharedSnapshotField(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// shared ns + 发布
	nsHandler := configcenter.NewHandler(repo)
	nsHandler.Authorize = func(r *http.Request, perm string) bool { return true }
	req := httptest.NewRequest("POST", "/api/configcenter/namespaces", strings.NewReader(`{"name":"common"}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	nsHandler.ServeHTTP(rec, req)
	var created struct{ Data configcenter.Namespace }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	req = httptest.NewRequest("POST", "/api/configcenter/namespaces/"+created.Data.ID+"/items", strings.NewReader(`{"key":"sk","value":"sv"}`))
	req = req.WithContext(ctx)
	nsHandler.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest("POST", "/api/configcenter/namespaces/"+created.Data.ID+"/publish", nil)
	req = req.WithContext(ctx)
	nsHandler.ServeHTTP(httptest.NewRecorder(), req)

	// 应用 + 引用
	run := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, path, rd)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"k","value":"v"}`)
	run("POST", "/api/applications/app-1/dynamic-configs/publish", "")
	run("POST", "/api/applications/app-1/dynamic-configs/shared-refs", `{"sharedNsId":"`+created.Data.ID+`"}`)

	// 应用维度端点：sharedSnapshot 含 sk 且 snapshot 含 sk（merge 后）
	{
		rec := run("GET", "/api/applications/app-1/dynamic-configs/published", "")
		var pub struct {
			Snapshot       map[string]string `json:"snapshot"`
			SharedSnapshot map[string]string `json:"sharedSnapshot"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &pub); err != nil {
			t.Fatal(err)
		}
		if pub.Snapshot["sk"] != "sv" || pub.SharedSnapshot["sk"] != "sv" {
			t.Fatalf("sharedSnapshot 字段缺失或不含 shared key: %s", rec.Body.String())
		}
		if _, polluted := pub.SharedSnapshot["k"]; polluted {
			t.Fatalf("sharedSnapshot 不应含应用自身 key: %v", pub.SharedSnapshot)
		}
	}

	// 按应用名发现端点（Handler.serveAppPublished）：三层 merge 同款生效。
	{
		req := httptest.NewRequest("GET", "/api/configcenter/apps/Nope/published", nil)
		_ = req
	}
}

// TestPublishNoChangesRejected 锁住空发布闸门：draft 与 active 完全一致时 publish 409，
// 改值后放行（防无变更重复发布虚涨版本号——应用维度与 shared ns 维度同款语义）。
func TestPublishNoChangesRejected(t *testing.T) {
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	run := func(method, path, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, path, rd)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"k","value":"v"}`)
	if rec := run("POST", "/api/applications/app-1/dynamic-configs/publish", ""); rec.Code != 201 {
		t.Fatalf("首次发布应 201: %d %s", rec.Code, rec.Body.String())
	}
	// 无变更再发布 → 409
	if rec := run("POST", "/api/applications/app-1/dynamic-configs/publish", ""); rec.Code != 409 {
		t.Fatalf("空发布应 409: %d %s", rec.Code, rec.Body.String())
	}
	// 版本仍 1
	pub := run("GET", "/api/applications/app-1/dynamic-configs/published", "")
	if !strings.Contains(pub.Body.String(), `"version":1`) {
		t.Fatalf("版本应仍 1: %s", pub.Body.String())
	}
	// 改值后放行 → v2
	run("POST", "/api/applications/app-1/dynamic-configs", `{"key":"k","value":"v2"}`)
	if rec := run("POST", "/api/applications/app-1/dynamic-configs/publish", ""); rec.Code != 201 {
		t.Fatalf("变更后发布应 201: %d %s", rec.Code, rec.Body.String())
	}
	if pub := run("GET", "/api/applications/app-1/dynamic-configs/published", ""); !strings.Contains(pub.Body.String(), `"version":2`) {
		t.Fatalf("应 v2: %s", pub.Body.String())
	}
}
