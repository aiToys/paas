//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 secrets/audit_logs）避免残留。

package pg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/security"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表。
// 与 appconfig/pg 样板同构，DROP 列表覆盖已迁的全部模块表。
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
// 包含本包（secrets/audit_logs）+ 已迁的全部其它模块表。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS audit_logs, secrets,
		    billing_records, billing_usages, billing_quotas,
		    configcenter_publishes, configcenter_items, configcenter_namespaces,
		    governance_breakers, governance_routes, governance_instances, governance_services,
		    devops_releases, devops_images, devops_build_runs, devops_repos,
		    workloads,
		    dataservices,
		    app_configs,
		    environments, application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants
		    CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

func tenantSecret(id, name string) security.Secret {
	return security.Secret{
		ID:        id,
		TenantID:  "t-acme", // CreateSecret 以 ctx 覆盖；此字段仅请求体占位
		Name:      name,
		Type:      security.TypeSecret,
		Scope:     security.ScopeTenant,
		Value:     "real-secret-value",
		Desc:      "测试密钥",
		UpdatedAt: time.Now(),
	}
}

func platformSecret(id, name string) security.Secret {
	s := tenantSecret(id, name)
	s.Scope = security.ScopePlatform
	s.TenantID = "" // 请求体带平台级；CreateSecret 应以 NULL 写入
	return s
}

// —— Secret 基础 CRUD —— //

func TestSecCreateListGetDelete(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	created, err := s.CreateSecret(ctx, tenantSecret("sec-1", "DB_PWD"))
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}
	// Create 返回掩码。
	if created.Value != security.SecretMask {
		t.Fatalf("CreateSecret 返回值应掩码 %s, got %s", security.SecretMask, created.Value)
	}
	if created.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", created.TenantID)
	}

	// List 见到。
	list, err := s.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(list) != 1 || list[0].Name != "DB_PWD" {
		t.Fatalf("List 应 1 条 DB_PWD, got %v", list)
	}
	if list[0].Value != security.SecretMask {
		t.Fatalf("List 返回值应掩码, got %s", list[0].Value)
	}

	// Get 掩码。
	got, err := s.GetSecret(ctx, "sec-1")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != security.SecretMask {
		t.Fatalf("GetSecret 返回值应掩码, got %s", got.Value)
	}

	// Delete。
	if err := s.DeleteSecret(ctx, "sec-1"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := s.GetSecret(ctx, "sec-1"); err == nil {
		t.Fatal("删除后 GetSecret 应报错")
	}
}

func TestSecDuplicateNameSameTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	if _, err := s.CreateSecret(ctx, tenantSecret("sec-d1", "DUP_NAME")); err != nil {
		t.Fatalf("首次 CreateSecret: %v", err)
	}
	// 同租户同名应冲突（partial unique index uniq_secret_tenant）。
	_, err := s.CreateSecret(ctx, tenantSecret("sec-d2", "DUP_NAME"))
	if err == nil {
		t.Fatal("同租户同名应报已存在")
	}
	if !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("错误消息应含「已存在」, got %v", err)
	}
}

// —— Scope 关键用例 —— //

func TestSecPlatformSharedAcrossTenants(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	// acme 创建平台级密钥（任何租户都能创建平台级；与内存一致不限制 role）。
	if _, err := s.CreateSecret(acmeCtx(), platformSecret("sec-p1", "OPENAI_KEY")); err != nil {
		t.Fatalf("CreateSecret platform: %v", err)
	}

	// globex 也能见到平台级（全租户可见）。
	list, err := s.ListSecrets(globexCtx())
	if err != nil {
		t.Fatalf("ListSecrets globex: %v", err)
	}
	var found bool
	for _, sec := range list {
		if sec.ID == "sec-p1" && sec.Scope == security.ScopePlatform {
			found = true
			if sec.Value != security.SecretMask {
				t.Fatalf("平台级 List 返回应掩码, got %s", sec.Value)
			}
		}
	}
	if !found {
		t.Fatal("平台级密钥应全租户可见，globex 未见到")
	}

	// globex 也能 Get 平台级（跨租户可读，掩码）。
	got, err := s.GetSecret(globexCtx(), "sec-p1")
	if err != nil {
		t.Fatalf("跨租户 Get 平台级: %v", err)
	}
	if got.Value != security.SecretMask {
		t.Fatalf("平台级 GetSecret 跨租户应掩码, got %s", got.Value)
	}
}

