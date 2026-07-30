//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 app_configs）避免残留。

package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/appconfig"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表。
// 与 environment/pg 样板同构，DROP 列表覆盖已迁的全部模块表。
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
// 包含 app_configs（本包）+ environment/application/identity（其它已迁模块）所有表。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS app_configs, environments, application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

func sampleCfg(id, key string) appconfig.ConfigItem {
	return appconfig.ConfigItem{
		ID:        id,
		AppID:     "app-cs",
		EnvID:     "env-acme-test",
		Key:       key,
		Value:     "info",
		Type:      appconfig.TypeEnv,
		UpdatedAt: time.Now(),
	}
}

func TestCfgListUpsertDelete(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// Upsert 新增。
	c, err := s.Upsert(ctx, sampleCfg("cfg-1", "LOG_LEVEL"))
	if err != nil {
		t.Fatalf("Upsert 新增: %v", err)
	}
	if c.ID == "" {
		t.Fatal("返回的 ID 不能为空")
	}
	if c.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", c.TenantID)
	}

	// List 见到。
	list, err := s.List(ctx, "app-cs", "env-acme-test")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应 1 条, got %d", len(list))
	}
	if list[0].Key != "LOG_LEVEL" {
		t.Fatalf("Key=LOG_LEVEL, got %s", list[0].Key)
	}

	// Delete。
	if err := s.Delete(ctx, "cfg-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = s.List(ctx, "app-cs", "env-acme-test")
	if len(list) != 0 {
		t.Fatalf("删除后应 0 条, got %d", len(list))
	}
	// 重复删除报错。
	if err := s.Delete(ctx, "cfg-1"); err == nil {
		t.Fatal("重复 Delete 应报错")
	}
}

func TestCfgUpsertSameKeyUpdates(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	c1, err := s.Upsert(ctx, sampleCfg("cfg-a", "KEY_A"))
	if err != nil {
		t.Fatalf("Upsert 首次: %v", err)
	}

	// 同 (tenant, app, env, key) 不同 ID，应触发 ON CONFLICT 更新而非新增。
	upd := sampleCfg("cfg-a-different-id", "KEY_A")
	upd.Value = "debug"
	upd.Type = appconfig.TypeEnv
	c2, err := s.Upsert(ctx, upd)
	if err != nil {
		t.Fatalf("Upsert 更新: %v", err)
	}
	// ID 应保持首次插入的值（ON CONFLICT 不改主键）。
	if c2.ID != c1.ID {
		t.Fatalf("同 key 应更新而非新增，ID 应=%s, got=%s", c1.ID, c2.ID)
	}
	// Value 应为新值。
	list, _ := s.List(ctx, "app-cs", "env-acme-test")
	if len(list) != 1 {
		t.Fatalf("同 key 更新后仍应 1 条, got %d", len(list))
	}
	if list[0].Value != "debug" {
		t.Fatalf("Value 应为更新后的 debug, got %s", list[0].Value)
	}
}

func TestCfgSecretMasked(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	secret := appconfig.ConfigItem{
		ID:        "cfg-secret",
		AppID:     "app-cs",
		EnvID:     "env-acme-test",
		Key:       "API_KEY",
		Value:     "sk-real-secret-value",
		Type:      appconfig.TypeSecret,
		UpdatedAt: time.Now(),
	}
	// Upsert 返回值应为掩码。
	got, err := s.Upsert(ctx, secret)
	if err != nil {
		t.Fatalf("Upsert secret: %v", err)
	}
	if got.Value != appconfig.SecretMask {
		t.Fatalf("Upsert 返回值应掩码 %s, got %s", appconfig.SecretMask, got.Value)
	}
	if got.Value == "sk-real-secret-value" {
		t.Fatal("secret 明文绝不能返回")
	}

	// List 返回值也应掩码。
	list, _ := s.List(ctx, "app-cs", "env-acme-test")
	for _, c := range list {
		if c.Type == appconfig.TypeSecret && c.Value != appconfig.SecretMask {
			t.Fatalf("List secret 应掩码, got %s", c.Value)
		}
	}
}

func TestCfgTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if _, err := s.Upsert(acmeCtx(), sampleCfg("cfg-iso", "K_ACME")); err != nil {
		t.Fatalf("Upsert acme: %v", err)
	}
	// globex 不应见到 acme 配置。
	list, _ := s.List(globexCtx(), "app-cs", "env-acme-test")
	if len(list) != 0 {
		t.Fatalf("跨租户 List 应 0 条, got %d", len(list))
	}
	// 跨租户 Delete not found。
	if err := s.Delete(globexCtx(), "cfg-iso"); err == nil {
		t.Fatal("跨租户 Delete 应 not found")
	}
	// globex 用相同 key 在自己租户下是不同条目（隔离）。
	if _, err := s.Upsert(globexCtx(), sampleCfg("cfg-iso-globex", "K_ACME")); err != nil {
		t.Fatalf("Upsert globex 同 key: %v", err)
	}
}

func TestCfgMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	// List 缺失租户拒绝。
	if _, err := s.List(noTenantCtx(), "", ""); err == nil {
		t.Fatal("List 缺失租户应拒绝")
	}
	// Upsert 缺失租户拒绝。
	if _, err := s.Upsert(noTenantCtx(), sampleCfg("cfg-x", "K_X")); err == nil {
		t.Fatal("Upsert 缺失租户应拒绝")
	}
	// Delete 缺失租户拒绝。
	if err := s.Delete(noTenantCtx(), "cfg-x"); err == nil {
		t.Fatal("Delete 缺失租户应拒绝")
	}
}

func TestCfgUpsertIgnoresRequestBodyTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	// 请求体 TenantID 故意写 globex，应以 ctx（acme）为准。
	c := sampleCfg("cfg-t", "K_T")
	c.TenantID = "t-globex"
	got, err := s.Upsert(ctx, c)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}
	// globex 不能见到此配置（证明以 acme 写入）。
	list, _ := s.List(globexCtx(), "app-cs", "env-acme-test")
	if len(list) != 0 {
		t.Fatalf("跨租户应 0 条，证明写入用了 ctx 租户, got %d", len(list))
	}
}

func TestCfgListFilters(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	_, _ = s.Upsert(ctx, func() appconfig.ConfigItem {
		c := sampleCfg("cfg-f1", "K1")
		c.EnvID = "env-a"
		return c
	}())
	_, _ = s.Upsert(ctx, func() appconfig.ConfigItem {
		c := sampleCfg("cfg-f2", "K2")
		c.EnvID = "env-b"
		return c
	}())

	// 按 envID 过滤。
	list, _ := s.List(ctx, "", "env-a")
	if len(list) != 1 || list[0].Key != "K1" {
		t.Fatalf("envID 过滤应只返 K1, got %v", list)
	}
	// 按 appID 过滤。
	list, _ = s.List(ctx, "app-cs", "")
	if len(list) != 2 {
		t.Fatalf("appID 过滤应返 2 条, got %d", len(list))
	}
	// 无过滤返回全部。
	list, _ = s.List(ctx, "", "")
	if len(list) != 2 {
		t.Fatalf("无过滤应返 2 条, got %d", len(list))
	}
}

func TestCfgUpsertTypeChange(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 先 env 后 secret 同 key：ON CONFLICT 更新 type 字段。
	_, _ = s.Upsert(ctx, sampleCfg("cfg-tc", "K_TC"))
	secret := sampleCfg("cfg-tc-2", "K_TC")
	secret.Value = "topsecret"
	secret.Type = appconfig.TypeSecret
	got, err := s.Upsert(ctx, secret)
	if err != nil {
		t.Fatalf("Upsert 切 secret: %v", err)
	}
	if got.Type != appconfig.TypeSecret {
		t.Fatalf("Type 应更新为 secret, got %s", got.Type)
	}
	if got.Value != appconfig.SecretMask {
		t.Fatalf("切到 secret 后应掩码返回, got %s", got.Value)
	}
}

func TestCfgConfigsCount(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	n, err := s.ConfigsCount(ctx)
	if err != nil {
		t.Fatalf("ConfigsCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("空表应 0 条, got %d", n)
	}
	_, _ = s.Upsert(acmeCtx(), sampleCfg("cfg-cc1", "K1"))
	_, _ = s.Upsert(globexCtx(), sampleCfg("cfg-cc2", "K2"))
	n, _ = s.ConfigsCount(ctx)
	if n != 2 {
		t.Fatalf("全表应 2 条, got %d", n)
	}
}
