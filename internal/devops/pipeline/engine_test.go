package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	imageID   string // LatestReadyImage 返回
	latestErr error
	deployErr error  // Deploy 错误（可选）
	pollErr   error  // PollWorkloadReady 错误（可选，模拟 deploy 后探活失败）
	domain    string // Deploy/WorkloadDomain 返回（空则默认 wl-{id}.svc.cluster.local)

	deployLane      string          // Deploy 收到的 lane（断言用）
	deployImageID   string          // Deploy 收到的 imageID（canary 放量断言用）
	deployPort      int             // Deploy 收到的 port（断言用）
	deploySvcID     string          // Deploy 收到的 serviceID（断言用，服务模型 Phase 1 透传）
	deployResources DeployResources // Deploy 收到的 resources（断言用，资源规格注入）
	deployReplicas  int             // Deploy 收到的 replicas（断言用，联调泳道降级）

	promoteRel devops.Release // Promote 返回（空则默认 Release{ID:"rel-promoted"}）
	promoteErr error
	versionIDs []string // SetVersion 收到的 releaseIDs（最后一次）
	versionSet string   // SetVersion 收到的 version

	deleted   []string // DeleteWorkload 收到的 workloadID（canary 清理断言用）
	deleteErr error    // DeleteWorkload 返回错误（可选）

	mu sync.Mutex // 守护 deploy*/deleted 记录：CanaryResume 后 Start 异步 advance 并发写，测试读需同步
}

// lastLane/lastImage/deletedList 同步读断言字段（防 -race 数据竞争：异步 advance 并发写）。
func (f *fakeReleaser) lastLane() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployLane
}
func (f *fakeReleaser) lastImage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployImageID
}
func (f *fakeReleaser) deletedList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func (f *fakeReleaser) CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error) {
	if f.deployErr != nil {
		return devops.Release{}, f.deployErr
	}
	return devops.Release{ID: "rel-1", AppID: input.AppID, EnvID: input.EnvID, ImageID: input.ImageID, WorkloadID: "wl-1"}, nil
}
func (f *fakeReleaser) PollWorkloadReady(ctx context.Context, workloadID string) error {
	return f.pollErr
}
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
func (f *fakeReleaser) Deploy(ctx context.Context, appID, envID, lane, service, serviceID, imageID string, port, containerPort int, resources DeployResources, replicas int, sourceRunID string) (devops.Release, string, error) {
	f.mu.Lock()
	f.deployLane = lane
	f.deployImageID = imageID
	f.deployPort = port
	f.deploySvcID = serviceID
	f.deployResources = resources
	f.deployReplicas = replicas
	f.mu.Unlock()
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

// DeployCanary stub：转调 Deploy 记录链路，lane 按真实语义派生 canary-<dns1035(sourceRunID)>、
// 单副本，供 execCanary/CanaryResume 断言（deployLane/deployReplicas 记录最后一次部署调用）。
func (f *fakeReleaser) DeployCanary(ctx context.Context, appID, envID, service, serviceID, imageID string, resources DeployResources, sourceRunID string) (devops.Release, string, error) {
	return f.Deploy(ctx, appID, envID, "canary-"+dns1035(sourceRunID), service, serviceID, imageID, 0, 0, resources, 1, sourceRunID)
}

// DeleteWorkload stub：记录到 deleted 切片（canary abort/终止清理断言用）。
func (f *fakeReleaser) DeleteWorkload(ctx context.Context, workloadID string) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, workloadID)
	f.mu.Unlock()
	return f.deleteErr
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
	owner    string
	repo     string
	repoErr  error
	mergeSHA string
	mergeErr error
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
			"port": 8081, "containerPort": 8081,
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
	// Deploy 收到 port=8081（新建 Workload 时设定，驱动 reconciler 建 Service）
	if rel.deployPort != 8081 {
		t.Errorf("Deploy 收到 port=%d, want 8081", rel.deployPort)
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

// TestExecDeployPassesServiceID：deploy stage 的 serviceId param 透传到 Releaser.Deploy
// （服务模型 Phase 1 断链回归——修复前接口无 serviceID 参数，服务实体 Port 永不生效）。
func TestExecDeployPassesServiceID(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-svcid", "app-svcid", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "serviceId": "svc-abc", "service": "journey-web",
			"imageSource": ImageSelected, "imageId": "img-1",
		}},
	})

	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	if rel.deploySvcID != "svc-abc" {
		t.Errorf("Deploy 收到 serviceID=%q, want svc-abc（服务实体断链回归）", rel.deploySvcID)
	}
}

