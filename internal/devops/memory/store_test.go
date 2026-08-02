package memory

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// seedImage 白盒注入镜像 fixture（ImageRepository 无 Create 方法，测试直接造静态构建产物）。
// 去假数据后 NewStore 不再 seed，依赖镜像的测试需显式调用本 helper 自建数据。
func seedImage(s *Store, id, tid, appID string) devops.Image {
	img := devops.Image{
		ID:         id,
		TenantID:   tid,
		AppID:      appID,
		Registry:   "registry.paas.local/" + appID,
		Tag:        "main-" + id,
		Digest:     "sha256:" + sha256hex(id),
		Source:     "a1b2c3d4e5f67890a1b2c3d4e5f67890a1b2c3d4",
		Branch:     "main",
		BuildRunID: "build-" + id,
		BuiltAt:    time.Now(),
		Status:     devops.ImageReady,
	}
	s.mu.Lock()
	s.images[id] = img
	s.mu.Unlock()
	return img
}

// seedRelease 白盒注入发布单 fixture（CreateRelease 是编排方法，不适合直接造静态历史发布）。
// 供跨租户隔离等需要预置历史发布单的测试显式调用。
func seedRelease(s *Store, id, tid, appID, envID, imageID, imageDigest, workloadID string) devops.Release {
	rel := devops.Release{
		ID:          id,
		TenantID:    tid,
		AppID:       appID,
		EnvID:       envID,
		ImageID:     imageID,
		ImageDigest: imageDigest,
		Strategy:    devops.StrategyRolling,
		Status:      devops.ReleaseSucceeded,
		WorkloadID:  workloadID,
		CreatedAt:   time.Now(),
		CreatedBy:   "u-test",
	}
	s.mu.Lock()
	s.releases[id] = rel
	s.mu.Unlock()
	return rel
}

// TestBuildRunMockCI 验证 mock CI runner 异步流转 pending->running->success 并产出 Image。
func TestBuildRunMockCI(t *testing.T) {
	s := NewStore(wlmemory.NewStore())
	ctx := acmeCtx()
	// 自建仓库（构建需校验仓库归属应用）
	if err := s.CreateRepo(ctx, devops.CodeRepo{
		ID: "repo-acme-cs", AppID: "app-cs", GitURL: "https://github.com/acme/cs-svc.git", Branch: "main",
	}); err != nil {
		t.Fatalf("建仓库失败: %v", err)
	}

	if err := s.CreateBuildRun(ctx, devops.BuildRun{
		AppID: "app-cs", RepoID: "repo-acme-cs", Trigger: devops.TriggerManual,
	}); err != nil {
		t.Fatalf("触发构建失败: %v", err)
	}

	builds, _ := s.ListBuildRuns(ctx, "app-cs")
	br := builds[0] // 最新构建在前
	if br.Status != devops.BuildPending && br.Status != devops.BuildRunning {
		t.Fatalf("初始状态应为 pending/running，got %s", br.Status)
	}
	if br.Commit == "" {
		t.Fatal("mock commit 应已生成")
	}

	// 等待 mock CI runner 完成（pending 300ms + running 500ms）
	time.Sleep(1200 * time.Millisecond)

	br2, _ := s.GetBuildRun(ctx, br.ID)
	if br2.Status != devops.BuildSuccess {
		t.Fatalf("构建应为 success，got %s", br2.Status)
	}
	if br2.ImageID == "" {
		t.Fatal("构建成功应产出镜像")
	}
	if br2.Log == "" {
		t.Fatal("构建日志应已填充")
	}

	img, _ := s.GetImage(ctx, br2.ImageID)
	if img.Digest == "" {
		t.Fatal("镜像 digest 不可为空")
	}
	if img.Source != br2.Commit {
		t.Fatalf("镜像来源应等于构建 commit")
	}
	if img.BuildRunID != br2.ID {
		t.Fatalf("镜像应反指构建")
	}
}