func TestSecPlatformNameGlobalUnique(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	if _, err := s.CreateSecret(acmeCtx(), platformSecret("sec-pu1", "SHARED_KEY")); err != nil {
		t.Fatalf("首次 platform CreateSecret: %v", err)
	}
	// 不同租户创建同名平台级也应冲突（partial unique index uniq_secret_platform）。
	_, err := s.CreateSecret(globexCtx(), platformSecret("sec-pu2", "SHARED_KEY"))
	if err == nil {
		t.Fatal("平台级同名应全局唯一冲突")
	}
	if !strings.Contains(err.Error(), "平台级密钥名已存在") {
		t.Fatalf("错误消息应含「平台级密钥名已存在」, got %v", err)
	}
}

func TestSecTenantAndPlatformSameNameNoConflict(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	// 租户级 + 平台级同名：两个 partial index 互不影响（关键语义）。
	if _, err := s.CreateSecret(acmeCtx(), tenantSecret("sec-t1", "SAME_NAME")); err != nil {
		t.Fatalf("首次 tenant CreateSecret: %v", err)
	}
	if _, err := s.CreateSecret(acmeCtx(), platformSecret("sec-t2", "SAME_NAME")); err != nil {
		t.Fatalf("platform 同名不冲突应成功, got: %v", err)
	}
	// 不同租户同名 tenant 级也不冲突（tenant_id 隔离）。
	if _, err := s.CreateSecret(globexCtx(), tenantSecret("sec-t3", "SAME_NAME")); err != nil {
		t.Fatalf("跨租户 tenant 同名不冲突应成功, got: %v", err)
	}
}

// —— Resolve 明文 / not found —— //

func TestSecResolvePlatformReturnsPlaintext(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	sec := platformSecret("sec-rp", "PROVIDER_TOKEN")
	sec.Value = "sk-real-provider-key-123"
	if _, err := s.CreateSecret(acmeCtx(), sec); err != nil {
		t.Fatalf("CreateSecret platform: %v", err)
	}

	// Resolve 应返回明文（供 Provider 运行时使用）。
	got, err := s.Resolve(context.Background(), "sec-rp")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Value != "sk-real-provider-key-123" {
		t.Fatalf("Resolve 平台级应返明文, got %s", got.Value)
	}
	if got.Scope != security.ScopePlatform {
		t.Fatalf("Resolve 返 scope 应为 platform, got %s", got.Scope)
	}
}

func TestSecResolveTenantReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	if _, err := s.CreateSecret(acmeCtx(), tenantSecret("sec-rt", "TENANT_ONLY")); err != nil {
		t.Fatalf("CreateSecret tenant: %v", err)
	}
	// 租户级 Secret 经 Resolve 应 not found（防绕过掩码读明文）。
	_, err := s.Resolve(context.Background(), "sec-rt")
	if err == nil {
		t.Fatal("Resolve 租户级应报错 not found")
	}
	if !strings.Contains(err.Error(), "平台级密钥不存在") {
		t.Fatalf("错误消息应含「平台级密钥不存在」, got %v", err)
	}
}

func TestSecResolveMissingReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	_, err := s.Resolve(context.Background(), "sec-not-exist")
	if err == nil {
		t.Fatal("Resolve 不存在应报错")
	}
}

// —— 租户隔离 —— //

func TestSecTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	if _, err := s.CreateSecret(acmeCtx(), tenantSecret("sec-iso", "ACME_ONLY")); err != nil {
		t.Fatalf("CreateSecret acme: %v", err)
	}
	// globex List 看不到 acme 的 tenant 级。
	list, _ := s.ListSecrets(globexCtx())
	for _, sec := range list {
		if sec.ID == "sec-iso" {
			t.Fatal("跨租户 List 不应见到 acme 租户级密钥")
		}
	}
	// globex Get 跨租户 tenant 级 → not found。
	if _, err := s.GetSecret(globexCtx(), "sec-iso"); err == nil {
		t.Fatal("跨租户 GetSecret 应 not found")
	}
	// globex Delete 跨租户 tenant 级 → not found。
	if err := s.DeleteSecret(globexCtx(), "sec-iso"); err == nil {
		t.Fatal("跨租户 DeleteSecret 应 not found")
	}
}

// —— 缺失租户 —— //

func TestSecMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	if _, err := s.ListSecrets(noTenantCtx()); err == nil {
		t.Fatal("ListSecrets 缺失租户应拒绝")
	}
	if _, err := s.CreateSecret(noTenantCtx(), tenantSecret("sec-x", "X")); err == nil {
		t.Fatal("CreateSecret 缺失租户应拒绝")
	}
	if err := s.DeleteSecret(noTenantCtx(), "sec-x"); err == nil {
		t.Fatal("DeleteSecret 缺失租户应拒绝")
	}
	if _, err := s.GetSecret(noTenantCtx(), "sec-x"); err == nil {
		t.Fatal("GetSecret 缺失租户应拒绝")
	}
	if err := s.RecordAudit(noTenantCtx(), security.AuditLog{Action: security.ActionCreate}); err == nil {
		t.Fatal("RecordAudit 缺失租户应拒绝")
	}
}

