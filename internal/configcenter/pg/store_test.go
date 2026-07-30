//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 configcenter 3 表）避免残留。
//
// 测试覆盖：
//   - Namespace CRUD + 租户内 Name 唯一冲突 + 跨租户同名允许
//   - Item CRUD + UpsertItem 同 key 更新（ON CONFLICT 主路径）
//   - Item 校验 namespace 存在 + 属本租户
//   - CreatePublish 后 version 单调递增（连发 v1/v2/v3）
//   - CreatePublish 旧 active → rolled-back（同 namespace 仅 1 个 active）
//   - CreatePublish snapshot 多 key 内容正确（无 item 时空 snapshot）
//   - ActivePublish 返回最新 active；无发布 false
//   - RollbackPublish 激活历史 + 当前 active 转 rolled-back
//   - RollbackPublish 对已是 active 的发布拒绝
//   - DeleteNamespace 级联清 items + publishes（事务原子）
//   - 多租户隔离（缺失拒、跨租户 not found 不泄漏）
//   - Count 方法（seed 判空用）

package pg

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/configcenter"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表。
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
// 覆盖全部已迁模块表（含 configcenter 3 表）。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS cc_publishes, cc_items, cc_namespaces,
				gov_breakers, gov_routes, gov_instances, gov_services,
				releases, images, build_runs, code_repos,
				workloads, data_services, appconfigs, environments,
				application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

// sampleNamespace 构造一条合法 Namespace（不含 TenantID，由 ctx 写入）。
func sampleNamespace(id, name string) configcenter.Namespace {
	return configcenter.Namespace{ID: id, Name: name, Desc: "测试 ns"}
}

// sampleItem 构造一条合法 ConfigItem（不含 TenantID，由 ctx 写入）。
func sampleItem(id, nsID, key, value string) configcenter.ConfigItem {
	return configcenter.ConfigItem{ID: id, NamespaceID: nsID, Key: key, Value: value, Type: configcenter.TypeText}
}

// createNamespace helper：保证前置 Namespace 存在，简化后续 item/publish 测试。
func createNamespace(t *testing.T, s *Store, ctx context.Context, id, name string) configcenter.Namespace {
	t.Helper()
	n, err := s.CreateNamespace(ctx, sampleNamespace(id, name))
	if err != nil {
		t.Fatalf("CreateNamespace(%s): %v", name, err)
	}
	return n
}

// ---------- Namespace CRUD ----------

func TestNamespaceCreateGetList(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	n, err := s.CreateNamespace(ctx, sampleNamespace("ns-1", "app-cfg"))
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if n.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", n.TenantID)
	}
	if n.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt 应由 store 填充")
	}

	// Get 往返。
	g, err := s.GetNamespace(ctx, "ns-1")
	if err != nil {
		t.Fatalf("GetNamespace: %v", err)
	}
	if g.Name != "app-cfg" || g.Desc != "测试 ns" {
		t.Fatalf("GetNamespace 往返不一致: %+v", g)
	}

	// List 多条 + 按 name 升序。
	createNamespace(t, s, ctx, "ns-2", "beta-cfg")
	list, err := s.ListNamespaces(ctx)
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListNamespaces 应 2 条, got %d", len(list))
	}
	if list[0].Name != "app-cfg" {
		t.Fatalf("ListNamespaces 应按 name 升序, first=%s", list[0].Name)
	}

	// Delete。
	if err := s.DeleteNamespace(ctx, "ns-1"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := s.GetNamespace(ctx, "ns-1"); err == nil {
		t.Fatalf("Delete 后 GetNamespace 应报错")
	}
}

func TestNamespaceCreateUniqueName(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	createNamespace(t, s, ctx, "ns-a", "dup")
	_, err := s.CreateNamespace(ctx, sampleNamespace("ns-b", "dup"))
	if err == nil || !strings.Contains(err.Error(), "命名空间已存在") {
		t.Fatalf("期望「命名空间已存在」, got %v", err)
	}
	// 跨租户同名应允许（UNIQUE 含 tenant_id）。
	if _, err := s.CreateNamespace(globexCtx(), sampleNamespace("ns-g", "dup")); err != nil {
		t.Fatalf("跨租户同名应允许, got %v", err)
	}
}

// ---------- Item CRUD + Upsert ----------

