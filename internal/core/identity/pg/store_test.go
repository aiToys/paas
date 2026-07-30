//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 运行前自动 down→up 重建 schema，保证测试隔离。

package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
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
	// 每测重建 schema（down→up），保证隔离。
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

func TestPGCreateAndGetTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateTenant(ctx, identity.Tenant{ID: "t-x", Name: "X", CreatedAt: now}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	got, err := s.GetTenant(ctx, "t-x")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Name != "X" {
		t.Fatalf("名字不符: %s", got.Name)
	}
	if _, err := s.GetTenant(ctx, "nope"); err == nil {
		t.Fatal("不存在应报错")
	}
}

func TestPGCreateTenantDuplicate(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	a := identity.Tenant{ID: "t-d", Name: "D", CreatedAt: time.Now()}
	if err := s.CreateTenant(ctx, a); err != nil {
		t.Fatalf("首次创建: %v", err)
	}
	if err := s.CreateTenant(ctx, a); err == nil {
		t.Fatal("重复创建应报错")
	}
}

func TestPGLookupAPIKey(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	_ = s.CreateTenant(ctx, identity.Tenant{ID: "t-a", Name: "A", CreatedAt: time.Now()})
	k := identity.APIKey{
		ID: "k1", TenantID: "t-a", UserID: "u1",
		Roles: []string{"tenant-admin", "developer"}, Key: "sk-x", CreatedAt: time.Now(),
	}
	if err := s.CreateAPIKey(ctx, k); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	got, err := s.LookupAPIKey(ctx, "sk-x")
	if err != nil {
		t.Fatalf("LookupAPIKey: %v", err)
	}
	if got.TenantID != "t-a" || got.UserID != "u1" {
		t.Fatalf("解析不符: %+v", got)
	}
	if len(got.Roles) != 2 {
		t.Fatalf("角色数应为 2，got %d", len(got.Roles))
	}
	if _, err := s.LookupAPIKey(ctx, "nope"); err == nil {
		t.Fatal("无效 Key 应报错")
	}
}
