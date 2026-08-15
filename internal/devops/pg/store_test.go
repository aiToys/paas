//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表避免残留。
//
// 测试覆盖：
//   - 4 实体 CRUD（CodeRepo/BuildRun/Image/Release）
//   - 多租户隔离（缺失拒、跨租户 not found 不泄漏）
//   - CreateRelease：找到/建基线 Workload + UpdateImage + 记 PreviousImageID
//   - RollbackRelease：回退镜像 + 标 rolled-back + 新建 is_rollback=true release
//   - 编排调注入的 workload.Repository（用 workloadmemory.NewStore() 作 fake，验证接口透明）
//   - BuildRun runner 异步流转（pending → running → success + 产出 Image）

package pg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops"
	devopsmemory "github.com/aitoys/paas/internal/devops/memory"
	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
	workloadmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表（含 devops 4 表）。
// 与 workload/pg 样板同构，resetSchema 覆盖已迁全部模块表。
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
// 覆盖全部已迁模块表（devops + workload + environment + appconfig + dataservice + identity + application）。
func resetSchema(t *testing.T, db *pg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS releases, images, build_runs, code_repos,
		 workloads, data_services, appconfigs, environments,
		 application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
		 DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

// sampleRepo 构造一个 CodeRepo 样本（不带 ID/TenantID，由 Create 补）。
func sampleRepo() devops.CodeRepo {
	return devops.CodeRepo{
		AppID: "app-cs", GitURL: "https://github.com/acme/cs.git", Branch: "main",
	}
}

// seedImage 直接灌入一条 images 行（绕过 BuildRun runner），供 Release 编排测试快速就绪。
func seedImage(t *testing.T, s *Store, ctx context.Context, id, appID, digest string) devops.Image {
	t.Helper()
	im := devops.Image{
		ID: id, TenantID: "t-acme", AppID: appID, Registry: "registry.paas.local/" + appID,
		Tag: "main-abc12345", Digest: digest, Source: "abc12345", Branch: "main",
		BuildRunID: "build-x", BuiltAt: time.Now(), Status: devops.ImageReady,
	}
	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO images (`+imageCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		im.ID, im.TenantID, im.AppID, im.Registry, im.Tag, im.Digest, im.Source,
		im.Branch, im.BuildRunID, im.BuiltAt, im.Status, im.Version)
	if err != nil {
		t.Fatalf("灌入镜像失败: %v", err)
	}
	return im
}

// ---------- CodeRepo CRUD ----------

func TestCodeRepoCRUD(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, nil)
	ctx := acmeCtx()

	r := sampleRepo()
	if err := s.CreateRepo(ctx, r); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	// CreateRepo 入参按值传，调用方拿不到回填的 ID（与内存版同款语义）；
	// 通过 List 取实际生成的 ID。
	list, err := s.ListRepos(ctx, "app-cs")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应 1 条, got %d", len(list))
	}
	repoID := list[0].ID

	// 默认值补齐：Dockerfile/BuildContext/Status
	got, err := s.GetRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if got.Dockerfile != devops.DefaultDockerfile {
		t.Errorf("Dockerfile 默认值, got %s", got.Dockerfile)
	}
	if got.BuildContext != devops.DefaultBuildContext {
		t.Errorf("BuildContext 默认值, got %s", got.BuildContext)
	}
	if got.Status != devops.RepoStatusActive {
		t.Errorf("Status 默认 active, got %s", got.Status)
	}
	if got.TenantID != "t-acme" {
		t.Errorf("TenantID 应以 ctx 为准, got %s", got.TenantID)
	}

	// 再创建一条以验证 List appID 过滤
	if err := s.CreateRepo(ctx, sampleRepo()); err != nil {
		t.Fatalf("CreateRepo 2: %v", err)
	}
	list, err = s.ListRepos(ctx, "app-cs")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("应 2 条, got %d", len(list))
	}
	other, _ := s.ListRepos(ctx, "app-other")
	if len(other) != 0 {
		t.Errorf("app-other 应 0 条, got %d", len(other))
	}

	// Delete
	if err := s.DeleteRepo(ctx, repoID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	if _, err := s.GetRepo(ctx, repoID); err == nil {
		t.Fatalf("删除后应 not found")
	}
	// 重复删 → not found
	if err := s.DeleteRepo(ctx, repoID); err == nil {
		t.Fatalf("重复删除应 not found")
	}
}

// ---------- BuildRun + 异步 runner ----------

func TestBuildRunRunner(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, nil)
	ctx := acmeCtx()

	// 前置：一个仓库（ID 由 Create 内部生成，需 List 取回）
	if err := s.CreateRepo(ctx, sampleRepo()); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repoList, _ := s.ListRepos(ctx, "app-cs")
	if len(repoList) != 1 {
		t.Fatalf("应 1 个仓库, got %d", len(repoList))
	}
	repoID := repoList[0].ID

	// 触发构建
	if _, err := s.CreateBuildRun(ctx, devops.BuildRun{AppID: "app-cs", RepoID: repoID}); err != nil {
		t.Fatalf("CreateBuildRun: %v", err)
	}
	// BuildRun ID 同理由 List 取回
	bList, _ := s.ListBuildRuns(ctx, "app-cs")
	if len(bList) != 1 {
		t.Fatalf("应 1 条构建, got %d", len(bList))
	}
	buildID := bList[0].ID

	// 初态：pending
	got, err := s.GetBuildRun(ctx, buildID)
	if err != nil {
		t.Fatalf("GetBuildRun: %v", err)
	}
	if got.Status != devops.BuildPending {
		t.Fatalf("初态 pending, got %s", got.Status)
	}
	if got.Branch != "main" {
		t.Errorf("Branch 应继承仓库 main, got %s", got.Branch)
	}

	// 轮询等待 runner 完成（最多 3s）
	deadline := time.Now().Add(3 * time.Second)
	var final devops.BuildRun
	for time.Now().Before(deadline) {
		final, err = s.GetBuildRun(ctx, buildID)
		if err != nil {
			t.Fatalf("GetBuildRun 轮询: %v", err)
		}
		if final.Status == devops.BuildSuccess || final.Status == devops.BuildFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if final.Status != devops.BuildSuccess {
		t.Fatalf("最终应 success, got %s（log: %s）", final.Status, final.Log)
	}
	if final.ImageID == "" {
		t.Fatalf("ImageID 应被回填")
	}
	if final.FinishedAt.IsZero() {
		t.Errorf("FinishedAt 应非零")
	}
	if !strings.Contains(final.Log, "docker push") {
		t.Errorf("Log 应含构建日志, got: %s", final.Log)
	}

	// 产出的 Image 应可查
	img, err := s.GetImage(ctx, final.ImageID)
	if err != nil {
		t.Fatalf("GetImage: %v", err)
	}
	if img.AppID != "app-cs" {
		t.Errorf("镜像 AppID, got %s", img.AppID)
	}
	if !strings.HasPrefix(img.Digest, "sha256:") {
		t.Errorf("Digest sha256 前缀, got %s", img.Digest)
	}
	if img.BuildRunID != buildID {
		t.Errorf("BuildRunID 反向关联, got %s", img.BuildRunID)
	}
}

func TestCreateBuildRunRepoMismatch(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, nil)
	ctx := acmeCtx()

	// 不存在的仓库
	if _, err := s.CreateBuildRun(ctx, devops.BuildRun{AppID: "app-cs", RepoID: "no-such"}); err == nil {
		t.Fatalf("仓库不存在应报错")
	}

	// 仓库存在但 AppID 不匹配（ID 由 Create 内部生成，需 List 取回）
	if err := s.CreateRepo(ctx, sampleRepo()); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repoList, _ := s.ListRepos(ctx, "app-cs")
	if len(repoList) != 1 {
		t.Fatalf("应 1 个仓库, got %d", len(repoList))
	}
	if _, err := s.CreateBuildRun(ctx, devops.BuildRun{AppID: "wrong-app", RepoID: repoList[0].ID}); err == nil {
		t.Fatalf("AppID 不匹配应报错")
	}
}

// ---------- Image ----------

func TestImageListFilter(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, nil)
	ctx := acmeCtx()

	seedImage(t, s, ctx, "img-1", "app-cs", "sha256:aaa")
	seedImage(t, s, ctx, "img-2", "app-cs", "sha256:bbb")
	seedImage(t, s, ctx, "img-3", "app-rec", "sha256:ccc")

	all, err := s.ListImages(ctx, "")
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("全部 3 条, got %d", len(all))
	}
	csOnly, _ := s.ListImages(ctx, "app-cs")
	if len(csOnly) != 2 {
		t.Errorf("app-cs 2 条, got %d", len(csOnly))
	}
}

// ---------- 多租户隔离 ----------

func TestTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, nil)

	// acme 创建仓库
	if err := s.CreateRepo(acmeCtx(), sampleRepo()); err != nil {
		t.Fatalf("CreateRepo acme: %v", err)
	}
	// globex 看不到
	got, err := s.ListRepos(globexCtx(), "")
	if err != nil {
		t.Fatalf("ListRepos globex: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("globex 应 0 条, got %d", len(got))
	}

	// 缺失租户即拒
	if err := s.CreateRepo(noTenantCtx(), sampleRepo()); err == nil {
		t.Fatalf("缺失租户应拒")
	}
}

func TestCrossTenantNotFound(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, nil)
	ctx := acmeCtx()

	if err := s.CreateRepo(ctx, sampleRepo()); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repo, _ := s.ListRepos(ctx, "")
	id := repo[0].ID

	// globex 访问 acme 的仓库：not found（不泄漏存在性）
	if _, err := s.GetRepo(globexCtx(), id); err == nil {
		t.Fatalf("跨租户应 not found")
	}
	if err := s.DeleteRepo(globexCtx(), id); err == nil {
		t.Fatalf("跨租户删除应 not found")
	}
}

// ---------- CreateRelease 编排 ----------

// TestCreateReleaseExistingWL 覆盖：找到目标环境已有基线 service Workload → UpdateImage → 记 PreviousImageID。
func TestCreateReleaseExistingWL(t *testing.T) {
	db := newTestDB(t)
	// 注入内存 workload store（验证对 workload 后端透明）
	wlStore := workloadmemory.NewStore()
	s := NewStore(db, wlStore)
	ctx := acmeCtx()

	// 灌旧镜像 + 已有 Workload（持有旧 digest）
	oldDigest := "sha256:old"
	seedImage(t, s, ctx, "img-old", "app-cs", oldDigest)
	// 灌新镜像（即将发布的）
	newDigest := "sha256:new"
	newImg := seedImage(t, s, ctx, "img-new", "app-cs", newDigest)

	// 直接在 workload store 内放一条基线 service Workload，持有旧 digest
	existingWL := workload.Workload{
		ID: "wl-existing", AppID: "app-cs", EnvID: "env-test", LaneID: workload.LaneDefault,
		Type: workload.TypeService, Name: "cs-svc", Image: "registry.paas.local/app-cs:main-old",
		ImageRef: oldDigest, Replicas: 1, Status: workload.StatusRunning, CreatedAt: time.Now(),
	}
	if err := wlStore.Create(ctx, existingWL); err != nil {
		t.Fatalf("wlStore.Create: %v", err)
	}

	// 发布新镜像
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-test", ImageID: newImg.ID, Strategy: devops.StrategyRolling,
		CreatedBy: "u-acme-admin",
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if rel.Status != devops.ReleaseSucceeded {
		t.Errorf("Status=succeeded, got %s", rel.Status)
	}
	if rel.WorkloadID != "wl-existing" {
		t.Errorf("WorkloadID 应复用 existing, got %s", rel.WorkloadID)
	}
	if rel.PreviousImageID != "img-old" {
		t.Errorf("PreviousImageID 应=旧镜像, got %s", rel.PreviousImageID)
	}
	if rel.ImageDigest != newDigest {
		t.Errorf("ImageDigest 应=新 digest, got %s", rel.ImageDigest)
	}
	if rel.IsRollback {
		t.Errorf("IsRollback 应 false")
	}

	// 验证 Workload 镜像已更新到新 display + 新 digest
	wl, err := wlStore.Get(ctx, "wl-existing")
	if err != nil {
		t.Fatalf("wlStore.Get: %v", err)
	}
	if wl.ImageRef != newDigest {
		t.Errorf("Workload ImageRef 应更新到新 digest, got %s", wl.ImageRef)
	}
	if !strings.HasSuffix(wl.Image, newImg.Tag) {
		t.Errorf("Workload Image 应更新到新 tag, got %s", wl.Image)
	}

	// 发布单可查
	list, _ := s.ListReleases(ctx, "app-cs")
	if len(list) != 1 {
		t.Errorf("应 1 条发布, got %d", len(list))
	}
}

// TestCreateReleaseNewWL 覆盖：无基线 Workload → 创建 + ImageRef=digest + Replicas=1。
func TestCreateReleaseNewWL(t *testing.T) {
	db := newTestDB(t)
	wlStore := workloadmemory.NewStore()
	s := NewStore(db, wlStore)
	ctx := acmeCtx()

	digest := "sha256:only"
	im := seedImage(t, s, ctx, "img-only", "app-newapp", digest)

	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-newapp", EnvID: "env-test", ImageID: im.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if rel.WorkloadID == "" {
		t.Fatalf("应创建 Workload，WorkloadID 空")
	}
	if rel.PreviousImageID != "" {
		t.Errorf("新 Workload 无前镜像, got %s", rel.PreviousImageID)
	}
	// 默认策略 rolling
	if rel.Strategy != devops.StrategyRolling {
		t.Errorf("Strategy 默认 rolling, got %s", rel.Strategy)
	}

	// 验证 Workload 已创建
	wl, err := wlStore.Get(ctx, rel.WorkloadID)
	if err != nil {
		t.Fatalf("wlStore.Get: %v", err)
	}
	if wl.Type != workload.TypeService {
		t.Errorf("Type=service, got %s", wl.Type)
	}
	if wl.ImageRef != digest {
		t.Errorf("ImageRef=digest, got %s", wl.ImageRef)
	}
	if wl.Replicas != 1 {
		t.Errorf("Replicas=1, got %d", wl.Replicas)
	}
	if wl.Name != "app-newapp-svc" {
		t.Errorf("Name=app-svc, got %s", wl.Name)
	}
}

// TestCreateReleaseImageNotFound 覆盖：镜像不存在或租户/应用不匹配 → 报错。
func TestCreateReleaseImageNotFound(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, workloadmemory.NewStore())
	ctx := acmeCtx()

	_, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-test", ImageID: "no-such",
	})
	if err == nil {
		t.Fatalf("镜像不存在应报错")
	}

	// 镜像存在但 AppID 不匹配
	im := seedImage(t, s, ctx, "img-x", "app-cs", "sha256:x")
	_, err = s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-other", EnvID: "env-test", ImageID: im.ID,
	})
	if err == nil {
		t.Fatalf("AppID 不匹配应报错")
	}
}

// ---------- RollbackRelease ----------

// TestRollbackRelease 覆盖完整回滚：旧发布存在 PreviousImageID → 回退 Workload 镜像 +
// 原发布标 rolled-back + 新建 is_rollback=true 发布单。
func TestRollbackRelease(t *testing.T) {
	db := newTestDB(t)
	wlStore := workloadmemory.NewStore()
	s := NewStore(db, wlStore)
	ctx := acmeCtx()

	// 灌两条镜像：v1（旧） + v2（新）
	oldDigest := "sha256:v1"
	newDigest := "sha256:v2"
	oldImg := seedImage(t, s, ctx, "img-v1", "app-cs", oldDigest)
	newImg := seedImage(t, s, ctx, "img-v2", "app-cs", newDigest)

	// 先建一条持有 v1 的基线 Workload
	wl := workload.Workload{
		ID: "wl-rb", AppID: "app-cs", EnvID: "env-test", LaneID: workload.LaneDefault,
		Type: workload.TypeService, Name: "cs-svc", Image: "reg:v1", ImageRef: oldDigest,
		Replicas: 1, Status: workload.StatusRunning, CreatedAt: time.Now(),
	}
	if err := wlStore.Create(ctx, wl); err != nil {
		t.Fatalf("wlStore.Create: %v", err)
	}

	// 发布 v2 → 记 PreviousImageID=img-v1
	orig, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-test", ImageID: newImg.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelease v2: %v", err)
	}
	if orig.PreviousImageID != oldImg.ID {
		t.Fatalf("PreviousImageID 应=img-v1, got %s", orig.PreviousImageID)
	}

	// 回滚
	rb, err := s.RollbackRelease(ctx, orig.ID)
	if err != nil {
		t.Fatalf("RollbackRelease: %v", err)
	}
	if !rb.IsRollback {
		t.Errorf("IsRollback 应 true")
	}
	if rb.Status != devops.ReleaseSucceeded {
		t.Errorf("Status=succeeded, got %s", rb.Status)
	}
	if rb.ImageID != oldImg.ID {
		t.Errorf("ImageID 应=旧镜像, got %s", rb.ImageID)
	}
	if rb.PreviousImageID != orig.ImageID {
		t.Errorf("PreviousImageID 应=原发布的 ImageID（互指）, got %s", rb.PreviousImageID)
	}

	// 原发布应标 rolled-back
	origAfter, _ := s.GetRelease(ctx, orig.ID)
	if origAfter.Status != devops.ReleaseRolledBack {
		t.Errorf("原发布应 rolled-back, got %s", origAfter.Status)
	}

	// Workload 镜像应回退到 v1
	wlAfter, _ := wlStore.Get(ctx, "wl-rb")
	if wlAfter.ImageRef != oldDigest {
		t.Errorf("Workload ImageRef 应回退到 v1 digest, got %s", wlAfter.ImageRef)
	}

	// 现在有 2 条发布（原 + 回滚单）
	list, _ := s.ListReleases(ctx, "app-cs")
	if len(list) != 2 {
		t.Errorf("应 2 条发布, got %d", len(list))
	}
}

// TestRollbackNoPrevious 覆盖：PreviousImageID 为空 → 报错。
func TestRollbackNoPrevious(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, workloadmemory.NewStore())
	ctx := acmeCtx()

	// 直接灌一条没有 PreviousImageID 的发布
	now := time.Now()
	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO releases (`+releaseCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		"rel-noprev", "t-acme", "app-cs", "env-test", "img-x", "sha256:x",
		devops.StrategyRolling, devops.ReleaseSucceeded, "wl-x", "", false, now, "u", "", "", "", "")
	if err != nil {
		t.Fatalf("灌发布失败: %v", err)
	}
	if _, err := s.RollbackRelease(ctx, "rel-noprev"); err == nil {
		t.Fatalf("无上一镜像应报错")
	}
}

// TestRollbackCrossTenant 覆盖：跨租户访问 → not found。
func TestRollbackCrossTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, workloadmemory.NewStore())
	ctx := acmeCtx()

	im := seedImage(t, s, ctx, "img-x", "app-cs", "sha256:x")
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-test", ImageID: im.ID,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	// globex 回滚 acme 的发布 → not found
	if _, err := s.RollbackRelease(globexCtx(), rel.ID); err == nil {
		t.Fatalf("跨租户回滚应 not found")
	}
}