// TestExecCanaryWaitsForConfirmation：canary stage 部署到 canary-<runID> 泳道（replicas=1，基线不动）
// 后暂停等待人工决策——run Paused、stage Waiting、Output 含 canaryWorkloadId/canaryDomain。
func TestExecCanaryWaitsForConfirmation(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-canary", "app-canary", KindCI, []StageDef{
		{Name: "金丝雀验证", Type: StageCanary, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
		}},
	})

	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunPaused {
		t.Fatalf("期望 paused，got %s", run.Status)
	}
	sr := run.StageRuns[0]
	if sr.Status != StageWaiting {
		t.Fatalf("期望 stage waiting，got %s", sr.Status)
	}
	// Deploy 收到并行验证泳道 canary-<runID>
	wantLane := "canary-" + r.ID
	if rel.deployLane != wantLane {
		t.Errorf("Deploy 收到 lane=%q, want %q", rel.deployLane, wantLane)
	}
	// 单副本验证（replicas=1）
	if rel.deployReplicas != 1 {
		t.Errorf("Deploy 收到 replicas=%d, want 1", rel.deployReplicas)
	}
	// Output 链：部署记录 + canary workload + 验证域名
	if sr.Output[OutReleaseID] != "rel-fake" {
		t.Errorf("Output.releaseId 期望 rel-fake，got %v", sr.Output[OutReleaseID])
	}
	if sr.Output[OutCanaryWorkloadID] != "wl-fake" {
		t.Errorf("Output.canaryWorkloadId 期望 wl-fake，got %v", sr.Output[OutCanaryWorkloadID])
	}
	if sr.Output[OutCanaryDomain] == "" {
		t.Error("Output.canaryDomain 不应为空")
	}
	// Input 固化（决策/终止清理消费）
	if sr.Input["envId"] != "env-test" || sr.Input["imageId"] != "img-1" {
		t.Errorf("Input 固化缺失: envId=%v imageId=%v", sr.Input["envId"], sr.Input["imageId"])
	}
	// 基线零调用：未见 default 泳道部署（fakeReleaser 只记录最后一次 Deploy，这里仅 canary 一次）
	if rel.deleted != nil {
		t.Errorf("canary 部署阶段不应删 workload，got %v", rel.deleted)
	}
}

// TestExecCanaryMissingEnvId：canary 无 envId 参数且 Input 无（无前序 deploy）→ stage failed「缺 envId 参数」。
func TestExecCanaryMissingEnvId(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-canary-noenv", "app-canary-noenv", KindCI, []StageDef{
		{Name: "金丝雀验证", Type: StageCanary, Params: map[string]any{
			"imageSource": ImageSelected, "imageId": "img-1",
		}},
	})

	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunFailed {
		t.Fatalf("期望 failed，got %s", run.Status)
	}
	sr := run.StageRuns[0]
	if sr.Status != StageFailed {
		t.Fatalf("期望 stage failed，got %s", sr.Status)
	}
	if !strings.Contains(sr.Error, "缺 envId 参数") {
		t.Errorf("stage.Error 应含「缺 envId 参数」，got %q", sr.Error)
	}
	if rel.deployLane != "" {
		t.Errorf("缺 envId 不应触发 Deploy，got lane=%q", rel.deployLane)
	}
}

// canaryWaitingRun 辅助：Advance 到 canary stage StageWaiting，返回 store/runID/releaser/engine。
func canaryWaitingRun(t *testing.T) (*memoryStore, string, *fakeReleaser, *Engine) {
	t.Helper()
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-canary", "app-canary", KindCI, []StageDef{
		{Name: "金丝雀验证", Type: StageCanary, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
		}},
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
		}},
	})
	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunPaused || run.StageRuns[0].Status != StageWaiting {
		t.Fatalf("期望 paused/waiting，got run=%s stage=%s", run.Status, run.StageRuns[0].Status)
	}
	return s, r.ID, rel, eng
}

// TestCanaryPromoteRollsBaselineAndCleans：确认放量 → 基线全量滚动（lane=default 同 imageId）
// + 删 canary workload + stage Success + run 续推后续 stage。
func TestCanaryPromoteRollsBaselineAndCleans(t *testing.T) {
	s, runID, rel, eng := canaryWaitingRun(t)

	if err := eng.CanaryResume(acmeCtxEngine(), runID, 0, true); err != nil {
		t.Fatalf("CanaryResume(promote): %v", err)
	}
	// 基线放量：lane=default + 同一镜像
	if lane := rel.lastLane(); lane != LaneDefault {
		t.Errorf("放量 Deploy lane=%q, want %q", lane, LaneDefault)
	}
	if img := rel.lastImage(); img != "img-1" {
		t.Errorf("放量 Deploy imageId=%q, want img-1（与 canary 验证镜像一致）", img)
	}
	// canary 负载回收
	if del := rel.deletedList(); len(del) != 1 || del[0] != "wl-fake" {
		t.Errorf("DeleteWorkload 期望 [wl-fake]，got %v", del)
	}
	// run 续推：异步推进到后续 deploy stage 完成
	var run PipelineRun
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = s.GetRun(acmeCtxEngine(), runID)
		if run.Status == RunSucceeded || run.Status == RunFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("放量后期望 succeeded，got %s", run.Status)
	}
	if run.StageRuns[0].Status != StageSuccess {
		t.Errorf("canary stage 期望 success，got %s", run.StageRuns[0].Status)
	}
	if run.CurrentStage != 2 {
		t.Errorf("CurrentStage 期望 2，got %d", run.CurrentStage)
	}
}

