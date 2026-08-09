# 流水线 deploy/release 分离 + 泳道 + 实时日志 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把流水线 `deploy`（部署到环境×泳道 + 部署记录）与 `release`（打版本号里程碑）解耦，接入泳道 lane 参数（L1 数据模型，为 L2 联调锁接口），并补 stage 实时日志。

**Architecture:** engine 层 `execDeploy` 改调新 `Releaser.Deploy(ctx,appID,envID,lane,imageID)`（找/建对应 lane 基线 Workload + UpdateImage + 产生部署记录，不打版本）；新增 `execRelease` 调 `Releaser.Publish`（git tag + Image.Version + PipelineRun.version，不部署）。`baseline` 瘦身为只合并主干。`promote` 重定义为 deploy 的「自动算下一环境」变体。StageRun 加 `Log` 字段，engine 各 stage 用 `logf` helper 追加事件，前端展开查看。deploy 带 `lane` 参数，Workload 唯一性扩到 (app,env,lane)。

**Tech Stack:** Go（controller-runtime 风格 engine + pgxpool）、Vue 3 + TS + Element Plus、PostgreSQL JSONB。

## Global Constraints

- 主语言 Go；业务领域逻辑不进 Platform Core（pipeline 是 DevOps 子系统，属业务域，OK）。
- 多租户隔离：所有 store 方法强制 tenant 过滤；跨租户 not found 不泄漏。
- 错误脱敏：`httputil.WriteServiceError` 不泄漏 pgx/SQLSTATE；deploy/release 错误按 sentinel 分类。
- 出站 HTTP 用 `httputil.NewClient`（CheckRedirect=ErrUseLastResponse）。
- 生产写操作受 `prod:write` 保护（deploy 到 prod 环境需 `allowProd`，已有机制，deploy stage 复用）。
- 注释语言与代码库一致（中文）。
- 测试：`go test ./...` 全绿；PG 集成测试 `//go:build integration` 门控。
- 项目未上线，PG schema 可在 `0001_init` 合并新列 + 增量 migration 并行（已部署实例走增量 `ADD COLUMN IF NOT EXISTS`）。
- 未经用户明确要求不执行 git commit（SDD 执行时每个 task 末尾 commit 是 plan 指令，属明确授权）。

**Spec:** `docs/superpowers/specs/2026-08-09-pipeline-deploy-release-lane-design.md`

---

## File Structure

**后端（pipeline 引擎 + devops store + workload）：**
- `internal/devops/pipeline/model.go` — StageRun 加 Log；StageType 加 StageRelease；lane 常量；validate 更新。
- `internal/devops/pipeline/engine.go` — execDeploy 改写（Deploy+lane+logf）/ 新增 execRelease / execBaseline 瘦身 / execPromote 重定义 / logf helper。
- `internal/devops/pipeline/resolver.go` — ParamResolver 加 `{{run.branch}}` 占位符。
- `internal/devops/pipeline/templates.go` — tpl-ci/tpl-cd 重写。
- `cmd/core/pipeline_adapters.go` — releaseBridge 实现 Deploy/Publish。
- `internal/devops/model.go` — Release 加 LaneID/SourceRunID；Image 加 Version。
- `internal/devops/memory/store.go` + `internal/devops/pg/store.go` — CreateRelease 找/建含 lane；Release/Image 新列读写。
- `internal/workload/model.go` + `memory` + `pg` — Repository List 加 laneID 过滤。

**前端（console-user）：**
- `frontend/console-user/src/views/app-tabs/PipelineRunView.vue` — stage 展开 + 日志区 + lane 展示 + build 拉全量日志。
- `frontend/console-user/src/views/app-tabs/PipelineDesigner.vue` — deploy stage 加 lane 参数。
- `frontend/console-user/src/api/pipeline.ts` — StageRun.Log 类型。

**持久化：**
- `internal/storage/pg/migrations/0022_pipeline_lane_release.up.sql` — releases 加 lane_id/source_run_id；images 加 version；pipeline_stage_runs 加 log（或 stage_runs 序列化内含）。

---

## Task 1: model 层字段与常量扩展

**Files:**
- Modify: `internal/devops/pipeline/model.go`
- Test: `internal/devops/pipeline/model_test.go`

**Interfaces:**
- Produces: `StageRelease = "release"` 常量；`StageRun.Log string` 字段；`LaneDefault = "default"` 常量（pipeline 层，复用 workload.LaneDefault 语义）；`StageDef.validate()` 接受 StageRelease。

- [ ] **Step 1: 写失败测试**

追加到 `model_test.go`：

```go
func TestStageDefValidateAcceptsRelease(t *testing.T) {
	s := StageDef{Name: "发布版本", Type: StageRelease}
	if err := s.validate(); err != nil {
		t.Errorf("release stage 应合法: %v", err)
	}
}

func TestStageRunLogField(t *testing.T) {
	sr := StageRun{Index: 0, Type: StageBuild, Name: "构建", Log: "构建已提交\n"}
	if sr.Log != "构建已提交\n" {
		t.Error("StageRun.Log 字段未生效")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/pipeline/ -run TestStageDefValidateAcceptsRelease -v`
Expected: FAIL（`StageRelease` 未定义 / validate 不接受）。

- [ ] **Step 3: 实现**

