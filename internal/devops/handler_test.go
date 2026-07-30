package devops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/devops"
	devopsmemory "github.com/aitoys/paas/internal/devops/memory"
	envmemory "github.com/aitoys/paas/internal/environment/memory"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newHandler 构造集成测试 handler：真实 devops/workload/env 内存仓储 + stub 鉴权。
// prodWrite=true 模拟 admin（持 prod:write），false 模拟 developer（生产只读）。
func newHandler(prodWrite bool) *devops.Handler {
	wl := wlmemory.NewStore()
	env := envmemory.NewStore()
	s := devopsmemory.NewStore(wl)
	h := devops.NewHandler(s, s, s, s, devops.WithEnvResolver(env))
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == devops.PermProdWrite {
			return prodWrite
		}
		return true
	}
	return h
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
	h := newHandler(true)
	r := req(acmeCtx(), "GET", "/api/applications/app-cs/images", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("镜像列表应 200，got %d", w.Code)
	}
	list := decodeList(t, w.Body.Bytes())
	if len(list) != 1 || list[0]["id"] != "img-acme-001" {
		t.Fatalf("acme 应有 1 个镜像 img-acme-001，got %+v", list)
	}
}

func TestHandlerBuildTrigger(t *testing.T) {
	h := newHandler(true)
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/buildruns", map[string]string{
		"repoId": "repo-acme-cs",
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("触发构建应 201，got %d: %s", w.Code, w.Body.String())
	}

	// 列表可见
	r = req(acmeCtx(), "GET", "/api/applications/app-cs/buildruns", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("构建列表应 200，got %d", w.Code)
	}
}

// TestHandlerReleaseProdGuard 验证发布的生产权限守卫：
// dev 发布到 prod 403、dev 发布到 test 201、admin 发布到 prod 201。
func TestHandlerReleaseProdGuard(t *testing.T) {
	hDev := newHandler(false)

	// dev -> prod 403
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-prod-bj", ImageID: "img-acme-001",
	})
	w := httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 发布生产应 403，got %d", w.Code)
	}

	// dev -> test 201
	r = req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-test", ImageID: "img-acme-001",
	})
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("dev 发布测试应 201，got %d: %s", w.Code, w.Body.String())
	}

	// admin -> prod 201
	hAdmin := newHandler(true)
	r = req(acmeCtx(), "POST", "/api/applications/app-cs/releases", devops.ReleaseInput{
		EnvID: "env-acme-prod-bj", ImageID: "img-acme-001",
	})
	w = httptest.NewRecorder()
	hAdmin.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin 发布生产应 201，got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandlerRollbackProdGuard 验证回滚的生产权限守卫：dev 回滚 prod 发布 403。
func TestHandlerRollbackProdGuard(t *testing.T) {
	hDev := newHandler(false)
	// rel-globex-001 属 globex，目标 env-globex-prod（生产）
	r := req(globexCtx(), "POST", "/api/releases/rel-globex-001/rollback", nil)
	w := httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 回滚生产应 403，got %d", w.Code)
	}
}

func TestHandlerCrossAppList(t *testing.T) {
	h := newHandler(true)
	// acme 跨应用构建列表
	r := req(acmeCtx(), "GET", "/api/buildruns", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
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
		h.ServeHTTP(ww, rr)
		if ww.Code != http.StatusOK {
			t.Fatalf("%s 应 200，got %d", p, ww.Code)
		}
	}
	// 租户隔离：globex 跨应用列表不应含 acme 数据
	r2 := req(globexCtx(), "GET", "/api/buildruns", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	for _, b := range decodeList(t, w2.Body.Bytes()) {
		if b["tenantId"] == "t-acme" {
			t.Fatalf("globex 不应见到 acme 构建: %+v", b)
		}
	}
}

func TestHandlerTenantIsolation(t *testing.T) {
	h := newHandler(true)
	// globex 访问 acme 镜像 -> 404
	r := req(globexCtx(), "GET", "/api/images/img-acme-001", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户访问应 404，got %d", w.Code)
	}
	// acme 访问 globex 仓库 -> 404
	r = req(acmeCtx(), "GET", "/api/applications/app-agent/repositories", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200")
	}
	// acme 查 globex 应用仓库列表应为空（app-agent 属 globex，acme 无数据）
	if len(decodeList(t, w.Body.Bytes())) != 0 {
		t.Fatal("acme 不应见到 globex 应用的仓库")
	}
}
