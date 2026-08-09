package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/gitea"
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
	latestErr error
	deployErr error // Deploy 错误（可选）
	domain    string // Deploy/WorkloadDomain 返回（空则默认 wl-{id}.svc.cluster.local)

	deployLane string // Deploy 收到的 lane（断言用）

	promoteRel  devops.Release // Promote 返回（空则默认 Release{ID:"rel-promoted"}）
	promoteErr  error
	versionIDs  []string // SetVersion 收到的 releaseIDs（最后一次）
	versionSet  string   // SetVersion 收到的 version
}

func (f *fakeReleaser) CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error) {
	if f.deployErr != nil {
		return devops.Release{}, f.deployErr
	}
	return devops.Release{ID: "rel-1", AppID: input.AppID, EnvID: input.EnvID, ImageID: input.ImageID, WorkloadID: "wl-1"}, nil
}
func (f *fakeReleaser) PollWorkloadReady(ctx context.Context, workloadID string) error { return nil }
func (f *fakeReleaser) WorkloadDomain(ctx context.Context, workloadID string) string {
	if f.domain != "" {
		return f.domain
	}
	return workloadID + ".svc.cluster.local"
}
func (f *fakeReleaser) LatestReadyImage(ctx context.Context, appID string) (string, error) {
	return f.imageID, f.latestErr
}
func (f *fakeReleaser) Promote(ctx context.Context, srcReleaseID string) (devops.Release, error) {
	if f.promoteErr != nil {
		return devops.Release{}, f.promoteErr
	}
	if f.promoteRel.ID != "" {
		return f.promoteRel, nil
	}
	return devops.Release{ID: "rel-promoted"}, nil
}
func (f *fakeReleaser) SetVersion(ctx context.Context, releaseIDs []string, version string) error {
	f.versionIDs = releaseIDs
	f.versionSet = version
	return nil
}

// Deploy stub：返有效部署记录（rel-fake/wl-fake）+ 尊重 domain 字段，供 execDeploy 链路测试可控。
func (f *fakeReleaser) Deploy(ctx context.Context, appID, envID, lane, imageID, sourceRunID string) (devops.Release, string, error) {
	f.deployLane = lane
	if f.deployErr != nil {
		return devops.Release{}, "", f.deployErr
	}
	wlID := "wl-fake"
	domain := f.domain
	if domain == "" {
		domain = wlID + ".svc.cluster.local"
	}
	return devops.Release{ID: "rel-fake", WorkloadID: wlID}, domain, nil
}

// Publish stub（Task 5/6 集成测试覆盖真实语义；本 task 仅满足接口编译）。
func (f *fakeReleaser) Publish(ctx context.Context, appID, imageID, version, commit string) (string, error) {
	return "", nil
}

// publishCapturingReleaser 嵌入 fakeReleaser 复用既有 stub，仅覆盖 Publish 记录入参。
// execRelease 主路径断言：Publish 收到 version + imageID，run.Version 被写入。
type publishCapturingReleaser struct {
	*fakeReleaser
	publishVersion string
	publishImageID string
}

func (p *publishCapturingReleaser) Publish(ctx context.Context, appID, imageID, version, commit string) (string, error) {
	p.publishVersion = version
	p.publishImageID = imageID
	return "sha256:abcdef1234567890abcdef1234567890", nil
}

// fakeGiteaMerger 桥接 GiteaMerger（baseline merge 测试）。
type fakeGiteaMerger struct {
	owner     string
	repo      string
	repoErr   error
	mergeSHA  string
	mergeErr  error
}

func (g *fakeGiteaMerger) ResolveRepo(ctx context.Context, appID string) (string, string, error) {
	if g.repoErr != nil {
		return "", "", g.repoErr
	}
	owner := g.owner
	if owner == "" {
		owner = "paas-bot"
	}
	repo := g.repo
	if repo == "" {
		repo = appID
	}
	return owner, repo, nil
}
func (g *fakeGiteaMerger) Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error) {
	return g.mergeSHA, g.mergeErr
}

// ---------- 测试辅助 ----------

func acmeCtxEngine() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