func TestItemUpsertCreateAndUpdate(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	ns := createNamespace(t, s, ctx, "ns-iu", "iu-ns")

	// 新增 item-1。
	it, err := s.UpsertItem(ctx, sampleItem("item-1", ns.ID, "feature.x", "on"))
	if err != nil {
		t.Fatalf("UpsertItem create: %v", err)
	}
	if it.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", it.TenantID)
	}
	if it.Type != configcenter.TypeText {
		t.Fatalf("Type 空应补 text, got %s", it.Type)
	}

	// 同 (ns, key) 不同 value：命中 ON CONFLICT 更新（不会插新行）。
	up, err := s.UpsertItem(ctx, sampleItem("item-1-diff-id", ns.ID, "feature.x", "off"))
	if err != nil {
		t.Fatalf("UpsertItem update: %v", err)
	}
	// ON CONFLICT 命中：RETURNING 返回原行 id（不是传入的新 id）。
	if up.ID != "item-1" {
		t.Fatalf("Upsert 同 key 应保留原 id, got %s", up.ID)
	}
	if up.Value != "off" {
		t.Fatalf("Upsert 后 Value 应为 off, got %s", up.Value)
	}

	// 新增第二条不同 key。
	s.UpsertItem(ctx, sampleItem("item-2", ns.ID, "rate.limit", "100"))

	// List 按 key 升序。
	list, err := s.ListItems(ctx, ns.ID)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListItems 应 2 条, got %d", len(list))
	}
	if list[0].Key != "feature.x" {
		t.Fatalf("ListItems 应按 key 升序, first=%s", list[0].Key)
	}
	// 跨租户 ns 列表项数 0（item 表查不到 acme 数据）。
	if list, _ := s.ListItems(globexCtx(), ns.ID); len(list) != 0 {
		t.Fatalf("跨租户 ListItems 应 0 条, got %d", len(list))
	}

	// 全租户 item 列表（namespaceID 空）。
	all, _ := s.ListItems(ctx, "")
	if len(all) != 2 {
		t.Fatalf("全租户 item 应 2 条, got %d", len(all))
	}

	// Delete。
	if err := s.DeleteItem(ctx, "item-1"); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	if list, _ := s.ListItems(ctx, ns.ID); len(list) != 1 {
		t.Fatalf("Delete 后应剩 1 条, got %d", len(list))
	}
}

func TestItemUpsertToMissingNamespace(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	_, err := s.UpsertItem(ctx, sampleItem("item-x", "no-such-ns", "k", "v"))
	if err == nil || !strings.Contains(err.Error(), "命名空间不存在") {
		t.Fatalf("期望「命名空间不存在」, got %v", err)
	}
}

// ---------- Publish（CreatePublish version 单调 + active 唯一 + snapshot） ----------

