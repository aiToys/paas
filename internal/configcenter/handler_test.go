package configcenter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	ccmemory "github.com/aitoys/paas/internal/configcenter/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func newHandler() *configcenter.Handler {
	h := configcenter.NewHandler(ccmemory.NewStore())
	h.Authorize = func(r *http.Request, perm string) bool { return true }
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
	rq := httptest.NewRequest(method, path, r)
	return rq.WithContext(ctx)
}

// TestHandlerList 验证命名空间列表（租户隔离）。
func TestHandlerList(t *testing.T) {
	h := newHandler()
	r := req(acmeCtx(), "GET", "/api/configcenter/namespaces", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，got %d", w.Code)
	}
	var out struct {
		Data []configcenter.Namespace `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, n := range out.Data {
		if n.TenantID != "t-acme" {
			t.Fatalf("泄漏其他租户: %s", n.Name)
		}
	}
}

// TestHandlerPublishAndDiscover 验证发布 + 发现闭环。
func TestHandlerPublishAndDiscover(t *testing.T) {
	h := newHandler()
	// 改 draft
	r := req(acmeCtx(), "POST", "/api/configcenter/namespaces/ns-acme-app/items", configcenter.ConfigItem{
		Key: "rate.limit", Value: "300", Type: configcenter.TypeText,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("upsert 应 201，got %d", w.Code)
	}
	// 发布
	r = req(acmeCtx(), "POST", "/api/configcenter/namespaces/ns-acme-app/publish", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("发布应 201，got %d: %s", w.Code, w.Body.String())
	}
	var pub configcenter.Publish
	if err := json.Unmarshal(w.Body.Bytes(), &pub); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if pub.Version != 2 {
		t.Fatalf("版本应 2，got %d", pub.Version)
	}
	// 发现
	r = req(acmeCtx(), "GET", "/api/configcenter/namespaces/ns-acme-app/published", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var disc struct {
		Published bool              `json:"published"`
		Version   int               `json:"version"`
		Snapshot  map[string]string `json:"snapshot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &disc); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !disc.Published || disc.Version != 2 || disc.Snapshot["rate.limit"] != "300" {
		t.Fatalf("发现应返回 active v2 含新值，got %+v", disc)
	}
}

// TestHandlerRollback 验证回滚。
func TestHandlerRollback(t *testing.T) {
	h := newHandler()
	// 先发布 v2（v1 转 rolled-back）
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces/ns-acme-app/publish", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("发布 v2 应 201，got %d: %s", w.Code, w.Body.String())
	}
	// 回滚 v1
	r := req(acmeCtx(), "POST", "/api/configcenter/publishes/pub-acme-1/rollback", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("回滚应 200，got %d: %s", w.Code, w.Body.String())
	}
	// 发现应为 v1
	r = req(acmeCtx(), "GET", "/api/configcenter/namespaces/ns-acme-app/published", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var disc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &disc); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if disc.Version != 1 {
		t.Fatalf("回滚后 active 应 v1，got v%d", disc.Version)
	}
}

// TestHandlerCrossTenantHidden 验证跨租户访问不泄漏。
func TestHandlerCrossTenantHidden(t *testing.T) {
	h := newHandler()
	r := req(globexCtx(), "GET", "/api/configcenter/namespaces/ns-acme-app", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户应 404，got %d", w.Code)
	}
}

// TestHandlerItemDeleteCrossTenantHidden 锁住 I5 修复：跨租户/跨 ns DELETE item 必须先校验
// item 归属该 namespace（ListItems 按租户+nsID 过滤），不归属则 404，杜绝越权删除。
// 回归点：serveItemDelete 的 belongs 校验若被误删，globex 可删掉 acme 的 item。
func TestHandlerItemDeleteCrossTenantHidden(t *testing.T) {
	h := newHandler()
	// acme 在 ns-acme-app 下建一项，拿到 itemID。
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/configcenter/namespaces/ns-acme-app/items", configcenter.ConfigItem{
		Key: "del.target", Value: "1", Type: configcenter.TypeText,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("建 item 应 201，got %d: %s", w.Code, w.Body.String())
	}
	var created configcenter.ConfigItem
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析建 item 响应失败: %v", err)
	}
	// globex 尝试删 acme 的 item：ListItems(ns-acme-app) 在 globex ctx 下为空 → 404。
	r := req(globexCtx(), "DELETE", "/api/configcenter/namespaces/ns-acme-app/items/"+created.ID, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户删 item 应 404，got %d（越权删除成功=回归）", w.Code)
	}
}
