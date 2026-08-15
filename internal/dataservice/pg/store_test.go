//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 data_services）避免残留。

package pg

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/dataservice"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表（含 data_services）。
// 与 environment/pg / appconfig/pg 样板同构，DROP 列表覆盖已迁的全部模块表。
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
// 包含 data_services（本包）+ 已迁模块全部表。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS data_services, app_configs, environments, application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

// sampleDS 构造一条合法的 DataService 用于测试。
func sampleDS(id, name string) dataservice.DataService {
	return dataservice.DataService{
		ID:        id,
		Kind:      dataservice.KindDB,
		Name:      name,
		Spec:      map[string]string{"engine": "postgres", "version": "15", "size_gb": "100"},
		Status:    dataservice.StatusRunning,
		EnvID:     "env-acme-test",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// TestDSCreateGetList 覆盖基本 CRUD + 默认 status 补 running + List/Get 往返。
func TestDSCreateGetList(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// Create status 空 → 补 running。
	d := sampleDS("ds-1", "orders-db")
	d.Status = ""
	got, err := s.Create(ctx, d)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Status != dataservice.StatusRunning {
		t.Fatalf("status 空应补 running, got %s", got.Status)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}

	// Get 往返。
	g, err := s.Get(ctx, "ds-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if g.Spec["engine"] != "postgres" {
		t.Fatalf("Spec.engine 往返失败: %v", g.Spec)
	}

	// List 见到。
	list, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应 1 条, got %d", len(list))
	}
}

// TestDSSpecJSONBRoundTrip 覆盖 JSONB 读写：多键、空 map、含中文值、nil。
func TestDSSpecJSONBRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	cases := []struct {
		name string
		spec map[string]string
	}{
		{"multi-keys", map[string]string{"engine": "mysql", "mode": "cluster", "maxmemory_mb": "2048"}},
		{"empty-map", map[string]string{}},
		{"chinese-value", map[string]string{"engine": "postgres", "备注": "订单库-生产"}},
		{"nil-map", nil},
	}
	for i, c := range cases {
		id := "ds-spec-" + c.name
		d := dataservice.DataService{
			ID:        id,
			Kind:      dataservice.KindDB,
			Name:      c.name,
			Spec:      c.spec,
			Status:    dataservice.StatusRunning,
			EnvID:     "env-acme-test",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if _, err := s.Create(ctx, d); err != nil {
			t.Fatalf("[%d] Create %s: %v", i, c.name, err)
		}
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("[%d] Get %s: %v", i, c.name, err)
		}
		// 读出的 Spec 必须非 nil（即使是 nil/空 map 写入）。
		if got.Spec == nil {
			t.Fatalf("[%d] Spec 读出不能为 nil（避免后续写入 panic）: %s", i, c.name)
		}
		// 内容比对。
		for k, v := range c.spec {
			if got.Spec[k] != v {
				t.Fatalf("[%d] Spec[%s] 往返失败: want %q, got %q", i, k, v, got.Spec[k])
			}
		}
		// 中文值校验。
		if c.name == "chinese-value" && got.Spec["备注"] != "订单库-生产" {
			t.Fatalf("中文值往返失败: got %q", got.Spec["备注"])
		}
	}
}

// TestDSCreateUniqueNameConflict 覆盖租户内 name 唯一冲突。
func TestDSCreateUniqueNameConflict(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	if _, err := s.Create(ctx, sampleDS("ds-u1", "conflict-name")); err != nil {
		t.Fatalf("Create 首次: %v", err)
	}
	// 同租户同名第二次 → 应冲突。
	_, err := s.Create(ctx, sampleDS("ds-u2", "conflict-name"))
	if err == nil {
		t.Fatal("同租户同名 Create 应冲突")
	}
	if !errors.Is(err, storagepg.ErrAlreadyExists) {
		t.Fatalf("冲突错误应为 ErrAlreadyExists, got %v", err)
	}
	// 不同租户用同名应成功（隔离）。
	if _, err := s.Create(globexCtx(), sampleDS("ds-u3", "conflict-name")); err != nil {
		t.Fatalf("跨租户同名应成功（隔离）: %v", err)
	}
}

