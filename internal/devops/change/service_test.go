package change

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/devops/gitea"
)

// fakeBrancher 记录调用序列 + 可注入 merge 冲突。
type fakeBrancher struct {
	created   []string         // branch 名
	deleted   []string         // branch 名
	merges    [][2]string      // {head, base}
	mergeErrs map[string]error // head 分支名 -> 错误（注入冲突）
}

func (f *fakeBrancher) CreateBranch(ctx context.Context, owner, repo, branch, from string) error {
	f.created = append(f.created, branch)
	return nil
}
func (f *fakeBrancher) GetBranch(ctx context.Context, owner, repo, branch string) (gitea.Branch, error) {
	return gitea.Branch{Name: branch, CommitSHA: "sha-" + branch}, nil
}
func (f *fakeBrancher) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	f.deleted = append(f.deleted, branch)
	return nil
}
func (f *fakeBrancher) Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error) {
	f.merges = append(f.merges, [2]string{head, base})
	if err := f.mergeErrs[head]; err != nil {
		return "", err
	}
	return "sha-" + head, nil
}

// fakeRuns 同时实现 RunTrigger + RunReader + 集成 run/流水线查询扩展。
type fakeRuns struct {
	triggered []string // branch
	status    string
	releases  []string
	pipeline  string // FindPipeline 返回
}

func (f *fakeRuns) TriggerAppRun(ctx context.Context, appID, pid, branch string) (string, error) {
	f.triggered = append(f.triggered, branch)
	return "run-" + branch, nil
}
func (f *fakeRuns) GetRunStatus(ctx context.Context, runID string) (string, error) {
	return f.status, nil
}
func (f *fakeRuns) FindPipeline(ctx context.Context, appID, kind string) (string, error) {
	return f.pipeline, nil
}
func (f *fakeRuns) ReleasesOfRun(ctx context.Context, runID string) ([]string, error) {
	return f.releases, nil
}

func newTestService(t *testing.T) (*Service, *MemoryStore, *fakeBrancher, *fakeRuns) {
	t.Helper()
	store := NewMemoryStore()
	g, runs := &fakeBrancher{}, &fakeRuns{pipeline: "pipe-ci", status: "succeeded"}
	s := NewService(store, WithGitea(g), WithRunTrigger(runs), WithRunReader(runs),
		WithRepoLookup(func(ctx context.Context, appID string) (string, string, string, error) {
			return "paas-bot", "app-1", "repo-1", nil
		}))
	return s, store, g, runs
}

func TestCreateChangeBranchAndExisting(t *testing.T) {
	s, _, g, _ := newTestService(t)
	ctx := acmeCtx()
	// 平台建分支
	c, err := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "导出", Type: ChangeFeat, Branch: "feat/export", CreateBranch: true})
	if err != nil {
		t.Fatalf("建分支创建: %v", err)
	}
	if !c.BranchCreated || len(g.created) != 1 {
		t.Fatalf("应调 Gitea CreateBranch")
	}
	// 引用已有分支（不建）
	c2, err := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "修复", Type: ChangeHotfix, Branch: "hotfix/login", CreateBranch: false})
	if err != nil {
		t.Fatal(err)
	}
	if c2.BranchCreated || len(g.created) != 1 {
		t.Fatalf("引用已有分支不应再建")
	}
}

func TestIntegrateHappyPath(t *testing.T) {
	s, store, g, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a", CreateBranch: false})
	c2, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "b", Type: ChangeFeat, Branch: "feat/b", CreateBranch: false})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "集成", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	b, _ = s.AddChangeToBatch(ctx, b.ID, c2.ID)

	got, err := s.Integrate(ctx, b.ID)
	if err != nil {
		t.Fatalf("Integrate: %v", err)
	}
	if got.Status != BatchTesting {
		t.Fatalf("应 testing, got %s", got.Status)
	}
	// merge 顺序 = ChangeIDs 顺序，base 均为集成分支
	if len(g.merges) != 2 || g.merges[0][0] != "feat/a" || g.merges[0][1] != "integration/x" || g.merges[1][0] != "feat/b" {
		t.Fatalf("merge 序列不符: %v", g.merges)
	}
	// 集成分支重建（先删后建）
	if len(g.deleted) != 1 || g.deleted[0] != "integration/x" {
		t.Fatalf("应重建集成分支: %v", g.deleted)
	}
	// CI run 以集成分支触发
	if len(runs.triggered) != 1 || runs.triggered[0] != "integration/x" {
		t.Fatalf("CI run branch 应=集成分支: %v", runs.triggered)
	}
	// change → integrated
	ch1, _ := store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeIntegrated || ch1.BatchID != b.ID {
		t.Fatalf("change 应 integrated: %+v", ch1)
	}
}

