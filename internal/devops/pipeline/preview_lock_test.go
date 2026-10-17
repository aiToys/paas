// preview_lock_test.go 执行计划与产物锁定测试（spec 2026-10-17 §6/§7）：
// - preview 与 trigger 的 planHash 一致性（预览到执行之间候选变化 → 409）
// - latestReady 触发时锁定（审批等待期间新构建不漂移实际发布产物）
// - decisions 端点（manual-test reject 质量否决 / approve confirm|reject / canary 拒收）
// - smoke 探活 CheckResult 结构化
package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops"
)

// seedCDPipe 预建 approve→canary(latestReady) 模板 + 绑定 + engine/handler。
func seedCDPipe(t *testing.T, rel *fakeReleaser) (*memoryStore, context.Context, Pipeline, *Handler) {
	t.Helper()
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-cd-lock", Name: "锁定CD", Kind: KindCD,
		Stages: []StageDef{
			{Name: "上线审批", Type: StageApprove},
			{Name: "金丝雀验证", Type: StageCanary, Params: map[string]any{
				"envId": "env-prod", "imageSource": ImageLatestReady,
			}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-cd", AppID: "app-lock", Kind: KindCD, TemplateID: "tpl-cd-lock",
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: rel}
	h := NewHandler(s, s, s, eng)
	h.Authorize = allowAll
	return s, ctx, p, h
}

// TestPreviewPlanHashConsistency preview→trigger 带 hash 执行；hash 过期（新构建完成）→ 409。
func TestPreviewPlanHashConsistency(t *testing.T) {
	rel := &fakeReleaser{imageID: "img-a"}
	s, ctx, p, h := seedCDPipe(t, rel)

	// ① preview：拿到 planHash + 锁定候选 img-a。
	req := acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/preview", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	var pv struct {
		Data RunPreview `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pv)
	if pv.Data.PlanHash == "" {
		t.Fatal("preview 应返回 planHash")
	}
	if pv.Data.Artifact == nil || pv.Data.Artifact.ImageID != "img-a" {
		t.Fatalf("preview 应预解析候选 img-a，got %+v", pv.Data.Artifact)
	}

	// ② 预览到执行之间新构建完成：latestReady 变为 img-b → 旧 hash 409。
	rel.imageID = "img-b"
	req = acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/run",
		`{"branch":"main","planHash":"`+pv.Data.PlanHash+`"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("候选已变化期望 409，got %d body %s", rec.Code, rec.Body.String())
	}

	// ③ 不带 hash 的执行不受影响（webhook/经典路径），触发后锁定当下候选 img-b。
	req = acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("run 期望 201，got %d body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Data.Artifact == nil || created.Data.Artifact.ImageID != "img-b" {
		t.Fatalf("run.Artifact 应锁定 img-b，got %+v", created.Data.Artifact)
	}
	// 触发后再有新构建（img-c），也不改 run 锁定值。
	rel.imageID = "img-c"
	run, err := s.GetRun(ctx, created.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Artifact.ImageID != "img-b" {
		t.Fatalf("run.Artifact 锁定值被漂移：want img-b got %s", run.Artifact.ImageID)
	}
}

// TestCanaryUsesLockedArtifact 审批通过后 latestReady 已变，canary 仍部署触发时锁定的镜像。
func TestCanaryUsesLockedArtifact(t *testing.T) {
	rel := &fakeReleaser{imageID: "img-a"}
	s, ctx, p, h := seedCDPipe(t, rel)

	req := acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("run 期望 201，got %d", rec.Code)
	}
	var created struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// 审批等待期间新构建完成（latestReady → img-b）。
	rel.imageID = "img-b"
	// approve 后 canary 进入 waiting（DeployCanary 执行）。
	waitPaused := func() PipelineRun {
		for i := 0; i < 100; i++ {
			run, _ := s.GetRun(ctx, created.Data.ID)
			if run.Status == RunPaused && run.CurrentStage == 1 {
				return run
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("canary 未进入 waiting")
		return PipelineRun{}
	}
	// 等 approve 段就位再放行（引擎异步推进，POST 早到会被 409 拒——固有竞态显式等待）。
	for i := 0; i < 100; i++ {
		run, _ := s.GetRun(ctx, created.Data.ID)
		if run.Status == RunPaused && run.CurrentStage == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	approveURL := "/api/pipelineruns/" + created.Data.ID + "/stages/0/approve"
	req = acmeReq(http.MethodPost, approveURL, "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	waitPaused()
	// 金丝雀部署的是锁定值 img-a（不是此刻的 latestReady img-b）。
	if got := rel.lastImage(); got != "img-a" {
		t.Fatalf("canary 应部署锁定镜像 img-a，got %s", got)
	}
	// 锁定值也固化在 stage Input（Retry 同源）。
	run, _ := s.GetRun(ctx, created.Data.ID)
	if v, _ := run.StageRuns[1].Input["lockedImageId"].(string); v != "img-a" {
		t.Fatalf("stage Input 应含 lockedImageId=img-a，got %v", run.StageRuns[1].Input["lockedImageId"])
	}
}

// TestDecisionsManualTestReject manual-test 拒绝：run failed + 理由落 Error + Decisions 记录。
func TestDecisionsManualTestReject(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-manual", Name: "人工", Kind: KindCI,
		Stages: []StageDef{
			{Name: "构建", Type: StageBuild},
			{Name: "人工确认", Type: StageTest, Params: map[string]any{"mode": TestManual}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{Name: "p-m", AppID: "app-m", Kind: KindCI, TemplateID: "tpl-manual"})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-1"}, Releases: &fakeReleaser{}}
	h := NewHandler(s, s, s, eng)
	h.Authorize = allowAll

	req := acmeReq(http.MethodPost, "/api/applications/app-m/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var created struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	for i := 0; i < 100; i++ {
		run, _ := s.GetRun(ctx, created.Data.ID)
		if run.Status == RunPaused {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/stages/1/decisions",
		`{"action":"reject","reason":"下单接口 500"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("decisions reject 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	run, _ := s.GetRun(ctx, created.Data.ID)
	if run.Status != RunFailed {
		t.Fatalf("拒绝后 run 期望 failed，got %s", run.Status)
	}
	if !strings.Contains(run.StageRuns[1].Error, "下单接口 500") {
		t.Fatalf("stage Error 应含拒绝理由，got %q", run.StageRuns[1].Error)
	}
	if len(run.Decisions) != 1 || run.Decisions[0].Type != "manual-test" || run.Decisions[0].Action != "reject" {
		t.Fatalf("Decisions 应记录 manual-test/reject，got %+v", run.Decisions)
	}
}

// TestDecisionsApproveConfirmAndReject approve：confirm 放行 / reject 中止。
func TestDecisionsApproveConfirmAndReject(t *testing.T) {
	rel := &fakeReleaser{imageID: "img-a"}
	s, ctx, p, h := seedCDPipe(t, rel)

	// confirm：批准 → canary 继续推进到 waiting。
	req := acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var created struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	for i := 0; i < 100; i++ {
		run, _ := s.GetRun(ctx, created.Data.ID)
		if run.Status == RunPaused {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/stages/0/decisions", `{"action":"confirm"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	run, _ := s.GetRun(ctx, created.Data.ID)
	if d := run.Decisions; len(d) != 1 || d[0].Type != "approve" || d[0].Action != "confirm" {
		t.Fatalf("Decisions 应记录 approve/confirm，got %+v", run.Decisions)
	}

	// reject：第二个 run 的 approve 拒绝 → aborted。
	// 先终止上面的 canary run（单实例串行约束）。
	reqTerm := acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/abort", "")
	recTerm := httptest.NewRecorder()
	h.ServeHTTP(recTerm, reqTerm)
	if recTerm.Code != http.StatusOK {
		t.Fatalf("abort 期望 200，got %d", recTerm.Code)
	}

	req = acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	for i := 0; i < 100; i++ {
		run, _ := s.GetRun(ctx, created.Data.ID)
		if run.Status == RunPaused {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/stages/0/decisions",
		`{"action":"reject","reason":"变更回退"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	run, _ = s.GetRun(ctx, created.Data.ID)
	if run.Status != RunAborted {
		t.Fatalf("approve reject 后 run 期望 aborted，got %s", run.Status)
	}
}

// TestDecisionsCanaryRejected canary stage 不走通用 decisions（专属端点语义）。
func TestDecisionsCanaryRejected(t *testing.T) {
	rel := &fakeReleaser{imageID: "img-a"}
	s, ctx, p, h := seedCDPipe(t, rel)

	req := acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var created struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	// 等 canary waiting。
	for i := 0; i < 200; i++ {
		run, _ := s.GetRun(ctx, created.Data.ID)
		if run.Status == RunPaused && run.CurrentStage == 1 {
			break
		}
		if run.Status == RunPaused { // approve 暂停：先放行
			r2 := acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/stages/0/approve", "")
			h.ServeHTTP(httptest.NewRecorder(), r2)
		}
		time.Sleep(20 * time.Millisecond)
	}
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/stages/1/decisions", `{"action":"confirm"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("canary 走 decisions 期望 400，got %d body %s", rec.Code, rec.Body.String())
	}
	_ = ctx
}

// TestSmokeCheckResultStructured smoke 探活成功后 Checks 结构化（pass + 目标 + 2xx）。
func TestSmokeCheckResultStructured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-smoke", Name: "冒烟", Kind: KindCI,
		Stages: []StageDef{
			{Name: "构建", Type: StageBuild},
			{Name: "部署", Type: StageDeploy, Params: map[string]any{"envId": "env-dev", "imageSource": ImagePriorBuild, "port": 8080}},
			{Name: "冒烟", Type: StageTest, Params: map[string]any{"path": "/livez"}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{Name: "p-s", AppID: "app-s", Kind: KindCI, TemplateID: "tpl-smoke"})
	eng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-1"},
		Releases: &fakeReleaser{domain: addr}, // 探活直打测试 server
	}
	h := NewHandler(s, s, s, eng)
	h.Authorize = allowAll

	req := acmeReq(http.MethodPost, "/api/applications/app-s/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var created struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	run := waitRun(t, s, ctx, created.Data.ID, RunSucceeded, 3*time.Second)
	smoke := run.StageRuns[2]
	if len(smoke.Checks) == 0 {
		t.Fatal("smoke stage 应有 CheckResult")
	}
	c := smoke.Checks[0]
	if c.Type != "smoke" || c.Status != "pass" || c.Actual != "2xx" {
		t.Fatalf("CheckResult 不符： %+v", c)
	}
	if !strings.Contains(c.Target, "/livez") {
		t.Fatalf("CheckResult.Target 应含探活路径： %s", c.Target)
	}
}

// TestPlanHashStability 同输入同 hash；锁定值参与 hash。
func TestPlanHashStability(t *testing.T) {
	stages := []StageDef{
		{Name: "canary", Type: StageCanary, Params: map[string]any{"imageSource": ImageLatestReady}},
	}
	h1 := PlanHash("tpl", 3, "main", stages)
	if h1 != PlanHash("tpl", 3, "main", stages) {
		t.Fatal("同输入应同 hash")
	}
	if h1 == PlanHash("tpl", 4, "main", stages) {
		t.Fatal("模板版本变化应改变 hash")
	}
	stages[0].Params["lockedImageId"] = "img-x"
	if h1 == PlanHash("tpl", 3, "main", stages) {
		t.Fatal("锁定镜像变化应改变 hash")
	}
}

// TestCanaryAbortDuringReadinessWindow 就绪等待窗口内 Abort：workloadID 已随 Output 落库，
// 级联清理可读到并删除 canary workload（修复前 Output 就绪后才写，窗口内 abort 泄漏到 LaneGC TTL）。
func TestCanaryAbortDuringReadinessWindow(t *testing.T) {
	rel := &fakeReleaser{imageID: "img-a", pollBlock: make(chan struct{})}
	defer close(rel.pollBlock) // 放行阻塞的 Poll，防 goroutine 泄漏拖累后续测试
	s, ctx, p, h := seedCDPipe(t, rel)

	req := acmeReq(http.MethodPost, "/api/applications/app-lock/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var created struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	for i := 0; i < 100; i++ {
		run, _ := s.GetRun(ctx, created.Data.ID)
		if run.Status == RunPaused && run.CurrentStage == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/stages/0/decisions", `{"action":"confirm"}`)
	h.ServeHTTP(httptest.NewRecorder(), req)

	// 等 workloadID 落库（DeployCanary 成功即写 + flush 即时落库——本修复的行为锁）。
	for i := 0; i < 100; i++ {
		run, _ := s.GetRun(ctx, created.Data.ID)
		if run.CurrentStage == 1 {
			if _, ok := run.StageRuns[1].Output[OutCanaryWorkloadID]; ok {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		if i == 99 {
			t.Fatal("canary workloadID 未在就绪窗口内落库")
		}
	}

	// 窗口内 Abort → 级联删除（库内 Output 可读）。
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+created.Data.ID+"/abort", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("abort 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	// 断言语义=级联删除已触发且只删 canary 目标；重复删除（级联与超时清理并发到达）
	// 是幂等无害的，不锁精确次数（race 下偶发双触发）。
	if got := rel.deletedList(); len(got) == 0 || got[0] != "wl-fake" {
		t.Fatalf("abort 应级联删除 canary workload wl-fake，got %v", got)
	}
}
