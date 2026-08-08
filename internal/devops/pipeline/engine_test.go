package pipeline

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/pkg/tenant"
)

// ---------- fake BuildRunner / Releaser ----------

type fakeBuilder struct {
	buildStatus string // PollBuildRun 返的 status
	imageID     string
}

func (f fakeBuilder) CreateBuildRun(ctx context.Context, appID, repoID, branch, commit string, buildArgs map[string]string) (devops.BuildRun, error) {
	return devops.BuildRun{ID: "b1", AppID: appID, RepoID: repoID, Branch: branch, Commit: commit, Status: devops.BuildRunning}, nil
}

func (f fakeBuilder) PollBuildRun(ctx context.Context, buildID string) (devops.BuildRun, error) {
	return devops.BuildRun{ID: buildID, Status: f.buildStatus, ImageID: f.imageID}, nil
}

type fakeReleaser struct {
	imageID    string // LatestReadyImage 返回
	latestErr  error
	deployErr  error // CreateRelease 错误（可选）
}

func (f fakeReleaser) CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error) {
	if f.deployErr != nil {
		return devops.Release{}, f.deployErr
	}
	return devops.Release{ID: "rel-1", AppID: input.AppID, EnvID: input.EnvID, ImageID: input.ImageID, WorkloadID: "wl-1"}, nil
}
func (f fakeReleaser) PollWorkloadReady(ctx context.Context, workloadID string) error  { return nil }
func (f fakeReleaser) WorkloadDomain(ctx context.Context, workloadID string) string     { return workloadID + ".svc.cluster.local" }
func (f fakeReleaser) LatestReadyImage(ctx context.Context, appID string) (string, error) {
	return f.imageID, f.latestErr
}

// ---------- 测试辅助 ----------

func acmeCtxEngine() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

// seedBuildDeployPipeline 建 pipeline [build, deploy(priorBuild)] + run，返 runID。
func seedBuildDeployPipeline(t *testing.T, s *memoryStore, deployParams map[string]any) (string, PipelineRun) {
	t.Helper()
	ctx := acmeCtxEngine()
	p, err := s.CreatePipeline(ctx, Pipeline{
		Name: "p-eng", AppID: "app-eng", Kind: KindCI,
		Stages: []StageDef{
			{Name: "构建", Type: StageBuild},
			{Name: "部署", Type: StageDeploy, Params: deployParams},
		},
	})
	if err != nil {
		t.Fatalf("CreatePipeline 失败: %v", err)
	}
	r, err := s.CreateRun(ctx, PipelineRun{
		PipelineID: p.ID, AppID: p.AppID, Branch: "main", Commit: "abc123", RepoID: "repo-1",
		Trigger: "manual", Status: RunRunning, CurrentStage: 0,
		StageRuns: []StageRun{
			{Index: 0, Type: StageBuild, Name: "构建", Status: StagePending},
			{Index: 1, Type: StageDeploy, Name: "部署", Status: StagePending},
		},
	})
	if err != nil {
		t.Fatalf("CreateRun 失败: %v", err)
	}
	return r.ID, r
}

// ---------- 测试 ----------

func TestEngineBuildDeployChain(t *testing.T) {
	s := NewMemoryStore()
	runID, _ := seedBuildDeployPipeline(t, s, map[string]any{
		"envId": "env-dev", "imageSource": ImagePriorBuild, "strategy": "rolling",
	})

	eng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-1"},
		Releases: fakeReleaser{},
	}
	if err := eng.Advance(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunSucceeded {
		t.Fatalf("run.Status 期望 succeeded，got %s", run.Status)
	}
	if len(run.StageRuns) != 2 {
		t.Fatalf("期望 2 stageRuns，got %d", len(run.StageRuns))
	}
	// build stage 输出 imageId
	if run.StageRuns[0].Output[OutImageID] != "img-1" {
		t.Fatalf("build Output.imageId 期望 img-1，got %v", run.StageRuns[0].Output[OutImageID])
	}
	if run.StageRuns[0].Status != StageSuccess {
		t.Fatalf("build stage 期望 success，got %s", run.StageRuns[0].Status)
	}
	// deploy stage 输出 releaseId + workloadDomain
	if run.StageRuns[1].Output[OutReleaseID] != "rel-1" {
		t.Fatalf("deploy Output.releaseId 期望 rel-1，got %v", run.StageRuns[1].Output[OutReleaseID])
	}
	if run.StageRuns[1].Output[OutWorkloadDomain] != "wl-1.svc.cluster.local" {
		t.Fatalf("deploy Output.workloadDomain 期望 wl-1.svc，got %v", run.StageRuns[1].Output[OutWorkloadDomain])
	}
	// priorBuild 链：deploy Input.imageId 来自前序 build Output.imageId
	if run.StageRuns[1].Input["imageId"] != "img-1" {
		t.Fatalf("deploy Input.imageId（priorBuild 链）期望 img-1，got %v", run.StageRuns[1].Input["imageId"])
	}
	if run.CurrentStage != 2 {
		t.Fatalf("CurrentStage 期望 2，got %d", run.CurrentStage)
	}
}

