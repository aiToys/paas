package governance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	envmemory "github.com/aitoys/paas/internal/environment/memory"
	"github.com/aitoys/paas/internal/governance"
	govmemory "github.com/aitoys/paas/internal/governance/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newHandler 构造集成 handler：真实 governance/env 内存仓储 + stub 鉴权。
// prodWrite=true 模拟 admin，false 模拟 developer（生产只读）。
func newHandler(prodWrite bool) *governance.Handler {
	h := governance.NewHandler(govmemory.NewStore(), governance.WithEnvResolver(envmemory.NewStore()))
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == governance.PermProdWrite {
			return prodWrite
		}
		return true
	}
	return h
}

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func req(ctx context.Context, method, path string, body interface{}) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	return rq.WithContext(ctx)
}

// TestHandlerList 验证服务列表（租户隔离）。
func TestHandlerList(t *testing.T) {
	h := newHandler(true)
	r := req(acmeCtx(), "GET", "/api/services?envId=env-acme-test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", w.Code)
	}
	var out struct {
		Data []governance.Service `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, s := range out.Data {
		if s.TenantID != "t-acme" {
			t.Fatalf("泄漏其他租户服务: %s", s.Name)
		}
	}
}

// TestHandlerProdGuard 验证生产注册/注销权限守卫。
func TestHandlerProdGuard(t *testing.T) {
	hDev := newHandler(false)
	// dev 注册生产服务 -> 403
	r := req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "prod-new", EnvID: "env-acme-prod-bj", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w := httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 注册生产服务应 403，got %d", w.Code)
	}
	// dev 注册测试服务 -> 201
	r = req(acmeCtx(), "POST", "/api/services", governance.Service{
		Name: "test-new", EnvID: "env-acme-test", Protocol: governance.ProtocolHTTP, Port: 8080,
	})
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("dev 注册测试服务应 201，got %d", w.Code)
	}
	// admin 注销生产服务（svc-acme-rec 属 env-acme-prod-bj）-> dev 403 / admin 200
	r = req(acmeCtx(), "DELETE", "/api/services/svc-acme-rec", nil)
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 注销生产服务应 403，got %d", w.Code)
	}
	hAdmin := newHandler(true)
	r = req(acmeCtx(), "DELETE", "/api/services/svc-acme-rec", nil)
	w = httptest.NewRecorder()
	hAdmin.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 注销生产服务应 200，got %d", w.Code)
	}
}

// TestHandlerInstanceOps 验证实例注册/发现/心跳。
func TestHandlerInstanceOps(t *testing.T) {
	h := newHandler(true)
	// 注册实例（svc-acme-cs 属测试环境，dev 也能写）
	r := req(acmeCtx(), "POST", "/api/services/svc-acme-cs/instances", governance.Instance{
		Addr: "10.0.1.200:8080",
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("注册实例应 201，got %d: %s", w.Code, w.Body.String())
	}
	var created governance.Instance
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 服务详情应包含新实例
	r = req(acmeCtx(), "GET", "/api/services/svc-acme-cs", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var detail struct {
		Instances []governance.Instance `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	found := false
	for _, x := range detail.Instances {
		if x.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("详情应包含新注册实例")
	}
	// 心跳
	r = req(acmeCtx(), "PUT", "/api/instances/"+created.ID+"/heartbeat", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("心跳应 200，got %d", w.Code)
	}
}

// TestHandlerInstanceDeleteCrossService 验证实例归属校验。
func TestHandlerInstanceDeleteCrossService(t *testing.T) {
	h := newHandler(true)
	// 用 svc-acme-rec 的实例去 svc-acme-cs 下注销 -> not found
	r := req(acmeCtx(), "DELETE", "/api/services/svc-acme-cs/instances/inst-acme-rec-1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨服务注销应 404，got %d", w.Code)
	}
}
