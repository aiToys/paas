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
	if len(resp.Data.Stages) == 0 {
		t.Fatal("从模板创建 stages 期望非空（复制自模板）")
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
	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	// 缺 name -> 400
	req := acmeReq(http.MethodPost, "/api/applications/app-1/pipelines",
		`{"kind":"ci","stages":[{"name":"b","type":"build"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 name 期望 400，got %d body %s", rec.Code, rec.Body.String())
	}

	// 空 stages -> 400
	req = acmeReq(http.MethodPost, "/api/applications/app-1/pipelines",
		`{"name":"p1","kind":"ci","stages":[]}`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 stages 期望 400，got %d", rec.Code)
	}
}

func TestPipelineCrossTenantIsolation(t *testing.T) {
	s := NewMemoryStore()
	acmeCtx := tenant.WithTenant(context.Background(), "t-acme")
	_, _ = s.CreatePipeline(acmeCtx, Pipeline{
		Name: "p-acme", AppID: "app-1", Kind: KindCI,
		Stages: []StageDef{{Name: "b", Type: StageBuild}},
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
	json.Unmarshal(rec.Body.Bytes(), &resp)
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
	req = acmeReq(http.MethodPost, "/api/applications/app-1/pipelines", `{"name":"p","kind":"ci","stages":[{"name":"b","type":"build"}]}`)
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
		Name: "p1", AppID: "app-1", Kind: KindCI,
		Stages: []StageDef{{Name: "b", Type: StageBuild}},
	})

	h := NewHandler(s, s, s, nil)
	h.Authorize = allowAll

	// PUT 更新（加 deploy stage）
	body := `{"name":"p1-renamed","kind":"ci","stages":[{"name":"b","type":"build"},{"name":"d","type":"deploy","params":{"envId":"e1"}}]}`
	req := acmeReq(http.MethodPut, "/api/applications/app-1/pipelines/"+p.ID, body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	var updated struct {
		Data Pipeline `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Data.Name != "p1-renamed" {
		t.Fatalf("更新后 name 期望 p1-renamed，got %s", updated.Data.Name)
	}
	if len(updated.Data.Stages) != 2 {
		t.Fatalf("更新后 stages 期望 2，got %d", len(updated.Data.Stages))
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
	json.Unmarshal(rec.Body.Bytes(), &resp)
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
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-ci", AppID: "app-ci", Kind: KindCI,
		Stages: []StageDef{
			{Name: "构建", Type: StageBuild},
			{Name: "部署", Type: StageDeploy, Params: map[string]any{"envId": "env-dev", "imageSource": ImagePriorBuild}},
		},
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
	json.Unmarshal(rec.Body.Bytes(), &resp)
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
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-cd", AppID: "app-cd", Kind: KindCD,
		Stages: []StageDef{
			{Name: "审批", Type: StageApprove},
			{Name: "部署", Type: StageDeploy, Params: map[string]any{
				"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
			}},
		},
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
	json.Unmarshal(rec.Body.Bytes(), &resp)
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
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-prod", AppID: "app-prod", Kind: KindCD,
		Stages: []StageDef{
			{Name: "部署", Type: StageDeploy, Params: map[string]any{
				"envId": "env-prod", "imageSource": ImageSelected, "imageId": "img-1",
			}},
		},
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

func TestRunSingleInstance(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-si", AppID: "app-si", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
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
	p, _ := s.CreatePipeline(ctx, Pipeline{
		Name: "p-abort", AppID: "app-abort", Kind: KindCD,
		Stages: []StageDef{
			{Name: "审批", Type: StageApprove},
			{Name: "部署", Type: StageDeploy, Params: map[string]any{
				"envId": "env-dev", "imageSource": ImageSelected, "imageId": "img-1",
			}},
		},
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
	json.Unmarshal(rec.Body.Bytes(), &resp)
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
