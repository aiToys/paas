package devops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops"
	devopsmemory "github.com/aitoys/paas/internal/devops/memory"
	envmemory "github.com/aitoys/paas/internal/environment/memory"
	"github.com/aitoys/paas/internal/workload"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newHandler 构造集成测试 handler：真实 devops/workload/env 内存仓储 + stub 鉴权。
// prodWrite=true 模拟 admin（持 prod:write），false 模拟 developer（生产只读）。
func newHandler(prodWrite bool) *devops.Handler {
	wl := wlmemory.NewStore()
	env := envmemory.NewStore()
	s := devopsmemory.NewStore(wl)
	h := devops.NewHandler(s, s, s, s, devops.WithEnvResolver(env), devops.WithEnvPromoter(env))
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == devops.PermProdWrite {
			return prodWrite
		}
		return true
	}
	return h
}

// devopsFixture 封装带演示数据的 handler + 自建的镜像/发布（构建产出的 ID 随机，需动态拿）。
type devopsFixture struct {
	h         *devops.Handler
	acmeImg   devops.Image
	globexImg devops.Image
	globexRel devops.Release
}

// newFixture 构造带 devops 演示数据的 handler：acme/globex 各建仓库 + 触发构建产出镜像 +
// globex 发布到生产环境（供发布/回滚 prod guard 测试）。
// 去假数据后 NewStore 不再 seed，且 Image 无 Create 方法，黑盒测试只能经 CreateBuildRun
// 异步产出（mock CI runner 约 1s 完成）。
func newFixture(prodWrite bool) devopsFixture {
	wl := wlmemory.NewStore()
	env := envmemory.NewStore()
	s := devopsmemory.NewStore(wl)
	h := devops.NewHandler(s, s, s, s, devops.WithEnvResolver(env), devops.WithEnvPromoter(env))
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == devops.PermProdWrite {
			return prodWrite
		}
		return true
	}
	acme := tenant.WithTenant(context.Background(), "t-acme")
	globex := tenant.WithTenant(context.Background(), "t-globex")
	// 仓库（指定固定 ID，便于测试按 ID 触发构建）
	if err := s.CreateRepo(acme, devops.CodeRepo{ID: "repo-acme-cs", AppID: "app-cs", GitURL: "https://github.com/acme/cs.git", Branch: "main"}); err != nil {
		panic(err)
	}
	if err := s.CreateRepo(globex, devops.CodeRepo{ID: "repo-globex-agent", AppID: "app-agent", GitURL: "https://github.com/globex/agent.git", Branch: "main"}); err != nil {
		panic(err)
	}
	// 触发构建产出镜像（mock CI runner 异步，并行触发后统一等待）
	if _, err := s.CreateBuildRun(acme, devops.BuildRun{AppID: "app-cs", RepoID: "repo-acme-cs"}); err != nil {
		panic(err)
	}
	if _, err := s.CreateBuildRun(globex, devops.BuildRun{AppID: "app-agent", RepoID: "repo-globex-agent"}); err != nil {
		panic(err)
	}
	time.Sleep(1300 * time.Millisecond)
	acmeImgs, _ := s.ListImages(acme, "")
	globexImgs, _ := s.ListImages(globex, "")
	// globex 发布到生产环境（env-globex-prod），供回滚 prod guard 测试（store 层不校验 prod:write）
	globexRel, _ := s.CreateRelease(globex, devops.ReleaseInput{
		AppID: "app-agent", EnvID: "env-globex-prod", ImageID: globexImgs[0].ID,
	})
	return devopsFixture{h: h, acmeImg: acmeImgs[0], globexImg: globexImgs[0], globexRel: globexRel}
}

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

func req(ctx context.Context, method, path string, body interface{}) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	return req.WithContext(ctx)
}

func decodeList(t *testing.T, b []byte) []map[string]interface{} {
	t.Helper()
	var out struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("解码列表失败: %v, body: %s", err, b)
	}
	return out.Data
}