// TestCanaryTerminateKeepsBaseline：终止 → 仅删 canary workload，基线 Deploy 未调，run Failed。
func TestCanaryTerminateKeepsBaseline(t *testing.T) {
	s, runID, rel, eng := canaryWaitingRun(t)

	if err := eng.CanaryResume(acmeCtxEngine(), runID, 0, false); err != nil {
		t.Fatalf("CanaryResume(terminate): %v", err)
	}
	if del := rel.deletedList(); len(del) != 1 || del[0] != "wl-fake" {
		t.Errorf("DeleteWorkload 期望 [wl-fake]，got %v", del)
	}
	if lane := rel.lastLane(); lane != "" && lane != "canary-"+dns1035(runID) {
		// Advance 期 DeployCanary 记录了 canary-<runID>；终止决策不得再触发基线部署
		t.Errorf("终止不应触发基线 Deploy（lane=%q）", lane)
	}
	run, _ := s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunFailed {
		t.Fatalf("终止后期望 failed，got %s", run.Status)
	}
	sr := run.StageRuns[0]
	if sr.Status != StageFailed {
		t.Errorf("canary stage 期望 failed，got %s", sr.Status)
	}
	if !strings.Contains(sr.Error, "终止") {
		t.Errorf("stage.Error 应含「终止」，got %q", sr.Error)
	}
	if run.FinishedAt.IsZero() {
		t.Error("run.FinishedAt 应已设置")
	}
}

// TestCanaryAbortCleansWorkload：canary waiting 期 Abort → DeleteWorkload 调 + run Aborted。
func TestCanaryAbortCleansWorkload(t *testing.T) {
	s, runID, rel, eng := canaryWaitingRun(t)

	if err := eng.Abort(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if del := rel.deletedList(); len(del) != 1 || del[0] != "wl-fake" {
		t.Errorf("Abort 应清理 canary workload，got %v", del)
	}
	run, _ := s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunAborted {
		t.Fatalf("期望 aborted，got %s", run.Status)
	}
	if run.StageRuns[0].Status != StageAborted {
		t.Errorf("canary stage 期望 aborted，got %s", run.StageRuns[0].Status)
	}
}

// TestCanaryConcurrentDecisionSingleWinner：并发相反决策（promote×terminate）只有一方生效——
// 认领 CAS（stage Waiting→Running 锁内持久化）在副作用之前拦截第二方（审查 I1 回归）。
func TestCanaryConcurrentDecisionSingleWinner(t *testing.T) {
	s, runID, rel, eng := canaryWaitingRun(t)

	errCh := make(chan error, 2)
	go func() { errCh <- eng.CanaryResume(acmeCtxEngine(), runID, 0, true) }()
	go func() { errCh <- eng.CanaryResume(acmeCtxEngine(), runID, 0, false) }()

	promoteWon := false
	wins := 0
	for i := 0; i < 2; i++ {
		if err := <-errCh; err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("并发相反决策应恰一方成功，got %d（errors 见 goroutine）", wins)
	}
	run, _ := s.GetRun(acmeCtxEngine(), runID)
	// 胜者是谁由调度决定，但终态必须与胜者决策一致且副作用一致
	if run.Status == RunRunning || run.Status == RunSucceeded {
		promoteWon = true
	}
	if run.Status != RunRunning && run.Status != RunSucceeded && run.Status != RunFailed {
		t.Fatalf("终态异常: %s", run.Status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = s.GetRun(acmeCtxEngine(), runID)
		if run.Status == RunSucceeded || run.Status == RunFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if promoteWon {
		if run.Status != RunSucceeded {
			t.Fatalf("放量胜出期望 succeeded，got %s", run.Status)
		}
		if lane := rel.lastLane(); lane != LaneDefault {
			t.Errorf("放量胜出应触发基线 Deploy lane=default，got %q", lane)
		}
	} else {
		if run.Status != RunFailed {
			t.Fatalf("终止胜出期望 failed，got %s", run.Status)
		}
		if lane := rel.lastLane(); lane != "" && lane != "canary-"+dns1035(runID) {
			t.Errorf("终止胜出不应触发基线 Deploy（lane=%q）", lane)
		}
	}
	// canary workload 恰删一次（两决策均会删，败者在副作用前被拒不应删）
	if del := rel.deletedList(); len(del) != 1 {
		t.Errorf("canary workload 应恰删一次，got %v", del)
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
		"envId":     "env-prod",
		"empty":     "",
		"num":       42,
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
	return seedPipelineBranch(t, s, name, appID, kind, stages, "main")
}

// seedPipelineBranch 同 seedPipeline，分支可指定（baseline merge 场景需 feature 分支）。
func seedPipelineBranch(t *testing.T, s *memoryStore, name, appID, kind string, stages []StageDef, branch string) PipelineRun {
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
		PipelineID: p.ID, AppID: p.AppID, Branch: branch, Commit: "abc123", RepoID: "repo-1",
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

	rc := &versionCapturingReleaser{fakeReleaser: &fakeReleaser{}}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rc} // Gitea=nil 跳过 merge
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunSucceeded {
		t.Fatalf("期望 succeeded，got %s", run.Status)
	}
	// Task 7：baseline 瘦身，不再打版本（版本归 release stage）。
	// 兼容旧模板（baseline stage 无 mainBranch）-> 跳过合并直接 success。
	if rc.setVersionCalled {
		t.Error("baseline 不应再调 SetVersion（版本归 release stage）")
	}
	if run.Version != "" {
		t.Errorf("baseline 不应写 run.Version，got %q", run.Version)
	}
	if _, ok := run.StageRuns[1].Output[OutVersion]; ok {
		t.Error("baseline Output 不应含 version")
	}
}

func TestEngineBaselineMergeConflict(t *testing.T) {
	s := NewMemoryStore()
	// 用 feature 分支触发（≠ mainBranch=main），使 merge 真正被调用以测冲突路径。
	r := seedPipelineBranch(t, s, "p-conflict", "app-conflict", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
		}},
		{Name: "基线", Type: StageBaseline, Params: map[string]any{"mainBranch": "main"}},
	}, "feature-x")

	eng := &Engine{
		Pipelines: s, Runs: s, Builds: fakeBuilder{},
		Releases: &fakeReleaser{},
		Gitea:    &fakeGiteaMerger{mergeErr: gitea.ErrMergeConflict},
	}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}

	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	// merge 冲突不中止，baseline 仍 success（合并可手动补）
	if run.Status != RunSucceeded {
		t.Fatalf("merge 冲突后 baseline 期望 success，got %s", run.Status)
	}
	if run.StageRuns[1].Error != "合并冲突，请手动解决" {
		t.Fatalf("sr.Error 期望「合并冲突」，got %q", run.StageRuns[1].Error)
	}
}

