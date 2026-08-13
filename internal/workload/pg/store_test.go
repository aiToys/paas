//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 workloads）避免残留。

package pg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表（含 workloads）。
// 与 environment/pg 样板同构，自带 resetSchema（DROP 列表覆盖已迁模块全部表）。
func newTestDB(t *testing.T) *pg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	db, err := pg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := pg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

// resetSchema 清空所有业务表 + 迁移版本表，避免跨包测试残留污染。
// 包含 workload（本包）+ identity/application/environment（其它已迁模块）所有表。
func resetSchema(t *testing.T, db *pg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS workloads, data_services, appconfigs, environments, application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

func sampleWL(id string) workload.Workload {
	return workload.Workload{
		ID:        id,
		AppID:     "app-cs",
		EnvID:     "env-acme-test",
		Type:      workload.TypeService,
		Name:      id,
		Image:     "paas/demo:latest",
		Replicas:  2,
		Ready:     1,
		Status:    workload.StatusDeploying,
		CreatedAt: time.Now(),
	}
}

func TestWLCreateListGet(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	w := sampleWL("wl-1")
	if err := s.Create(ctx, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "wl-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Type != workload.TypeService {
		t.Fatalf("Type=service, got %s", got.Type)
	}
	if got.LaneID != workload.LaneDefault {
		t.Fatalf("LaneID 空 → 默认 default, got %s", got.LaneID)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}
	if got.Replicas != 2 || got.Ready != 1 {
		t.Fatalf("Replicas=2 Ready=1, got %d/%d", got.Replicas, got.Ready)
	}

	list, err := s.List(ctx, "", "", "", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应 1 条, got %d", len(list))
	}
}

func TestWLListFiltersCombination(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()

	// 三条工作负载：不同 env / type / app。
	if err := s.Create(ctx, workload.Workload{
		ID: "wl-a", AppID: "app-1", EnvID: "env-test", Type: workload.TypeService,
		Name: "a", Image: "i", Status: workload.StatusRunning, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if err := s.Create(ctx, workload.Workload{
		ID: "wl-b", AppID: "app-1", EnvID: "env-test", Type: workload.TypeCronJob,
		Name: "b", Image: "i", Status: workload.StatusRunning, Schedule: "0 * * * *", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if err := s.Create(ctx, workload.Workload{
		ID: "wl-c", AppID: "app-2", EnvID: "env-prod", Type: workload.TypeService,
		Name: "c", Image: "i", Status: workload.StatusRunning, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create c: %v", err)
	}

	// envID + type 组合：env-test + service → 仅 wl-a。
	got, err := s.List(ctx, "env-test", "", "", workload.TypeService, "")
	if err != nil {
		t.Fatalf("List env+type: %v", err)
	}
	if len(got) != 1 || got[0].ID != "wl-a" {
		t.Fatalf("env-test+service 应仅 wl-a, got %+v", got)
	}

	// envID + appID 组合：env-test + app-1 → wl-a, wl-b。
	got, err = s.List(ctx, "env-test", "app-1", "", "", "")
	if err != nil {
		t.Fatalf("List env+app: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("env-test+app-1 应 2 条, got %d", len(got))
	}

	// 单 type 过滤：cronjob → wl-b。
	got, err = s.List(ctx, "", "", "", workload.TypeCronJob, "")
	if err != nil {
		t.Fatalf("List type: %v", err)
	}
	if len(got) != 1 || got[0].ID != "wl-b" {
		t.Fatalf("type=cronjob 应仅 wl-b, got %+v", got)
	}
	if got[0].Schedule != "0 * * * *" {
		t.Fatalf("Schedule 应回填, got %s", got[0].Schedule)
	}

	// 全空过滤 → 3 条。
	got, err = s.List(ctx, "", "", "", "", "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("全过滤空应 3 条, got %d", len(got))
	}
}

func TestWLUpdateReturnsValueAndSemantic(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	if err := s.Create(ctx, sampleWL("wl-u")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// replicas=5, status="" 不改状态；ready 不变。
	got, err := s.Update(ctx, "wl-u", 5, "")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Replicas != 5 {
		t.Fatalf("Replicas=5, got %d", got.Replicas)
	}
	if got.Status != workload.StatusDeploying {
		t.Fatalf("status 空串不改状态, got %s", got.Status)
	}
	if got.Ready != 1 {
		t.Fatalf("Ready 不变 (非 running), got %d", got.Ready)
	}

	// status=running → ready 跟随 replicas（mock 语义）。
	got, err = s.Update(ctx, "wl-u", 5, workload.StatusRunning)
	if err != nil {
		t.Fatalf("Update running: %v", err)
	}
	if got.Status != workload.StatusRunning {
		t.Fatalf("Status=running, got %s", got.Status)
	}
	if got.Ready != 5 {
		t.Fatalf("running 时 ready 跟随 replicas=5, got %d", got.Ready)
	}
}

func TestWLUpdateCrossTenantNotFound(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if err := s.Create(acmeCtx(), sampleWL("wl-x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// globex 看不到 wl-x。
	if _, err := s.Update(globexCtx(), "wl-x", 3, workload.StatusRunning); err == nil {
		t.Fatal("跨租户 Update 应 not found")
	}
	if _, err := s.UpdateImage(globexCtx(), "wl-x", "img", "sha256:abc"); err == nil {
		t.Fatal("跨租户 UpdateImage 应 not found")
	}
	// 不存在的 ID 也返回 not found。
	if _, err := s.Update(acmeCtx(), "wl-missing", 3, ""); err == nil {
		t.Fatal("Update 不存在 ID 应报错")
	}
}

func TestWLUpdateImageReturnsValueAndSemantic(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	w := sampleWL("wl-img")
	w.ImageRef = "sha256:old"
	if err := s.Create(ctx, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// imageRef 空串不覆盖已有 digest。
	got, err := s.UpdateImage(ctx, "wl-img", "paas/new:1", "")
	if err != nil {
		t.Fatalf("UpdateImage: %v", err)
	}
	if got.Image != "paas/new:1" {
		t.Fatalf("Image 应更新, got %s", got.Image)
	}
	if got.ImageRef != "sha256:old" {
		t.Fatalf("imageRef 空串不应覆盖, got %s", got.ImageRef)
	}
	// imageRef 非空则覆盖。
	got, err = s.UpdateImage(ctx, "wl-img", "paas/new:1", "sha256:new")
	if err != nil {
		t.Fatalf("UpdateImage: %v", err)
	}
	if got.ImageRef != "sha256:new" {
		t.Fatalf("imageRef 应更新, got %s", got.ImageRef)
	}
}

func TestWLCreateMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if err := s.Create(noTenantCtx(), sampleWL("wl-nt")); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}

func TestWLListMissingTenantRejected(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if _, err := s.List(noTenantCtx(), "", "", ""); err == nil {
		t.Fatal("List 缺失租户应拒绝")
	}
}

func TestWLTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	if err := s.Create(acmeCtx(), sampleWL("wl-iso")); err != nil {
		t.Fatalf("Create acme: %v", err)
	}
	// globex 不应见到 acme 工作负载。
	if _, err := s.Get(globexCtx(), "wl-iso"); err == nil {
		t.Fatal("跨租户 Get 应 not found")
	}
	if list, _ := s.List(globexCtx(), "", "", ""); len(list) != 0 {
		t.Fatalf("globex 应 0 条, got %d", len(list))
	}
	// 跨租户过滤也隔离：globex 列 env-acme-test 应 0 条。
	if list, _ := s.List(globexCtx(), "env-acme-test", "", ""); len(list) != 0 {
		t.Fatalf("globex 跨租户 env 过滤应 0 条, got %d", len(list))
	}
	// 跨租户 Delete 返回错误。
	if err := s.Delete(globexCtx(), "wl-iso"); err == nil {
		t.Fatal("跨租户 Delete 应 not found")
	}
}

func TestWLCreateUniqueViolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	if err := s.Create(ctx, sampleWL("wl-dup")); err != nil {
		t.Fatalf("首次 Create: %v", err)
	}
	err := s.Create(ctx, sampleWL("wl-dup"))
	if err == nil {
		t.Fatal("重复 ID 应报「已存在」")
	}
	if !strings.Contains(err.Error(), "工作负载已存在") {
		t.Fatalf("错误消息应与内存一致 (工作负载已存在: <id>), got: %v", err)
	}
}

func TestWLCreateIgnoresRequestBodyTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	w := sampleWL("wl-t")
	w.TenantID = "t-globex" // 故意写错，应以 ctx 为准
	if err := s.Create(ctx, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "wl-t")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准 = t-acme, got %s", got.TenantID)
	}
	// globex 不能见到此工作负载。
	if _, err := s.Get(globexCtx(), "wl-t"); err == nil {
		t.Fatal("跨租户访问应 not found")
	}
}

func TestWLDelete(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	_ = s.Create(ctx, sampleWL("wl-d"))
	if err := s.Delete(ctx, "wl-d"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "wl-d"); err == nil {
		t.Fatal("删除后应 not found")
	}
	// 重复删除返回错误。
	if err := s.Delete(ctx, "wl-d"); err == nil {
		t.Fatal("重复 Delete 应报错")
	}
}

func TestWLCreateInvalidType(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := acmeCtx()
	w := sampleWL("wl-bad")
	w.Type = "unknown"
	if err := s.Create(ctx, w); err == nil {
		t.Fatal("非法 type 应拒绝")
	}
}

func TestWLWorkloadsCount(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	n, err := s.WorkloadsCount(ctx)
	if err != nil {
		t.Fatalf("WorkloadsCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("空表应 0 条, got %d", n)
	}
	_ = s.Create(acmeCtx(), sampleWL("wl-c1"))
	_ = s.Create(globexCtx(), sampleWL("wl-c2"))
	n, _ = s.WorkloadsCount(ctx)
	if n != 2 {
		t.Fatalf("全表应 2 条, got %d", n)
	}
}
