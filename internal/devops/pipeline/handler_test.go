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
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/pkg/tenant"
)

// ---------- 测试辅助 ----------

func acmeReq(method, target string, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(tenant.WithTenant(context.Background(), "t-acme"))
}

func allowAll(*http.Request, string) bool { return true }

// ---------- 测试 ----------

func TestPipelineCreateFromTemplate(t *testing.T) {
	s := NewMemoryStore()
	_ = SeedTemplates(acmeCtxEngine(), s) // 灌 tpl-ci/tpl-cd

	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	req := acmeReq(http.MethodPost, "/api/applications/app-1/pipelines",
		`{"templateId":"tpl-ci","name":"p1"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("期望 201，got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data Pipeline `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("反序列化失败: %v body %s", err, rec.Body.String())
	}
	if resp.Data.Kind != KindCI {
		t.Fatalf("从 tpl-ci 创建 kind 期望 ci，got %s", resp.Data.Kind)
	}
	if resp.Data.TemplateID != "tpl-ci" {
		t.Fatalf("从模板创建 TemplateID 期望 tpl-ci，got %q", resp.Data.TemplateID)
	}
	if resp.Data.AppID != "app-1" {
		t.Fatalf("appId 期望 app-1（路径），got %s", resp.Data.AppID)
	}
	if resp.Data.ID == "" {
		t.Fatal("ID 应由 store 生成")
	}
}

func TestPipelineCreateValidation(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建 tpl-x（createPipeline 先查模板存在再 Validate，故缺 name 用例需模板存在以触达 Validate）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-x", Name: "X", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
	})
	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	// 缺 name -> 400（模板存在，进 CreatePipeline -> Validate 报 errNameRequired）
	req := acmeReq(http.MethodPost, "/api/applications/app-1/pipelines",
		`{"kind":"ci","templateId":"tpl-x"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 name 期望 400，got %d body %s", rec.Code, rec.Body.String())
	}

	// 缺 templateId -> 400（绑定模型：TemplateID 必填，Validate 返 ErrTemplateRequired）
	req = acmeReq(http.MethodPost, "/api/applications/app-1/pipelines",
		`{"name":"p1","kind":"ci"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 templateId 期望 400，got %d", rec.Code)
	}
}

func TestPipelineCrossTenantIsolation(t *testing.T) {
	s := NewMemoryStore()
	acmeCtx := tenant.WithTenant(context.Background(), "t-acme")
	_, _ = s.CreatePipeline(acmeCtx, Pipeline{
		Name: "p-acme", AppID: "app-1", Kind: KindCI, TemplateID: "tpl-test",
	})

	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	// t-globex GET app-1 pipelines -> 空列表（app-1 属 t-acme，globex 看不到）
	globexReq := httptest.NewRequest(http.MethodGet, "/api/applications/app-1/pipelines", nil)
	globexReq = globexReq.WithContext(tenant.WithTenant(context.Background(), "t-globex"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, globexReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，got %d", rec.Code)
	}
	var resp struct {
		Data []Pipeline `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Data) != 0 {
		t.Fatalf("跨租户期望空列表，got %d 条", len(resp.Data))
	}
}

func TestPipelineCRUDPermissionDenied(t *testing.T) {
	s := NewMemoryStore()
	h := NewHandler(s, s, s, nil)
	h.Authorize = func(*http.Request, string) bool { return false } // 拒绝所有

	// GET list -> 403
	req := acmeReq(http.MethodGet, "/api/applications/app-1/pipelines", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET 拒绝期望 403，got %d", rec.Code)
	}
	// POST -> 403
	req = acmeReq(http.MethodPost, "/api/applications/app-1/pipelines", `{"name":"p","kind":"ci","templateId":"tpl-x"}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST 拒绝期望 403，got %d", rec.Code)
	}
}

func TestPipelineUpdateDelete(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p1", AppID: "app-1", Kind: KindCI, TemplateID: "tpl-ci",
	})

	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	// PUT 更新（改名 + 换模板 + 加 ParamOverrides）
	body := `{"name":"p1-renamed","kind":"ci","templateId":"tpl-cd","paramOverrides":{"0.envId":"env-x"}}`
	req := acmeReq(http.MethodPut, "/api/applications/app-1/pipelines/"+p.ID, body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	var updated struct {
		Data Pipeline `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Data.Name != "p1-renamed" {
		t.Fatalf("更新后 name 期望 p1-renamed，got %s", updated.Data.Name)
	}
	if updated.Data.TemplateID != "tpl-cd" {
		t.Fatalf("更新后 TemplateID 期望 tpl-cd，got %q", updated.Data.TemplateID)
	}
	if updated.Data.ParamOverrides["0.envId"] != "env-x" {
		t.Fatalf("更新后 ParamOverrides.0.envId 期望 env-x，got %v", updated.Data.ParamOverrides["0.envId"])
	}
	if updated.Data.TenantID != "t-acme" {
		t.Fatalf("更新应保留 TenantID，got %s", updated.Data.TenantID)
	}

	// DELETE
	req = acmeReq(http.MethodDelete, "/api/applications/app-1/pipelines/"+p.ID, "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE 期望 200，got %d", rec.Code)
	}

	// 再 GET -> 404
	req = acmeReq(http.MethodGet, "/api/applications/app-1/pipelines/"+p.ID, "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("删除后 GET 期望 404，got %d", rec.Code)
	}
}

func TestTemplateList(t *testing.T) {
	s := NewMemoryStore()
	_ = SeedTemplates(acmeCtxEngine(), s)

	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	req := acmeReq(http.MethodGet, "/api/pipeline-templates", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，got %d", rec.Code)
	}
	var resp struct {
		Data []PipelineTemplate `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Data) < 2 {
		t.Fatalf("期望至少 2 平台预置模板，got %d", len(resp.Data))
	}
}