// versionCapturingReleaser 嵌入 fakeReleaser 复用 stub，覆盖 SetVersion 记录是否被调。
// execBaseline 新语义（Task 7）：不应再调 SetVersion（版本归 release stage）。
type versionCapturingReleaser struct {
	*fakeReleaser
	setVersionCalled bool
}

func (v *versionCapturingReleaser) SetVersion(ctx context.Context, releaseIDs []string, version string) error {
	v.setVersionCalled = true
	return v.fakeReleaser.SetVersion(ctx, releaseIDs, version)
}

// deployCapturingReleaser 嵌入 fakeReleaser 复用 stub，覆盖 Promote 记录入参 + 返回固定晋升 Release。
type deployCapturingReleaser struct {
	*fakeReleaser
	promoteSrc string
	promoted   devops.Release
}

func (d *deployCapturingReleaser) Promote(ctx context.Context, srcReleaseID string) (devops.Release, error) {
	d.promoteSrc = srcReleaseID
	if d.promoted.ID != "" {
		return d.promoted, nil
	}
	return devops.Release{ID: "rel-promoted-next", EnvID: "env-prod", LaneID: LaneDefault, WorkloadID: "wl-promoted"}, nil
}

func TestExecBaselineOnlyMergesNoVersion(t *testing.T) {
	s := NewMemoryStore()
	rc := &versionCapturingReleaser{fakeReleaser: &fakeReleaser{}}
	e := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rc}
	run := seedPipeline(t, s, "p-base2", "app-base2", KindCI, []StageDef{
		{Type: StageBaseline, Name: "基线", Params: map[string]any{"mainBranch": "main", "mergeMode": "squash"}},
	})
	sr := &run.StageRuns[0]
	finished, err := e.execBaseline(acmeCtxEngine(), &run, StageDef{Type: StageBaseline, Params: map[string]any{"mainBranch": "main", "mergeMode": "squash"}}, sr)
	if err != nil {
		t.Fatalf("execBaseline: %v", err)
	}
	if !finished {
		t.Fatal("execBaseline 应 finished=true")
	}
	if rc.setVersionCalled {
		t.Error("baseline 不应再调 SetVersion（版本归 release）")
	}
	if run.Version != "" {
		t.Errorf("baseline 不应写 run.Version，got %q", run.Version)
	}
}