// TestDSUpdateSpecStatus 覆盖 Update 改 spec/status + 不存在 not found。
func TestDSUpdateSpecStatus(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	if _, err := s.Create(ctx, sampleDS("ds-up", "to-update")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 仅改 status。
	upd := dataservice.DataService{ID: "ds-up", Status: dataservice.StatusStopped}
	got, err := s.Update(ctx, upd)
	if err != nil {
		t.Fatalf("Update status: %v", err)
	}
	if got.Status != dataservice.StatusStopped {
		t.Fatalf("status 应 stopped, got %s", got.Status)
	}
	// 改 spec（保留旧 status）。
	upd = dataservice.DataService{
		ID:   "ds-up",
		Spec: map[string]string{"engine": "mysql", "version": "8", "size_gb": "200"},
	}
	got, err = s.Update(ctx, upd)
	if err != nil {
		t.Fatalf("Update spec: %v", err)
	}
	if got.Spec["engine"] != "mysql" {
		t.Fatalf("Spec.engine 应更新为 mysql, got %s", got.Spec["engine"])
	}
	// status 不应被空字符串覆盖（保留原 stopped）。
	if got.Status != dataservice.StatusStopped {
		t.Fatalf("status 应保留 stopped, got %s", got.Status)
	}

	// 不存在 → not found。
	_, err = s.Update(ctx, dataservice.DataService{ID: "ds-missing", Status: dataservice.StatusRunning})
	if err == nil {
		t.Fatal("Update 不存在应 not found")
	}

	// 非法状态应拒绝。
	_, err = s.Update(ctx, dataservice.DataService{ID: "ds-up", Status: "bogus"})
	if err == nil {
		t.Fatal("非法 status 应拒绝")
	}
}

// TestDSListKindFilter 覆盖 List 按 kind 过滤。
func TestDSListKindFilter(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	mk := func(id, kind string) dataservice.DataService {
		d := sampleDS(id, id)
		d.Kind = kind
		// 引擎须与 Kind 匹配（managed 白名单校验）。
		d.Spec["engine"] = map[string]string{
			dataservice.KindDB:    "postgres",
			dataservice.KindCache: "redis",
			dataservice.KindMQ:    "nats",
		}[kind]
		return d
	}
	for _, d := range []dataservice.DataService{
		mk("ds-k1", dataservice.KindDB),
		mk("ds-k2", dataservice.KindCache),
		mk("ds-k3", dataservice.KindMQ),
	} {
		if _, err := s.Create(ctx, d); err != nil {
			t.Fatalf("Create %s: %v", d.ID, err)
		}
	}
	// kind=cache 仅返 1 条。
	list, err := s.List(ctx, dataservice.KindCache)
	if err != nil {
		t.Fatalf("List cache: %v", err)
	}
	if len(list) != 1 || list[0].Kind != dataservice.KindCache {
		t.Fatalf("kind=cache 应只返 1 条 cache, got %v", list)
	}
	// kind 空返全部。
	all, _ := s.List(ctx, "")
	if len(all) != 3 {
		t.Fatalf("无过滤应返 3 条, got %d", len(all))
	}
}

// TestDSTenantIsolation 覆盖跨租户隔离（不泄漏存在性）。
func TestDSTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if _, err := s.Create(acmeCtx(), sampleDS("ds-iso", "acme-only")); err != nil {
		t.Fatalf("Create acme: %v", err)
	}
	// globex List 应不见 acme 数据。
	list, _ := s.List(globexCtx(), "")
	if len(list) != 0 {
		t.Fatalf("跨租户 List 应 0 条, got %d", len(list))
	}
	// 跨租户 Get not found。
	if _, err := s.Get(globexCtx(), "ds-iso"); err == nil {
		t.Fatal("跨租户 Get 应 not found")
	}
	// 跨租户 Update not found。
	if _, err := s.Update(globexCtx(), dataservice.DataService{ID: "ds-iso", Status: dataservice.StatusStopped}); err == nil {
		t.Fatal("跨租户 Update 应 not found")
	}
	// 跨租户 Delete not found。
	if err := s.Delete(globexCtx(), "ds-iso"); err == nil {
		t.Fatal("跨租户 Delete 应 not found")
	}
}

// TestDSMissingTenantRejected 覆盖缺失租户拒绝（fail-closed）。
func TestDSMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if _, err := s.List(noTenantCtx(), ""); err == nil {
		t.Fatal("List 缺失租户应拒绝")
	}
	if _, err := s.Get(noTenantCtx(), "ds-x"); err == nil {
		t.Fatal("Get 缺失租户应拒绝")
	}
	if _, err := s.Create(noTenantCtx(), sampleDS("ds-x", "x")); err == nil {
		t.Fatal("Create 缺失租户应拒绝")
	}
	if _, err := s.Update(noTenantCtx(), dataservice.DataService{ID: "ds-x"}); err == nil {
		t.Fatal("Update 缺失租户应拒绝")
	}
	if err := s.Delete(noTenantCtx(), "ds-x"); err == nil {
		t.Fatal("Delete 缺失租户应拒绝")
	}
}

// TestDSCreateIgnoresRequestBodyTenant 覆盖请求体 TenantID 被忽略，以 ctx 为准（防越权写）。
func TestDSCreateIgnoresRequestBodyTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	d := sampleDS("ds-t", "tenant-test")
	d.TenantID = "t-globex" // 故意写错
	got, err := s.Create(ctx, d)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}
	// globex 不能见到此数据（证明以 acme 写入）。
	list, _ := s.List(globexCtx(), "")
	if len(list) != 0 {
		t.Fatalf("跨租户应 0 条，证明写入用了 ctx 租户, got %d", len(list))
	}
}

// TestDSDataServicesCount 覆盖全表计数（seed 判空用）。
func TestDSDataServicesCount(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	n, err := s.DataServicesCount(ctx)
	if err != nil {
		t.Fatalf("DataServicesCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("空表应 0 条, got %d", n)
	}
	if _, err := s.Create(acmeCtx(), sampleDS("ds-cc1", "cc1")); err != nil {
		t.Fatalf("Create acme: %v", err)
	}
	if _, err := s.Create(globexCtx(), sampleDS("ds-cc2", "cc2")); err != nil {
		t.Fatalf("Create globex: %v", err)
	}
	n, _ = s.DataServicesCount(ctx)
	if n != 2 {
		t.Fatalf("全表应 2 条, got %d", n)
	}
}
