package dataservice_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
	dsmemory "github.com/aitoys/paas/internal/dataservice/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// stubResolver 按预设 envID -> 类型 映射。
type stubResolver struct {
	types map[string]string
}

func (s stubResolver) EnvType(_ context.Context, envID string) (string, error) {
	if t, ok := s.types[envID]; ok {
		return t, nil
	}
	return "", nil
}

func newReq(method, target, body, tid string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	ctx := tenant.WithTenant(r.Context(), tid)
	return r.WithContext(ctx)
}

func allowAll(*http.Request, string) bool { return true }

func decode(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("解码失败: %v (body=%s)", err, w.Body.String())
	}
}

// TestMeta 验证 KindMeta 端点返回 6 个 kind。
func TestMeta(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore())
	r := newReq(http.MethodGet, "/api/dataservices/meta", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("meta 应 200，got %d", w.Code)
	}
	var resp struct {
		Data []dataservice.KindMeta `json:"data"`
	}
	decode(t, w, &resp)
	if len(resp.Data) != 6 {
		t.Fatalf("应 6 个 kind，got %d", len(resp.Data))
	}
}

// TestListByKind 验证列表 + kind 过滤。
func TestListByKind(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore())
	h.Authorize = allowAll
	r := newReq(http.MethodGet, "/api/dataservices?kind=db", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp struct {
		Data []dataservice.DataService `json:"data"`
	}
	decode(t, w, &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("acme db 应 1 个，got %d", len(resp.Data))
	}
}

// TestCreateAndDelete 验证创建/删除闭环 + 默认 running。
func TestCreateAndDelete(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore(), dataservice.WithEnvResolver(stubResolver{
		types: map[string]string{"env-acme-test": "test"},
	}))
	h.Authorize = allowAll
	body := `{"kind":"db","name":"new-db","spec":{"engine":"postgres","version":"15","size_gb":"30"},"envId":"env-acme-test"}`
	r := newReq(http.MethodPost, "/api/dataservices", body, "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d body=%s", w.Code, w.Body.String())
	}
	var d dataservice.DataService
	decode(t, w, &d)
	if d.Status != dataservice.StatusRunning {
		t.Fatalf("应默认 running，got %s", d.Status)
	}

	// 删除
	r2 := newReq(http.MethodDelete, "/api/dataservices/"+d.ID, "", "t-acme")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("删除应 200，got %d", w2.Code)
	}
}

// TestProdCreateBlocked 验证 developer 在生产环境创建被 prod:write 拦截。
func TestProdCreateBlocked(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore(), dataservice.WithEnvResolver(stubResolver{
		types: map[string]string{"env-acme-prod-bj": "prod"},
	}))
	// 模拟 developer：有 write 但无 prod:write
	h.Authorize = func(_ *http.Request, perm string) bool { return perm != "prod:write" }
	body := `{"kind":"db","name":"prod-db","spec":{"engine":"postgres","version":"15","size_gb":"30"},"envId":"env-acme-prod-bj"}`
	r := newReq(http.MethodPost, "/api/dataservices", body, "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("生产创建应 403，got %d", w.Code)
	}
}

// TestProdDeleteBlocked 验证跨租户删除生产资源被拒（先 not found 或 prod 拦截）。
func TestProdDeleteAsAdmin(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore(), dataservice.WithEnvResolver(stubResolver{
		types: map[string]string{"env-acme-prod-bj": "prod"},
	}))
	h.Authorize = allowAll // admin 有 prod:write
	// ds-acme-mq 在生产环境 env-acme-prod-bj
	r := newReq(http.MethodDelete, "/api/dataservices/ds-acme-mq", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 删除生产应 200，got %d", w.Code)
	}
}

// TestTenantIsolation 验证跨租户不可见。
func TestTenantIsolation(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore())
	h.Authorize = allowAll
	r := newReq(http.MethodGet, "/api/dataservices", "", "t-globex")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp struct {
		Data []dataservice.DataService `json:"data"`
	}
	decode(t, w, &resp)
	for _, d := range resp.Data {
		if d.TenantID == "t-acme" {
			t.Fatal("globex 不应见到 acme 资源")
		}
	}
}