func TestPipelineNotFound(t *testing.T) {
	s := NewMemoryStore()
	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	req := acmeReq(http.MethodGet, "/api/applications/app-1/pipelines/no-such-id", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("期望 404，got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPipelineMethodNotAllowed(t *testing.T) {
	s := NewMemoryStore()
	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	// /api/applications/{id}/pipelines 用 PATCH -> 405
	req := acmeReq(http.MethodPatch, "/api/applications/app-1/pipelines", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，got %d", rec.Code)
	}
}

// ---------- PipelineRun 测试 ----------

// waitRun 轮询 run 直到达到 want 或终态（failed/aborted）或超时。
func waitRun(t *testing.T, s *memoryStore, ctx context.Context, runID, want string, timeout time.Duration) PipelineRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var run PipelineRun
	for time.Now().Before(deadline) {
		run, _ = s.GetRun(ctx, runID)
		if run.Status == want || run.Status == RunFailed || run.Status == RunAborted {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	return run
}

func TestRunManualTriggerAndAdvance(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建模板（triggerRun 从模板解析 stages -> StageRuns.Input）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-ci-test", Name: "测试CI", Kind: KindCI,
		Stages: []StageDef{
			{Name: "构建", Type: StageBuild},
			{Name: "部署", Type: StageDeploy, Params: map[string]any{"envId": "env-dev", "imageSource": ImagePriorBuild}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-ci", AppID: "app-ci", Kind: KindCI, TemplateID: "tpl-ci-test",
	})
	eng := &Engine{
		Pipelines: s, Runs: s,
		Builds:   fakeBuilder{buildStatus: devops.BuildSuccess, imageID: "img-1"},
		Releases: &fakeReleaser{},
	}
	h := NewHandler(s, s, s, eng)
	h.Authorize = allowAll

	req := acmeReq(http.MethodPost, "/api/applications/app-ci/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("run 期望 201，got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Status != RunRunning {
		t.Fatalf("新建 run 期望 running，got %s", resp.Data.Status)
	}

	run := waitRun(t, s, ctx, resp.Data.ID, RunSucceeded, 2*time.Second)
	if run.Status != RunSucceeded {
		t.Fatalf("期望 succeeded，got %s", run.Status)
	}
	for i, sr := range run.StageRuns {
		if sr.Status != StageSuccess {
			t.Fatalf("stage %d 期望 success，got %s", i, sr.Status)
		}
	}
}

func TestRunApproveFlow(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建模板：审批 -> 部署 dev（triggerRun 从模板解析 stages -> StageRuns.Input）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-cd-test", Name: "测试CD", Kind: KindCD,
		Stages: []StageDef{
			{Name: "审批", Type: StageApprove},
			{Name: "部署", Type: StageDeploy, Params: map[string]any{
				"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
			}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-cd", AppID: "app-cd", Kind: KindCD, TemplateID: "tpl-cd-test",
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	h := NewHandler(s, s, s, eng)
	h.Authorize = allowAll

	// run -> 异步推进到 approve 暂停
	req := acmeReq(http.MethodPost, "/api/applications/app-cd/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("run 期望 201，got %d", rec.Code)
	}
	var resp struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	runID := resp.Data.ID

	// 等暂停
	run := waitRun(t, s, ctx, runID, RunPaused, 2*time.Second)
	if run.Status != RunPaused {
		t.Fatalf("期望 paused，got %s", run.Status)
	}
	// POST approve stage 0
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+runID+"/stages/0/approve", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	// 等恢复后 succeeded
	run = waitRun(t, s, ctx, runID, RunSucceeded, 2*time.Second)
	if run.Status != RunSucceeded {
		t.Fatalf("approve 后期望 succeeded，got %s", run.Status)
	}
}

func TestRunProdWriteGuard(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建模板：部署 prod（triggerRun allowProdFlow 拦截）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-prod-test", Name: "测试Prod部署", Kind: KindCD,
		Stages: []StageDef{
			{Name: "部署", Type: StageDeploy, Params: map[string]any{
				"envId": "env-prod", "imageSource": ImageSelected, "imageId": "img-1",
			}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-prod", AppID: "app-prod", Kind: KindCD, TemplateID: "tpl-prod-test",
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	h := NewHandler(s, s, s, eng)
	// developer：pipeline:write 通，prod:write 拒
	h.Authorize = func(r *http.Request, perm string) bool { return perm != PermProdWrite }
	h.envType = func(ctx context.Context, envID string) (string, error) {
		if envID == "env-prod" {
			return environment.TypeProd, nil
		}
		return "test", nil
	}

	req := acmeReq(http.MethodPost, "/api/applications/app-prod/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer deploy prod 期望 403，got %d body %s", rec.Code, rec.Body.String())
	}
}

// TestRunPromoteProdGuard 验证 promote 链路 prod:write 横切：
// pipeline [deploy(env-test), promote]，promote 目标=prod（NextPromoteTarget(test)=prod），
// developer（pipeline:write 通，prod:write 拒）触发应被 403 拦截——防绕过 prod:write 经 CI 发布生产。
func TestRunPromoteProdGuard(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建模板：部署测试 -> promote（triggerRun allowProdFlow 扫描 promote 目标=prod 拦截）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-promote-test", Name: "测试Promote", Kind: KindCD,
		Stages: []StageDef{
			{Name: "部署测试", Type: StageDeploy, Params: map[string]any{
				"envId": "env-test", "imageSource": ImageSelected, "imageId": "img-1",
			}},
			{Name: "提升", Type: StagePromote},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-promote", AppID: "app-promote", Kind: KindCD, TemplateID: "tpl-promote-test",
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	h := NewHandler(s, s, s, eng)
	// developer：pipeline:write 通，prod:write 拒
	h.Authorize = func(r *http.Request, perm string) bool { return perm != PermProdWrite }
	h.envType = func(ctx context.Context, envID string) (string, error) {
		if envID == "env-prod" {
			return environment.TypeProd, nil
		}
		return "test", nil // env-test 非生产
	}
	// promote 目标：env-test 的下一阶 = prod
	h.promoteTargetType = func(ctx context.Context, envID string) (string, error) {
		if envID == "env-test" {
			return environment.TypeProd, nil
		}
		return "", nil
	}

	req := acmeReq(http.MethodPost, "/api/applications/app-promote/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer [deploy test, promote→prod] 期望 403，got %d body %s", rec.Code, rec.Body.String())
	}
}

// TestRunDeployMissingEnvId 验证 deploy stage 缺 envId fail-fast（400，不占单实例槽位）。
func TestRunDeployMissingEnvId(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建模板：deploy stage 缺 envId（triggerRun validateDeployEnvs fail-fast 400）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-noenv-test", Name: "测试无envId", Kind: KindCD,
		Stages: []StageDef{
			{Name: "部署", Type: StageDeploy, Params: map[string]any{ // 缺 envId
				"imageSource": ImageSelected, "imageId": "img-1",
			}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-noenv", AppID: "app-noenv", Kind: KindCD, TemplateID: "tpl-noenv-test",
	})
	h := NewHandler(s, s, s, &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}})
	h.Authorize = allowAll

	req := acmeReq(http.MethodPost, "/api/applications/app-noenv/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy 缺 envId 期望 400，got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRunSingleInstance(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建模板：单 build stage（triggerRun 需模板解析 stages 才能到 HasActiveRun 检查）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-si-test", Name: "测试单实例", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-si", AppID: "app-si", Kind: KindCI, TemplateID: "tpl-si-test",
	})
	// 已有 running run
	_, _ = s.CreateRun(ctx, PipelineRun{
		PipelineID: p.ID, AppID: p.AppID, Branch: "main", Trigger: "manual",
		Status: RunRunning, CurrentStage: 0,
		StageRuns: []StageRun{{Index: 0, Type: StageBuild, Name: "构建", Status: StageRunning}},
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	h := NewHandler(s, s, s, eng)
	h.Authorize = allowAll

	req := acmeReq(http.MethodPost, "/api/applications/app-si/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("已有 running run 期望 409，got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRunAbort(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 预建模板：审批 -> 部署 dev（triggerRun 异步推进到 approve 暂停）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-abort-test", Name: "测试Abort", Kind: KindCD,
		Stages: []StageDef{
			{Name: "审批", Type: StageApprove},
			{Name: "部署", Type: StageDeploy, Params: map[string]any{
				"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
			}},
		},
	})
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-abort", AppID: "app-abort", Kind: KindCD, TemplateID: "tpl-abort-test",
	})
	eng := &Engine{Pipelines: s, Runs: s, Builds: fakeBuilder{}, Releases: &fakeReleaser{}}
	h := NewHandler(s, s, s, eng)
	h.Authorize = allowAll

	// run -> 异步推进到 approve 暂停
	req := acmeReq(http.MethodPost, "/api/applications/app-abort/pipelines/"+p.ID+"/run", `{"branch":"main"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp struct {
		Data PipelineRun `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	runID := resp.Data.ID

	// 等 paused
	run := waitRun(t, s, ctx, runID, RunPaused, 2*time.Second)
	if run.Status != RunPaused {
		t.Fatalf("期望 paused，got %s", run.Status)
	}
	// abort
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+runID+"/abort", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("abort 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	run, _ = s.GetRun(ctx, runID)
	if run.Status != RunAborted {
		t.Fatalf("期望 aborted，got %s", run.Status)
	}
	// 再次 abort 已终态 -> 409
	req = acmeReq(http.MethodPost, "/api/pipelineruns/"+runID+"/abort", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("已终态 abort 期望 409，got %d", rec.Code)
	}
}

// TestAdminTemplateCRUD 验证 admin 模板 CRUD + builtin 保护 + 非 super_admin 403。
func TestAdminTemplateCRUD(t *testing.T) {
	s := NewMemoryStore()
	_ = SeedTemplates(acmeCtxEngine(), s) // 灌 builtin tpl-ci/tpl-cd

	// 非 super_admin → 403
	hNoAdmin := NewHandler(s, s, s, nil)
	hNoAdmin.Authorize = allowAll
	req := acmeReq(http.MethodGet, "/api/admin/pipeline-templates", "")
	rec := httptest.NewRecorder()
	hNoAdmin.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非 super_admin 期望 403，got %d", rec.Code)
	}

	// super_admin
	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll
	h.isPlatformAdmin = func(*http.Request) bool { return true }

	// 列表（含 builtin）
	req = acmeReq(http.MethodGet, "/api/admin/pipeline-templates", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list 期望 200，got %d", rec.Code)
	}

	// 创建公共模板
	req = acmeReq(http.MethodPost, "/api/admin/pipeline-templates",
		`{"name":"Public CI","kind":"ci","stages":[{"name":"build","type":"build"}]}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create 期望 201，got %d body %s", rec.Code, rec.Body.String())
	}
	var created PipelineTemplate
	_ = json.Unmarshal(rec.Body.Bytes(), &struct {
		Data *PipelineTemplate `json:"data"`
	}{Data: &created})
	if created.Builtin {
		t.Fatal("admin create 必为非 builtin")
	}

	// 更新自定义模板
	req = acmeReq(http.MethodPut, "/api/admin/pipeline-templates/"+created.ID,
		`{"name":"Public CI v2","kind":"ci","stages":[{"name":"build","type":"build"}]}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin update 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}

	// builtin 拒改（tpl-ci）
	req = acmeReq(http.MethodPut, "/api/admin/pipeline-templates/tpl-ci",
		`{"name":"hack","kind":"ci","stages":[]}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("builtin update 期望 409，got %d", rec.Code)
	}

	// builtin 拒删
	req = acmeReq(http.MethodDelete, "/api/admin/pipeline-templates/tpl-ci", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("builtin delete 期望 409，got %d", rec.Code)
	}

	// 删自定义模板
	req = acmeReq(http.MethodDelete, "/api/admin/pipeline-templates/"+created.ID, "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin delete 期望 200，got %d", rec.Code)
	}
}

// TestPipelineWebhookTrigger 验证 webhook 触发器：创建生成 token、get 清空 token、
// webhook 端点正确 token+push 触发 run、错误 token 401、不匹配分支静默忽略。
func TestPipelineWebhookTrigger(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	// 自定义模板（无占位符，避免 paramResolver 依赖）
	_, _ = s.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-wh", Name: "WH测试", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
	})
	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	// 创建 webhook pipeline（应自动生成 token）
	req := acmeReq(http.MethodPost, "/api/applications/app-wh/pipelines",
		`{"name":"p-wh","kind":"ci","templateId":"tpl-wh","trigger":{"type":"webhook","branch":"main"}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建期望 201，got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data Pipeline `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	pid := resp.Data.ID
	token := resp.Data.Trigger.Token
	if token == "" {
		t.Fatal("webhook pipeline 应自动生成 token")
	}

	// get 返回 token（同租户可见，designer 展示 webhook URL 配 Gitea 用）
	req = acmeReq(http.MethodGet, "/api/applications/app-wh/pipelines/"+pid, "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var get1 struct {
		Data Pipeline `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &get1)
	if get1.Data.Trigger.Token == "" {
		t.Fatal("get pipeline 应返回 trigger token（同租户可见）")
	}

	// webhook 触发：正确 token + Gitea push event（无 tenant ctx，模拟外部 webhook 调用）
	pushBody := `{"ref":"refs/heads/main","after":"abc123"}`
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/pipeline/"+pid+"?token="+token, strings.NewReader(pushBody))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook 触发期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	runs, _ := s.ListRuns(ctx, "", pid, "")
	if len(runs) != 1 {
		t.Fatalf("期望 1 个 run，got %d", len(runs))
	}
	if runs[0].Trigger != TriggerWebhook {
		t.Errorf("run trigger 期望 webhook，got %s", runs[0].Trigger)
	}
	if runs[0].Branch != "main" || runs[0].Commit != "abc123" {
		t.Errorf("run branch/commit 错: %s/%s", runs[0].Branch, runs[0].Commit)
	}

	// 错误 token -> 401
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/pipeline/"+pid+"?token=wrong", strings.NewReader(pushBody))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错误 token 期望 401，got %d", rec.Code)
	}

	// 不匹配分支（Trigger.Branch=main，push other）-> 200 但不触发额外 run
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/pipeline/"+pid+"?token="+token,
		strings.NewReader(`{"ref":"refs/heads/other","after":"d"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("不匹配分支期望 200，got %d", rec.Code)
	}
	runs2, _ := s.ListRuns(ctx, "", pid, "")
	if len(runs2) != 1 {
		t.Errorf("不匹配分支不应触发，期望仍 1 run，got %d", len(runs2))
	}

	// 非 webhook 类型 pipeline 的 webhook 端点 -> 400
	req2 := acmeReq(http.MethodPost, "/api/applications/app-wh2/pipelines",
		`{"name":"p-manual","kind":"ci","templateId":"tpl-wh","trigger":{"type":"manual"}}`)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	var resp2 struct {
		Data Pipeline `json:"data"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/pipeline/"+resp2.Data.ID+"?token=x", strings.NewReader(pushBody))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非 webhook pipeline 触发期望 400，got %d", rec.Code)
	}
}