// —— CreateSecret 忽略请求体 TenantID —— //

func TestSecCreateIgnoresRequestBodyTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	// 请求体 TenantID 故意写 globex，应以 ctx（acme）为准。
	sec := tenantSecret("sec-tt", "T_T")
	sec.TenantID = "t-globex"
	got, err := s.CreateSecret(acmeCtx(), sec)
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}
	// globex 不应见到（证明以 acme 写入）。
	list, _ := s.ListSecrets(globexCtx())
	for _, sec := range list {
		if sec.ID == "sec-tt" {
			t.Fatal("跨租户不应见到此密钥，证明写入用了 ctx 租户")
		}
	}
}

// —— 审计 —— //

func TestSecAuditCreateAndList(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	logs := []security.AuditLog{
		{ID: "audit-1", Actor: "u-acme-admin", Action: security.ActionCreate, ResourceType: security.ResourceSecret, ResourceID: "sec-1", Detail: "创建 1", At: time.Now().Add(-1 * time.Hour)},
		{ID: "audit-2", Actor: "u-acme-admin", Action: security.ActionDelete, ResourceType: security.ResourceSecret, ResourceID: "sec-2", Detail: "删除 2", At: time.Now()},
	}
	for _, l := range logs {
		if err := s.RecordAudit(ctx, l); err != nil {
			t.Fatalf("RecordAudit %s: %v", l.ID, err)
		}
	}

	// 全部查询（倒序：audit-2 在前）。
	list, err := s.ListAuditLogs(ctx, "", "")
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应 2 条, got %d", len(list))
	}
	if list[0].ID != "audit-2" {
		t.Fatalf("倒序应 audit-2 在前, got %s", list[0].ID)
	}
	if list[0].TenantID != "t-acme" {
		t.Fatalf("审计 TenantID 应以 ctx 为准, got %s", list[0].TenantID)
	}

	// 过滤 resourceType=secret。
	list, _ = s.ListAuditLogs(ctx, security.ResourceSecret, "")
	if len(list) != 2 {
		t.Fatalf("resourceType=secret 应 2 条, got %d", len(list))
	}
	// 过滤 action=delete。
	list, _ = s.ListAuditLogs(ctx, "", security.ActionDelete)
	if len(list) != 1 || list[0].ID != "audit-2" {
		t.Fatalf("action=delete 应只返 audit-2, got %v", list)
	}
}

func TestSecAuditTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	if err := s.RecordAudit(acmeCtx(), security.AuditLog{ID: "audit-a", Actor: "u-a", Action: security.ActionCreate, ResourceType: security.ResourceSecret, ResourceID: "s-a"}); err != nil {
		t.Fatalf("RecordAudit acme: %v", err)
	}
	// globex 看不到 acme 审计。
	list, _ := s.ListAuditLogs(globexCtx(), "", "")
	if len(list) != 0 {
		t.Fatalf("跨租户审计应 0 条, got %d", len(list))
	}
}

func TestSecAuditNoDelete(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	// AuditStore 接口无删除方法；确认 Repository 不暴露删除。
	var _ security.Repository = s
	// 仅靠接口断言保证：审计只增不删（合规）。
	_ = db
}

// —— Count seed 判空 —— //

func TestSecSecretsAndAuditsCount(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	n, err := s.SecretsCount(ctx)
	if err != nil {
		t.Fatalf("SecretsCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("空表 secrets 应 0, got %d", n)
	}
	m, err := s.AuditsCount(ctx)
	if err != nil {
		t.Fatalf("AuditsCount: %v", err)
	}
	if m != 0 {
		t.Fatalf("空表 audit_logs 应 0, got %d", m)
	}

	_, _ = s.CreateSecret(acmeCtx(), tenantSecret("sec-c1", "K1"))
	_ = s.RecordAudit(acmeCtx(), security.AuditLog{ID: "a-1", Actor: "u", Action: security.ActionCreate, ResourceType: security.ResourceSecret, ResourceID: "sec-c1"})

	n, _ = s.SecretsCount(ctx)
	if n != 1 {
		t.Fatalf("secrets 应 1 条, got %d", n)
	}
	m, _ = s.AuditsCount(ctx)
	if m != 1 {
		t.Fatalf("audit_logs 应 1 条, got %d", m)
	}
}
