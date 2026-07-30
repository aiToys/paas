package appconfig_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/appconfig"
	appcfgmemory "github.com/aitoys/paas/internal/appconfig/memory"
	envmemory "github.com/aitoys/paas/internal/environment/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newHandler 构造集成 handler：真实 appconfig/env 内存仓储 + stub 鉴权。
// prodWrite=true 模拟 admin，false 模拟 developer（生产只读）。
func newHandler(prodWrite bool) *appconfig.Handler {
	h := appconfig.NewHandler(appcfgmemory.NewStore(), appconfig.WithEnvResolver(envmemory.NewStore()))
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == appconfig.PermProdWrite {
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

// TestHandlerListSecretMasked 验证 API 返回 Secret 掩码。
func TestHandlerListSecretMasked(t *testing.T) {
	h := newHandler(true)
	r := req(acmeCtx(), "GET", "/api/applications/app-cs/configs?envId=env-acme-test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", w.Code)
	}
	var out struct {
		Data []appconfig.ConfigItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	for _, c := range out.Data {
		if c.Type == appconfig.TypeSecret && c.Value != appconfig.SecretMask {
			t.Fatalf("secret 值应掩码，got %q", c.Value)
		}
	}
}

// TestHandlerUpsert 验证新增配置。
func TestHandlerUpsert(t *testing.T) {
	h := newHandler(true)
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/configs", appconfig.ConfigItem{
		EnvID: "env-acme-test", Key: "NEW", Value: "v", Type: appconfig.TypeEnv,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("新增应 201，got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandlerProdGuard 验证生产权限守卫。
func TestHandlerProdGuard(t *testing.T) {
	hDev := newHandler(false)

	// dev 改生产 -> 403
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/configs", appconfig.ConfigItem{
		EnvID: "env-acme-prod-bj", Key: "K", Value: "v", Type: appconfig.TypeEnv,
	})
	w := httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 改生产配置应 403，got %d", w.Code)
	}

	// dev 改测试 -> 201
	r = req(acmeCtx(), "POST", "/api/applications/app-cs/configs", appconfig.ConfigItem{
		EnvID: "env-acme-test", Key: "K", Value: "v", Type: appconfig.TypeEnv,
	})
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("dev 改测试配置应 201，got %d", w.Code)
	}

	// admin 改生产 -> 201
	hAdmin := newHandler(true)
	r = req(acmeCtx(), "POST", "/api/applications/app-cs/configs", appconfig.ConfigItem{
		EnvID: "env-acme-prod-bj", Key: "K", Value: "v", Type: appconfig.TypeEnv,
	})
	w = httptest.NewRecorder()
	hAdmin.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin 改生产配置应 201，got %d", w.Code)
	}
}

// TestHandlerDeleteProdGuard 验证删除的生产权限守卫。
func TestHandlerDeleteProdGuard(t *testing.T) {
	// 共享 store：admin 建的配置 dev 可见，验证权限拦截（非 404）
	store := appcfgmemory.NewStore()
	envRes := envmemory.NewStore()
	mkAuth := func(prodWrite bool) func(*http.Request, string) bool {
		return func(r *http.Request, perm string) bool {
			if perm == appconfig.PermProdWrite {
				return prodWrite
			}
			return true
		}
	}
	hAdmin := appconfig.NewHandler(store, appconfig.WithEnvResolver(envRes))
	hAdmin.Authorize = mkAuth(true)
	hDev := appconfig.NewHandler(store, appconfig.WithEnvResolver(envRes))
	hDev.Authorize = mkAuth(false)

	// admin 建生产配置
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/configs", appconfig.ConfigItem{
		EnvID: "env-acme-prod-bj", Key: "PROD_K", Value: "v", Type: appconfig.TypeEnv,
	})
	w := httptest.NewRecorder()
	hAdmin.ServeHTTP(w, r)
	var created appconfig.ConfigItem
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}

	// dev 删生产配置 -> 403（权限拦截，配置存在）
	r = req(acmeCtx(), "DELETE", "/api/applications/app-cs/configs/"+created.ID, nil)
	w = httptest.NewRecorder()
	hDev.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("dev 删生产配置应 403，got %d", w.Code)
	}
}

// TestHandlerDeleteCrossAppForbidden 锁住 C2 修复：DELETE 必须先确认 cfgID 归属路径中的 appID，
// 否则同租户下用 app-B 路径可删掉 app-A 的配置（跨应用越权）。
// 回归点：serveItem 的 belongs 校验若被误删，app-rec 路径能删掉 app-cs 的配置。
func TestHandlerDeleteCrossAppForbidden(t *testing.T) {
	h := newHandler(true)
	// 在 app-cs 下建测试环境配置，拿到 cfgID。
	r := req(acmeCtx(), "POST", "/api/applications/app-cs/configs", appconfig.ConfigItem{
		EnvID: "env-acme-test", Key: "CROSS_APP", Value: "v", Type: appconfig.TypeEnv,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("建配置应 201，got %d: %s", w.Code, w.Body.String())
	}
	var created appconfig.ConfigItem
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	// 用 app-rec 路径删 app-cs 的配置：List(app-rec) 不含该 cfgID → 404。
	r = req(acmeCtx(), "DELETE", "/api/applications/app-rec/configs/"+created.ID, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨应用删配置应 404，got %d（越权删除成功=回归）", w.Code)
	}
}