func TestHandlerRepoCRUD(t *testing.T) {
	h := newHandler(true)

	// 绑定仓库
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/repositories", map[string]string{
		"gitUrl": "https://github.com/acme/new.git", "branch": "main",
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("绑定仓库应 201，got %d: %s", w.Code, w.Body.String())
	}

	// 列表
	r = req(acmeCtx(), "GET", "/api/applications/app-cs/repositories", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("仓库列表应 200，got %d", w.Code)
	}
	if len(decodeList(t, w.Body.Bytes())) < 1 {
		t.Fatal("应至少 1 个仓库")
	}
}

func TestHandlerImageList(t *testing.T) {
	f := newFixture(true)
	r := req(acmeCtx(), "GET", "/api/applications/app-cs/images", nil)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("镜像列表应 200，got %d", w.Code)
	}
	list := decodeList(t, w.Body.Bytes())
	if len(list) != 1 {
		t.Fatalf("acme 应有 1 个镜像，got %d", len(list))
	}
}

func TestHandlerBuildTrigger(t *testing.T) {
	f := newFixture(true)
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/buildruns", map[string]string{
		"repoId": "repo-acme-cs",
	})
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("触发构建应 201，got %d: %s", w.Code, w.Body.String())
	}

	// 列表可见
	r = req(acmeCtx(), "GET", "/api/applications/app-cs/buildruns", nil)
	w = httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("构建列表应 200，got %d", w.Code)
	}
}

// TestHandlerReleaseProdGuard 验证发布的生产权限守卫：
// dev 发布到 prod 403、dev 发布到 test 201、admin 发布到 prod 201。
func TestHandlerReleaseProdGuard(t *testing.T) {
	fDev := newFixture(false)

	// dev -> prod 403
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-prod-bj", ImageID: fDev.acmeImg.ID,
	})
	w := httptest.NewRecorder()
	fDev.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 发布生产应 403，got %d", w.Code)
	}

	// dev -> test 201
	r = req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-test", ImageID: fDev.acmeImg.ID,
	})
	w = httptest.NewRecorder()
	fDev.h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("dev 发布测试应 201，got %d: %s", w.Code, w.Body.String())
	}

	// admin -> prod 201
	fAdmin := newFixture(true)
	r = req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-prod-bj", ImageID: fAdmin.acmeImg.ID,
	})
	w = httptest.NewRecorder()
	fAdmin.h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin 发布生产应 201，got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandlerRollbackProdGuard 验证回滚的生产权限守卫：dev 回滚 prod 发布 403。
func TestHandlerRollbackProdGuard(t *testing.T) {
	fDev := newFixture(false)
	// globexRel 属 globex，目标 env-globex-prod（生产）
	r := req(globexCtx(), "POST", "/api/releases/"+fDev.globexRel.ID+"/rollback", nil)
	w := httptest.NewRecorder()
	fDev.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 回滚生产应 403，got %d", w.Code)
	}
}