// TestReleaseOrchestration 验证发布编排：找基线 Workload + 更新 ImageRef + 记录发布单。
func TestReleaseOrchestration(t *testing.T) {
	wl := wlmemory.NewStore()
	s := NewStore(wl)
	ctx := acmeCtx()
	// 自建镜像 fixture（构建产物）
	seedImage(s, "img-acme-001", "t-acme", "app-cs")

	// img-acme-001 发布到 env-acme-test（wl-cs-api 由 wlmemory seed 提供 service 基线）
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-acme-test", ImageID: "img-acme-001",
	})
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if rel.Status != devops.ReleaseSucceeded {
		t.Fatalf("发布应成功，got %s", rel.Status)
	}
	if rel.WorkloadID != "wl-cs-api" {
		t.Fatalf("应编排到已存在的 wl-cs-api，got %s", rel.WorkloadID)
	}
	if rel.PreviousImageID != "" {
		t.Fatalf("首次发布 PreviousImageID 应为空，got %s", rel.PreviousImageID)
	}

	// Workload.ImageRef 应更新为镜像 digest（生产锁定）
	w, _ := wl.Get(ctx, "wl-cs-api")
	img, _ := s.GetImage(ctx, "img-acme-001")
	if w.ImageRef != img.Digest {
		t.Fatalf("workload.ImageRef 应为镜像 digest，got %s", w.ImageRef)
	}
}

// TestReleasePreviousAndRollback 验证二次发布记录回滚指针 + 回滚回退镜像。
func TestReleasePreviousAndRollback(t *testing.T) {
	wl := wlmemory.NewStore()
	s := NewStore(wl)
	ctx := acmeCtx()
	// 自建镜像 img-acme-001 + 仓库（构建 img2 需要）
	seedImage(s, "img-acme-001", "t-acme", "app-cs")
	if err := s.CreateRepo(ctx, devops.CodeRepo{
		ID: "repo-acme-cs", AppID: "app-cs", GitURL: "https://github.com/acme/cs-svc.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}

	// 第一次发布 img-acme-001
	if _, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-acme-test", ImageID: "img-acme-001",
	}); err != nil {
		t.Fatal(err)
	}

	// 触发构建产出新镜像 img2
	if err := s.CreateBuildRun(ctx, devops.BuildRun{AppID: "app-cs", RepoID: "repo-acme-cs"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	builds, _ := s.ListBuildRuns(ctx, "app-cs")
	img2, _ := s.GetImage(ctx, builds[0].ImageID)

	// 第二次发布 img2 -> PreviousImageID 应指向 img-acme-001
	rel2, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-acme-test", ImageID: img2.ID,
	})
	if err != nil {
		t.Fatalf("二次发布失败: %v", err)
	}
	if rel2.PreviousImageID != "img-acme-001" {
		t.Fatalf("二次发布 PreviousImageID 应为 img-acme-001，got %s", rel2.PreviousImageID)
	}

	// 回滚 rel2 -> Workload 回退到 img-acme-001
	rb, err := s.RollbackRelease(ctx, rel2.ID)
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if !rb.IsRollback || rb.ImageID != "img-acme-001" {
		t.Fatalf("回滚发布应指向 img-acme-001，got %+v", rb)
	}

	w, _ := wl.Get(ctx, "wl-cs-api")
	img1, _ := s.GetImage(ctx, "img-acme-001")
	if w.ImageRef != img1.Digest {
		t.Fatalf("回滚后 workload.ImageRef 应为 img-acme-001 digest，got %s", w.ImageRef)
	}

	// 原 rel2 应标记 rolled-back
	rel2Check, _ := s.GetRelease(ctx, rel2.ID)
	if rel2Check.Status != devops.ReleaseRolledBack {
		t.Fatalf("原发布应标记 rolled-back，got %s", rel2Check.Status)
	}
}

// TestRollbackNoPrevious 验证无上一镜像时回滚报错。
func TestRollbackNoPrevious(t *testing.T) {
	wl := wlmemory.NewStore()
	s := NewStore(wl)
	ctx := acmeCtx()
	// 自建镜像 fixture
	seedImage(s, "img-acme-001", "t-acme", "app-cs")

	// 首次发布（无 previous）
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-acme-test", ImageID: "img-acme-001",
	})
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}

	if _, err := s.RollbackRelease(ctx, rel.ID); err == nil {
		t.Fatal("无上一镜像应无法回滚，应报错")
	}
}