`model.go`：
- `StageType` 常量块加 `StageRelease = "release"`。
- `validate()` switch 加 `case StageRelease:`。
- `StageRun` struct 加 `Log string \`json:"log,omitempty"\``（在 Error 字段后）。
- 顶部常量区加 `const LaneDefault = "default"` 并注释「与 workload.LaneDefault 同值；pipeline 层独立声明避免 import 循环」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/pipeline/ -run 'TestStageDefValidateAcceptsRelease|TestStageRunLogField' -v`
Expected: PASS。

- [ ] **Step 5: 更新 devops model（Release/Image 字段）+ 测试**

`internal/devops/model.go`：
- `Release` struct 加：
  ```go
  LaneID     string `json:"laneId,omitempty"`     // 部署到的泳道（default=基线）
  SourceRunID string `json:"sourceRunId,omitempty"` // 由哪次 pipeline run 部署
  ```
- `Image` struct 加 `Version string \`json:"version,omitempty"\``（release stage 写入点）。
- 在 `internal/devops/model_test.go`（若无则新建）加：
  ```go
  func TestReleaseLaneAndSourceRunFields(t *testing.T) {
  	r := Release{LaneID: "feature-x", SourceRunID: "run-abc"}
  	if r.LaneID != "feature-x" || r.SourceRunID != "run-abc" {
  		t.Error("Release 新字段未生效")
  	}
  }
  ```

Run: `go test ./internal/devops/ -run TestReleaseLane -v` → PASS。

- [ ] **Step 6: commit**

```bash
git add internal/devops/pipeline/model.go internal/devops/pipeline/model_test.go internal/devops/model.go internal/devops/model_test.go
git commit -m "feat(pipeline): model 层加 StageRelease/StageRun.Log/Release.LaneID/Image.Version"
```

---

## Task 2: workload Repository 按 lane 过滤

**Files:**
- Modify: `internal/workload/repository.go`, `internal/workload/memory/store.go`, `internal/workload/pg/store.go`, 调用方适配
- Test: `internal/workload/memory/store_test.go`, `internal/workload/handler_test.go`（stub 适配）

**Interfaces:**
- Produces: `Repository.List` 签名变为 `List(ctx, envID, appID, laneID, wtype string) ([]Workload, error)`（laneID 空串=不过滤，向后兼容）。
- Consumes: 无新依赖。

- [ ] **Step 1: 写失败测试**

`memory/store_test.go` 加：
```go
func TestListFilterByLane(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t1")
	s.Create(ctx, Workload{ID: "wl-1", AppID: "a", EnvID: "e", LaneID: LaneDefault, Type: TypeService, Name: "a-svc", Image: "img"})
	s.Create(ctx, Workload{ID: "wl-2", AppID: "a", EnvID: "e", LaneID: "feature-x", Type: TypeService, Name: "a-svc-x", Image: "img"})

	// lane="" 返回全部
	all, _ := s.List(ctx, "e", "a", "", TypeService)
	if len(all) != 2 {
		t.Errorf("lane 空应返 2，得 %d", len(all))
	}
	// lane=feature-x 只返泳道
	feature, _ := s.List(ctx, "e", "a", "feature-x", TypeService)
	if len(feature) != 1 || feature[0].ID != "wl-2" {
		t.Errorf("lane=feature-x 应返 wl-2，得 %+v", feature)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/workload/ -run TestListFilterByLane -v`
Expected: 编译失败（List 签名不匹配）。

- [ ] **Step 3: 实现**

- `repository.go`：`List(ctx context.Context, envID, appID, laneID, wtype string) ([]Workload, error)`
- `memory/store.go`：List 实现加 `laneID != "" && w.LaneID != laneID { continue }`
- `pg/store.go`：SQL 加 `AND ($4 = '' OR lane_id = $4)`（参数顺序调整，wtype 顺延为 $5）
- 适配所有 List 调用方：`internal/workload/handler.go`、`internal/workload/admin_handler.go`、`internal/devops/memory/store.go:485`（CreateRelease 内）、`internal/devops/pg/store.go`（CreateRelease 内）、`internal/devops/pipeline/store_memory.go`/`store_pg.go`（PollWorkloadReady 相关若有）、前端无关。调用方传 `""` 保持原行为，CreateRelease 处传目标 lane（Task 4 接入）。
- `handler_test.go` / `applier_test.go` 的 stub `List` 签名同步加 laneID 参数（传 ""）。

- [ ] **Step 4: 跑全 workload 测试**

Run: `go test ./internal/workload/... -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
git add internal/workload/
git commit -m "feat(workload): List 加 laneID 过滤参数（空串=不过滤，向后兼容）"
```

---

## Task 3: devops store CreateRelease 找/建含 lane

**Files:**
- Modify: `internal/devops/memory/store.go:462-542`, `internal/devops/pg/store.go`（CreateRelease）
- Test: `internal/devops/memory/store_test.go`, `internal/devops/pg/store_test.go`（integration）

**Interfaces:**
- Produces: `CreateRelease` 找/建基线 Workload 按 (env, app, lane)；Release 记 LaneID。
- Consumes: `workload.List(ctx, envID, appID, laneID, wtype)`（Task 2）。

- [ ] **Step 1: 写失败测试**

