package configcenter_test

import (
	"context"
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