// TestReleaseCreateWorkload 验证目标环境无基线 Workload 时自动创建。
func TestReleaseCreateWorkload(t *testing.T) {
	wl := wlmemory.NewStore()
	s := NewStore(wl)
	ctx := acmeCtx()
	// 自建镜像 fixture
	seedImage(s, "img-acme-001", "t-acme", "app-cs")

	// app-cs 在 env-acme-prod-bj 无 service 基线（wlmemory seed 仅 env-acme-test 有 service）-> 发布应创建新 Workload
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-cs", EnvID: "env-acme-prod-bj", ImageID: "img-acme-001",
	})
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if rel.WorkloadID == "" {
		t.Fatal("应创建新 Workload")
	}

	w, err := wl.Get(ctx, rel.WorkloadID)
	if err != nil {
		t.Fatalf("新 Workload 应可查: %v", err)
	}
	if w.AppID != "app-cs" || w.EnvID != "env-acme-prod-bj" || w.Type != "service" {
		t.Fatalf("新 Workload 字段不正确: %+v", w)
	}
	img, _ := s.GetImage(ctx, "img-acme-001")
	if w.ImageRef != img.Digest {
		t.Fatalf("新 Workload.ImageRef 应为镜像 digest")
	}
}

// TestTenantIsolation 验证跨租户访问 not found 不泄漏。
func TestTenantIsolation(t *testing.T) {
	s := NewStore(wlmemory.NewStore())
	// 自建跨租户数据：acme 仓库+镜像+发布，globex 镜像
	if err := s.CreateRepo(acmeCtx(), devops.CodeRepo{
		ID: "repo-acme-cs", AppID: "app-cs", GitURL: "https://github.com/acme/cs.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	acmeImg := seedImage(s, "img-acme-001", "t-acme", "app-cs")
	seedImage(s, "img-globex-001", "t-globex", "app-agent")
	seedRelease(s, "rel-acme-001", "t-acme", "app-cs", "env-acme-test", "img-acme-001", acmeImg.Digest, "wl-cs-api")

	// acme 访问 globex 镜像 -> not found
	if _, err := s.GetImage(acmeCtx(), "img-globex-001"); err == nil {
		t.Fatal("跨租户访问镜像应失败")
	}
	// globex 只见自己的镜像
	imgs, _ := s.ListImages(globexCtx(), "")
	if len(imgs) != 1 || imgs[0].ID != "img-globex-001" {
		t.Fatalf("globex 应仅见 globex 镜像，got %d 条", len(imgs))
	}
	// globex 访问 acme 仓库 -> not found
	if _, err := s.GetRepo(globexCtx(), "repo-acme-cs"); err == nil {
		t.Fatal("跨租户访问仓库应失败")
	}
	// globex 回滚 acme 发布 -> not found
	if _, err := s.RollbackRelease(globexCtx(), "rel-acme-001"); err == nil {
		t.Fatal("跨租户回滚应失败")
	}
}

// TestCreateRepoDefaults 验证仓库默认值填充。
func TestCreateRepoDefaults(t *testing.T) {
	s := NewStore(wlmemory.NewStore())
	ctx := acmeCtx()

	if err := s.CreateRepo(ctx, devops.CodeRepo{
		AppID: "app-cs", GitURL: "https://github.com/acme/new.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}

	repos, _ := s.ListRepos(ctx, "app-cs")
	var created devops.CodeRepo
	for _, r := range repos {
		if r.GitURL == "https://github.com/acme/new.git" {
			created = r
		}
	}
	if created.ID == "" {
		t.Fatal("应找到新建仓库")
	}
	if created.Dockerfile != devops.DefaultDockerfile || created.BuildContext != devops.DefaultBuildContext {
		t.Fatalf("Dockerfile/BuildContext 应填默认值: %+v", created)
	}
	if created.Status != devops.RepoStatusActive {
		t.Fatalf("Status 应默认 active")
	}
	if created.TenantID != "t-acme" {
		t.Fatalf("TenantID 应由 ctx 写入")
	}
}
