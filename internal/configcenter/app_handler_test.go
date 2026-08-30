package configcenter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	ccmemory "github.com/aitoys/paas/internal/configcenter/memory"
	"encoding/json"

	"github.com/aitoys/paas/pkg/tenant"
)

// guardFunc 把 func 适配为 configcenter.GuardAdapter（测试用）。
type guardFunc func(r *http.Request, appID, action string) bool

func (f guardFunc) Allow(r *http.Request, appID, action string) bool { return f(r, appID, action) }

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