`memory/store_test.go` 加：
```go
func TestCreateReleaseUsesLane(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t1")
	// 先建 default 基线 + 一个 image
	s.SeedForTest(ctx) // 或手动 seed app/env/image/workload
	// 部署到 feature-x 泳道
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID: "app-x", EnvID: "env-test", LaneID: "feature-x",
		ImageID: "img-x", CreatedBy: "u1",
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if rel.LaneID != "feature-x" {
		t.Errorf("Release.LaneID = %q, want feature-x", rel.LaneID)
	}
	// 应新建 feature-x 泳道 Workload（不复用 default）
	wls, _ := s.workload.List(ctx, "env-test", "app-x", "feature-x", workload.TypeService)
	if len(wls) != 1 {
		t.Errorf("feature-x 泳道应建 1 个 Workload，得 %d", len(wls))
	}
}
```
（具体 seed helper 按 store 现有测试模式调整；核心断言是 LaneID + 泳道 Workload 创建。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/ -run TestCreateReleaseUsesLane -v`
Expected: FAIL（CreateRelease 不认 LaneID，或建到 default）。

- [ ] **Step 3: 实现**

`devops.ReleaseInput` 加 `LaneID string`（model.go，Task 1 同批或此处补）。

`memory/store.go` CreateRelease：
- 读 `lane := input.LaneID; if lane == "" { lane = workload.LaneDefault }`
- `s.workload.List(ctx, input.EnvID, input.AppID, lane, workload.TypeService)`（加 lane 参数）
- 新建分支的 Workload 设 `LaneID: lane`，Name 设 `appID + "-svc-" + lane`（同 app×env×lane 唯一；default 仍 `appID + "-svc"` 兼容——判断 `lane == LaneDefault` 时用旧名）。
- `rel.LaneID = lane`。

`pg/store.go` CreateRelease：同样按 lane List + Workload.Name 规则 + INSERT releases 加 lane_id 列（列在 Task 9 migration，此处先写 SQL 占位 `$N`，Task 9 补列后串通；本 task 先让 memory 跑通，pg 在 Task 9 统一）。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/devops/ -run TestCreateReleaseUsesLane -v`
Expected: PASS（memory）。pg 路径 Task 9 验证。

- [ ] **Step 5: commit**

```bash
git add internal/devops/
git commit -m "feat(devops): CreateRelease 按 (env,app,lane) 找/建基线 Workload，Release 记 LaneID"
```

---

## Task 4: Releaser 接口 Deploy/Publish + adapter 实现

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（Releaser 接口）, `cmd/core/pipeline_adapters.go`（releaseBridge）
- Test: `cmd/core/pipeline_adapters_test.go`（若无则新建轻量测，或靠 engine 集成测试覆盖）

**Interfaces:**
- Produces（engine.Releaser 新增方法）：
  ```go
  Deploy(ctx, appID, envID, lane, imageID, sourceRunID string) (deployment devops.Release, domain string, err error)
  Publish(ctx, appID, imageID, version, commit string) (tagSha string, err error)
  ```
- Consumes: `devops.ReleaseInput{LaneID}`（Task 3）、`workload.LaneDefault`、gitea client（Publish 打 tag）。

- [ ] **Step 1: 扩接口 + 占位实现先让编译过**

`engine.go` Releaser 接口加 Deploy/Publish 两个方法签名（如上）。

- [ ] **Step 2: adapter 实现 Deploy**

`pipeline_adapters.go` releaseBridge：
```go
// Deploy 部署镜像到 env×lane（找/建基线 Workload + UpdateImage），产生部署记录，不打版本。
// 内部经 CreateRelease 编排（已支持 lane），domain 经 WorkloadDomain 拼探活地址。
func (r *releaseBridge) Deploy(ctx context.Context, appID, envID, lane, imageID, sourceRunID string) (devops.Release, string, error) {
	rel, err := r.releases.CreateRelease(ctx, devops.ReleaseInput{
		AppID: appID, EnvID: envID, LaneID: lane, ImageID: imageID,
	})
	if err != nil {
		return devops.Release{}, "", err
	}
	// 回填 SourceRunID（CreateRelease 不接受此字段，单独 update 或扩展 ReleaseInput）
	if err := r.releases.MarkSourceRun(ctx, rel.ID, sourceRunID); err != nil {
		return devops.Release{}, "", err // 见下 Note
	}
	rel.SourceRunID = sourceRunID
	domain := r.WorkloadDomain(ctx, rel.WorkloadID)
	return rel, domain, nil
}
```
Note：`MarkSourceRun(ctx, releaseID, runID)` 需在 devops ReleaseRepository 加方法（memory/pg 各加一行 UPDATE/赋值）。本 step 一并加。

- [ ] **Step 3: adapter 实现 Publish**

```go
// Publish 打版本号里程碑：git tag + Image.Version 回填。不部署。
func (r *releaseBridge) Publish(ctx context.Context, appID, imageID, version, commit string) (string, error) {
	if err := r.images.SetVersion(ctx, imageID, version); err != nil {
		return "", err
	}
	// 打 git tag（gitea）；commit 为空则跳过 tag
	if commit == "" || r.gitea == nil {
		return "", nil
	}
	owner, repo, err := r.gitea.ResolveRepo(ctx, appID)
	if err != nil {
		return "", nil // external repo 跳过 tag，不报错
	}
	return r.gitea.CreateTag(ctx, owner, repo, version, commit)
}
```
需新增：
- `devops.ImageRepository.SetVersion(ctx, imageID, version)`（memory/pg 各加）。
- `gitea.Client.CreateTag(ctx, owner, repo, tag, commit)`（client.go 加，调 gitea git refs API 创建 tag）。
- `releaseBridge` 加 `images devops.ImageRepository` + `gitea GiteaTagger` 字段；cmd/core 装配处注入。

- [ ] **Step 4: 写测试（adapter 层用 fake）**

`pipeline_adapters_test.go`：用 fake ReleaseRepository（记 MarkSourceRun 调用）+ fake ImageRepository（记 SetVersion）验证 Deploy 传 lane + Publish 调 SetVersion。若 adapter 测试太重，改为在 engine 集成测试（Task 5/6）用 stub Releaser 覆盖——**本 task 至少保证 `go build ./...` 通过 + 现有 engine_test.go 适配新接口（stub Releaser 加 Deploy/Publish 空实现）**。