func TestIntegrateConflict(t *testing.T) {
	s, store, g, _ := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	c2, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "b", Type: ChangeFeat, Branch: "feat/b"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.AddChangeToBatch(ctx, b.ID, c2.ID)
	g.mergeErrs = map[string]error{"feat/b": gitea.ErrMergeConflict}

	_, err := s.Integrate(ctx, b.ID)
	var ce *BatchConflictError
	if !errors.As(err, &ce) || ce.FailedChangeID != c2.ID || ce.PrevChangeID != c1.ID {
		t.Fatalf("应返 BatchConflictError(c2 冲突 c1), got %v", err)
	}
	got, _ := store.GetBatch(ctx, b.ID)
	if got.Status != BatchConflict {
		t.Fatalf("批次应 conflict, got %s", got.Status)
	}
	ch2, _ := store.GetChange(ctx, c2.ID)
	if ch2.ConflictWith != c1.ID {
		t.Fatalf("change.ConflictWith 应=c1: %+v", ch2)
	}
	// conflict 状态可移出变更（spec 状态机）
	if _, err := s.RemoveChangeFromBatch(ctx, b.ID, c2.ID); err != nil {
		t.Fatalf("conflict 批次移出变更应允许: %v", err)
	}
}

func TestSyncBatchStatusAdvances(t *testing.T) {
	s, store, _, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.Integrate(ctx, b.ID)

	// CI succeeded → tested
	runs.status = "succeeded"
	got, err := s.SyncBatchStatus(ctx, b.ID)
	if err != nil || got.Status != BatchTested {
		t.Fatalf("应 tested: %+v err=%v", got, err)
	}
	ch1, _ := store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeTested {
		t.Fatalf("change 应 tested: %s", ch1.Status)
	}

	// approve → releasing（清 RunID：防 SyncBatchStatus 读旧 CI run 提前判 released，审计第 2 轮 I1）
	got, err = s.Approve(ctx, b.ID)
	if err != nil || got.Status != BatchReleasing {
		t.Fatalf("approve: %+v err=%v", got, err)
	}

	// releasing 态无 RunID 不推进（守卫验证）
	got, err = s.SyncBatchStatus(ctx, b.ID)
	if err != nil || got.Status != BatchReleasing {
		t.Fatalf("无 RunID 的 releasing 不应推进: %+v err=%v", got, err)
	}

	// Release 触发 CD run（merge 到 main）后才有 RunID
	if _, err := s.Release(ctx, b.ID); err != nil {
		t.Fatalf("release: %v", err)
	}

	// CD succeeded → released + change released + ReleaseIDs 回填
	runs.status = "succeeded"
	runs.releases = []string{"rel-1", "rel-2"}
	got, err = s.SyncBatchStatus(ctx, b.ID)
	if err != nil || got.Status != BatchReleased {
		t.Fatalf("应 released: %+v err=%v", got, err)
	}
	if len(got.ReleaseIDs) != 2 {
		t.Fatalf("ReleaseIDs 应回填: %v", got.ReleaseIDs)
	}
	ch1, _ = store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeReleased {
		t.Fatalf("change 应 released: %s", ch1.Status)
	}
}

func TestSyncBatchStatusCIFailedReopens(t *testing.T) {
	s, store, _, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.Integrate(ctx, b.ID)

	runs.status = "failed"
	got, _ := s.SyncBatchStatus(ctx, b.ID)
	if got.Status != BatchFailed {
		t.Fatalf("应 failed: %s", got.Status)
	}
	ch1, _ := store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeOpen || ch1.BatchID != "" {
		t.Fatalf("change 应回 open 且出批: %+v", ch1)
	}
	// failed 后可重新 integrate（状态机循环）
	if _, err := s.AddChangeToBatch(ctx, b.ID, c1.ID); err != nil {
		t.Fatalf("failed 批次重新收变更应允许: %v", err)
	}
}

func TestReleaseMergesToMainAndTriggersCD(t *testing.T) {
	s, store, g, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.Integrate(ctx, b.ID)
	runs.status = "succeeded"
	_, _ = s.SyncBatchStatus(ctx, b.ID)
	_, _ = s.Approve(ctx, b.ID)

	got, err := s.Release(ctx, b.ID)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	// merge 到 main
	if len(g.merges) < 2 || g.merges[len(g.merges)-1][0] != "feat/a" || g.merges[len(g.merges)-1][1] != "main" {
		t.Fatalf("应有 merge 到 main: %v", g.merges)
	}
	// CD run 以 main 触发
	if runs.triggered[len(runs.triggered)-1] != "main" {
		t.Fatalf("CD run branch 应=main: %v", runs.triggered)
	}
	if got.Status != BatchReleasing {
		t.Fatalf("应 releasing（等 CD 终态）: %s", got.Status)
	}
}

func TestStateMachineGuards(t *testing.T) {
	s, store, _, _ := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	// 空批次 integrate 拒
	if _, err := s.Integrate(ctx, b.ID); err == nil {
		t.Fatal("空批次 integrate 应拒")
	}
	// collecting 才能 approve
	if _, err := s.Approve(ctx, b.ID); err == nil {
		t.Fatal("非 tested 批次 approve 应拒")
	}
	// 已入批变更不能 abandon
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	if _, err := s.AbandonChange(ctx, c1.ID); err == nil {
		t.Fatal("integrated 变更 abandon 应拒（先移出批次）")
	}
}