func TestExecPromoteDeploysToNextEnv(t *testing.T) {
	s := NewMemoryStore()
	dep := &deployCapturingReleaser{fakeReleaser: &fakeReleaser{}}
	// 通过 promoted 字段控制 Promote 返回：下一环境 env-prod + 基线泳道。
	dep.promoted = devops.Release{ID: "rel-promoted-next", EnvID: "env-prod", LaneID: LaneDefault, WorkloadID: "wl-promoted"}
	e := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: dep}
	// 构造：前序 deploy（env-test，已成功，Output 含 releaseId）-> promote stage 待执行。
	run := PipelineRun{
		ID:           "run-promote",
		AppID:        "app-promote",
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
				Output: map[string]any{OutReleaseID: "rel-deployed-test"},
			},
			{Index: 1, Type: StagePromote, Name: "晋升", Status: StagePending},
		},
	}
	sr := &run.StageRuns[1]
	finished, err := e.execPromote(acmeCtxEngine(), &run, StageDef{Type: StagePromote}, sr)
	if err != nil {
		t.Fatalf("execPromote: %v", err)
	}
	if !finished {
		t.Fatal("execPromote 应 finished=true")
	}
	// 应调 Promote（不是旧 CreateRelease 路径），传入前序 releaseId
	if dep.promoteSrc != "rel-deployed-test" {
		t.Errorf("Promote srcReleaseID 期望 rel-deployed-test，got %q", dep.promoteSrc)
	}
	// 晋升到下一环境（非 env-test）+ 基线泳道
	if dep.promoted.EnvID == "" || dep.promoted.EnvID == "env-test" {
		t.Errorf("promote 应部署到下一阶序环境，got envID=%q", dep.promoted.EnvID)
	}
	if dep.promoted.LaneID != LaneDefault {
		t.Errorf("promote 应部署到基线泳道，got laneID=%q", dep.promoted.LaneID)
	}
	// Output 含新 releaseId + workloadDomain
	if sr.Output[OutReleaseID] != dep.promoted.ID {
		t.Errorf("Output.releaseId 期望 %s，got %v", dep.promoted.ID, sr.Output[OutReleaseID])
	}
	if sr.Output[OutWorkloadDomain] == "" {
		t.Error("Output.workloadDomain 应非空")
	}
	if sr.Status != StageSuccess {
		t.Errorf("promote stage 期望 success，got %s", sr.Status)
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

// TestEngineRetry 验证失败 run 重试：从失败 stage 重新推进到成功。
// build 先失败（fakeBuilder 失败），Retry 后换成功 builder，run 从 failed→succeeded。
func TestEngineRetry(t *testing.T) {
	s := NewMemoryStore()
	runID, _ := seedBuildDeployPipeline(t, s, map[string]any{
		"envId": "env-dev", "imageSource": ImagePriorBuild,
	})

	// 先用失败 builder 跑到 failed
	failEng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildFailed, imageID: ""},
		Releases: &fakeReleaser{},
	}
	if err := failEng.Advance(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	run, _ := s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunFailed {
		t.Fatalf("期望 failed，got %s", run.Status)
	}

	// Retry 拒绝非 failed 状态守卫（用一个 succeeded run）—— 此处直接测失败 run retry 成功路径
	// 用成功 builder 重试
	successEng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-retry"},
		Releases: &fakeReleaser{},
	}
	if err := successEng.Retry(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Retry 失败: %v", err)
	}
	// Retry 起 goroutine（Start），轮询到终态
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := s.GetRun(acmeCtxEngine(), runID)
		if r.Status == RunSucceeded || r.Status == RunFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ = s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunSucceeded {
		t.Fatalf("Retry 后期望 succeeded，got %s (stage0 err=%s)", run.Status, run.StageRuns[0].Error)
	}
	// 失败 stage 的 Error 应被清空
	if run.StageRuns[0].Error != "" {
		t.Fatalf("Retry 后 stage Error 应清空，got %q", run.StageRuns[0].Error)
	}
}