// seedBuildDeployPipeline 建 pipeline [build, deploy(priorBuild)] + run，返 runID。
// 绑定模型：Pipeline 不带 Stages（运行时从模板解析）；engine 用 run.StageRuns[i].Input 作 stage.Params，
// 故 deploy StageRun 显式存 Input=deployParams（build stage 无 params，Input 空）。
func seedBuildDeployPipeline(t *testing.T, s *memoryStore, deployParams map[string]any) (string, PipelineRun) {
	t.Helper()
	ctx := acmeCtxEngine()
	p, err := s.CreatePipeline(ctx, Pipeline{
		Name: "p-eng", AppID: "app-eng", Kind: KindCI, TemplateID: "tpl-test",
	})
	if err != nil {
		t.Fatalf("CreatePipeline 失败: %v", err)
	}
	r, err := s.CreateRun(ctx, PipelineRun{
		PipelineID: p.ID, AppID: p.AppID, Branch: "main", Commit: "abc123", RepoID: "repo-1",
		Trigger: "manual", Status: RunRunning, CurrentStage: 0,
		StageRuns: []StageRun{
			{Index: 0, Type: StageBuild, Name: "构建", Status: StagePending},
			{Index: 1, Type: StageDeploy, Name: "部署", Status: StagePending, Input: deployParams},
		},
	})
	if err != nil {
		t.Fatalf("CreateRun 失败: %v", err)
	}
	return r.ID, r
}

// ---------- 测试 ----------

func TestExecDeployUsesLaneAndLogs(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-lane", "app-lane", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "lane": "feature-x",
			"imageSource": ImageSelected, "imageId": "img-1",
		}},
	})

	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunSucceeded {
		t.Fatalf("期望 succeeded，got %s", run.Status)
	}
	sr := run.StageRuns[0]
	// Deploy 收到 lane=feature-x
	if rel.deployLane != "feature-x" {
		t.Errorf("Deploy 收到 lane=%q, want feature-x", rel.deployLane)
	}
	// StageRun.Input.lane 记录泳道（Task 6/7 + 前端消费）
	if sr.Input["lane"] != "feature-x" {
		t.Errorf("StageRun.Input.lane 期望 feature-x，got %v", sr.Input["lane"])
	}
	// Log 含 lane 标识 + Workload 就绪事件
	if !strings.Contains(sr.Log, "feature-x") {
		t.Errorf("StageRun.Log 应含 lane=feature-x，got %q", sr.Log)
	}
	if !strings.Contains(sr.Log, "Workload 就绪") {
		t.Errorf("StageRun.Log 应含 Workload 就绪事件，got %q", sr.Log)
	}
	// deploy Output 链（Task 6 release stage 依赖）
	if sr.Output[OutReleaseID] != "rel-fake" {
		t.Errorf("deploy Output.releaseId 期望 rel-fake，got %v", sr.Output[OutReleaseID])
	}
}

