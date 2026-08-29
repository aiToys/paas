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