// TestEngineRetryDeployAfterPollFail 验证 deploy stage 在 PollWorkloadReady 失败后 retry 不缺 envId。
// 回归 finding：execDeploy 曾用 `sr.Input = map{...}` 整覆盖 Input，丢掉初始 envId；
// PollWorkloadReady 失败 → run failed → Retry 不重置 Input → 重新执行 execDeploy 时 envId 为空 →
// fail-fast「deploy stage 缺 envId 参数」。修复后 Input 合并保留 envId，retry 正常推进。
func TestEngineRetryDeployAfterPollFail(t *testing.T) {
	s := NewMemoryStore()
	runID, _ := seedBuildDeployPipeline(t, s, map[string]any{
		"envId": "env-dev", "imageSource": ImagePriorBuild,
	})

	// build 成功 + deploy 的 PollWorkloadReady 失败 → run failed at stage[1]（deploy）
	failEng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-1"},
		Releases: &fakeReleaser{pollErr: errors.New("workload not ready")},
	}
	if err := failEng.Advance(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	run, _ := s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunFailed {
		t.Fatalf("期望 failed（deploy 探活失败），got %s (stage1 err=%s)", run.Status, run.StageRuns[1].Error)
	}

	// Retry：换探活成功的 releaser，deploy 应能重新取 envId 推进（修复后 Input.envId 保留）
	successEng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-1"},
		Releases: &fakeReleaser{},
	}
	if err := successEng.Retry(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Retry 失败: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := s.GetRun(acmeCtxEngine(), runID)
		if r.Status == RunSucceeded || r.Status == RunFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ = s.GetRun(acmeCtxEngine(), runID)
	if run.Status != RunSucceeded {
		t.Fatalf("deploy retry 后期望 succeeded，got %s (stage1 err=%s)", run.Status, run.StageRuns[1].Error)
	}
}

// TestEngineRetryGuards 验证 Retry 仅 failed 可重试。
func TestEngineRetryGuards(t *testing.T) {
	s := NewMemoryStore()
	runID, _ := seedBuildDeployPipeline(t, s, map[string]any{
		"envId": "env-dev", "imageSource": ImagePriorBuild,
	})
	eng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-x"},
		Releases: &fakeReleaser{},
	}
	// 跑到 succeeded
	if err := eng.Advance(acmeCtxEngine(), runID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	// succeeded run retry 应拒绝 ErrNotFailed
	if err := eng.Retry(acmeCtxEngine(), runID); err != ErrNotFailed {
		t.Fatalf("succeeded run retry 期望 ErrNotFailed，got %v", err)
	}
}

// TestAbortClearsRunningStage 验证 Abort 清理残留 running 的 stage_runs 标 StageAborted。
// 场景：run=running 且某 stage=running 时 abort，该 stage 应标 aborted（数据一致），
// 已终态 stage（success）不动。修复第 18 轮「abort 后 stage_runs 残留 running」数据不一致。
func TestAbortClearsRunningStage(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	tpl, _ := s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-abort-clear", Name: "AbortClear测试", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}, {Name: "部署", Type: StageDeploy}},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{Name: "p-abort-clear", AppID: "app-clear", Kind: KindCI, TemplateID: tpl.ID})
	// 注入 running 状态的 run：stage0 已 success，stage1 正 running
	run, err := s.CreateRun(ctx, PipelineRun{
		PipelineID: p.ID, AppID: "app-clear", Status: RunRunning, CurrentStage: 1,
		StageRuns: []StageRun{
			{Index: 0, Name: "构建", Type: StageBuild, Status: StageSuccess},
			{Index: 1, Name: "部署", Type: StageDeploy, Status: StageRunning},
		},
	})
	if err != nil {
		t.Fatalf("CreateRun 失败: %v", err)
	}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	if err := eng.Abort(ctx, run.ID); err != nil {
		t.Fatalf("Abort 失败: %v", err)
	}
	got, _ := s.GetRun(ctx, run.ID)
	if got.Status != RunAborted {
		t.Errorf("run status want aborted，got %s", got.Status)
	}
	if len(got.StageRuns) != 2 {
		t.Fatalf("StageRuns want 2，got %d", len(got.StageRuns))
	}
	if got.StageRuns[0].Status != StageSuccess {
		t.Errorf("已终态 stage 不应变，want success，got %s", got.StageRuns[0].Status)
	}
	if got.StageRuns[1].Status != StageAborted {
		t.Errorf("残留 running stage 应标 aborted，got %s", got.StageRuns[1].Status)
	}
}

// TestEngineBaselineSameBranchSkipsMerge 同分支（run.Branch == mainBranch）明确跳过合并，
// 不对 Gitea 发 head==base 的 PR（历史 bug：422 错误被吞、stage 假绿）。
func TestEngineBaselineSameBranchSkipsMerge(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-same", "app-same", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
		}},
		{Name: "基线", Type: StageBaseline, Params: map[string]any{"mainBranch": "main", "mergeMode": "squash"}},
	})

	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{},
		Gitea: &fakeGiteaMerger{mergeSHA: "sha-same"}} // 若被调用返回成功 sha
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunSucceeded {
		t.Fatalf("期望 succeeded，got %s", run.Status)
	}
	sr := run.StageRuns[1]
	if sr.Status != StageSuccess || sr.Error != "" {
		t.Fatalf("同分支跳过应 success 无 error，got status=%s error=%q", sr.Status, sr.Error)
	}
	if _, ok := sr.Output[OutMergeSHA]; ok {
		t.Error("同分支跳过不应产出 mergeSHA")
	}
	if !strings.Contains(sr.Log, "无变更可合并") {
		t.Errorf("日志应含明确跳过说明，got %q", sr.Log)
	}
}

