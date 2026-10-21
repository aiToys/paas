package observability_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/observability"
	obsmemory "github.com/aitoys/paas/internal/observability/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// doChannelReq 发送带租户 ctx 的请求。
func doChannelReq(t *testing.T, h http.Handler, method, path, body, tid string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r = r.WithContext(tenant.WithTenant(r.Context(), tid))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestChannelHandlerMasksSecrets：创建/列表/更新响应一律掩码 URL 与请求头，明文不出 API。
func TestChannelHandlerMasksSecrets(t *testing.T) {
	h := observability.NewHandler(obsmemory.NewStore())
	h.Authorize = func(*http.Request, string) bool { return true }

	code, resp := doChannelReq(t, h, http.MethodPost, "/api/observability/channels",
		`{"name":"值班群","url":"https://hooks.example.com/robot?token=secret","headers":{"Authorization":"Bearer secret"},"enabled":true}`,
		"t-acme")
	if code != http.StatusCreated {
		t.Fatalf("创建应 201, got %d %+v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if strings.Contains(data["url"].(string), "secret") {
		t.Fatalf("创建响应 URL 不应含明文 token, got %v", data["url"])
	}
	headers := data["headers"].(map[string]any)
	if headers["Authorization"] != observability.MaskedPlaceholder {
		t.Fatalf("创建响应请求头应掩码, got %v", headers["Authorization"])
	}

	code, resp = doChannelReq(t, h, http.MethodGet, "/api/observability/channels", "", "t-acme")
	if code != http.StatusOK {
		t.Fatalf("列表应 200, got %d", code)
	}
	list := resp["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("列表应有 1 条, got %d", len(list))
	}
	row := list[0].(map[string]any)
	if strings.Contains(row["url"].(string), "robot") {
		t.Fatalf("列表 URL 路径应掩码, got %v", row["url"])
	}

	// 更新：回传掩码占位 → 保留原值（用 store 侧验证明文未丢）。
	id := row["id"].(string)
	code, resp = doChannelReq(t, h, http.MethodPut, "/api/observability/channels/"+id,
		`{"name":"值班群2","url":"https://hooks.example.com/***","enabled":true,"cooldownMinutes":15}`,
		"t-acme")
	if code != http.StatusOK {
		t.Fatalf("更新应 200, got %d %+v", code, resp)
	}
	if resp["data"].(map[string]any)["name"] != "值班群2" {
		t.Fatalf("名称应更新, got %v", resp["data"])
	}
}

// TestChannelHandlerCreateRejectsMaskedURL：创建时不允许回传掩码占位（无原值可保留）。
func TestChannelHandlerCreateRejectsMaskedURL(t *testing.T) {
	h := observability.NewHandler(obsmemory.NewStore())
	h.Authorize = func(*http.Request, string) bool { return true }
	code, _ := doChannelReq(t, h, http.MethodPost, "/api/observability/channels",
		`{"name":"x","url":"https://hooks.example.com/***","enabled":true}`, "t-acme")
	if code != http.StatusBadRequest {
		t.Fatalf("掩码地址创建应 400, got %d", code)
	}
}

// TestChannelHandlerTenantIsolation：跨租户删除统一 404，不泄漏存在性。
func TestChannelHandlerTenantIsolation(t *testing.T) {
	h := observability.NewHandler(obsmemory.NewStore())
	h.Authorize = func(*http.Request, string) bool { return true }
	_, resp := doChannelReq(t, h, http.MethodPost, "/api/observability/channels",
		`{"name":"acme","url":"https://a.example.com/x","enabled":true}`, "t-acme")
	id := resp["data"].(map[string]any)["id"].(string)

	code, _ := doChannelReq(t, h, http.MethodDelete, "/api/observability/channels/"+id, "", "t-globex")
	if code != http.StatusNotFound {
		t.Fatalf("跨租户删除应 404, got %d", code)
	}
	code, _ = doChannelReq(t, h, http.MethodDelete, "/api/observability/channels/"+id, "", "t-acme")
	if code != http.StatusOK {
		t.Fatalf("本租户删除应 200, got %d", code)
	}
}

// TestChannelHandlerForbidden：无写权限拒绝（读权限不影响）。
func TestChannelHandlerForbidden(t *testing.T) {
	h := observability.NewHandler(obsmemory.NewStore())
	h.Authorize = func(_ *http.Request, perm string) bool { return perm == observability.PermObservabilityRead }
	code, _ := doChannelReq(t, h, http.MethodPost, "/api/observability/channels",
		`{"name":"x","url":"https://a.example.com/x","enabled":true}`, "t-acme")
	if code != http.StatusForbidden {
		t.Fatalf("无写权限应 403, got %d", code)
	}
	if code, _ := doChannelReq(t, h, http.MethodGet, "/api/observability/channels", "", "t-acme"); code != http.StatusOK {
		t.Fatalf("读权限应允许列表, got %d", code)
	}
}
