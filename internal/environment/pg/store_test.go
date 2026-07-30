//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 environments）避免残留。

package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/environment"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表（含 environments）。
// 与 application/pg 样板同构，本测试自带 resetSchema（DROP 列表含 environments），
// 因 environment/pg 的测试不依赖 application/pg 测试残留重置。
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
// 包含 environment（本包）+ identity/application（其它已迁模块）所有表。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS environments, application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

func sampleEnv(id string) environment.Environment {
	return environment.Environment{
		ID:        id,
		Name:      id,
		Type:      environment.TypeTest,
		Cluster:   "test-bj",
		Desc:      "d",
		CreatedAt: time.Now(),
	}
}

func TestEnvCreateListGet(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	if err := s.Create(ctx, sampleEnv("env-1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "env-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Type != environment.TypeTest {
		t.Fatalf("Type=test, got %s", got.Type)
	}
	if got.Cluster != "test-bj" {
		t.Fatalf("Cluster=test-bj, got %s", got.Cluster)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应 1 条, got %d", len(list))
	}
}

func TestEnvCreateMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if err := s.Create(noTenantCtx(), sampleEnv("env-x")); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}

func TestEnvTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if err := s.Create(acmeCtx(), sampleEnv("env-acme")); err != nil {
		t.Fatalf("Create acme: %v", err)
	}
	// globex 不应见到 acme 环境。
	if _, err := s.Get(globexCtx(), "env-acme"); err == nil {
		t.Fatal("跨租户访问应 not found")
	}
	if list, _ := s.List(globexCtx()); len(list) != 0 {
		t.Fatalf("globex 应 0 条, got %d", len(list))
	}
	// EnvType 跨租户也返回错误（不泄漏存在性）。
	if _, err := s.EnvType(globexCtx(), "env-acme"); err == nil {
		t.Fatal("EnvType 跨租户应 not found")
	}
	// Delete 跨租户返回错误。
	if err := s.Delete(globexCtx(), "env-acme"); err == nil {
		t.Fatal("跨租户 Delete 应 not found")
	}
}

func TestEnvTypeResolver(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	if err := s.Create(ctx, environment.Environment{
		ID: "env-prod", Name: "生产", Type: environment.TypeProd,
		Cluster: "prod-bj", Desc: "生产环境", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.EnvType(ctx, "env-prod")
	if err != nil {
		t.Fatalf("EnvType: %v", err)
	}
	if got != environment.TypeProd {
		t.Fatalf("EnvType=prod, got %s", got)
	}
}

func TestEnvDelete(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	_ = s.Create(ctx, sampleEnv("env-d"))
	if err := s.Delete(ctx, "env-d"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "env-d"); err == nil {
		t.Fatal("删除后应 not found")
	}
	// 重复删除返回错误。
	if err := s.Delete(ctx, "env-d"); err == nil {
		t.Fatal("重复 Delete 应报错")
	}
}

func TestEnvCreateUniqueViolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	if err := s.Create(ctx, sampleEnv("env-u")); err != nil {
		t.Fatalf("首次 Create: %v", err)
	}
	// 同 ID（主键）冲突。
	if err := s.Create(ctx, sampleEnv("env-u")); err == nil {
		t.Fatal("重复 ID 应报「已存在」")
	}
	// 同 tenant_id + name（UNIQUE）冲突，不同 ID。
	dup := sampleEnv("env-u2")
	dup.Name = "env-u" // 同名
	if err := s.Create(ctx, dup); err == nil {
		t.Fatal("租户内重名应报「已存在」")
	}
}

func TestEnvCreateIgnoresRequestBodyTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	// 请求体 TenantID 故意写 globex，应以 ctx（acme）为准。
	e := sampleEnv("env-t")
	e.TenantID = "t-globex"
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "env-t")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}
	// globex 不能见到此环境（证明以 acme 写入）。
	if _, err := s.Get(globexCtx(), "env-t"); err == nil {
		t.Fatal("跨租户访问应 not found，证明写入用了 ctx 租户")
	}
}

func TestEnvsCount(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	n, err := s.EnvsCount(ctx)
	if err != nil {
		t.Fatalf("EnvsCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("空表应 0 条, got %d", n)
	}
	_ = s.Create(acmeCtx(), sampleEnv("env-c1"))
	_ = s.Create(globexCtx(), sampleEnv("env-c2"))
	n, _ = s.EnvsCount(ctx)
	if n != 2 {
		t.Fatalf("全表应 2 条, got %d", n)
	}
}

func TestEnvListMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if _, err := s.List(noTenantCtx()); err == nil {
		t.Fatal("List 缺失租户应拒绝")
	}
}