// fakeLaneEnsurer 记录 Ensure 调用（泳道实体化懒建断言用）。
type fakeLaneEnsurer struct {
	calls [][2]string
	err   error
}

func (f *fakeLaneEnsurer) Ensure(ctx context.Context, envID, name string) error {
	f.calls = append(f.calls, [2]string{envID, name})
	return f.err
}

// fakeAppResourceLookup 返回固定资源模板（应用默认值断言用）。
type fakeAppResourceLookup struct {
	tpl DeployResources
	err error
}

func (f fakeAppResourceLookup) Template(ctx context.Context, appID string) (DeployResources, error) {
	return f.tpl, f.err
}

// TestExecDeployLaneEnsure：非 default 泳道 deploy 前懒建 Lane 实体；
// default 泳道不调 Ensure（基线无需实体）。
func TestExecDeployLaneEnsure(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-lane-ensure", "app-le", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "lane": "feature-x",
			"imageSource": ImageSelected, "imageId": "img-1",
		}},
	})
	ensurer := &fakeLaneEnsurer{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}, LaneEnsurer: ensurer}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	if len(ensurer.calls) != 1 || ensurer.calls[0] != [2]string{"env-test", "feature-x"} {
		t.Errorf("Ensure 调用期望 [env-test feature-x]，got %v", ensurer.calls)
	}
	// Log 含泳道实体就绪事件
	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if !strings.Contains(run.StageRuns[0].Log, "泳道实体就绪") {
		t.Errorf("Log 应含泳道实体就绪，got %q", run.StageRuns[0].Log)
	}

	// default 泳道不调 Ensure
	s2 := NewMemoryStore()
	r2 := seedPipeline(t, s2, "p-lane-def", "app-le2", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
		}},
	})
	ensurer2 := &fakeLaneEnsurer{}
	eng2 := &Engine{Pipelines: s2, Runs: s2, Builds: fakeBuilder{}, Releases: &fakeReleaser{}, LaneEnsurer: ensurer2}
	if err := eng2.Advance(acmeCtxEngine(), r2.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	if len(ensurer2.calls) != 0 {
		t.Errorf("default 泳道不应调 Ensure，got %d 次", len(ensurer2.calls))
	}
}

// TestExecDeployLaneEnsureFailed：Ensure 出错 stage failed + run failed。
func TestExecDeployLaneEnsureFailed(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-lane-err", "app-le3", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "lane": "feature-y",
			"imageSource": ImageSelected, "imageId": "img-1",
		}},
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{},
		LaneEnsurer: &fakeLaneEnsurer{err: fmt.Errorf("db down")}}
	_ = eng.Advance(acmeCtxEngine(), r.ID) // stage 内部 failed，Advance 对已终态 run 返 nil
	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunFailed || run.StageRuns[0].Status != StageFailed {
		t.Fatalf("期望 run/stage failed，got %s/%s", run.Status, run.StageRuns[0].Status)
	}
	if !strings.Contains(run.StageRuns[0].Error, "泳道实体创建失败") {
		t.Errorf("Error 应含泳道实体创建失败，got %q", run.StageRuns[0].Error)
	}
}

// TestExecDeployResources：资源规格优先级——stage params 显式 resources > 应用模板 > 空。
func TestExecDeployResources(t *testing.T) {
	// ① 显式 resources 覆盖应用默认
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-res-1", "app-r1", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
			"resources": map[string]any{"cpuRequest": "250m", "memLimit": "512Mi"},
		}},
	})
	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel,
		AppResourceLookup: fakeAppResourceLookup{tpl: DeployResources{CPURequest: "1", MemLimit: "2Gi"}}}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	if rel.deployResources != (DeployResources{CPURequest: "250m", MemLimit: "512Mi"}) {
		t.Errorf("显式 resources 应覆盖应用模板，got %+v", rel.deployResources)
	}

	// ② 无显式 -> 应用模板
	s2 := NewMemoryStore()
	r2 := seedPipeline(t, s2, "p-res-2", "app-r2", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
		}},
	})
	rel2 := &fakeReleaser{}
	eng2 := &Engine{Pipelines: s2, Runs: s2, Builds: fakeBuilder{}, Releases: rel2,
		AppResourceLookup: fakeAppResourceLookup{tpl: DeployResources{CPURequest: "500m"}}}
	if err := eng2.Advance(acmeCtxEngine(), r2.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	if rel2.deployResources.CPURequest != "500m" {
		t.Errorf("应用模板应生效，got %+v", rel2.deployResources)
	}

	// ③ 两者皆空 + Lookup 失败降级空（不 fail）
	s3 := NewMemoryStore()
	r3 := seedPipeline(t, s3, "p-res-3", "app-r3", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
		}},
	})
	rel3 := &fakeReleaser{}
	eng3 := &Engine{Pipelines: s3, Runs: s3, Builds: fakeBuilder{}, Releases: rel3,
		AppResourceLookup: fakeAppResourceLookup{err: fmt.Errorf("not found")}}
	if err := eng3.Advance(acmeCtxEngine(), r3.ID); err != nil {
		t.Fatalf("Lookup 失败应降级不 fail: %v", err)
	}
	if !rel3.deployResources.IsEmpty() {
		t.Errorf("Lookup 失败应透传空，got %+v", rel3.deployResources)
	}
	run3, _ := s3.GetRun(acmeCtxEngine(), r3.ID)
	if run3.Status != RunSucceeded {
		t.Errorf("run 应 succeeded，got %s", run3.Status)
	}
}

