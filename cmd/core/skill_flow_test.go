package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/metrics"
)

// 使用真实路由、鉴权与仓储装配，验证模板副本绑定后不允许直接删除/停用。
func TestSkillTemplateBindingLifecycle(t *testing.T) {
	for _, key := range []string{"PAAS_DB_URL", "PAAS_PROMETHEUS_URL", "PAAS_LOKI_URL", "PAAS_JAEGER_URL", "PAAS_AGENTRUN_URL", "PAAS_PROD", "PAAS_API_KEY", "PAAS_AIROUTER_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("PAAS_DISABLE_DEMO_SEED", "false")
	t.Setenv("PAAS_DEVOPS_BUILDER", "mock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stores, cleanup, err := buildAllStores(ctx, k8sAppliers{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	srv := buildHTTPServer(ctx, gateway.New(), &gateway.Meter{}, stores, k8sAppliers{}, metrics.NewRegistry(), nil)
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})
	request := func(key, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, r)
		return w
	}
	w := request("invalid", "GET", "/api/skills/templates", "")
	if w.Code != 401 {
		t.Fatalf("模板目录应保留鉴权：%d", w.Code)
	}
	w = request("sk-acme-admin", "POST", "/api/skills/templates/structured-answer/install", "")
	if w.Code != 201 {
		t.Fatalf("安装失败：%d %s", w.Code, w.Body.String())
	}
	var installed struct {
		Data skill.Skill `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	if !installed.Data.Enabled {
		t.Fatal("复制的模板不可用")
	}
	w = request("sk-acme-admin", "POST", "/api/agents", `{"name":"skill-lifecycle","model":"local-test-only","skills":["`+installed.Data.ID+`"]}`)
	if w.Code != 201 {
		t.Fatalf("绑定失败：%d %s", w.Code, w.Body.String())
	}
	w = request("sk-acme-admin", "DELETE", "/api/skills/"+installed.Data.ID, "")
	if w.Code != 409 {
		t.Fatalf("引用中的 Skill 应拒绝删除：%d %s", w.Code, w.Body.String())
	}
	w = request("sk-globex-admin", "GET", "/api/skills/"+installed.Data.ID+"/references", "")
	if w.Code != 404 {
		t.Fatalf("不应跨租户列出引用：%d", w.Code)
	}
}