func TestEngineBuildFailed(t *testing.T) {
	s := NewMemoryStore()
	runID, _ := seedBuildDeployPipeline(t, s, map[string]any{
		"envId": "env-dev", "imageSource": ImagePriorBuild,
	})

	eng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildFailed, imageID: ""},
		Releases: fakeReleaser{},
	}
	if err := eng.Advance(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunFailed {
		t.Fatalf("build 失败后 run.Status 期望 failed，got %s", run.Status)
	}
	if run.StageRuns[0].Status != StageFailed {
		t.Fatalf("build stage 期望 failed，got %s", run.StageRuns[0].Status)
	}
	if run.StageRuns[0].Error == "" {
		t.Fatal("build stage Error 应非空")
	}
	// deploy stage 不应执行
	if run.CurrentStage != 0 {
		t.Fatalf("build 失败 CurrentStage 期望 0，got %d", run.CurrentStage)
	}
}

func TestResolveImageSources(t *testing.T) {
	eng := &Engine{Releases: fakeReleaser{imageID: "img-latest"}}
	ctx := context.Background()

	t.Run("selected", func(t *testing.T) {
		stage := StageDef{Type: StageDeploy, Params: map[string]any{
			"imageSource": ImageSelected, "imageId": "img-x",
		}}
		id, err := eng.resolveImage(ctx, stage, PipelineRun{})
		if err != nil || id != "img-x" {
			t.Fatalf("selected 期望 img-x，got %q err %v", id, err)
		}
	})

	t.Run("selected_missing_imageId", func(t *testing.T) {
		stage := StageDef{Type: StageDeploy, Params: map[string]any{"imageSource": ImageSelected}}
		if _, err := eng.resolveImage(ctx, stage, PipelineRun{}); err == nil {
			t.Fatal("selected 缺 imageId 应报错")
		}
	})

	t.Run("latestReady", func(t *testing.T) {
		stage := StageDef{Type: StageDeploy, Params: map[string]any{"imageSource": ImageLatestReady}}
		id, err := eng.resolveImage(ctx, stage, PipelineRun{AppID: "app-1"})
		if err != nil || id != "img-latest" {
			t.Fatalf("latestReady 期望 img-latest，got %q err %v", id, err)
		}
	})

	t.Run("priorBuild", func(t *testing.T) {
		stage := StageDef{Type: StageDeploy, Params: map[string]any{"imageSource": ImagePriorBuild}}
		run := PipelineRun{
			CurrentStage: 1,
			StageRuns: []StageRun{
				{Index: 0, Output: map[string]any{OutImageID: "img-from-build"}},
			},
		}
		id, err := eng.resolveImage(ctx, stage, run)
		if err != nil || id != "img-from-build" {
			t.Fatalf("priorBuild 期望 img-from-build，got %q err %v", id, err)
		}
	})

	t.Run("priorBuild_no_prior", func(t *testing.T) {
		stage := StageDef{Type: StageDeploy, Params: map[string]any{"imageSource": ImagePriorBuild}}
		run := PipelineRun{CurrentStage: 0} // 无前序
		if _, err := eng.resolveImage(ctx, stage, run); err == nil {
			t.Fatal("priorBuild 无前序应报错")
		}
	})
}

func TestResolvePriorOutput(t *testing.T) {
	run := PipelineRun{
		CurrentStage: 2,
		StageRuns: []StageRun{
			{Index: 0, Output: map[string]any{OutImageID: "img-0"}},
			{Index: 1, Output: map[string]any{OutReleaseID: "rel-1"}}, // 不含 imageId
		},
	}
	// 从 stage 0 找到 imageId（跳过 stage 1）
	id, err := resolvePriorOutput(run, OutImageID)
	if err != nil || id != "img-0" {
		t.Fatalf("向前扫描期望 img-0，got %q err %v", id, err)
	}
	// releaseId 在 stage 1
	rel, err := resolvePriorOutput(run, OutReleaseID)
	if err != nil || rel != "rel-1" {
		t.Fatalf("期望 rel-1，got %q err %v", rel, err)
	}
	// 不存在的 key
	if _, err := resolvePriorOutput(run, OutMergeSHA); err == nil {
		t.Fatal("不存在的 key 应报错")
	}
}

func TestStrOrAndGetStringMap(t *testing.T) {
	params := map[string]any{
		"envId":   "env-prod",
		"empty":   "",
		"num":     42,
		"buildArgs": map[string]any{"KEY1": "v1", "KEY2": "v2", "BAD": 99},
	}
	if got := strOr(params, "envId", "def"); got != "env-prod" {
		t.Fatalf("strOr envId 期望 env-prod，got %q", got)
	}
	if got := strOr(params, "empty", "def"); got != "def" {
		t.Fatalf("strOr 空值应返 def，got %q", got)
	}
	if got := strOr(params, "missing", "def"); got != "def" {
		t.Fatalf("strOr 缺失应返 def，got %q", got)
	}
	if got := strOr(params, "num", "def"); got != "def" {
		t.Fatalf("strOr 非 string 应返 def，got %q", got)
	}
	m := getStringMap(params, "buildArgs")
	if m["KEY1"] != "v1" || m["KEY2"] != "v2" {
		t.Fatalf("getStringMap 期望 KEY1=v1 KEY2=v2，got %v", m)
	}
	if _, ok := m["BAD"]; ok {
		t.Fatal("getStringMap 应跳过非 string 值")
	}
}
