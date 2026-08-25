package memory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/pkg/tenant"
)

func context2() context.Context { return context.Background() }

// memberHandlerEnv 组装 MemberHandler 测试环境（内存 store + 可控身份/权限注入）。
type memberHandlerEnv struct {
	apps    *Store
	members *MemberStore
	h       *application.MemberHandler
	admin   bool // 模拟租户管理员身份
}

func newMemberHandlerEnv(t *testing.T) *memberHandlerEnv {
	t.Helper()
	apps := NewStore()
	if err := apps.Create(tenant.WithTenant(context2(), "t-acme"), application.Application{ID: "app-x", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	members := NewMemberStore()
	h := application.NewMemberHandler(members, apps)
	h.Authorize = func(r *http.Request, perm string) bool { return true } // 租户级权限恒过（聚焦应用级）
	h.Guard = &application.AppGuard{
		Apps:     apps,
		Members:  members,
		IsAdmin:  func(r *http.Request) bool { return r.Header.Get("X-Admin") == "1" },
		UserIDFn: func(context.Context) string { return "" },
	}
	return &memberHandlerEnv{apps: apps, members: members, h: h}
}

// 提权链回归：非受限应用，非管理员（有 application:write）不可加成员/开受限——
// 防自封 owner → 开 restrict → 锁死他人。
func TestMemberHandlerPrivilegeEscalationBlocked(t *testing.T) {
	e := newMemberHandlerEnv(t)
	// 非管理员 POST members -> 403
	r := httptest.NewRequest(http.MethodPost, "/api/applications/app-x/members", strings.NewReader(`{"userId":"u1","role":"app-owner"}`))
	r = r.WithContext(tenant.WithTenant(r.Context(), "t-acme"))
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("非受限应用非管理员加成员 = %d, want 403", w.Code)
	}
}

// 管理员可在非受限应用加成员（初始 owner 授予路径）。
func TestMemberHandlerAdminGrantsOnUnrestricted(t *testing.T) {
	e := newMemberHandlerEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/applications/app-x/members", strings.NewReader(`{"userId":"u1","role":"app-owner"}`))
	r.Header.Set("X-Admin", "1")
	r = r.WithContext(tenant.WithTenant(r.Context(), "t-acme"))
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Errorf("管理员加成员 = %d, want 201", w.Code)
	}
}

// 受限应用：owner 可管成员，developer 不可。
func TestMemberHandlerRestrictedOwnerOnly(t *testing.T) {
	e := newMemberHandlerEnv(t)
	ctx := tenant.WithTenant(context2(), "t-acme")
	// admin 开受限 + 授 owner
	r := httptest.NewRequest(http.MethodPost, "/api/applications/app-x/members", strings.NewReader(`{"userId":"u-own","role":"app-owner"}`))
	r.Header.Set("X-Admin", "1")
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup owner = %d", w.Code)
	}
	if err := e.apps.SetRestricted(ctx, "app-x", true); err != nil {
		t.Fatal(err)
	}
	// owner 身份（UserIDFn 返回 u-own）加 developer 成员 -> 允许
	e.h.Guard.UserIDFn = func(context.Context) string { return "u-own" }
	r2 := httptest.NewRequest(http.MethodPost, "/api/applications/app-x/members", strings.NewReader(`{"userId":"u-dev","role":"app-developer"}`))
	r2 = r2.WithContext(ctx)
	w2 := httptest.NewRecorder()
	e.h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusCreated {
		t.Errorf("owner 加成员 = %d, want 201", w2.Code)
	}
	// developer 身份加成员 -> 403
	e.h.Guard.UserIDFn = func(context.Context) string { return "u-dev" }
	r3 := httptest.NewRequest(http.MethodPost, "/api/applications/app-x/members", strings.NewReader(`{"userId":"u2","role":"app-viewer"}`))
	r3 = r3.WithContext(ctx)
	w3 := httptest.NewRecorder()
	e.h.ServeHTTP(w3, r3)
	if w3.Code != http.StatusForbidden {
		t.Errorf("developer 管成员 = %d, want 403", w3.Code)
	}
}