- [ ] **Step 5: 跑全 pipeline 测试确认编译 + 现有用例不破**

Run: `go test ./internal/devops/pipeline/... ./cmd/core/... `
Expected: PASS（stub 适配后）。

- [ ] **Step 6: commit**

```bash
git add internal/devops/pipeline/engine.go cmd/core/pipeline_adapters.go internal/devops/ internal/devops/gitea/client.go
git commit -m "feat(pipeline): Releaser 接口加 Deploy/Publish + adapter 实现 + Image.SetVersion/gitea.CreateTag"
```

---

## Task 5: engine execDeploy 改用 Deploy + lane + logf

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（execDeploy:214-246, 新增 logf helper）
- Test: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Consumes: `Releaser.Deploy`（Task 4）。
- Produces: `execDeploy` 读 `lane` 参数 + 写 StageRun.Log；`logf(sr, fmt, args)` helper。

- [ ] **Step 1: 写失败测试**

`engine_test.go` 加（用现有 stub Releaser 模式）：
```go
func TestExecDeployUsesLaneAndLogs(t *testing.T) {
	e := newTestEngine(t) // 现有 helper
	// stub Releaser.Deploy 记录收到的 lane
	e.Releases = &deployCapturingReleaser{lane: ""} // 自定义 stub 记 lane
	run := buildRunWithStage(StageDeploy, map[string]any{
		"envId": "env-test", "lane": "feature-x", "imageSource": ImageSelected, "imageId": "img-1",
	})
	_, err := e.execStage(ctx, run, stageDef(run), &run.StageRuns[0])
	if err != nil {
		t.Fatalf("execDeploy: %v", err)
	}
	if got := e.Releases.(*deployCapturingReleaser).lane; got != "feature-x" {
		t.Errorf("Deploy 收到 lane=%q, want feature-x", got)
	}
	if !strings.Contains(run.StageRuns[0].Log, "feature-x") {
		t.Errorf("StageRun.Log 应含 lane，得 %q", run.StageRuns[0].Log)
	}
}
```
（`deployCapturingReleaser` 实现完整 Releaser 接口，Deploy 记录 lane 参数。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/pipeline/ -run TestExecDeployUsesLaneAndLogs -v`
Expected: FAIL（execDeploy 未读 lane / 未调 Deploy）。

- [ ] **Step 3: 实现 logf helper + 改写 execDeploy**

engine.go 顶部加：
```go
// logf 追加 stage 日志事件（append-only，前端展开查看）。
func logf(sr *StageRun, format string, args ...any) {
	sr.Log += time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...) + "\n"
}
```

改写 `execDeploy`：
```go
func (e *Engine) execDeploy(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	imageID, err := e.resolveImage(ctx, stage, *run)
	if err != nil { sr.Error = err.Error(); return true, err }
	envID := strOr(stage.Params, "envId", "")
	if envID == "" { err := fmt.Errorf("deploy stage 缺 envId"); sr.Error = err.Error(); return true, err }
	lane := strOr(stage.Params, "lane", LaneDefault)
	logf(sr, "部署镜像 %s 到 env=%s lane=%s", imageID, envID, lane)
	// prod 环境写受 prod:write 保护（adapter 内 CreateRelease 走 EnvTypeResolver）
	deployment, domain, err := e.Releases.Deploy(ctx, run.AppID, envID, lane, imageID, run.ID)
	if err != nil { sr.Error = err.Error(); return true, err }
	sr.Input = map[string]any{"releaseId": deployment.ID, "imageId": imageID, "lane": lane}
	logf(sr, "等待 Workload %s 就绪", deployment.WorkloadID)
	if err := e.Releases.PollWorkloadReady(ctx, deployment.WorkloadID); err != nil {
		sr.Error = err.Error(); return true, err
	}
	logf(sr, "Workload 就绪，访问地址 %s", domain)
	sr.Output = map[string]any{OutReleaseID: deployment.ID, OutWorkloadDomain: domain}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/pipeline/ -run TestExecDeployUsesLaneAndLogs -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
git add internal/devops/pipeline/engine.go internal/devops/pipeline/engine_test.go
git commit -m "feat(pipeline): execDeploy 用 Releaser.Deploy + lane 参数 + logf 日志"
```

---

## Task 6: engine execRelease 新增

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（execStage switch 加 StageRelease 分支 + 新增 execRelease）
- Test: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Consumes: `Releaser.Publish`（Task 4）；`computeVersion`（现有）。
- Produces: `execRelease` 打版本 + 写 StageRun.Log + PipelineRun.version + 给本 run 部署记录回填 version。

- [ ] **Step 1: 写失败测试**

```go
func TestExecReleasePublishesVersion(t *testing.T) {
	e := newTestEngine(t)
	pub := &publishCapturingReleaser{} // 记录 Publish 收到的 version/imageID
	e.Releases = pub
	run := buildRunWithPriorDeploy() // 前序 deploy 已产出 releaseId
	stage := StageDef{Type: StageRelease, Params: map[string]any{"versionStrategy": "auto-increment"}}
	_, err := e.execStage(ctx, run, stage, &run.StageRuns[run.CurrentStage])
	if err != nil { t.Fatalf("execRelease: %v", err) }
	if pub.version == "" { t.Error("Publish 未收到版本号") }
	if run.Version == "" { t.Error("PipelineRun.version 未写入") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/pipeline/ -run TestExecReleasePublishesVersion -v`
Expected: FAIL（StageRelease 无 handler / unknown stage）。

- [ ] **Step 3: 实现**

engine.go `execStage` switch 加 `case StageRelease: return e.execRelease(...)`。

```go
// execRelease 发布版本号里程碑：打 git tag + Image.Version + 给本 run 部署记录回填 version。
// 不部署（部署是 deploy stage 的事）。version 缺省由 computeVersion 生成。
func (e *Engine) execRelease(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	version := computeVersion(*run, stage)
	logf(sr, "发布版本 %s", version)
	// 取前序 deploy 产出的 imageId（release 标记的是这个镜像）
	imageID, err := resolvePriorOutput(*run, OutImageID)
	if err == nil {
		tagSha, perr := e.Releases.Publish(ctx, run.AppID, imageID, version, run.Commit)
		if perr != nil { sr.Error = perr.Error(); return true, perr }
		if tagSha != "" { logf(sr, "已打 git tag %s @ %s", version, tagSha[:min(8,len(tagSha))]) }
	} else {
		logf(sr, "无前序镜像，跳过 tag（仅记录版本号）")
	}
	// 给本 run 涉及的部署记录回填 version（复用 SetVersion）
	var releaseIDs []string
	for i := 0; i < run.CurrentStage && i < len(run.StageRuns); i++ {
		if id, ok := run.StageRuns[i].Output[OutReleaseID].(string); ok && id != "" {
			releaseIDs = append(releaseIDs, id)
		}
	}
	if len(releaseIDs) > 0 {
		if err := e.Releases.SetVersion(ctx, releaseIDs, version); err != nil {
			sr.Error = err.Error(); return true, err
		}
	}
	run.Version = version
	sr.Output = map[string]any{OutVersion: version}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}
```
（`min` 用 Go 1.21+ 内置或自加 helper。）

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/devops/pipeline/ -run TestExecRelease -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
git add internal/devops/pipeline/engine.go internal/devops/pipeline/engine_test.go
git commit -m "feat(pipeline): 新增 execRelease stage（打版本号里程碑，不部署）"
```

---

## Task 7: engine execBaseline 瘦身 + execPromote 重定义 + 各 stage 日志

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（execBaseline:308-349, execPromote:285-304, execBuild/execTest 加 logf）
- Test: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Produces: `execBaseline` 只合并主干（去掉打版本——归 release）；`execPromote` = Deploy 到下一阶序环境基线；build/test/approve 加日志。

- [ ] **Step 1: 写失败测试**

```go
func TestExecBaselineOnlyMergesNoVersion(t *testing.T) {
	e := newTestEngine(t)
	// execBaseline 不应调 SetVersion（版本归 release）
	rc := &versionCapturingReleaser{}
	e.Releases = rc
	run := buildRunWithStage(StageBaseline, map[string]any{"mainBranch": "main", "mergeMode": "squash"})
	e.execStage(ctx, run, stageDef(run), &run.StageRuns[0])
	if rc.setVersionCalled { t.Error("baseline 不应再调 SetVersion（版本归 release）") }
}

func TestExecPromoteDeploysToNextEnv(t *testing.T) {
	e := newTestEngine(t)
	dep := &deployCapturingReleaser{}
	e.Releases = dep
	run := buildRunWithPriorDeploy() // 前序 deploy(env-test) 产出 releaseId+imageId
	_, err := e.execStage(ctx, run, StageDef{Type: StagePromote}, &run.StageRuns[run.CurrentStage])
	if err != nil { t.Fatalf("execPromote: %v", err) }
	if dep.envID == "" || dep.envID == "env-test" { t.Error("promote 应部署到下一阶序环境") }
	if dep.lane != LaneDefault { t.Errorf("promote 应部署到基线泳道，得 lane=%q", dep.lane) }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/pipeline/ -run 'TestExecBaselineOnlyMerges|TestExecPromoteDeploys' -v`
Expected: FAIL。

- [ ] **Step 3: 实现 execBaseline 瘦身**

去掉 execBaseline 内的「打版本 + SetVersion + run.Version」逻辑（移至 execRelease）。execBaseline 只保留合并主干：
```go
func (e *Engine) execBaseline(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	mainBranch := strOr(stage.Params, "mainBranch", "")
	out := map[string]any{}
	if mainBranch == "" {
		logf(sr, "未配 mainBranch，跳过合并")
		sr.Output = out; sr.Status = StageSuccess; sr.FinishedAt = time.Now()
		return true, nil
	}
	if e.Gitea == nil {
		logf(sr, "未接入 Gitea，跳过合并")
		sr.Output = out; sr.Status = StageSuccess; sr.FinishedAt = time.Now()
		return true, nil
	}
	logf(sr, "合并 %s -> %s", run.Branch, mainBranch)
	owner, repo, err := e.Gitea.ResolveRepo(ctx, run.AppID)
	if err == nil {
		mergeSHA, merr := e.Gitea.Merge(ctx, owner, repo, run.Branch, mainBranch, strOr(stage.Params, "mergeMode", "squash"))
		if merr != nil {
			if errors.Is(merr, gitea.ErrMergeConflict) { sr.Error = "合并冲突，请手动解决" } else { sr.Error = merr.Error() }
		} else { out[OutMergeSHA] = mergeSHA; logf(sr, "已合并 %s", mergeSHA[:min(8,len(mergeSHA))]) }
	}
	sr.Output = out
	sr.Status = StageSuccess // 冲突也标 success（merge 可手动补）
	sr.FinishedAt = time.Now()
	return true, nil
}
```

- [ ] **Step 4: 实现 execPromote 重定义**

execPromote = 找前序 deploy 的 imageId + Releaser.Promote 算下一环境 + Deploy 到下一环境基线。但 Releaser.Promote 当前是「晋升 release」（基于 environment.NextPromoteTarget）。重定义为：
```go
func (e *Engine) execPromote(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	srcReleaseID, err := resolvePriorOutput(*run, OutReleaseID)
	if err != nil { sr.Error = err.Error(); return true, err }
	logf(sr, "晋升发布 %s 到下一阶序环境", srcReleaseID)
	rel, err := e.Releases.Promote(ctx, srcReleaseID) // adapter 内算下一环境 + 部署到基线
	if err != nil {
		if errors.Is(err, environment.ErrNoPromoteTarget) { sr.Error = "已是最高阶环境" } else { sr.Error = err.Error() }
		return true, err
	}
	logf(sr, "已晋升到 %s", rel.ID)
	sr.Output = map[string]any{OutReleaseID: rel.ID, OutWorkloadDomain: e.Releases.WorkloadDomain(ctx, rel.WorkloadID)}
	sr.Status = StageSuccess; sr.FinishedAt = time.Now()
	return true, nil
}
```
注：`releaseBridge.Promote`（pipeline_adapters.go）现有实现已算 NextPromoteTarget + PromoteRelease。需让 PromoteRelease 走新的 lane-aware CreateRelease（Task 3 已支持，target 基线 lane=default）。确认 PromoteRelease 传 LaneID=default。

- [ ] **Step 5: build/test/approve 加 logf**

execBuild: `logf(sr, "构建提交 buildRunId=%s", br.ID)` + 轮询中 `logf(sr, "构建 %s", br.Status)`。
execTest(smoke): `logf(sr, "探活 %s", url)` + 成功 `logf(sr, "探活通过")`。
execApprove: `logf(sr, "等待审批：%s", message)`。

- [ ] **Step 6: 跑全 engine 测试**

Run: `go test ./internal/devops/pipeline/ -v`
Expected: 全 PASS（含回归）。

- [ ] **Step 7: commit**

```bash
git add internal/devops/pipeline/engine.go internal/devops/pipeline/engine_test.go
git commit -m "refactor(pipeline): execBaseline 瘦身(只合并) + execPromote 重定义(部署到下一环境基线) + 各 stage 日志"
```

---

## Task 8: ParamResolver 加 {{run.branch}} 占位符

**Files:**
- Modify: `internal/devops/pipeline/resolver.go`
- Test: `internal/devops/pipeline/resolver_test.go`

**Interfaces:**
- Produces: `ResolveStages` 支持 `{{run.branch}}` → 触发时的 run.Branch。

- [ ] **Step 1: 写失败测试**

```go
func TestResolveRunBranch(t *testing.T) {
	r := NewParamResolver(stubEnvResolver{}, stubRepoResolver{})
	stages, err := r.ResolveStages(ctx, "app-1", "feature-x", []pipeline.StageDef{
		{Type: pipeline.StageDeploy, Params: map[string]any{"lane": "{{run.branch}}"}},
	})
	if err != nil { t.Fatalf("ResolveStages: %v", err) }
	if got := stages[0].Params["lane"]; got != "feature-x" {
		t.Errorf("{{run.branch}} 应解析为 feature-x，得 %v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/pipeline/ -run TestResolveRunBranch -v`
Expected: FAIL（{{run.branch}} 未被替换）。

- [ ] **Step 3: 实现**

`resolver.go` ResolveStages 签名加 `branch string` 参数（或复用现有触发上下文）。在占位符替换逻辑加：
```go
v = strings.ReplaceAll(v, "{{run.branch}}", branch)
```
（与现有 `{{app.env.test}}` 替换同位置）。适配 ResolveStages 调用方（store 触发 run 处传 run.Branch）。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/devops/pipeline/ -run TestResolve -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
git add internal/devops/pipeline/resolver.go internal/devops/pipeline/resolver_test.go
git commit -m "feat(pipeline): ParamResolver 支持 {{run.branch}} 占位符（分支独立泳道）"
```

---

## Task 9: 平台预置模板重写（tpl-ci/tpl-cd）

**Files:**
- Modify: `internal/devops/pipeline/templates.go`
- Test: `internal/devops/pipeline/templates_test.go`

**Interfaces:**
- Produces: tpl-ci（build→deploy test lane→test，无 release/baseline）；tpl-cd（deploy prod→release→baseline 合并）。

- [ ] **Step 1: 写失败测试**

```go
func TestBuiltinTemplatesSemantics(t *testing.T) {
	tpls := BuiltinTemplates()
	ci := findTpl(tpls, "tpl-ci")
	cd := findTpl(tpls, "tpl-cd")
	// ci 不含 release stage
	if hasStage(ci, StageRelease) { t.Error("ci 模板不应含 release（测试不打版本）") }
	// ci 的 deploy 含 lane 占位符
	dep := findStage(ci, StageDeploy)
	if dep.Params["lane"] != "{{run.branch}}" { t.Errorf("ci deploy lane 应为 {{run.branch}}，得 %v", dep.Params["lane"]) }
	// cd 含 release stage
	if !hasStage(cd, StageRelease) { t.Error("cd 模板应含 release（上线打版本）") }
	// cd 的 deploy lane=default
	cdDep := findStage(cd, StageDeploy)
	if cdDep.Params["lane"] != LaneDefault { t.Errorf("cd deploy lane 应为 default，得 %v", cdDep.Params["lane"]) }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/pipeline/ -run TestBuiltinTemplatesSemantics -v`
Expected: FAIL。

- [ ] **Step 3: 重写模板**

```go
func BuiltinTemplates() []PipelineTemplate {
	return []PipelineTemplate{
		{
			ID: "tpl-ci", Name: "测试联调流水线", Kind: KindCI, Builtin: true,
			Description: "变更验证：构建 → 部署到测试环境的分支泳道 → 冒烟联调（不打版本、不合并主干）",
			Stages: []StageDef{
				{Name: "构建", Type: StageBuild},
				{Name: "部署到测试泳道", Type: StageDeploy, Params: map[string]any{
					"envId": "{{app.env.test}}", "lane": "{{run.branch}}",
					"imageSource": ImagePriorBuild, "strategy": "rolling",
				}},
				{Name: "冒烟联调", Type: StageTest, Params: map[string]any{"mode": TestSmoke, "path": "/livez"}},
			},
		},
		{
			ID: "tpl-cd", Name: "上线发布流水线", Kind: KindCD, Builtin: true,
			Description: "正式上线：部署到生产基线 → 打版本号 → 合并主干",
			Stages: []StageDef{
				{Name: "上线审批", Type: StageApprove, Params: map[string]any{"message": "确认发布到生产环境"}},
				{Name: "部署到生产", Type: StageDeploy, Params: map[string]any{
					"envId": "{{app.env.prod}}", "lane": LaneDefault,
					"imageSource": ImageLatestReady, "strategy": "rolling",
				}},
				{Name: "发布版本", Type: StageRelease, Params: map[string]any{"versionStrategy": "auto-increment"}},
				{Name: "合并主干", Type: StageBaseline, Params: map[string]any{"mainBranch": "main", "mergeMode": "squash"}},
			},
		},
	}
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/devops/pipeline/ -run TestBuiltin -v`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
git add internal/devops/pipeline/templates.go internal/devops/pipeline/templates_test.go
git commit -m "feat(pipeline): 重写 tpl-ci(测试泳道无版本)/tpl-cd(生产基线+release+合并)"
```

---

## Task 10: PG migration + store_pg 新列读写

**Files:**
- Create: `internal/storage/pg/migrations/0022_pipeline_lane_release.up.sql` + `.down.sql`
- Modify: `internal/devops/pg/store.go`（releases/images Insert/Select 加新列）, `internal/devops/pipeline/store_pg.go`（StageRun.Log 读写）
- Test: `internal/devops/pg/store_test.go` (integration)

**Interfaces:**
- Produces: releases 加 lane_id/source_run_id；images 加 version；stage_runs 的 log 持久化（若 stage_runs 存 JSONB 则自动包含，若分表则加列）。

- [ ] **Step 1: 写 migration**

`0022_pipeline_lane_release.up.sql`:
```sql
ALTER TABLE releases ADD COLUMN IF NOT EXISTS lane_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE releases ADD COLUMN IF NOT EXISTS source_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE images ADD COLUMN IF NOT EXISTS version TEXT NOT NULL DEFAULT '';
```
注：StageRun.Log 若 stage_runs 以 JSONB 整体存（pipeline_runs.stage_runs JSONB），则 Log 自动持久化无需加列——确认 `store_pg.go` 中 stage_runs 读写是 JSONB marshal/unmarshal（若是，本 task 对 Log 零改动）。

`.down.sql`:
```sql
ALTER TABLE releases DROP COLUMN IF EXISTS lane_id;
ALTER TABLE releases DROP COLUMN IF EXISTS source_run_id;
ALTER TABLE images DROP COLUMN IF EXISTS version;
```

- [ ] **Step 2: 改 store_pg releases/images 读写**

`devops/pg/store.go`：
- `releaseCols` 加 lane_id, source_run_id；Scan 加两字段；INSERT 加两占位。
- `imageCols` 加 version；Scan/INSERT 加。
- `MarkSourceRun`：`UPDATE releases SET source_run_id=$2 WHERE id=$1 AND tenant_id=$3`。
- `SetVersion`（images）：`UPDATE images SET version=$2 WHERE id=$1 AND tenant_id=$3`。
- CreateRelease INSERT 写 lane_id（从 input.LaneID，空则 default）。

- [ ] **Step 3: 写 integration 测试**

```go
//go:build integration
func TestPGReleaseLaneAndImageVersion(t *testing.T) {
	resetSchema(t)
	s := newTestPGStore(t)
	ctx := tenant.WithTenant(context.Background(), "t1")
	// seed app/env/image
	rel, _ := s.CreateRelease(ctx, devops.ReleaseInput{AppID:"a", EnvID:"e", LaneID:"feature-x", ImageID:"img"})
	s.MarkSourceRun(ctx, rel.ID, "run-1")
	s.SetImageVersion(ctx, "img", "v1.2.0")
	got, _ := s.GetRelease(ctx, rel.ID)
	if got.LaneID != "feature-x" || got.SourceRunID != "run-1" { t.Error("PG Release 新列读写失败") }
	img, _ := s.GetImage(ctx, "img")
	if img.Version != "v1.2.0" { t.Error("PG Image.Version 读写失败") }
}
```

- [ ] **Step 4: 跑 integration 测试**

Run: `PAAS_TEST_PG_URL=postgres://... make test-pg`
Expected: PASS。

- [ ] **Step 5: commit**

```bash
git add internal/storage/pg/migrations/0022_pipeline_lane_release.up.sql internal/storage/pg/migrations/0022_pipeline_lane_release.down.sql internal/devops/pg/store.go internal/devops/pg/store_test.go
git commit -m "feat(pg): migration 0022 releases(images) 加 lane_id/source_run_id/version + store_pg 读写"
```

---

## Task 11: 前端 PipelineRunView stage 展开 + 日志区 + lane

**Files:**
- Modify: `frontend/console-user/src/views/app-tabs/PipelineRunView.vue`, `frontend/console-user/src/api/pipeline.ts`
**Interfaces:**
- Consumes: StageRun.Log（Task 1）；build stage 的 `GET /api/buildruns/{id}`（既有）。

- [ ] **Step 1: api 类型加 Log**

`api/pipeline.ts` `StageRun` 接口加 `log?: string`；`Input` 加 `lane?: string`。

- [ ] **Step 2: PipelineRunView 加展开日志区**

stage 卡片加可展开（el-collapse 或点击切换 `expandedIdx`）。展开区：
```vue
<div v-if="expandedIdx === s.index" class="stage-log">
  <div v-if="s.type === 'build' && buildLog" class="log-block">{{ buildLog }}</div>
  <div v-else class="log-block">{{ s.log || '（暂无日志）' }}</div>
</div>
```
- stage-head 加点击 `@click="toggleExpand(s)"` + 展开 icon。
- build stage 展开时调 `getBuildRun(buildRunId)` 拉全量日志（从 s.input.buildRunId）。
- lane 展示：deploy stage 卡片显示 `lane: feature-x` tag（从 s.input.lane）。

- [ ] **Step 3: 构建验证**

Run: `cd frontend && pnpm exec vue-tsc --noEmit && pnpm build --filter console-user`（或对应 workspace 命令）
Expected: 通过。

- [ ] **Step 4: commit**

```bash
git add frontend/console-user/src/views/app-tabs/PipelineRunView.vue frontend/console-user/src/api/pipeline.ts
git commit -m "feat(console-user): PipelineRunView stage 展开 + 日志区 + lane 展示"
```

---

## Task 12: 前端 PipelineDesigner deploy 加 lane 参数

**Files:**
- Modify: `frontend/console-user/src/views/app-tabs/PipelineDesigner.vue`
**Interfaces:**
- Produces: deploy stage 参数覆盖表单加 lane 字段（默认 `{{run.branch}}` 占位提示）。

- [ ] **Step 1: 加 lane 覆盖项**

PipelineDesigner 的 deployStages 覆盖区，envId 下方加：
```vue
<div class="override-item">
  <div class="override-label">阶段「{{ d.s.name }}」泳道（lane）</div>
  <el-input :model-value="getOverride(d.i, 'lane')" @update:model-value="(v:string)=>setOverride(d.i,'lane',v)"
            placeholder="默认 {{run.branch}}（分支独立泳道联调）；生产填 default" />
</div>
```
（getOverride/setOverride 既有，Task 已有 deploy envId 覆盖，复用同款。）

- [ ] **Step 2: 构建验证**

Run: `cd frontend && pnpm exec vue-tsc --noEmit && pnpm build`
Expected: 三套通过。

- [ ] **Step 3: commit**

```bash
git add frontend/console-user/src/views/app-tabs/PipelineDesigner.vue
git commit -m "feat(console-user): PipelineDesigner deploy stage 加 lane 参数覆盖"
```

---

## Task 13: dogfooding 适配 + k8s e2e 验证

**Files:**
- 无新文件；操作既有 paas-shop app + 集群

- [ ] **Step 1: 后端 + 前端全量构建**

Run:
```bash
cd /Users/wangtao/data/github.com/aitoys/paas
go test ./... && cd frontend && pnpm build && cd ..
```
Expected: 全绿。

- [ ] **Step 2: 部署**

Run: `./scripts/deploy-k8s.sh`
Expected: helm upgrade + rollout 成功。

- [ ] **Step 3: e2e 验证 CI（测试泳道，无版本）**

```bash
H="Authorization: Bearer sk-acme-admin"
# 触发 paas-shop CI run
curl -s -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"branch":"main"}' http://paas.k8s.dd/api/applications/<app-id>/pipelines/<ci-pipe-id>/run
# 轮询 run：build → deploy(test,lane=main) → test
# 断言：deploy 的 StageRun.Input.lane 非空；run 无 release stage；StageRun.Log 含 lane
```

- [ ] **Step 4: e2e 验证 CD（生产基线 + release 版本）**

```bash
# 触发 paas-shop CD run
curl -s -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"branch":"main","version":""}' http://paas.k8s.dd/api/applications/<app-id>/pipelines/<cd-pipe-id>/run
# approve 暂停后 POST approve
# 断言：deploy(prod) lane=default；release stage 产出 version；Image.Version 回填；baseline 合并主干
# 断言：PipelineRunView 展开 stage 能看到日志
```

- [ ] **Step 5: 回归测试环境部署记录可回滚**

确认测试环境（feature 泳道）的部署记录存在于 `/api/releases?envId=...`，可经 `/api/releases/{id}/rollback` 回滚（PreviousImageID 链）。

- [ ] **Step 6: 更新 CLAUDE.md + 记忆**

CLAUDE.md「DevOps 流水线引擎」章节更新：deploy/release 分离语义 + lane + 日志 + L2 立项。记忆加 deploy/release 模型决策。

- [ ] **Step 7: 最终 commit**

```bash
git add CLAUDE.md
git commit -m "docs: 流水线 deploy/release 分离 + 泳道 + 实时日志（L1 落地，L2 立项）"
```

---

## L2 立项（本 plan 不实现，锁定接口）

L2 泳道联调作为下一个独立 spec/plan，范围：
- 数据面 `/dp/instances?service=x&lane=feature-x`：优先返 feature-x 泳道实例，缺失降级返 default。
- 流量染色：请求 header `x-paas-lane`（L3 全链路 mesh 演进点）。
- 前端：环境详情页泳道拓扑可视化。
- 泳道生命周期：baseline 合并主干后回收 feature 泳道 Workload。

L1 已为 L2 锁定：Workload.LaneID + Release.LaneID + deploy 产 lane 标识，L2 直接消费。
