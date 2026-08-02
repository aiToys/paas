//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 运行前自动 down→up 重建 schema，保证测试隔离。

package pg

import (
	"context"
	"os"
	"testing"

	"github.com/aitoys/paas/internal/core/application"
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
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

func sampleApp(id string) application.Application {
	return application.Application{
		ID: id, Name: id, Initial: "A", Env: "开发", Status: "idle",
		Gradient: "g", Desc: "d", Replicas: "1", RPS: "0",
		Bindings: []application.Binding{
			{Type: "models", Name: "m1"},
			{Type: "mq", Name: "q1"},
		},
	}
}

func TestPGCreateListGet(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	if err := s.Create(ctx, sampleApp("app-1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("绑定数应为 2，got %d", len(got.Bindings))
	}
	if got.Resources.Models != 1 || got.Resources.MQ != 1 {
		t.Fatalf("Recount 计数错误: %+v", got.Resources)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应 1 条，got %d", len(list))
	}
}

func TestPGCreateMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if err := s.Create(noTenantCtx(), sampleApp("app-x")); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}

func TestPGTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if err := s.Create(acmeCtx(), sampleApp("app-acme")); err != nil {
		t.Fatalf("Create acme: %v", err)
	}
	// globex 不应见到 acme 应用。
	if _, err := s.Get(globexCtx(), "app-acme"); err == nil {
		t.Fatal("跨租户访问应 not found")
	}
	if list, _ := s.List(globexCtx()); len(list) != 0 {
		t.Fatalf("globex 应无应用，got %d", len(list))
	}
}

func TestPGBindUnbind(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	_ = s.Create(ctx, sampleApp("app-b")) // 含 models/mq 两个绑定

	a, _ := s.BindResource(ctx, "app-b", "dal", "db1")
	if a.Resources.DAL != 1 {
		t.Fatalf("DAL 应 1，got %d", a.Resources.DAL)
	}
	// 重复绑定拒绝。
	if _, err := s.BindResource(ctx, "app-b", "dal", "db1"); err == nil {
		t.Fatal("重复绑定应拒绝")
	}
	a, _ = s.Unbind(ctx, "app-b", "models", "m1")
	if a.Resources.Models != 0 {
		t.Fatalf("models 解绑后应 0，got %d", a.Resources.Models)
	}
}
