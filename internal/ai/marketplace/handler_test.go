package marketplace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/pkg/tenant"
)

// handler 层路由分发 + 权限 + 所有权测试（fakeRepo 在 marketplace_test.go）。

func newTestHandler() (*Handler, *fakeRepo) {
	repo := &fakeRepo{}
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	return h, repo
}

func TestRouteDispatch(t *testing.T) {
	h, repo := newTestHandler()
	ctx := tenant.WithTenant(context.Background(), "t-a")
	item, _ := repo.Create(ctx, Item{EntityType: EntitySkill, Name: "x", Snapshot: []byte(`{}`), PublisherTenant: "t-a"})

	cases := []struct {
		method, path string
		wantStatus   int
	}{
		{"GET", "/api/marketplace", 200},
		{"POST", "/api/marketplace", 400}, // 无效 body
		{"GET", "/api/marketplace/published", 200},
		{"GET", "/api/marketplace/" + item.ID, 200},
		{"GET", "/api/marketplace/不存在", 404},
		{"POST", "/api/marketplace/" + item.ID + "/install", 200}, // forker 未注册 → 500？不——注册表空返 500
		{"PUT", "/api/marketplace/" + item.ID, 405},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}")).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		// install 特例：forker 未注册时 500（internal error）——本测试只验证分发不 panic
		if c.path == "/api/marketplace/"+item.ID+"/install" {
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("%s %s: expect 500 (no forker), got %d", c.method, c.path, rec.Code)
			}
			continue
		}
		if rec.Code != c.wantStatus {
			t.Fatalf("%s %s: expect %d, got %d body=%s", c.method, c.path, c.wantStatus, rec.Code, rec.Body.String())
		}
	}
}

func TestDeleteOwnership(t *testing.T) {
	h, repo := newTestHandler()
	ctxA := tenant.WithTenant(context.Background(), "t-a")
	item, _ := repo.Create(ctxA, Item{EntityType: EntitySkill, Name: "x", Snapshot: []byte(`{}`), PublisherTenant: "t-a"})

	// 非发布者（t-b）下架 → 403
	req := httptest.NewRequest("DELETE", "/api/marketplace/"+item.ID, nil).
		WithContext(tenant.WithTenant(context.Background(), "t-b"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非发布者下架应 403, got %d", rec.Code)
	}

	// 发布者下架 → 200
	req = httptest.NewRequest("DELETE", "/api/marketplace/"+item.ID, nil).WithContext(ctxA)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("发布者下架应 200, got %d", rec.Code)
	}

	// super_admin 可下架他人条目
	item2, _ := repo.Create(ctxA, Item{EntityType: EntitySkill, Name: "y", Snapshot: []byte(`{}`), PublisherTenant: "t-a"})
	h.IsAdmin = func(r *http.Request) bool { return true }
	req = httptest.NewRequest("DELETE", "/api/marketplace/"+item2.ID, nil).
		WithContext(tenant.WithTenant(context.Background(), "t-admin"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin 下架应 200, got %d", rec.Code)
	}
}

func TestPublishRequiresCategory(t *testing.T) {
	h, repo := newTestHandler()
	repos := newRepos()
	h.RegisterForker(EntitySkill, skillForker{repos: repos})
	ctx := tenant.WithTenant(context.Background(), "t-a")
	sk, _ := repos.Skills.Create(ctx, skill.Skill{Name: "无分类", Instructions: "x"})

	req := httptest.NewRequest("POST", "/api/marketplace", strings.NewReader(
		`{"entityType":"skill","entityId":"`+sk.ID+`"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无分类发布应 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	_ = repo
}
