package memory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/pkg/tenant"
)

// guardEnv 组装 guard 全链路测试环境（应用 + 成员 store + 可控身份请求）。
type guardEnv struct {
	apps    *Store
	members *MemberStore
	guard   *application.AppGuard
}

func newGuardEnv(t *testing.T) *guardEnv {
	t.Helper()
	apps := NewStore()
	if err := apps.Create(tenant.WithTenant(context.Background(), "t-acme"), application.Application{ID: "app-x", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	members := NewMemberStore()
	return &guardEnv{
		apps:    apps,
		members: members,
		guard: &application.AppGuard{
			Apps:     apps,
			Members:  members,
			IsAdmin:  func(r *http.Request) bool { return r.Header.Get("X-Admin") == "1" },
			UserIDFn: func(ctx context.Context) string { return "u-dev" },
		},
	}
}

func (e *guardEnv) req(t *testing.T) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/applications/app-x/releases", nil)
	return r.WithContext(tenant.WithTenant(r.Context(), "t-acme"))
}

// 非受限应用：一切照旧（向后兼容）。
func TestGuardUnrestrictedAppPasses(t *testing.T) {
	e := newGuardEnv(t)
	if !e.guard.Allow(e.req(t), "app-x", application.AppActionRelease) {
		t.Error("非受限应用应放行")
	}
}

// 受限应用 + 非成员：fail-closed 拒绝。
func TestGuardRestrictedRejectsNonMember(t *testing.T) {
	e := newGuardEnv(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if err := e.apps.SetRestricted(ctx, "app-x", true); err != nil {
		t.Fatal(err)
	}
	if e.guard.Allow(e.req(t), "app-x", application.AppActionWrite) {
		t.Error("受限应用非成员应拒绝")
	}
}

// 受限应用 + app-developer 成员：write 放行 / release 拒绝（测试人员不可发布核心场景）。
func TestGuardDeveloperCannotRelease(t *testing.T) {
	e := newGuardEnv(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if err := e.apps.SetRestricted(ctx, "app-x", true); err != nil {
		t.Fatal(err)
	}
	if err := e.members.AddMember(ctx, application.Member{AppID: "app-x", UserID: "u-dev", Role: application.AppRoleDeveloper}); err != nil {
		t.Fatal(err)
	}
	if !e.guard.Allow(e.req(t), "app-x", application.AppActionWrite) {
		t.Error("app-developer 应有 write 权限")
	}
	if e.guard.Allow(e.req(t), "app-x", application.AppActionRelease) {
		t.Error("app-developer 不应有 release 权限")
	}
}

// 受限应用 + 租户管理员：通行（租户信任边界）。
func TestGuardAdminPasses(t *testing.T) {
	e := newGuardEnv(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if err := e.apps.SetRestricted(ctx, "app-x", true); err != nil {
		t.Fatal(err)
	}
	r := e.req(t)
	r.Header.Set("X-Admin", "1")
	if !e.guard.Allow(r, "app-x", application.AppActionRelease) {
		t.Error("租户管理员应通行")
	}
}

// 成员 CRUD roundtrip + 跨租户隔离。
func TestMemberStoreRoundTrip(t *testing.T) {
	s := NewMemberStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if err := s.AddMember(ctx, application.Member{AppID: "app-x", UserID: "u1", Role: application.AppRoleMaintainer}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, application.Member{AppID: "app-x", UserID: "u1", Role: application.AppRoleOwner}); err != nil {
		t.Fatal(err) // 覆盖语义
	}
	m, err := s.GetMember(ctx, "app-x", "u1")
	if err != nil || m.Role != application.AppRoleOwner {
		t.Fatalf("GetMember=%v,%v want owner", m, err)
	}
	// 跨租户不可见
	if _, err := s.GetMember(tenant.WithTenant(context.Background(), "t-globex"), "app-x", "u1"); err == nil {
		t.Error("跨租户应 not found")
	}
	// 非法角色拒绝
	if err := s.AddMember(ctx, application.Member{AppID: "app-x", UserID: "u2", Role: "root"}); err == nil {
		t.Error("非法角色应拒绝")
	}
	// 删除
	if err := s.RemoveMember(ctx, "app-x", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMember(ctx, "app-x", "u1"); err == nil {
		t.Error("删除后应 not found")
	}
}
