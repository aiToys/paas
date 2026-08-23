//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。

package pg

import (
	"context"
	"os"
	"testing"

	"github.com/aitoys/paas/internal/observability"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

func newTestDB(t *testing.T) *storagepg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	db, err := storagepg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := storagepg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		_, err := db.Pool().Exec(context.Background(),
			`DROP TABLE IF EXISTS alert_rules CASCADE; DROP TABLE IF EXISTS schema_migrations CASCADE`)
		if err != nil {
			t.Fatalf("重置 schema 失败: %v", err)
		}
	})
	return db
}

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

func sampleRule(name string) observability.AlertRule {
	return observability.AlertRule{
		Name:       name,
		MetricName: observability.MetricCPU,
		TargetType: observability.TargetApp,
		Operator:   observability.OpGT,
		Threshold:  80,
		Severity:   observability.SeverityCritical,
		Enabled:    true,
		WebhookURL: "http://hook.example.com/alert",
	}
}

// TestRuleCRUDRoundTrip 创建 → 列表 → 跨租户隔离 → webhook 字段往返。
func TestRuleCRUDRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	saved, err := s.CreateAlertRule(ctx, sampleRule("CPU 告警"))
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("ID 应自动生成")
	}
	if saved.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", saved.TenantID)
	}

	list, err := s.ListAlertRules(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAlertRules 应 1 条, got %d err=%v", len(list), err)
	}
	if list[0].WebhookURL != "http://hook.example.com/alert" {
		t.Fatalf("WebhookURL 往返不一致: %s", list[0].WebhookURL)
	}

	// 跨租户隔离：globex 看不到 acme 规则；删 acme 规则对 globex 报不存在。
	if gl, _ := s.ListAlertRules(globexCtx()); len(gl) != 0 {
		t.Fatalf("globex 应 0 条, got %d", len(gl))
	}
	if err := s.DeleteAlertRule(globexCtx(), saved.ID); err == nil {
		t.Fatal("跨租户删除应报不存在")
	}

	all, err := s.ListAllAlertRules(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAllAlertRules 应 1 条（跨租户无 ctx）, got %d err=%v", len(all), err)
	}

	if err := s.DeleteAlertRule(ctx, saved.ID); err != nil {
		t.Fatalf("DeleteAlertRule: %v", err)
	}
	if list, _ := s.ListAlertRules(ctx); len(list) != 0 {
		t.Fatal("删除后应 0 条")
	}
}