func TestEngineBuildDeployChain(t *testing.T) {
	s := NewMemoryStore()
	runID, _ := seedBuildDeployPipeline(t, s, map[string]any{
		"envId": "env-dev", "imageSource": ImagePriorBuild, "strategy": "rolling",
	})

	eng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-1"},
		Releases: &fakeReleaser{},
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
	// deploy stage 输出 releaseId + workloadDomain（经 Releaser.Deploy）
	if run.StageRuns[1].Output[OutReleaseID] != "rel-fake" {
		t.Fatalf("deploy Output.releaseId 期望 rel-fake，got %v", run.StageRuns[1].Output[OutReleaseID])
	}
	if run.StageRuns[1].Output[OutWorkloadDomain] != "wl-fake.svc.cluster.local" {
		t.Fatalf("deploy Output.workloadDomain 期望 wl-fake.svc，got %v", run.StageRuns[1].Output[OutWorkloadDomain])
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
		Releases: &fakeReleaser{},
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
	eng := &Engine{Releases: &fakeReleaser{imageID: "img-latest"}}
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

// seedPipeline 建 pipeline + run（stages 由调用方传，用于构造 StageRuns），返 run。
// 绑定模型：Pipeline 不带 Stages（用占位 TemplateID="tpl-test"）；StageRuns 显式存 Input=stages[i].Params，
// engine.Advance 用 run.StageRuns[i].Input 作 stage.Params（不再读 Pipeline.Stages）。
// 测试直接调 CreateRun 不走 triggerRun，故模板无需真实存在。
func seedPipeline(t *testing.T, s *memoryStore, name, appID, kind string, stages []StageDef) PipelineRun {
	t.Helper()
	ctx := acmeCtxEngine()
	p, err := s.CreatePipeline(ctx, Pipeline{
		Name: name, AppID: appID, Kind: kind, TemplateID: "tpl-test",
	})
	if err != nil {
		t.Fatalf("CreatePipeline 失败: %v", err)
	}
	stageRuns := make([]StageRun, len(stages))
	for i, st := range stages {
		stageRuns[i] = StageRun{Index: i, Type: st.Type, Name: st.Name, Status: StagePending, Input: st.Params}
	}
	r, err := s.CreateRun(ctx, PipelineRun{
		PipelineID: p.ID, AppID: p.AppID, Branch: "main", Commit: "abc123", RepoID: "repo-1",
		Trigger: "manual", Status: RunRunning, CurrentStage: 0, StageRuns: stageRuns,
	})
	if err != nil {
		t.Fatalf("CreateRun 失败: %v", err)
	}
	return r
}

func TestEngineTestSmokeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-smoke", "app-smoke", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
		}},
		{Name: "冒烟", Type: StageTest, Params: map[string]any{"mode": TestSmoke, "path": "/livez"}},
	})

	eng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{},
		Releases: &fakeReleaser{domain: srv.Listener.Addr().String()},
	}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunSucceeded {
		t.Fatalf("run.Status 期望 succeeded，got %s", run.Status)
	}
	if run.StageRuns[1].Status != StageSuccess {
		t.Fatalf("test stage 期望 success，got %s", run.StageRuns[1].Status)
	}
	if run.StageRuns[1].Output["result"] != "ok" {
		t.Fatalf("test Output.result 期望 ok，got %v", run.StageRuns[1].Output["result"])
	}
}

func TestEngineTestManualPause(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-manual", "app-manual", KindCI, []StageDef{
		{Name: "人工测试", Type: StageTest, Params: map[string]any{"mode": TestManual}},
	})

	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunPaused {
		t.Fatalf("test-manual 期望 paused，got %s", run.Status)
	}
	if run.StageRuns[0].Status != StageWaiting {
		t.Fatalf("test stage 期望 waiting，got %s", run.StageRuns[0].Status)
	}
	if run.StageRuns[0].Input["mode"] != TestManual {
		t.Fatalf("test Input.mode 期望 manual，got %v", run.StageRuns[0].Input["mode"])
	}
}

func TestEngineApprovePauseResume(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-appr", "app-appr", KindCD, []StageDef{
		{Name: "审批", Type: StageApprove},
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-prod", "imageSource": ImageSelected, "imageId": "img-1",
		}},
	})

	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	// 第一次 advance -> paused at approve
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 第一次: %v", err)
	}
	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunPaused {
		t.Fatalf("期望 paused，got %s", run.Status)
	}
	if run.StageRuns[0].Status != StageWaiting {
		t.Fatalf("approve stage 期望 waiting，got %s", run.StageRuns[0].Status)
	}
	if run.CurrentStage != 0 {
		t.Fatalf("paused 时 CurrentStage 期望 0，got %d", run.CurrentStage)
	}

	// Resume（异步推进，轮询等终态）
	if err := eng.Resume(acmeCtxEngine(), r.ID, 0); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = s.GetRun(acmeCtxEngine(), r.ID)
		if run.Status == RunSucceeded || run.Status == RunFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("Resume 后期望 succeeded，got %s", run.Status)
	}
	if run.CurrentStage != 2 {
		t.Fatalf("CurrentStage 期望 2，got %d", run.CurrentStage)
	}
	if run.StageRuns[0].Status != StageSuccess {
		t.Fatalf("approve stage 恢复后期望 success，got %s", run.StageRuns[0].Status)
	}
}

