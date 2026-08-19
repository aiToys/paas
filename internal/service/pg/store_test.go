//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 services）避免残留。

package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/service"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表（含 services）。
// 与 environment/pg 样板同构。
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
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

// resetSchema 清空所有业务表 + 迁移版本表，避免跨包测试残留污染。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS services, workloads, environments, application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func TestServiceRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-svc")
	in := service.Service{ID: "svc-pg-1", AppID: "app-1", Name: "chatbot", Type: service.TypeAgent,
		ModelRef: "glm-5.2", Tools: []string{"product"}, BuildArgs: map[string]string{"SERVICE": "chatbot"}, Port: 8080,
		CreatedAt: time.Now()}
	if err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "app-1", "svc-pg-1")
	if err != nil || got.ModelRef != "glm-5.2" || got.Tools[0] != "product" || got.BuildArgs["SERVICE"] != "chatbot" {
		t.Fatalf("round trip: %+v err=%v", got, err)
	}
	// GetOrCreateByName 幂等
	a, _ := s.GetOrCreateByName(ctx, "app-1", "chatbot", service.TypeAgent, nil)
	b, _ := s.GetOrCreateByName(ctx, "app-1", "chatbot", service.TypeAgent, nil)
	if a.ID != b.ID {
		t.Fatalf("GetOrCreateByName not idempotent: %s vs %s", a.ID, b.ID)
	}
	// List / Update / Delete
	list, _ := s.List(ctx, "app-1")
	if len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	got.Port = 9090
	if err := s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Get(ctx, "app-1", "svc-pg-1")
	if got2.Port != 9090 {
		t.Fatalf("update port: %+v", got2)
	}
	// 重名冲突
	if err := s.Create(ctx, service.Service{ID: "svc-pg-2", AppID: "app-1", Name: "chatbot", Type: service.TypeBackend, CreatedAt: time.Now()}); err != service.ErrExists {
		t.Fatalf("dup name err=%v", err)
	}
	if err := s.Delete(ctx, "app-1", "svc-pg-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "app-1", "svc-pg-1"); err != service.ErrNotFound {
		t.Fatalf("after delete err=%v", err)
	}
	// 跨租户不泄漏
	other := tenant.WithTenant(context.Background(), "t-other")
	if _, err := s.Get(other, "app-1", "svc-pg-1"); err != service.ErrNotFound {
		t.Fatalf("cross tenant err=%v", err)
	}
}
