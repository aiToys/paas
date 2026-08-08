package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

func TestPipelineCRUDMultiTenant(t *testing.T) {
	s := NewMemoryStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")

	// Create
	p, err := s.CreatePipeline(ctxA, Pipeline{
		Name:       "p1",
		AppID:      "a1",
		Kind:       KindCI,
		TemplateID: "tpl-test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == "" {
		t.Fatal("no id assigned")
	}
	if p.TemplateID != "tpl-test" {
		t.Fatalf("TemplateID 期望 tpl-test，got %q", p.TemplateID)
	}

	// Get
	got, err := s.GetPipeline(ctxA, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "p1" {
		t.Fatalf("get name=%s", got.Name)
	}

	// 跨租户隔离：t-globex 看不到 t-acme 的 pipeline
	if _, err := s.GetPipeline(ctxB, p.ID); !errors.Is(err, ErrPipelineNotFound) {
		t.Fatalf("cross-tenant want NotFound got %v", err)
	}

	// List 过滤
	listA, _ := s.ListPipelines(ctxA, "a1")
	if len(listA) != 1 {
		t.Fatalf("listA len=%d", len(listA))
	}
	listB, _ := s.ListPipelines(ctxB, "a1")
	if len(listB) != 0 {
		t.Fatalf("listB leak: %d", len(listB))
	}

	// 同 (tenant,app,name) 唯一
	if _, err := s.CreatePipeline(ctxA, Pipeline{
		Name:       "p1",
		AppID:      "a1",
		Kind:       KindCI,
		TemplateID: "tpl-test",
	}); !errors.Is(err, ErrPipelineExists) {
		t.Fatalf("dup want Exists got %v", err)
	}

	// CreateRun 单实例串行：HasActiveRun 拦截
	r1, err := s.CreateRun(ctxA, PipelineRun{
		ID:         "r1",
		AppID:      "a1",
		PipelineID: p.ID,
		Trigger:    "manual",
		Status:     RunRunning,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	_ = r1

	active, err := s.HasActiveRun(ctxA, p.ID)
	if err != nil {
		t.Fatalf("hasActive: %v", err)
	}
	if !active {
		t.Fatal("should have active run")
	}

	if _, err := s.CreateRun(ctxA, PipelineRun{
		ID:         "r2",
		AppID:      "a1",
		PipelineID: p.ID,
		Trigger:    "manual",
		Status:     RunRunning,
	}); !errors.Is(err, ErrActiveRunExists) {
		t.Fatalf("active run want ActiveRunExists got %v", err)
	}

	// 跨租户 HasActiveRun 不可见
	activeB, _ := s.HasActiveRun(ctxB, p.ID)
	if activeB {
		t.Fatal("cross-tenant HasActiveRun leak")
	}

	// UpdateRun 推进 stageRuns 写回
	r1.CurrentStage = 1
	r1.StageRuns = []StageRun{{Index: 0, Type: StageBuild, Name: "build", Status: StageSuccess}}
	if _, err := s.UpdateRun(ctxA, r1); err != nil {
		t.Fatalf("updateRun: %v", err)
	}
	gotRun, _ := s.GetRun(ctxA, r1.ID)
	if len(gotRun.StageRuns) != 1 || gotRun.StageRuns[0].Status != StageSuccess {
		t.Fatalf("stageRun not persisted: %+v", gotRun.StageRuns)
	}
	if gotRun.CurrentStage != 1 {
		t.Fatalf("currentStage=%d", gotRun.CurrentStage)
	}

	// 跨租户 UpdateRun 拒绝
	if _, err := s.UpdateRun(ctxB, r1); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("cross-tenant UpdateRun want NotFound got %v", err)
	}

	// 终态后可再创建 run（单实例串行解锁）
	r1.Status = RunSucceeded
	_, _ = s.UpdateRun(ctxA, r1)
	r2, err := s.CreateRun(ctxA, PipelineRun{
		ID:         "r2",
		AppID:      "a1",
		PipelineID: p.ID,
		Trigger:    "manual",
		Status:     RunRunning,
	})
	if err != nil {
		t.Fatalf("createRun after succeeded: %v", err)
	}
	_ = r2

	// UpdatePipeline
	p.Name = "p1-renamed"
	if _, err := s.UpdatePipeline(ctxA, p); err != nil {
		t.Fatalf("updatePipeline: %v", err)
	}

	// DeletePipeline 级联清 runs
	if err := s.DeletePipeline(ctxA, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetPipeline(ctxA, p.ID); !errors.Is(err, ErrPipelineNotFound) {
		t.Fatalf("after delete want NotFound got %v", err)
	}
	if _, err := s.GetRun(ctxA, r1.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("cascade delete run want NotFound got %v", err)
	}

	// 跨租户 Delete 拒绝（重建一个）
	p2, _ := s.CreatePipeline(ctxA, Pipeline{
		Name: "p2", AppID: "a1", Kind: KindCI, TemplateID: "tpl-test",
	})
	if err := s.DeletePipeline(ctxB, p2.ID); !errors.Is(err, ErrPipelineNotFound) {
		t.Fatalf("cross-tenant delete want NotFound got %v", err)
	}
}

func TestPipelineRunListFilter(t *testing.T) {
	s := NewMemoryStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")

	p, _ := s.CreatePipeline(ctxA, Pipeline{
		Name: "p", AppID: "a", Kind: KindCI, TemplateID: "tpl-test",
	})
	r1, _ := s.CreateRun(ctxA, PipelineRun{AppID: "a", PipelineID: p.ID, Trigger: "manual", Status: RunSucceeded})
	r2, _ := s.CreateRun(ctxA, PipelineRun{AppID: "a", PipelineID: p.ID, Trigger: "manual", Status: RunFailed})

	// status filter
	runs, _ := s.ListRuns(ctxA, "", p.ID, RunSucceeded)
	if len(runs) != 1 || runs[0].ID != r1.ID {
		t.Fatalf("status filter len=%d", len(runs))
	}
	// all
	all, _ := s.ListRuns(ctxA, "", p.ID, "")
	if len(all) != 2 {
		t.Fatalf("all runs len=%d", len(all))
	}
	// appID filter
	byApp, _ := s.ListRuns(ctxA, "a", "", "")
	if len(byApp) != 2 {
		t.Fatalf("byApp len=%d", len(byApp))
	}
	// wrong app
	wrong, _ := s.ListRuns(ctxA, "other", "", "")
	if len(wrong) != 0 {
		t.Fatalf("wrong app leak: %d", len(wrong))
	}
	_ = r2
}

func TestPipelineMissingTenant(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background() // 无租户

	if _, err := s.ListPipelines(ctx, ""); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("want ErrNoTenant got %v", err)
	}
	if _, err := s.CreatePipeline(ctx, Pipeline{}); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("want ErrNoTenant got %v", err)
	}
	if _, err := s.ListRuns(ctx, "", "", ""); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("ListRuns want ErrNoTenant got %v", err)
	}
}

func TestTemplateBuiltinAndCustom(t *testing.T) {
	s := NewMemoryStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")

	// 平台预置（tenant_id="")：admin 调用方 ctx 必须持租户，但模板归属空
	builtin, err := s.CreateTemplate(ctxA, PipelineTemplate{
		ID:       "tpl-builtin",
		TenantID: "", // 平台预置
		Name:     "Builtin CI",
		Kind:     KindCI,
		Stages:   []StageDef{{Name: "build", Type: StageBuild}},
	})
	if err != nil {
		t.Fatalf("create builtin: %v", err)
	}
	if builtin.TenantID != "" {
		t.Fatalf("builtin tenantId should be empty, got %q", builtin.TenantID)
	}

	// 租户自定义（显式带 TenantID；不传会被视为平台预置）
	custom, err := s.CreateTemplate(ctxA, PipelineTemplate{
		Name:     "Acme CI",
		TenantID: "t-acme",
		Kind:     KindCI,
		Stages:   []StageDef{{Name: "build", Type: StageBuild}},
	})
	if err != nil {
		t.Fatalf("create custom: %v", err)
	}
	if custom.TenantID != "t-acme" {
		t.Fatalf("custom tenantId=%q", custom.TenantID)
	}

	// ListTemplates：t-acme 看见 builtin + 自己（2 个）
	listA, _ := s.ListTemplates(ctxA)
	if len(listA) != 2 {
		t.Fatalf("listA len=%d", len(listA))
	}
	// t-globex 只看见 builtin（1 个）
	listB, _ := s.ListTemplates(ctxB)
	if len(listB) != 1 || listB[0].ID != "tpl-builtin" {
		t.Fatalf("listB should only see builtin, got %+v", listB)
	}

	// 跨租户访问租户自定义 not found
	if _, err := s.GetTemplate(ctxB, custom.ID); !errors.Is(err, ErrPipelineNotFound) {
		t.Fatalf("cross-tenant GetTemplate want NotFound got %v", err)
	}

	// builtin 跨租户可见
	got, err := s.GetTemplate(ctxB, "tpl-builtin")
	if err != nil || got.Name != "Builtin CI" {
		t.Fatalf("builtin should be visible cross-tenant, got %v / %+v", err, got)
	}

	// 同名冲突：两个租户都建自定义同名（应各自允许）
	if _, err := s.CreateTemplate(ctxB, PipelineTemplate{
		Name:     "Acme CI",
		TenantID: "t-globex",
		Kind:     KindCI,
		Stages:   []StageDef{{Name: "build", Type: StageBuild}},
	}); err != nil {
		t.Fatalf("cross-tenant same name custom should be allowed: %v", err)
	}
}

// TestPipelineDeepCopy 防 race：拿到返回后修改原切片/map，store 内部不应被波及。
func TestPipelineDeepCopy(t *testing.T) {
	s := NewMemoryStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")

	// Pipeline 现含 ParamOverrides（绑定模型下替代 Stages 的可变字段，深拷贝防 race）
	p, _ := s.CreatePipeline(ctxA, Pipeline{
		Name: "p", AppID: "a", Kind: KindCI, TemplateID: "tpl-test",
		ParamOverrides: map[string]any{"0.k": "v"},
	})

	got, _ := s.GetPipeline(ctxA, p.ID)
	got.ParamOverrides["0.k"] = "mutated"

	again, _ := s.GetPipeline(ctxA, p.ID)
	if again.ParamOverrides["0.k"] != "v" {
		t.Fatalf("ParamOverrides 深拷贝失败: got %v", again.ParamOverrides["0.k"])
	}

	// run stageRuns 深拷贝
	r, _ := s.CreateRun(ctxA, PipelineRun{
		AppID: "a", PipelineID: p.ID, Trigger: "manual", Status: RunRunning,
		StageRuns: []StageRun{{Index: 0, Name: "build", Output: map[string]any{"imageId": "img-1"}}},
	})
	gr, _ := s.GetRun(ctxA, r.ID)
	gr.StageRuns[0].Output["imageId"] = "tampered"
	gr2, _ := s.GetRun(ctxA, r.ID)
	if gr2.StageRuns[0].Output["imageId"] != "img-1" {
		t.Fatalf("stageRun output deep copy failed: %v", gr2.StageRuns[0].Output["imageId"])
	}
}
