package main

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/metrics"
)

// 直接从生产路由注册生成契约，不启动 HTTP listener 或 K8s manager。
func TestOpenAPIRepairContracts(t *testing.T) {
	for _, key := range []string{"PAAS_DB_URL", "PAAS_PROMETHEUS_URL", "PAAS_LOKI_URL", "PAAS_JAEGER_URL", "PAAS_AGENTRUN_URL", "PAAS_PROD"} {
		t.Setenv(key, "")
	}
	t.Setenv("PAAS_DISABLE_DEMO_SEED", "true")
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
	defer func() { _ = srv.Close() }()
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/openapi.json", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, expected := range []string{"expectedRevision", "Idempotency-Key", "/api/backups/{id}/restore", "/api/observability/metrics/quality", "/api/dataservices/{id}/capabilities",
		"/api/observability/channels", "/api/billing/budgets", "/api/admin/settlements",
		"/api/applications/{id}/pipelines/{pid}/deliveries/{deliveryId}/replay"} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("契约缺少 %s", expected)
		}
	}
	if path := os.Getenv("PAAS_OPENAPI_EXPORT_PATH"); path != "" {
		if err := os.WriteFile(path, w.Body.Bytes(), 0600); err != nil { //nolint:gosec // 导出路径来自开发者环境变量，非用户输入
			t.Fatal(err)
		}
	}
}
