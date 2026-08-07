package devops_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops"
	devopsmemory "github.com/aitoys/paas/internal/devops/memory"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeAudit 记录最后一次审计（action/resourceType/resourceID/tenantID）。
type fakeAudit struct {
	lastAction       string
	lastResourceType string
	lastResourceID   string
	lastTenantID     string
}

func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, resourceType, resourceID, detail string) error {
	a.lastAction = action
	a.lastResourceType = resourceType
	a.lastResourceID = resourceID
	a.lastTenantID = tid
	return nil
}

// adminFixture 构造 admin handler + 已 seed 的 devops 演示数据：
//   - acme：仓库 + 构建 + 镜像 + release1（基线 workload 创建）+ release2（指向新镜像，prev=image1）
//
// rollback release2 即可验证回滚链路。
type adminFixture struct {
	h         *devops.AdminHandler
	au        *fakeAudit
	acmeRel2  devops.Release
	acmeBuild devops.BuildRun
	acmeImg   devops.Image
}

func newAdminFixture(t *testing.T) adminFixture {
	t.Helper()
	wl := wlmemory.NewStore()
	s := devopsmemory.NewStore(wl)
	au := &fakeAudit{}
	h := devops.NewAdminHandler(s, s, s, devops.WithAdminAudit(au))

	acme := tenant.WithTenant(context.Background(), "t-acme")
	// 仓库
	if err := s.CreateRepo(acme, devops.CodeRepo{ID: "repo-acme-cs", AppID: "app-cs", GitURL: "https://github.com/acme/cs.git", Branch: "main"}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	// 触发两次构建产出两个镜像
	if err := s.CreateBuildRun(acme, devops.BuildRun{AppID: "app-cs", RepoID: "repo-acme-cs"}); err != nil {
		t.Fatalf("CreateBuildRun1: %v", err)
	}
	time.Sleep(1300 * time.Millisecond)
	imgs1, _ := s.ListImages(acme, "")
	if len(imgs1) == 0 {
		t.Fatalf("no images after first build")
	}
	if err := s.CreateBuildRun(acme, devops.BuildRun{AppID: "app-cs", RepoID: "repo-acme-cs"}); err != nil {
		t.Fatalf("CreateBuildRun2: %v", err)
	}
	time.Sleep(1300 * time.Millisecond)
	imgs2, _ := s.ListImages(acme, "")

	// release1 用 image1（建基线 workload，prev 为空）
	if _, err := s.CreateRelease(acme, devops.ReleaseInput{AppID: "app-cs", EnvID: "env-acme-test", ImageID: imgs1[0].ID}); err != nil {
		t.Fatalf("CreateRelease1: %v", err)
	}
	// release2 用 image2（基线 workload 已存在，prev=image1 的 ID）
	rel2, err := s.CreateRelease(acme, devops.ReleaseInput{AppID: "app-cs", EnvID: "env-acme-test", ImageID: imgs2[0].ID})
	if err != nil {
		t.Fatalf("CreateRelease2: %v", err)
	}

	builds, _ := s.ListBuildRuns(acme, "")
	return adminFixture{h: h, au: au, acmeRel2: rel2, acmeBuild: builds[0], acmeImg: imgs1[0]}
}

func TestAdminBuildRunDetail(t *testing.T) {
	f := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/buildruns/"+f.acmeBuild.ID, nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	if data["id"] != f.acmeBuild.ID {
		t.Fatalf("id=%v", data["id"])
	}
	// Log 字段必须在（构建日志，admin 可见）
	if _, ok := data["log"]; !ok {
		t.Fatalf("no log field in admin detail")
	}
}

func TestAdminBuildRunDetailNotFound(t *testing.T) {
	f := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/buildruns/nope", nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminImageDetail(t *testing.T) {
	f := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/images/"+f.acmeImg.ID, nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	if data["id"] != f.acmeImg.ID {
		t.Fatalf("id=%v", data["id"])
	}
	if data["digest"] != f.acmeImg.Digest {
		t.Fatalf("digest=%v want %s", data["digest"], f.acmeImg.Digest)
	}
}

func TestAdminReleaseDetail(t *testing.T) {
	f := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/releases/"+f.acmeRel2.ID, nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	if data["id"] != f.acmeRel2.ID {
		t.Fatalf("id=%v", data["id"])
	}
	if data["previousImageId"] != f.acmeImg.ID {
		t.Fatalf("previousImageId=%v want %s", data["previousImageId"], f.acmeImg.ID)
	}
}

// TestAdminReleaseRollback 验证回滚成功 + 记审计（admin:rollback，target_tenant=t-acme）。
// 注意：admin 路径绕过 prod:write（生产环境 release 也回滚），此处 release 在测试环境亦验证链路。
func TestAdminReleaseRollback(t *testing.T) {
	f := newAdminFixture(t)
	// 请求 ctx 故意带 t-globex（非资源所属租户）；handler 应以资源租户 t-acme ctx 落库 + 记审计。
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	req := httptest.NewRequest(http.MethodPost, "/api/admin/releases/"+f.acmeRel2.ID+"/rollback", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 审计校验
	if f.au.lastAction != "admin:rollback" {
		t.Fatalf("audit action=%s want admin:rollback", f.au.lastAction)
	}
	if f.au.lastResourceType != "release" {
		t.Fatalf("audit resourceType=%s want release", f.au.lastResourceType)
	}
	if f.au.lastResourceID != f.acmeRel2.ID {
		t.Fatalf("audit resourceId=%s want %s", f.au.lastResourceID, f.acmeRel2.ID)
	}
	if f.au.lastTenantID != "t-acme" {
		t.Fatalf("audit tenantId=%s want t-acme", f.au.lastTenantID)
	}
	// 响应：新建的回滚 release（IsRollback=true）
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	if data["isRollback"] != true {
		t.Fatalf("isRollback=%v", data["isRollback"])
	}
}

func TestAdminReleaseRollbackNotFound(t *testing.T) {
	f := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/releases/nope/rollback", nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 验证 admin handler 不依赖 ctx tenant（adminGuard 不注入租户，跨租户读全量）。
func TestAdminBuildRunDetailNoTenantCtx(t *testing.T) {
	f := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/buildruns/"+f.acmeBuild.ID, nil)
	req = req.WithContext(context.Background())
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 404 兜底：未知路径
func TestAdminNotFound(t *testing.T) {
	f := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/buildruns/x/y", nil)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
