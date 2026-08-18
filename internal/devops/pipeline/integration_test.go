//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表避免残留。
//
// 测试覆盖：
//   - Pipeline CRUD（Create/Get/List/Update/Delete 级联清 runs）
//   - 多租户隔离（缺失拒、跨租户 not found 不泄漏）
//   - 同 (tenant,app,name) 唯一 -> ErrPipelineExists
//   - Template：平台预置（tenant_id="")跨租户可见 + 租户自定义跨租户隔离 + 同 (tenant,name) 唯一
//   - CreateRun：RepoID 持久化 + 初始 stage_runs 写入 + pipeline 不存在 ErrPipelineNotFound
//   - CreateRun 单实例串行：同 pipeline 已有 running -> ErrActiveRunExists
//   - UpdateRun：全量重写 stage_runs（DELETE + INSERT）+ RepoID 保留
//   - 跨租户访问 not found 不泄漏（GetRun/GetPipeline）

package pipeline

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表。
// 复用 devops/pg 样板模式，resetSchema 覆盖 pipeline 4 表 + 相关租户/应用表。
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

// resetSchema 清空整个 public schema 并重建，保证下测从零迁移。
// 用 DROP SCHEMA 而非逐表 DROP：跨模块表 40+ 张且随迭代增长，逐表列举易漏（DRY）。
// 串行 make test-pg 模式下各包 newTestDB 都会 RunMigrations，DROP SCHEMA 不影响后续包。
func resetSchema(t *testing.T, db *pg.DB) {
	t.Helper()
	if _, err := db.Pool().Exec(context.Background(),
		`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func acmeCtx() context.Context     { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-globex") }
func noTenantCtx() context.Context { return context.Background() }

// samplePipeline 构造一个 Pipeline 样本（不带 ID/TenantID，由 Create 补）。
// 模板+绑定模型：Pipeline 只存 TemplateID + ParamOverrides，Stages 运行时从模板解析。
func samplePipeline(name string) Pipeline {
	return Pipeline{
		AppID:      "app-cs",
		Name:       name,
		Kind:       KindCI,
		TemplateID: "tpl-test",
		Trigger:    PipelineTrigger{Type: "manual"},
	}
}

// mustCreatePipeline 创建并断言成功（测试辅助）。
func mustCreatePipeline(t *testing.T, s *pgStore, ctx context.Context, name string) Pipeline {
	t.Helper()
	p, err := s.CreatePipeline(ctx, samplePipeline(name))
	if err != nil {
		t.Fatalf("CreatePipeline 失败: %v", err)
	}
	return p
}

// ---------- Pipeline CRUD ----------

func TestPGPipelineCRUD(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())

	// Create
	p := mustCreatePipeline(t, s, acmeCtx(), "ci-main")
	if p.ID == "" {
		t.Fatal("Create 未回填 ID")
	}
	if p.TenantID != "t-acme" {
		t.Fatalf("TenantID 应以 ctx 为准，got %q", p.TenantID)
	}

	// Get
	got, err := s.GetPipeline(acmeCtx(), p.ID)
	if err != nil {
		t.Fatalf("GetPipeline 失败: %v", err)
	}
	if got.Name != "ci-main" || got.TemplateID != "tpl-test" {
		t.Fatalf("Get 回读不一致: %+v", got)
	}

	// List（按 appID 过滤）
	list, err := s.ListPipelines(acmeCtx(), "app-cs")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListPipelines app-cs 期望 1 条，got %d err %v", len(list), err)
	}
	list2, err := s.ListPipelines(acmeCtx(), "other-app")
	if err != nil || len(list2) != 0 {
		t.Fatalf("ListPipelines other-app 期望 0 条，got %d", len(list2))
	}

	// Update（改名 + 参数覆盖）
	p.Name = "ci-main-v2"
	p.ParamOverrides = map[string]any{"0.branchOverride": "dev"}
	upd, err := s.UpdatePipeline(acmeCtx(), p)
	if err != nil {
		t.Fatalf("UpdatePipeline 失败: %v", err)
	}
	if upd.Name != "ci-main-v2" || upd.ParamOverrides["0.branchOverride"] != "dev" {
		t.Fatalf("Update 回读不一致: %+v", upd)
	}

	// Delete
	if err := s.DeletePipeline(acmeCtx(), p.ID); err != nil {
		t.Fatalf("DeletePipeline 失败: %v", err)
	}
	if _, err := s.GetPipeline(acmeCtx(), p.ID); err != ErrPipelineNotFound {
		t.Fatalf("删除后 Get 期望 ErrPipelineNotFound，got %v", err)
	}
}

func TestPGPipelineUniqueName(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	mustCreatePipeline(t, s, acmeCtx(), "dup")
	// 同 (tenant,app,name) 重复 -> ErrPipelineExists
	if _, err := s.CreatePipeline(acmeCtx(), samplePipeline("dup")); err != ErrPipelineExists {
		t.Fatalf("重名创建期望 ErrPipelineExists，got %v", err)
	}
	// 不同 app 同名允许
	if _, err := s.CreatePipeline(acmeCtx(), func() Pipeline {
		p := samplePipeline("dup")
		p.AppID = "app-other"
		return p
	}()); err != nil {
		t.Fatalf("不同 app 同名应允许，got %v", err)
	}
}

func TestPGPipelineMultiTenant(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	p := mustCreatePipeline(t, s, acmeCtx(), "tenanted")

	// 跨租户 Get -> NotFound 不泄漏
	if _, err := s.GetPipeline(globexCtx(), p.ID); err != ErrPipelineNotFound {
		t.Fatalf("跨租户 Get 期望 ErrPipelineNotFound，got %v", err)
	}
	// 跨租户 List 不见
	gList, _ := s.ListPipelines(globexCtx(), "")
	if len(gList) != 0 {
		t.Fatalf("globex 不应见 acme 的 pipeline，got %d 条", len(gList))
	}
	// 跨租户 Delete -> NotFound
	if err := s.DeletePipeline(globexCtx(), p.ID); err != ErrPipelineNotFound {
		t.Fatalf("跨租户 Delete 期望 ErrPipelineNotFound，got %v", err)
	}
	// 无租户 ctx -> ErrNoTenant
	if _, err := s.GetPipeline(noTenantCtx(), p.ID); err != ErrNoTenant {
		t.Fatalf("无租户 ctx 期望 ErrNoTenant，got %v", err)
	}
}

// ---------- Template ----------

func TestPGTemplateBuiltinAndCustom(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())

	// 平台预置（tenant_id=""）-- 用 acmeCtx 创建（调用方为平台 admin 语义）
	builtin := PipelineTemplate{
		Name: "tpl-builtin-ci", Kind: KindCI, Builtin: true,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
	}
	if _, err := s.CreateTemplate(acmeCtx(), builtin); err != nil {
		t.Fatalf("CreateTemplate 平台预置失败: %v", err)
	}

	// acme 自定义
	acmeTpl := PipelineTemplate{
		TenantID: "t-acme", Name: "tpl-acme", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
	}
	if _, err := s.CreateTemplate(acmeCtx(), acmeTpl); err != nil {
		t.Fatalf("CreateTemplate acme 自定义失败: %v", err)
	}

	// globex 自定义
	globexTpl := PipelineTemplate{
		TenantID: "t-globex", Name: "tpl-globex", Kind: KindCD,
		Stages: []StageDef{{Name: "审批", Type: StageApprove}},
	}
	if _, err := s.CreateTemplate(globexCtx(), globexTpl); err != nil {
		t.Fatalf("CreateTemplate globex 自定义失败: %v", err)
	}

	// acme 视角：可见 平台预置 + acme 自定义，不见 globex
	acmeList, _ := s.ListTemplates(acmeCtx())
	if len(acmeList) != 2 {
		t.Fatalf("acme 应见 2 个模板（builtin+acme），got %d", len(acmeList))
	}
	// 平台预置在前
	if acmeList[0].TenantID != "" || !acmeList[0].Builtin {
		t.Fatalf("首个应为平台预置，got %+v", acmeList[0])
	}

	// globex 视角：可见 平台预置 + globex 自定义，不见 acme
	globexList, _ := s.ListTemplates(globexCtx())
	if len(globexList) != 2 {
		t.Fatalf("globex 应见 2 个模板，got %d", len(globexList))
	}

	// 跨租户 Get acme 自定义 -> NotFound
	acmeTplID := acmeList[1].ID
	if _, err := s.GetTemplate(globexCtx(), acmeTplID); err != ErrPipelineNotFound {
		t.Fatalf("跨租户 Get 模板期望 ErrPipelineNotFound，got %v", err)
	}
	// 平台预置跨租户可见
	builtinID := acmeList[0].ID
	if _, err := s.GetTemplate(globexCtx(), builtinID); err != nil {
		t.Fatalf("平台预置应跨租户可见，got %v", err)
	}

	// 同 (tenant,name) 唯一 -- acme 再建同名自定义 -> ErrTemplateExists
	if _, err := s.CreateTemplate(acmeCtx(), PipelineTemplate{
		TenantID: "t-acme", Name: "tpl-acme", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
	}); err != ErrTemplateExists {
		t.Fatalf("重名模板期望 ErrTemplateExists，got %v", err)
	}

	// ReplaceBuiltinTemplate：覆盖 builtin 的 stages/name/description/version（seed 升级路径）
	newStages := []StageDef{
		{Name: "构建", Type: StageBuild},
		{Name: "部署", Type: StageDeploy, Params: map[string]any{"envId": "{{app.env.test}}"}},
	}
	replaced := PipelineTemplate{
		ID: builtinID, Name: "tpl-builtin-ci-v2", Kind: KindCI, Builtin: true, Version: 2,
		Description: "升级版", Stages: newStages,
	}
	if err := s.ReplaceBuiltinTemplate(context.Background(), replaced); err != nil {
		t.Fatalf("ReplaceBuiltinTemplate 失败: %v", err)
	}
	got, err := s.GetTemplate(globexCtx(), builtinID)
	if err != nil {
		t.Fatalf("覆盖后 Get 失败: %v", err)
	}
	if got.Version != 2 || got.Description != "升级版" || len(got.Stages) != 2 {
		t.Fatalf("覆盖后字段不符: %+v", got)
	}
	if got.TenantID != "" || !got.Builtin {
		t.Fatalf("覆盖不应改 kind/tenant/builtin: %+v", got)
	}
}

// ---------- PipelineRun ----------

// mustCreateRun 辅助：先建 pipeline，再建 run（含初始 stage_runs + RepoID）。
func mustCreateRun(t *testing.T, s *pgStore, ctx context.Context, pipelineName, status string) (Pipeline, PipelineRun) {
	t.Helper()
	p := mustCreatePipeline(t, s, ctx, pipelineName)
	r := PipelineRun{
		PipelineID: p.ID, AppID: p.AppID, Branch: "main", Commit: "abc12345",
		RepoID: "repo-xyz", Trigger: "manual", Status: status, CurrentStage: 0,
		StageRuns: []StageRun{
			{Index: 0, Type: StageBuild, Name: "构建", Status: StagePending},
			{Index: 1, Type: StageDeploy, Name: "部署", Status: StagePending},
		},
	}
	created, err := s.CreateRun(ctx, r)
	if err != nil {
		t.Fatalf("CreateRun 失败: %v", err)
	}
	return p, created
}

func TestPGRunCreateAndRepoIDPersisted(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	_, r := mustCreateRun(t, s, acmeCtx(), "p1", RunRunning)

	// 回读验证 RepoID 持久化（防 runCols 漏列）
	got, err := s.GetRun(acmeCtx(), r.ID)
	if err != nil {
		t.Fatalf("GetRun 失败: %v", err)
	}
	if got.RepoID != "repo-xyz" {
		t.Fatalf("RepoID 未持久化，期望 repo-xyz got %q", got.RepoID)
	}
	if len(got.StageRuns) != 2 {
		t.Fatalf("初始 stage_runs 期望 2 条，got %d", len(got.StageRuns))
	}
	if got.StageRuns[0].Type != StageBuild || got.StageRuns[1].Type != StageDeploy {
		t.Fatalf("stage_runs 顺序/类型错: %+v", got.StageRuns)
	}
}

func TestPGRunPipelineNotFound(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	// 不存在的 pipelineID
	_, err := s.CreateRun(acmeCtx(), PipelineRun{
		PipelineID: "no-such", AppID: "app-cs", Trigger: "manual", Status: RunRunning,
	})
	if err != ErrPipelineNotFound {
		t.Fatalf("不存在的 pipeline 期望 ErrPipelineNotFound，got %v", err)
	}
}

func TestPGRunSingleInstance(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	_, r := mustCreateRun(t, s, acmeCtx(), "p2", RunRunning)

	// 同 pipeline 已有 running -> ErrActiveRunExists
	_, err := s.CreateRun(acmeCtx(), PipelineRun{
		PipelineID: r.PipelineID, AppID: r.AppID, Trigger: "manual", Status: RunRunning,
	})
	if err != ErrActiveRunExists {
		t.Fatalf("单实例串行期望 ErrActiveRunExists，got %v", err)
	}

	// HasActiveRun 确认
	has, _ := s.HasActiveRun(acmeCtx(), r.PipelineID)
	if !has {
		t.Fatal("HasActiveRun 期望 true")
	}

	// 推进到 succeeded 后可再建
	r.Status = RunSucceeded
	r.FinishedAt = time.Now()
	if _, err := s.UpdateRun(acmeCtx(), r); err != nil {
		t.Fatalf("UpdateRun 失败: %v", err)
	}
	has2, _ := s.HasActiveRun(acmeCtx(), r.PipelineID)
	if has2 {
		t.Fatal("succeeded 后 HasActiveRun 期望 false")
	}
	// 再建成功
	if _, err := s.CreateRun(acmeCtx(), PipelineRun{
		PipelineID: r.PipelineID, AppID: r.AppID, Trigger: "manual", Status: RunRunning,
	}); err != nil {
		t.Fatalf("succeeded 后再建期望成功，got %v", err)
	}
}

func TestPGRunUpdateRewriteStages(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	_, r := mustCreateRun(t, s, acmeCtx(), "p3", RunRunning)

	// 推进：stage 0 success + 写输出，stage 1 running
	r.StageRuns[0].Status = StageSuccess
	r.StageRuns[0].Output = map[string]any{OutImageID: "img-abc"}
	r.StageRuns[0].FinishedAt = time.Now()
	r.StageRuns[1].Status = StageRunning
	r.StageRuns[1].StartedAt = time.Now()
	r.CurrentStage = 1
	upd, err := s.UpdateRun(acmeCtx(), r)
	if err != nil {
		t.Fatalf("UpdateRun 失败: %v", err)
	}
	if upd.StageRuns[0].Output[OutImageID] != "img-abc" {
		t.Fatalf("stage 输出未持久化: %+v", upd.StageRuns[0].Output)
	}
	if upd.CurrentStage != 1 {
		t.Fatalf("CurrentStage 期望 1，got %d", upd.CurrentStage)
	}

	// 验证 stage_runs 全量重写（不是追加）：读 DB stage 数应 == 2
	got, _ := s.GetRun(acmeCtx(), r.ID)
	if len(got.StageRuns) != 2 {
		t.Fatalf("全量重写后 stage_runs 期望 2 条，got %d（可能未 DELETE 旧记录）", len(got.StageRuns))
	}
	if got.StageRuns[0].Status != StageSuccess || got.StageRuns[1].Status != StageRunning {
		t.Fatalf("stage 状态未更新: %+v", got.StageRuns)
	}
}

func TestPGRunCrossTenant(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	_, r := mustCreateRun(t, s, acmeCtx(), "p4", RunRunning)

	// 跨租户 Get -> NotFound
	if _, err := s.GetRun(globexCtx(), r.ID); err != ErrRunNotFound {
		t.Fatalf("跨租户 GetRun 期望 ErrRunNotFound，got %v", err)
	}
	// 跨租户 List 不见
	gList, _ := s.ListRuns(globexCtx(), "", "", "")
	if len(gList) != 0 {
		t.Fatalf("globex 不应见 acme 的 run，got %d 条", len(gList))
	}
	// 跨租户 Update -> NotFound
	r.Status = RunSucceeded
	if _, err := s.UpdateRun(globexCtx(), r); err != ErrRunNotFound {
		t.Fatalf("跨租户 UpdateRun 期望 ErrRunNotFound，got %v", err)
	}
}

// DeletePipeline 级联清 runs 验证。
func TestPGDeletePipelineCascadeRuns(t *testing.T) {
	s := NewPGStore(newTestDB(t).Pool())
	p, r := mustCreateRun(t, s, acmeCtx(), "p5", RunRunning)

	if err := s.DeletePipeline(acmeCtx(), p.ID); err != nil {
		t.Fatalf("DeletePipeline 失败: %v", err)
	}
	// run 应随之不可读（pipeline_runs 已级联删）
	if _, err := s.GetRun(acmeCtx(), r.ID); err != ErrRunNotFound {
		t.Fatalf("删 pipeline 后 GetRun 期望 ErrRunNotFound，got %v", err)
	}
}