func TestCreatePublishVersionMonotonicAndActiveUnique(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	ns := createNamespace(t, s, ctx, "ns-pub", "pub-ns")

	// 灌 2 条 item 供 snapshot。
	s.UpsertItem(ctx, sampleItem("i1", ns.ID, "feature.newui", "on"))
	s.UpsertItem(ctx, sampleItem("i2", ns.ID, "rate.limit", "100"))

	// 第 1 次发布 → v1 active。
	p1, err := s.CreatePublish(ctx, ns.ID)
	if err != nil {
		t.Fatalf("CreatePublish #1: %v", err)
	}
	if p1.Version != 1 {
		t.Fatalf("首次发布 version 应 = 1, got %d", p1.Version)
	}
	if p1.Status != configcenter.StatusActive {
		t.Fatalf("首次发布 status 应 active, got %s", p1.Status)
	}
	// snapshot 内容（key→value 多对）。
	if len(p1.Snapshot) != 2 || p1.Snapshot["feature.newui"] != "on" || p1.Snapshot["rate.limit"] != "100" {
		t.Fatalf("snapshot 内容不一致: %v", p1.Snapshot)
	}

	// ActivePublish 返回当前 active = p1。
	active, ok, err := s.ActivePublish(ctx, ns.ID)
	if err != nil {
		t.Fatalf("ActivePublish: %v", err)
	}
	if !ok || active.ID != p1.ID {
		t.Fatalf("ActivePublish 应返回 p1, ok=%v id=%s", ok, active.ID)
	}

	// 第 2 次发布 → v2 active；p1 翻 rolled-back。
	p2, err := s.CreatePublish(ctx, ns.ID)
	if err != nil {
		t.Fatalf("CreatePublish #2: %v", err)
	}
	if p2.Version != 2 {
		t.Fatalf("第二次发布 version 应 = 2, got %d", p2.Version)
	}
	if p2.Status != configcenter.StatusActive {
		t.Fatalf("第二次发布 status 应 active, got %s", p2.Status)
	}

	// ListPublishes 按 version 倒序：v2 在前。
	list, _ := s.ListPublishes(ctx, ns.ID)
	if len(list) != 2 {
		t.Fatalf("应有 2 条 publish, got %d", len(list))
	}
	if list[0].Version != 2 || list[1].Version != 1 {
		t.Fatalf("ListPublishes 应按 version 倒序, got %d %d", list[0].Version, list[1].Version)
	}
	// 旧 active 应为 rolled-back。
	if list[1].Status != configcenter.StatusRolledBack {
		t.Fatalf("v1 应被翻 rolled-back, got %s", list[1].Status)
	}

	// ActivePublish 应返回 p2。
	active, _, _ = s.ActivePublish(ctx, ns.ID)
	if active.ID != p2.ID {
		t.Fatalf("ActivePublish 应返回 p2, got %s", active.ID)
	}

	// 第 3 次发布 → v3 active；p2 翻 rolled-back。验证连续单调。
	p3, err := s.CreatePublish(ctx, ns.ID)
	if err != nil {
		t.Fatalf("CreatePublish #3: %v", err)
	}
	if p3.Version != 3 {
		t.Fatalf("第三次发布 version 应 = 3, got %d", p3.Version)
	}
	list, _ = s.ListPublishes(ctx, ns.ID)
	if len(list) != 3 {
		t.Fatalf("应有 3 条 publish, got %d", len(list))
	}
	// 同 namespace 内 active 仅 1 个。
	activeCnt := 0
	for _, p := range list {
		if p.Status == configcenter.StatusActive {
			activeCnt++
		}
	}
	if activeCnt != 1 {
		t.Fatalf("namespace 内 active 应仅 1 个, got %d", activeCnt)
	}
}

func TestCreatePublishEmptySnapshot(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	ns := createNamespace(t, s, ctx, "ns-empty", "empty-ns")

	// 无 item 也允许发布空 snapshot。
	p, err := s.CreatePublish(ctx, ns.ID)
	if err != nil {
		t.Fatalf("CreatePublish 空 snapshot: %v", err)
	}
	if p.Snapshot == nil {
		t.Fatalf("空 snapshot 读出应为非 nil map")
	}
	if len(p.Snapshot) != 0 {
		t.Fatalf("空 snapshot 应 0 条, got %v", p.Snapshot)
	}
}

func TestCreatePublishMissingNamespace(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	_, err := s.CreatePublish(ctx, "no-such-ns")
	if err == nil || !strings.Contains(err.Error(), "命名空间不存在") {
		t.Fatalf("期望「命名空间不存在」, got %v", err)
	}
}

// ---------- Rollback ----------

func TestRollbackPublishActivateHistory(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	ns := createNamespace(t, s, ctx, "ns-rb", "rb-ns")

	p1, _ := s.CreatePublish(ctx, ns.ID)
	p2, _ := s.CreatePublish(ctx, ns.ID)
	p3, _ := s.CreatePublish(ctx, ns.ID)
	// 当前状态：p1=rolled-back, p2=rolled-back, p3=active。

	// 回滚 p1 → p1 active, p3 翻 rolled-back。
	rb, err := s.RollbackPublish(ctx, p1.ID)
	if err != nil {
		t.Fatalf("RollbackPublish p1: %v", err)
	}
	if rb.Status != configcenter.StatusActive {
		t.Fatalf("回滚后目标应 active, got %s", rb.Status)
	}
	// ActivePublish 返回 p1。
	active, _, _ := s.ActivePublish(ctx, ns.ID)
	if active.ID != p1.ID {
		t.Fatalf("回滚后 active 应 = p1, got %s", active.ID)
	}
	// p3 应翻 rolled-back。
	gp3, _ := s.ListPublishes(ctx, ns.ID)
	for _, p := range gp3 {
		if p.ID == p3.ID && p.Status != configcenter.StatusRolledBack {
			t.Fatalf("p3 应回 rolled-back, got %s", p.Status)
		}
	}

	// 回滚当前 active（p1）应报错。
	if _, err := s.RollbackPublish(ctx, p1.ID); err == nil ||
		!strings.Contains(err.Error(), "发布已是当前生效版本") {
		t.Fatalf("期望「发布已是当前生效版本」, got %v", err)
	}

	// 回滚 p2 → p2 active, p1 翻 rolled-back。
	if _, err := s.RollbackPublish(ctx, p2.ID); err != nil {
		t.Fatalf("RollbackPublish p2: %v", err)
	}
	active, _, _ = s.ActivePublish(ctx, ns.ID)
	if active.ID != p2.ID {
		t.Fatalf("再次回滚后 active 应 = p2, got %s", active.ID)
	}

	_ = p2 // 已使用
}