// ---------- Count 方法 ----------

func TestCountMethods(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db, nil)
	ctx := acmeCtx()

	// 初始全 0
	if n, _ := s.ReposCount(ctx); n != 0 {
		t.Errorf("初始 ReposCount 应 0, got %d", n)
	}

	if err := s.CreateRepo(ctx, sampleRepo()); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if n, _ := s.ReposCount(ctx); n != 1 {
		t.Errorf("ReposCount 应 1, got %d", n)
	}
	if n, _ := s.BuildRunsCount(ctx); n != 0 {
		t.Errorf("BuildRunsCount 应 0, got %d", n)
	}
	if n, _ := s.ImagesCount(ctx); n != 0 {
		t.Errorf("ImagesCount 应 0, got %d", n)
	}
	if n, _ := s.ReleasesCount(ctx); n != 0 {
		t.Errorf("ReleasesCount 应 0, got %d", n)
	}
}

// ---------- SeedDevOps 真源复用 ----------

func TestSeedDevOpsReuse(t *testing.T) {
	repos, builds, images, releases := devopsmemory.SeedDevOps()
	if len(repos) == 0 || len(builds) == 0 || len(images) == 0 || len(releases) == 0 {
		t.Fatalf("SeedDevOps 应返回非空 4 切片")
	}
	// 验证租户覆盖（acme + globex）
	acmeHit, globexHit := false, false
	for _, r := range repos {
		if r.TenantID == "t-acme" {
			acmeHit = true
		}
		if r.TenantID == "t-globex" {
			globexHit = true
		}
	}
	if !acmeHit || !globexHit {
		t.Errorf("SeedDevOps 应覆盖 acme+globex 两租户")
	}
}

