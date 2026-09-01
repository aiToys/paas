//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。

package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/observability"
	alertengine "github.com/aitoys/paas/internal/observability/alert"
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

// TestStateAndEventRoundTrip：状态机 upsert/load/delete + 历史事件追加/租户过滤/裁剪。
func TestStateAndEventRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	other := tenant.WithTenant(context.Background(), "t-globex")

	// 状态机：save → load → 覆盖 → delete。
	key := "rule-1|app|app-1"
	st := alertengine.PersistedState{
		StateKey: key, TenantID: "t-acme", TickBreach: 1,
		Alert: observability.Alert{RuleID: "rule-1", TenantID: "t-acme", TargetType: "app", TargetID: "app-1", Status: observability.AlertPending},
	}
	if err := s.SaveStates(ctx, []alertengine.PersistedState{st}); err != nil {
		t.Fatalf("SaveStates: %v", err)
	}
	loaded, err := s.LoadStates(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].TickBreach != 1 || loaded[0].Alert.Status != observability.AlertPending {
		t.Fatalf("LoadStates 应恢复 1 条快照: %v %+v", err, loaded)
	}
	st.TickBreach = 2
	st.Alert.Status = observability.AlertFiring
	if err := s.SaveStates(ctx, []alertengine.PersistedState{st}); err != nil {
		t.Fatalf("SaveStates 覆盖: %v", err)
	}
	if loaded, _ = s.LoadStates(ctx); len(loaded) != 1 || loaded[0].TickBreach != 2 {
		t.Fatalf("upsert 应覆盖同 key: %+v", loaded)
	}
	if err := s.DeleteStates(ctx, []string{key}); err != nil {
		t.Fatalf("DeleteStates: %v", err)
	}
	if loaded, _ = s.LoadStates(ctx); len(loaded) != 0 {
		t.Fatalf("删除后应 0 条, got %d", len(loaded))
	}

	// 历史事件：追加 → 租户查询 → 跨租户不可见。
	ev := observability.AlertEventFromAlert(observability.Alert{
		RuleID: "rule-1", RuleName: "CPU 高", TenantID: "t-acme",
		TargetType: "app", TargetID: "app-1", MetricName: "cpu",
		Value: 95, Threshold: 80, Operator: ">", Severity: "critical",
		Status: observability.AlertFiring, FiredAt: time.Now(),
	}, observability.AlertFiring, time.Now())
	if err := s.AppendEvent(ctx, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	list, err := s.ListAlertEvents(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Status != observability.AlertFiring || list[0].TenantID != "t-acme" {
		t.Fatalf("ListAlertEvents 应 1 条 firing: %v %+v", err, list)
	}
	if list, _ = s.ListAlertEvents(other, 10); len(list) != 0 {
		t.Fatalf("跨租户应不可见, got %d", len(list))
	}
}