// TestExecDeployLaneReplicaDowngrade：联调泳道副本降级——非 prod 环境 + 非 default 泳道 +
// replicas>1 截断为 1；prod 环境不降级；EnvType 未注入保守不降级。
func TestExecDeployLaneReplicaDowngrade(t *testing.T) {
	deploy := func(t *testing.T, lane string, envType EnvTypeResolver) (*fakeReleaser, *memoryStore) {
		t.Helper()
		s := NewMemoryStore()
		params := map[string]any{
			"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1", "replicas": 3,
			// prod 用例过「生产禁 BestEffort」fail-fast（终审 I2）必须带资源规格
			"resources": map[string]any{"cpuRequest": "100m", "memRequest": "128Mi"},
		}
		if lane != "" {
			params["lane"] = lane
		}
		r := seedPipeline(t, s, "p-rep-"+lane+fmt.Sprint(envType == nil), "app-rep", KindCI, []StageDef{
			{Name: "部署", Type: StageDeploy, Params: params},
		})
		rel := &fakeReleaser{}
		eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel, EnvType: envType}
		if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
			t.Fatalf("Advance 失败: %v", err)
		}
		return rel, s
	}
	// 非 prod 测试环境 + feature 泳道：降级为 1
	rel, _ := deploy(t, "feature-x", func(ctx context.Context, envID string) (string, error) { return "test", nil })
	if rel.deployReplicas != 1 {
		t.Errorf("非 prod 联调泳道副本应降级 1，got %d", rel.deployReplicas)
	}
	// prod 环境：不降级
	rel, _ = deploy(t, "gray-x", func(ctx context.Context, envID string) (string, error) { return "prod", nil })
	if rel.deployReplicas != 3 {
		t.Errorf("prod 泳道副本不应降级，got %d", rel.deployReplicas)
	}
	// EnvType 未注入：保守按 prod 不降级（fail-closed）
	rel, _ = deploy(t, "feature-z", nil)
	if rel.deployReplicas != 3 {
		t.Errorf("EnvType 未注入应保守不降级，got %d", rel.deployReplicas)
	}
	// default 泳道：不降级（基线按显式副本）
	rel, _ = deploy(t, "", func(ctx context.Context, envID string) (string, error) { return "test", nil })
	if rel.deployReplicas != 3 {
		t.Errorf("default 泳道不应降级，got %d", rel.deployReplicas)
	}
}

// 终审 I2 回归：生产环境 + resources 三级来源全空（stage 未配 + 无应用模板）→ deploy stage fail-fast，
// 不允许流水线建出 BestEffort 生产工作负载（标准基线「生产禁 BestEffort」）。
func TestExecDeployProdEmptyResourcesFails(t *testing.T) {
	s := NewMemoryStore()
	r := seedPipeline(t, s, "p-prod-res", "app-prod-res", KindCI, []StageDef{
		{Name: "部署", Type: StageDeploy, Params: map[string]any{
			"envId": "env-prod", "imageSource": ImageSelected, "imageId": "img-1",
		}},
	})
	rel := &fakeReleaser{}
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel,
		EnvType: func(ctx context.Context, envID string) (string, error) { return "prod", nil }}
	if err := eng.Advance(acmeCtxEngine(), r.ID); err != nil {
		t.Fatalf("Advance 失败: %v", err)
	}
	run, _ := s.GetRun(acmeCtxEngine(), r.ID)
	if run.Status != RunFailed {
		t.Fatalf("prod 空 resources 应 failed, got %s", run.Status)
	}
	if rel.deployReplicas != 0 || rel.deployResources != (DeployResources{}) {
		t.Fatal("prod 空 resources 不应执行 Deploy")
	}
}