func TestRollbackMissingPublish(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	_, err := s.RollbackPublish(ctx, "no-such-pub")
	if err == nil || !strings.Contains(err.Error(), "发布不存在") {
		t.Fatalf("期望「发布不存在」, got %v", err)
	}
}

func TestPublishNamespaceID(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	ns := createNamespace(t, s, ctx, "ns-sid", "sid-ns")
	p, _ := s.CreatePublish(ctx, ns.ID)
	got, err := s.PublishNamespaceID(ctx, p.ID)
	if err != nil {
		t.Fatalf("PublishNamespaceID: %v", err)
	}
	if got != ns.ID {
		t.Fatalf("PublishNamespaceID 不一致: got %s want %s", got, ns.ID)
	}
	// 跨租户 not found。
	if _, err := s.PublishNamespaceID(globexCtx(), p.ID); err == nil ||
		!strings.Contains(err.Error(), "发布不存在") {
		t.Fatalf("跨租户 PublishNamespaceID 应报「发布不存在」, got %v", err)
	}
}

// ---------- DeleteNamespace 级联 ----------

func TestDeleteNamespaceCascade(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	ns := createNamespace(t, s, ctx, "ns-casc", "casc-ns")

	// 灌子表：2 item + 2 publish。
	s.UpsertItem(ctx, sampleItem("ic-1", ns.ID, "k1", "v1"))
	s.UpsertItem(ctx, sampleItem("ic-2", ns.ID, "k2", "v2"))
	s.CreatePublish(ctx, ns.ID)
	s.CreatePublish(ctx, ns.ID)

	// 另一 ns（确认不被误删）。
	other := createNamespace(t, s, ctx, "ns-other", "other-ns")
	s.UpsertItem(ctx, sampleItem("io-1", other.ID, "ok1", "ov1"))
	s.CreatePublish(ctx, other.ID)

	// 删目标 ns。
	if err := s.DeleteNamespace(ctx, ns.ID); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}

	// 级联：items/publishes 全清。
	if list, _ := s.ListItems(ctx, ns.ID); len(list) != 0 {
		t.Fatalf("级联清 items 失败: 仍剩 %d", len(list))
	}
	if list, _ := s.ListPublishes(ctx, ns.ID); len(list) != 0 {
		t.Fatalf("级联清 publishes 失败: 仍剩 %d", len(list))
	}

	// 另一 ns 数据保留。
	if list, _ := s.ListItems(ctx, other.ID); len(list) != 1 {
		t.Fatalf("不应误删其他 ns 的 item, got %d", len(list))
	}
	if list, _ := s.ListPublishes(ctx, other.ID); len(list) != 1 {
		t.Fatalf("不应误删其他 ns 的 publish, got %d", len(list))
	}
}

// ---------- 多租户隔离 ----------

func TestTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	acme := acmeCtx()
	globex := globexCtx()

	ns := createNamespace(t, s, acme, "ns-acme", "acme-only")
	s.UpsertItem(acme, sampleItem("ia", ns.ID, "k", "v"))
	s.CreatePublish(acme, ns.ID)

	// Globex 看不到 Acme 的资源。
	if list, _ := s.ListNamespaces(globex); len(list) != 0 {
		t.Fatalf("跨租户 ListNamespaces 应 0 条, got %d", len(list))
	}
	if _, err := s.GetNamespace(globex, ns.ID); err == nil ||
		!strings.Contains(err.Error(), "命名空间不存在") {
		t.Fatalf("跨租户 GetNamespace 应 not found, got %v", err)
	}
	if list, _ := s.ListItems(globex, ns.ID); len(list) != 0 {
		t.Fatalf("跨租户 ListItems 应 0 条, got %d", len(list))
	}
	if list, _ := s.ListPublishes(globex, ns.ID); len(list) != 0 {
		t.Fatalf("跨租户 ListPublishes 应 0 条, got %d", len(list))
	}
	if _, _, err := s.ActivePublish(globex, ns.ID); err != nil {
		t.Fatalf("跨租户 ActivePublish 无 active 应返 false 不报越权错, got %v", err)
	}

	// Globex 删 Acme 资源：RowsAffected==0 → not found。
	if err := s.DeleteNamespace(globex, ns.ID); err == nil ||
		!strings.Contains(err.Error(), "命名空间不存在") {
		t.Fatalf("跨租户 DeleteNamespace 应 not found, got %v", err)
	}
	if err := s.DeleteItem(globex, "ia"); err == nil ||
		!strings.Contains(err.Error(), "配置项不存在") {
		t.Fatalf("跨租户 DeleteItem 应 not found, got %v", err)
	}

	// Globex 在 Acme 的 ns 上 UpsertItem 应失败（GetNamespace 校验不通过）。
	if _, err := s.UpsertItem(globex, sampleItem("ig", ns.ID, "k2", "v2")); err == nil ||
		!strings.Contains(err.Error(), "命名空间不存在") {
		t.Fatalf("跨租户 UpsertItem 应失败, got %v", err)
	}
}

func TestMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := noTenantCtx()

	// 全部方法：缺失租户即拒（fail-closed）。
	if _, err := s.ListNamespaces(ctx); err == nil {
		t.Fatalf("ListNamespaces 缺失租户应拒")
	}
	if _, err := s.GetNamespace(ctx, "x"); err == nil {
		t.Fatalf("GetNamespace 缺失租户应拒")
	}
	if _, err := s.CreateNamespace(ctx, sampleNamespace("x", "x")); err == nil {
		t.Fatalf("CreateNamespace 缺失租户应拒")
	}
	if err := s.DeleteNamespace(ctx, "x"); err == nil {
		t.Fatalf("DeleteNamespace 缺失租户应拒")
	}
	if _, err := s.ListItems(ctx, ""); err == nil {
		t.Fatalf("ListItems 缺失租户应拒")
	}
	if _, err := s.UpsertItem(ctx, sampleItem("x", "x", "k", "v")); err == nil {
		t.Fatalf("UpsertItem 缺失租户应拒")
	}
	if err := s.DeleteItem(ctx, "x"); err == nil {
		t.Fatalf("DeleteItem 缺失租户应拒")
	}
	if _, err := s.ListPublishes(ctx, ""); err == nil {
		t.Fatalf("ListPublishes 缺失租户应拒")
	}
	if _, err := s.CreatePublish(ctx, "x"); err == nil {
		t.Fatalf("CreatePublish 缺失租户应拒")
	}
	if _, err := s.RollbackPublish(ctx, "x"); err == nil {
		t.Fatalf("RollbackPublish 缺失租户应拒")
	}
	if _, _, err := s.ActivePublish(ctx, "x"); err == nil {
		t.Fatalf("ActivePublish 缺失租户应拒")
	}
	if _, err := s.PublishNamespaceID(ctx, "x"); err == nil {
		t.Fatalf("PublishNamespaceID 缺失租户应拒")
	}
}

// ---------- Count 方法（seed 判空用） ----------

func TestCountMethods(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 空表。
	if n, _ := s.NamespacesCount(ctx); n != 0 {
		t.Fatalf("空表 NamespacesCount 应 0, got %d", n)
	}
	if n, _ := s.ItemsCount(ctx); n != 0 {
		t.Fatalf("空表 ItemsCount 应 0, got %d", n)
	}
	if n, _ := s.PublishesCount(ctx); n != 0 {
		t.Fatalf("空表 PublishesCount 应 0, got %d", n)
	}

	// 灌数据。
	ns := createNamespace(t, s, ctx, "ns-cnt", "cnt-ns")
	s.UpsertItem(ctx, sampleItem("ic", ns.ID, "k", "v"))
	s.CreatePublish(ctx, ns.ID)

	if n, _ := s.NamespacesCount(ctx); n != 1 {
		t.Fatalf("NamespacesCount 应 1, got %d", n)
	}
	if n, _ := s.ItemsCount(ctx); n != 1 {
		t.Fatalf("ItemsCount 应 1, got %d", n)
	}
	if n, _ := s.PublishesCount(ctx); n != 1 {
		t.Fatalf("PublishesCount 应 1, got %d", n)
	}
}

// 编译期断言：pgx.ErrNoRows 用于错误映射（避免误删 import）。
var _ = errors.Is
var _ = pgx.ErrNoRows