// ---------- pipeline deploy/release/lane 新列持久化（Task 10） ----------

// TestPGReleaseLaneAndImageVersion 覆盖 migration 0022 引入的 3 个新列持久化：
//   - releases.lane_id / source_run_id（Task 3 引入，MarkSourceRun 写、CreateRelease 写 lane）
//   - images.version（Task 4 引入，SetVersion 写）
//   - stage_runs.log（Task 1 引入，stage_runs 重写时携带）
func TestPGReleaseLaneAndImageVersion(t *testing.T) {
	db := newTestDB(t)
	wlStore := workloadmemory.NewStore()
	s := NewStore(db, wlStore)
	ctx := acmeCtx()

	// 1. 灌基线镜像（seedImage 已包含 version 列；初始 version=""）
	im := seedImage(t, s, ctx, "img-lane", "app-lane", "sha256:lane-v1")

	// 2. 发布到 feature-x 泳道（CreateRelease 写 lane_id）
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-lane", EnvID: "env-test", LaneID: "feature-x", ImageID: im.ID,
		CreatedBy: "u-acme-admin",
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if rel.LaneID != "feature-x" {
		t.Errorf("Release.LaneID=feature-x, got %q", rel.LaneID)
	}

	// 3. 回填 source_run_id（MarkSourceRun 写 source_run_id）
	if err := s.MarkSourceRun(ctx, rel.ID, "run-abc"); err != nil {
		t.Fatalf("MarkSourceRun: %v", err)
	}

	// 4. 回填镜像 version（SetVersion 写 images.version）
	if err := s.SetVersion(ctx, im.ID, "v1.2.0"); err != nil {
		t.Fatalf("SetVersion: %v", err)
	}

	// 5. GetRelease 验证 lane_id + source_run_id 读回
	got, err := s.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if got.LaneID != "feature-x" {
		t.Errorf("PG Release.LaneID 持久化失败: want feature-x, got %q", got.LaneID)
	}
	if got.SourceRunID != "run-abc" {
		t.Errorf("PG Release.SourceRunID 持久化失败: want run-abc, got %q", got.SourceRunID)
	}

	// 6. GetImage 验证 version 读回
	gotImg, err := s.GetImage(ctx, im.ID)
	if err != nil {
		t.Fatalf("GetImage: %v", err)
	}
	if gotImg.Version != "v1.2.0" {
		t.Errorf("PG Image.Version 持久化失败: want v1.2.0, got %q", gotImg.Version)
	}
}