// decodeRelease 解包 {data:Release} 响应（WriteData 契约）。
func decodeRelease(t *testing.T, b []byte) devops.Release {
	t.Helper()
	var wrap struct {
		Data devops.Release `json:"data"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		t.Fatalf("解码发布响应失败: %v body=%s", err, b)
	}
	return wrap.Data
}

// createReleaseOnTest 在 acme test 环境发布，返回 release（供 promote 测试用）。
func createReleaseOnTest(t *testing.T, f devopsFixture) devops.Release {
	t.Helper()
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-test", ImageID: f.acmeImg.ID,
	})
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("建测试发布应 201，got %d: %s", w.Code, w.Body.String())
	}
	return decodeRelease(t, w.Body.Bytes())
}

// TestHandlerPromote 验证发布流水线逐级提升：test release → promote → prod 新 release（PromotedFrom 串链）。
func TestHandlerPromote(t *testing.T) {
	f := newFixture(true)
	relTest := createReleaseOnTest(t, f)

	r := req(acmeCtx(), "POST", "/api/releases/"+relTest.ID+"/promote", nil)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin promote 应 200，got %d: %s", w.Code, w.Body.String())
	}
	promoted := decodeRelease(t, w.Body.Bytes())
	if promoted.EnvID != "env-acme-prod-bj" {
		t.Fatalf("promote 目标应 env-acme-prod-bj（阶序 20 取 id 最小），got %s", promoted.EnvID)
	}
	if promoted.ImageID != relTest.ImageID {
		t.Fatalf("promote 应复用源镜像，got %s want %s", promoted.ImageID, relTest.ImageID)
	}
	if promoted.PromotedFrom != relTest.ID {
		t.Fatalf("promotedFrom 应指向源 release，got %s want %s", promoted.PromotedFrom, relTest.ID)
	}
}

// TestHandlerPromoteDevGuard 验证 promote 到 prod 受 prod:write 守卫：dev promote（目标 prod）403。
func TestHandlerPromoteDevGuard(t *testing.T) {
	f := newFixture(false)
	relTest := createReleaseOnTest(t, f)

	r := req(acmeCtx(), "POST", "/api/releases/"+relTest.ID+"/promote", nil)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev promote 到生产应 403，got %d", w.Code)
	}
}

// TestHandlerPromoteNoTarget 验证最高阶环境 promote 返 400（无晋升目标）。
func TestHandlerPromoteNoTarget(t *testing.T) {
	f := newFixture(true)
	// globexRel 已在 env-globex-prod（最高阶），promote 无目标
	r := req(globexCtx(), "POST", "/api/releases/"+f.globexRel.ID+"/promote", nil)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("最高阶环境 promote 应 400，got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlerCrossAppList(t *testing.T) {
	f := newFixture(true)
	// acme 跨应用构建列表
	r := req(acmeCtx(), "GET", "/api/buildruns", nil)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("跨应用构建列表应 200，got %d", w.Code)
	}
	acmeBuilds := decodeList(t, w.Body.Bytes())
	if len(acmeBuilds) == 0 {
		t.Fatal("acme 应有构建记录")
	}
	// 镜像 / 发布跨应用列表
	for _, p := range []string{"/api/images", "/api/releases"} {
		rr := req(acmeCtx(), "GET", p, nil)
		ww := httptest.NewRecorder()
		f.h.ServeHTTP(ww, rr)
		if ww.Code != http.StatusOK {
			t.Fatalf("%s 应 200，got %d", p, ww.Code)
		}
	}
	// 租户隔离：globex 跨应用列表不应含 acme 数据
	r2 := req(globexCtx(), "GET", "/api/buildruns", nil)
	w2 := httptest.NewRecorder()
	f.h.ServeHTTP(w2, r2)
	for _, b := range decodeList(t, w2.Body.Bytes()) {
		if b["tenantId"] == "t-acme" {
			t.Fatalf("globex 不应见到 acme 构建: %+v", b)
		}
	}
}

func TestHandlerTenantIsolation(t *testing.T) {
	f := newFixture(true)
	// globex 访问 acme 镜像 -> 404（跨租户隔离，非"不存在"）
	r := req(globexCtx(), "GET", "/api/images/"+f.acmeImg.ID, nil)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户访问应 404，got %d", w.Code)
	}
	// acme 访问 globex 应用仓库列表应为空（app-agent 属 globex，acme 无数据）
	r = req(acmeCtx(), "GET", "/api/applications/app-agent/repositories", nil)
	w = httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200")
	}
	if len(decodeList(t, w.Body.Bytes())) != 0 {
		t.Fatal("acme 不应见到 globex 应用的仓库")
	}
}

// TestHandlerBuildLogsStream 验证 /api/buildruns/{id}/logs/stream：
// 终态返全量日志 + streamer=nil 降级 + 跨租户 404 不泄漏。
func TestHandlerBuildLogsStream(t *testing.T) {
	wl := wlmemory.NewStore()
	env := envmemory.NewStore()
	s := devopsmemory.NewStore(wl)
	h := devops.NewHandler(s, s, s, s, devops.WithEnvResolver(env), devops.WithEnvPromoter(env))
	h.Authorize = func(r *http.Request, perm string) bool { return true }

	// 建一个 success 终态 BuildRun（含 Log）
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if err := s.CreateRepo(ctx, devops.CodeRepo{
		ID: "repo-1", TenantID: "t-acme", AppID: "app-cs",
		GitURL: "https://github.com/x/y.git", Branch: "main",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	br, err := s.CreateBuildRun(ctx, devops.BuildRun{
		AppID: "app-cs", RepoID: "repo-1", TenantID: "t-acme",
		Branch: "main", Status: devops.BuildSuccess, Log: "Step 1: build\nStep 2: push done",
	})
	if err != nil {
		t.Fatalf("CreateBuildRun: %v", err)
	}
	// CreateBuildRun 异步流转（pending→running→success），等终态
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.GetBuildRun(ctx, br.ID)
		if got.Status == devops.BuildSuccess || got.Status == devops.BuildFailed {
			br = got
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 终态：返全量 Log + end 事件
	req := httptest.NewRequest(http.MethodGet, "/api/buildruns/"+br.ID+"/logs/stream", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，got %d", rec.Code)
	}
	body := rec.Body.String()
	// 终态应返 BuildRun.Log 全量（mock builder 日志逐行 SSE）+ end 事件
	if !strings.Contains(body, "[mock]") || !strings.Contains(body, "event: end") {
		t.Fatalf("终态应返全量日志 + end 事件，got %s", body)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type 应 text/event-stream，got %s", rec.Header().Get("Content-Type"))
	}

	// 跨租户：globex 访问 acme 的 BuildRun → GetBuildRun 404（不泄漏）
	ctxGlobex := tenant.WithTenant(context.Background(), "t-globex")
	req = httptest.NewRequest(http.MethodGet, "/api/buildruns/"+br.ID+"/logs/stream", nil)
	req = req.WithContext(ctxGlobex)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户期望 404，got %d", rec.Code)
	}
}

// TestHandlerReleaseMultiServiceGuard 验证多服务发布防护：app 已部署多服务
// （Workload.Service 非空 ≥1）时不传 service 的发布被 400 拒（防基线查找命中
// ID 最小的 Workload 反复覆盖，多服务镜像互相踩——paas-shop 实测踩坑）；
// 传 service 正常发布；未注入 lister 时跳过校验（向后兼容）。
func TestHandlerReleaseMultiServiceGuard(t *testing.T) {
	// 构造带 svcLister 的 handler：app-cs 在 test 环境预置一个带 Service 的 Workload。
	acme := tenant.WithTenant(context.Background(), "t-acme")
	wl := wlmemory.NewStore()
	if err := wl.Create(acme, workload.Workload{
		ID: "wl-test-product", AppID: "app-cs", EnvID: "env-acme-test", Service: "product",
		Type: workload.TypeService, Name: "app-cs-product-svc", Replicas: 1,
		Image: "nginx:alpine",
	}); err != nil {
		t.Fatal(err)
	}
	env := envmemory.NewStore()
	s := devopsmemory.NewStore(wl)
	h := devops.NewHandler(s, s, s, s,
		devops.WithEnvResolver(env), devops.WithEnvPromoter(env),
		devops.WithWorkloadServiceLister(stubSvcLister{svcs: []string{"product"}}),
	)
	h.Authorize = func(*http.Request, string) bool { return true }

	// 构建产出一个可用镜像（先建仓库，CreateBuildRun 校验 repo 归属）
	if err := s.CreateRepo(acme, devops.CodeRepo{ID: "repo-acme-cs", AppID: "app-cs", GitURL: "https://github.com/acme/cs.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBuildRun(acme, devops.BuildRun{AppID: "app-cs", RepoID: "repo-acme-cs", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var imgID string
	for time.Now().Before(deadline) {
		imgs, _ := s.ListImages(acme, "app-cs")
		if len(imgs) > 0 {
			imgID = imgs[0].ID
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if imgID == "" {
		t.Fatal("mock 构建未产出镜像")
	}

	// 不传 service -> 400（多服务形态）
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-test", ImageID: imgID,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "service") {
		t.Fatalf("多服务不传 service 应 400 且提示 service，got %d: %s", w.Code, w.Body.String())
	}

	// 传 service -> 201
	r = req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-test", ImageID: imgID, Service: "product",
	})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("传 service 应 201，got %d: %s", w.Code, w.Body.String())
	}

	// 未注入 lister（默认 newHandler）-> 单服务路径不受影响，400 校验不触发。
	h2 := newHandler(true)
	_ = h2 // 行为已由既有 TestHandlerReleaseProdGuard 覆盖（无 lister 跳过校验）
}

// stubSvcLister 固定返回 svcs。
type stubSvcLister struct{ svcs []string }

func (l stubSvcLister) DeployedServices(context.Context, string, string) ([]string, error) {
	return l.svcs, nil
}