func TestEngineResumeGuards(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-guard", "app-guard", KindCD, []StageDef{
		{Name: "审批", Type: StageApprove},
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}

	// 未 paused 时 Resume -> ErrNotPaused
	if err := eng.Resume(acmeCtxEngine(), r.ID, 0); err != ErrNotPaused {
		t.Fatalf("未 paused Resume 期望 ErrNotPaused，got %v", err)
	}
	// advance -> paused
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	// stageIdx 不匹配 -> ErrStageNotCurrent
	if err := eng.Resume(acmeCtxEngine(), r.ID, 1); err != ErrStageNotCurrent {
		t.Fatalf("stageIdx 不匹配期望 ErrStageNotCurrent，got %v", err)
	}
}

func TestEnginePromote(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-promote", "app-promote", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
		}},
		{Name: "晋升", Type: StagePromote},
	})

	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunSucceeded {
		t.Fatalf("期望 succeeded，got %s", run.Status)
	}
	if run.StageRuns[1].Status != StageSuccess {
		t.Fatalf("promote stage 期望 success，got %s", run.StageRuns[1].Status)
	}
	// promote 取前序 deploy 的 releaseId="rel-1" -> Promote 返 rel-promoted
	if run.StageRuns[1].Output[OutReleaseID] != "rel-promoted" {
		t.Fatalf("promote Output.releaseId 期望 rel-promoted，got %v", run.StageRuns[1].Output[OutReleaseID])
	}
}

func TestEngineBaselineVersion(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-base", "app-base", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
		}},
		{Name: "基线", Type: StageBaseline, Params: map[string]any{"versionStrategy": "auto-increment"}},
	})

	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel} // Gitea=nil 跳过 merge
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunSucceeded {
		t.Fatalf("期望 succeeded，got %s", run.Status)
	}
	if run.Version == "" {
		t.Fatal("run.Version 期望非空")
	}
	if run.StageRuns[1].Output[OutVersion] != run.Version {
		t.Fatalf("baseline Output.version 期望 %s，got %v", run.Version, run.StageRuns[1].Output[OutVersion])
	}
	// SetVersion 被调，传入 deploy 的 releaseId + version
	if len(rel.versionIDs) == 0 {
		t.Fatal("SetVersion 应被调用")
	}
	if rel.versionSet != run.Version {
		t.Fatalf("SetVersion version 期望 %s，got %s", run.Version, rel.versionSet)
	}
}

func TestEngineBaselineMergeConflict(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-conflict", "app-conflict", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
		}},
		{Name: "基线", Type: StageBaseline, Params: map[string]any{"mainBranch": "main"}},
	})

	eng := &Engine{
		Pipelines: s, Runs: s, Builds: fakeBuilder{},
		Releases: &fakeReleaser{},
		Gitea:   &fakeGiteaMerger{mergeErr: gitea.ErrMergeConflict},
	}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	// merge 冲突不中止，baseline 仍 success（版本已打）
	if run.Status != RunSucceeded {
		t.Fatalf("merge 冲突后 baseline 期望 success（版本已打），got %s", run.Status)
	}
	if run.Version == "" {
		t.Fatal("版本应已打")
	}
	if run.StageRuns[1].Error != "合并冲突，请手动解决" {
		t.Fatalf("sr.Error 期望「合并冲突」，got %q", run.StageRuns[1].Error)
	}
}

// buildRunWithPriorDeploy 构造 CurrentStage 指向 release、且前序 stage 已产出 imageId + releaseId 的 run。
// StageRuns[0]=deploy（已完成 success，Output 含 imageId/releaseId），StageRuns[1]=release（待执行）。
func buildRunWithPriorDeploy() PipelineRun {
	return PipelineRun{
		ID:           "run-rel-1",
		AppID:        "app-rel",
		Branch:       "main",
		Commit:       "abc123def456",
		RepoID:       "repo-1",
		Status:       RunRunning,
		CurrentStage: 1,
		StageRuns: []StageRun{
			{
				Index:  0,
				Type:   StageDeploy,
				Status: StageSuccess,
				Output: map[string]any{OutImageID: "img-from-deploy", OutReleaseID: "rel-deployed"},
			},
			{Index: 1, Type: StageRelease, Name: "发布", Status: StagePending},
		},
	}
}

func TestExecReleasePublishesVersion(t *testing.T) {
	run := buildRunWithPriorDeploy()
	pub := &publishCapturingReleaser{fakeReleaser: &fakeReleaser{}}
	e := &Engine{Pipelines: NewMemoryStore(), Runs: NewMemoryStore(), Builds: fakeBuilder{}, Releases: pub}

	stage := StageDef{Type: StageRelease, Params: map[string]any{"versionStrategy": "auto-increment"}}
	sr := &run.StageRuns[run.CurrentStage]
	finished, err := e.execStage(acmeCtxEngine(), &run, stage, sr)
	if err != nil {
		t.Fatalf("execRelease: %v", err)
	}
	if !finished {
		t.Fatal("execRelease 应 finished=true")
	}
	if pub.publishVersion == "" {
		t.Error("Publish 未收到版本号")
	}
	if pub.publishImageID != "img-from-deploy" {
		t.Errorf("Publish imageID 期望 img-from-deploy，got %q", pub.publishImageID)
	}
	if run.Version == "" {
		t.Error("PipelineRun.version 未写入")
	}
	if run.Version != pub.publishVersion {
		t.Errorf("run.Version(%q) != Publish 收到的 version(%q)", run.Version, pub.publishVersion)
	}
	if sr.Output[OutVersion] != run.Version {
		t.Errorf("sr.Output.version 期望 %s，got %v", run.Version, sr.Output[OutVersion])
	}
	// SetVersion 应被调（前序 deploy 输出了 releaseId）
	if len(pub.versionIDs) == 0 || pub.versionIDs[0] != "rel-deployed" {
		t.Errorf("SetVersion releaseIDs 期望 [rel-deployed]，got %v", pub.versionIDs)
	}
	if pub.versionSet != run.Version {
		t.Errorf("SetVersion version 期望 %s，got %s", run.Version, pub.versionSet)
	}
	if sr.Status != StageSuccess {
		t.Errorf("stage 期望 success，got %s", sr.Status)
	}
}

// 无前序 imageId 时（如纯 CD pipeline 直接发布 latestReady 经 deploy 后 deploy 必产 imageId 输出，
// 但若 release stage 前无 deploy——异常配置——应跳过 Publish 仅记录版本号，不报错）。
func TestExecReleaseNoPriorImageSkipsPublish(t *testing.T) {
	run := buildRunWithPriorDeploy()
	// 清空前序 imageId
	delete(run.StageRuns[0].Output, OutImageID)

	e := &Engine{
		Pipelines: NewMemoryStore(), Runs: NewMemoryStore(),
		Builds:   fakeBuilder{},
		Releases: &publishCapturingReleaser{fakeReleaser: &fakeReleaser{}},
	}
	// 直接调 execRelease（不经 store），构造裸 run
	stage := StageDef{Type: StageRelease, Params: map[string]any{"versionStrategy": "auto-increment"}}
	sr := &run.StageRuns[run.CurrentStage]
	pub := e.Releases.(*publishCapturingReleaser)

	finished, err := e.execRelease(context.Background(), &run, stage, sr)
	if err != nil {
		t.Fatalf("execRelease: %v", err)
	}
	if !finished {
		t.Fatal("应 finished")
	}
	if pub.publishVersion != "" {
		t.Errorf("无前序镜像不应调 Publish，got version=%q", pub.publishVersion)
	}
	if run.Version == "" {
		t.Error("版本号仍应写入 run.Version")
	}
	if !strings.Contains(sr.Log, "跳过 tag") {
		t.Errorf("Log 应记录跳过 tag，got %q", sr.Log)
	}
	// SetVersion 仍应回填（releaseId 来自 deploy stage，与 imageId 解耦）
	if len(pub.versionIDs) == 0 {
		t.Error("SetVersion 仍应被调（回填 releaseId）")
	}
}
