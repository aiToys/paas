# Pipeline 引擎后端核心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现平台 Pipeline 引擎后端核心（4 实体数据模型 + Repository 双路径 + 异步状态机执行引擎 + REST handler + 平台预置模板），支持手动触发 CI/CD 流水线一键跑完 build→deploy→test→baseline，curl 端到端验证。

**Architecture:** 新增 `internal/devops/pipeline/` 子包，4 实体（PipelineTemplate/Pipeline/PipelineRun/StageRun）+ engine（goroutine 异步推进状态机，stage 间经 StageRun.Output 传递 imageId/releaseId）。engine 是现有 BuildRun/CreateRelease/PromoteRelease 的薄包装编排层，依赖倒置注入。CI/CD 通过 Image 实体解耦（deploy.imageSource = priorBuild|selected|latestReady）。借鉴 Spinnaker Application 1:N Pipeline + ArgoCD artifact source。

**Tech Stack:** Go + pgxpool（PG）+ golang-migrate + net/http + 现有 internal/devops（BuildRun/Release/Gitea client）。

**对应 spec：** `docs/superpowers/specs/2026-08-08-devos-pipeline-design.md`（注：spec 文件实际名 `2026-08-08-devops-pipeline-design.md`）

## Global Constraints

- 主语言 Go；所有依赖 Apache 2.0 兼容（CI 禁 GPL/AGPL）。
- 多租户隔离：Repository 全方法强制 tenant 过滤，跨租户 not found 不泄漏。
- 内存/PG 双路径：`PAAS_DB_URL` 非空走 PG（migration embed SQL 启动自动 up），空走 memory（零依赖）。
- 响应契约：成功 `{data:T}`（`httputil.WriteData`/`WriteDataCreated`），错误 `{error:msg}`（`WriteError`），500 脱敏（`WriteServiceError`/`WriteInternalError`）。
- PG migration 文件命名 `NNNN_name.up.sql` + `.down.sql`，本 plan 用新增 0011/0012（`IF NOT EXISTS`，已部署 PG 增量安全）。
- 权限常量定义在 pipeline 包，并入 `identity.BuiltinRoles()`；生产 deploy 走 `prod:write`（EnvTypeResolver 横切）。
- 注释中文，与代码库现有风格一致。
- 集成测试 `//go:build integration` 门控（`PAAS_TEST_PG_URL` 驱动）；单测走 memory + fake，不依赖 PG。
- 写操作记审计（`identityAuditAdapter` 模式）。
- 每个 task 结束 commit（commit message 用 `feat(pipeline): ...` 前缀）。

## 文件结构

新建：
- `internal/devops/pipeline/model.go` — 4 实体 + StageDef + Kind/Status/Type 常量
- `internal/devops/pipeline/repository.go` — Repository 接口（Pipeline/PipelineRun/Template）
- `internal/devops/pipeline/store_memory.go` — memory 实现
- `internal/devops/pipeline/store_pg.go` — PG 实现
- `internal/devops/pipeline/engine.go` — 执行引擎（状态机 + stage 输出链）
- `internal/devops/pipeline/handler.go` — REST handler
- `internal/devops/pipeline/templates.go` — 平台预置 ci/cd 模板
- `internal/devops/pipeline/*_test.go` — 单测
- `internal/devops/pipeline/integration_test.go` — PG 集成测试（`//go:build integration`）
- `internal/storage/pg/migrations/0011_release_version.{up,down}.sql`
- `internal/storage/pg/migrations/0012_pipeline.{up,down}.sql`

修改：
- `internal/devops/model.go` — Release 加 Version
- `internal/devops/memory/store.go` + `pg/store.go` — Release Version 读写
- `internal/devops/gitea/client.go` — 加 Merge 方法
- `internal/core/identity/model.go` — BuiltinRoles 加 pipeline 权限
- `cmd/core/persistence.go` — 装配 pipeline store + engine 依赖
- `cmd/core/main.go` — 路由注册 + composite 分发

---

## Task 1: Release 加 Version 字段（baseline stage 写入点）

**Files:**
- Modify: `internal/devops/model.go`（Release struct）
- Modify: `internal/devops/memory/store.go`（releaseCols/Scan/Insert）
- Modify: `internal/devops/pg/store.go`（同上）
- Create: `internal/storage/pg/migrations/0011_release_version.up.sql` + `.down.sql`
- Test: `internal/devops/memory/store_test.go`（已有，加用例）

**Interfaces:**
- Produces: `Release.Version string`（json `version,omitempty`），所有 Release 读写路径透传。

- [ ] **Step 1: 加 migration**

`internal/storage/pg/migrations/0011_release_version.up.sql`:
```sql
ALTER TABLE releases ADD COLUMN IF NOT EXISTS version TEXT NOT NULL DEFAULT '';
```
`0011_release_version.down.sql`:
```sql
ALTER TABLE releases DROP COLUMN IF EXISTS version;
```

- [ ] **Step 2: Release struct 加字段**

`internal/devops/model.go` Release struct 加：
```go
// Version 发布版本号（baseline stage 写入：auto-increment/tag/manual）。
Version string `json:"version,omitempty" db:"version"`
```

- [ ] **Step 3: memory/pg store 读写透传**

memory store：构造 Release 时初始化/透传 Version（seed Release 不设 Version，空串即可）。
pg store：`releaseCols` 常量加 `version`；INSERT 加 `:version`（或 `$N`）；Scan 加 `&r.Version`。**三处同步**（列错位 panic 警示，参照 P1.2+ vendor_id 经验）。

- [ ] **Step 4: 写失败测试**

`internal/devops/memory/store_test.go` 加（若无则参照同目录已有测试模式新建）：
```go
func TestReleaseVersionRoundTrip(t *testing.T) {
	store := NewStore(workloadRepoStub{})
	r := Release{ID:"r1", TenantID:"t1", AppID:"a1", EnvID:"e1", ImageID:"i1",
		ImageDigest:"sha256:x", WorkloadID:"w1", Status:"succeeded", Version:"v1.2.3"}
	if err := store.createRelease(context.Background(), r); err != nil { t.Fatal(err) }
	got, _ := store.GetRelease(context.Background(), "r1")
	if got.Version != "v1.2.3" { t.Fatalf("version=%s want v1.2.3", got.Version) }
}
```
（用 store 实际内部方法名；若 createRelease 是私有，测试同包内可访问。）

- [ ] **Step 5: 跑测试**

Run: `go test ./internal/devops/memory/ -run TestReleaseVersion -v`
Expected: 先 FAIL（字段未透传）→ 实现 Step 3 后 PASS。

- [ ] **Step 6: Commit**
```bash
git add internal/devops/model.go internal/devops/memory/store.go internal/devops/pg/store.go internal/storage/pg/migrations/0011_* internal/devops/memory/store_test.go
git commit -m "feat(devops): Release 加 Version 字段（baseline stage 写入点）"
```

---

## Task 2: Pipeline 实体模型 + 常量

**Files:**
- Create: `internal/devops/pipeline/model.go`
- Test: `internal/devops/pipeline/model_test.go`

**Interfaces:**
- Produces: `PipelineTemplate` / `Pipeline` / `PipelineRun` / `StageRun` / `StageDef` / `PipelineTrigger` 类型；`Kind*` / `RunStatus*` / `StageType*` / `ImageSource*` / `TestMode*` 常量；`Validate()` 方法。

- [ ] **Step 1: 写 model.go**

```go
// Package pipeline 实现 DevOps 流水线编排：声明式 stage 序列 + 异步状态机推进。
// 借鉴 Spinnaker（Application 1:N Pipeline）+ ArgoCD（artifact source 解耦）。
package pipeline

import "time"

// Kind 流水线分类（UI 分组 + 职责划分）。
const (
	KindCI     = "ci"     // 开发测试流水线：build→deploy(dev)→test→baseline(merge)
	KindCD     = "cd"     // 生产发布流水线：approve→deploy(prod)→baseline(版本)
	KindCustom = "custom"
)

// RunStatus PipelineRun 状态机。
const (
	RunRunning   = "running"
	RunPaused    = "paused"    // 等 approve/test-manual
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunAborted   = "aborted"
)

// StageStatus StageRun 状态。
const (
	StagePending  = "pending"
	StageRunning  = "running"
	StageSuccess  = "succeeded"
	StageFailed   = "failed"
	StageWaiting  = "waiting" // 等 approve
	StageSkipped  = "skipped"
)

// StageType stage 类型枚举。
const (
	StageBuild    = "build"
	StageDeploy   = "deploy"
	StageTest     = "test"
	StageApprove  = "approve"
	StagePromote  = "promote"
	StageBaseline = "baseline"
)

// ImageSource deploy stage 的镜像来源（CI/CD 解耦关键）。
const (
	ImagePriorBuild  = "priorBuild"   // 本流水线前序 build stage 产出
	ImageSelected    = "selected"     // 指定 imageId（CD 消费 CI 产物）
	ImageLatestReady = "latestReady"  // app 最新 ready Image
)

// TestMode test stage 子模式。
const (
	TestSmoke = "smoke"   // HTTP 探活（自动）
	TestManual = "manual" // 人工确认
)

// StageDef 阶段定义（模板与 Pipeline 共用）。
type StageDef struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// PipelineTrigger 流水线触发配置（Plan 1 只实现 manual；webhook/cron 占位 Plan 3）。
type PipelineTrigger struct {
	Type     string   `json:"type"`               // manual|webhook|cron（Plan 1: manual）
	Branch   string   `json:"branch,omitempty"`    // webhook: glob
	Events   []string `json:"events,omitempty"`    // webhook: ["push"]
	Token    string   `json:"-"`                   // webhook: URL token（不序列化前端）
	Schedule string   `json:"schedule,omitempty"`  // cron: 表达式
}

// PipelineTemplate 模板（平台预置 builtin + 租户自定义）。
type PipelineTemplate struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId,omitempty"` // ""=平台预置
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Description string     `json:"description,omitempty"`
	Stages      []StageDef `json:"stages"`
	Builtin     bool       `json:"builtin,omitempty"`
}

// Pipeline 应用绑定的流水线（主线实体，Application 1:N）。
type Pipeline struct {
	ID         string           `json:"id"`
	TenantID   string           `json:"tenantId"`
	AppID      string           `json:"appId"`
	Name       string           `json:"name"`
	Kind       string           `json:"kind"`
	TemplateID string           `json:"templateId,omitempty"`
	Stages     []StageDef       `json:"stages"`
	Trigger    PipelineTrigger  `json:"trigger"`
	Disabled   bool             `json:"disabled,omitempty"`
	CreatedAt  time.Time        `json:"createdAt"`
}

// PipelineRun 一次运行（异步状态机载体）。
type PipelineRun struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenantId"`
	AppID        string     `json:"appId"`
	PipelineID   string     `json:"pipelineId"`
	Branch       string     `json:"branch"`
	Commit       string     `json:"commit,omitempty"`
	RepoID       string     `json:"repoId,omitempty"` // run 时解析 app 绑定的 internal CodeRepo（build stage 用）
	Trigger      string     `json:"trigger"` // manual|webhook|cron
	TriggerRef   string     `json:"triggerRef,omitempty"`
	Status       string     `json:"status"`
	CurrentStage int        `json:"currentStage"`
	StageRuns    []StageRun `json:"stageRuns"`
	Version      string     `json:"version,omitempty"` // baseline 写入
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   time.Time  `json:"finishedAt,omitempty"`
}

// StageRun 单阶段执行记录（输出链载体）。
type StageRun struct {
	Index     int               `json:"index"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Input     map[string]any    `json:"input,omitempty"`
	Output    map[string]any    `json:"output,omitempty"`
	StartedAt time.Time         `json:"startedAt,omitempty"`
	FinishedAt time.Time        `json:"finishedAt,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// StageRun Output 已知 key（stage 输出链）。
const (
	OutImageID       = "imageId"
	OutReleaseID     = "releaseId"
	OutWorkloadDomain = "workloadDomain"
	OutVersion       = "version"
	OutMergeSHA      = "mergeSha"
)

// Validate Pipeline 基本校验。
func (p Pipeline) Validate() error {
	if p.Name == "" { return errNameRequired }
	if p.AppID == "" { return errAppRequired }
	switch p.Kind {
	case KindCI, KindCD, KindCustom:
	default: return ErrInvalidKind }
	if len(p.Stages) == 0 { return ErrNoStages }
	for i, s := range p.Stages {
		if err := s.validate(); err != nil {
			return fmt.Errorf("stage %d: %w", i, err)
		}
	}
	return nil
}

func (s StageDef) validate() error {
	switch s.Type {
	case StageBuild, StageDeploy, StageTest, StageApprove, StagePromote, StageBaseline:
	default: return ErrInvalidStageType }
	if s.Name == "" { return errStageNameRequired }
	return nil
}
```

错误 sentinel 同文件定义（`errNameRequired`/`errAppRequired`/`ErrInvalidKind`/`ErrNoStages`/`ErrInvalidStageType`/`errStageNameRequired`，参照 devops 包已有 sentinel 风格，导出/小写按是否需跨包）。`fmt` 已 import。

- [ ] **Step 2: 写测试**

`model_test.go`:
```go
func TestPipelineValidate(t *testing.T) {
	cases := []struct{ name string; p Pipeline; wantErr bool }{
		{"ok", Pipeline{Name:"p", AppID:"a", Kind:KindCI, Stages:[]StageDef{{Name:"build",Type:StageBuild}}}, false},
		{"empty stages", Pipeline{Name:"p", AppID:"a", Kind:KindCI}, true},
		{"bad kind", Pipeline{Name:"p", AppID:"a", Kind:"x", Stages:[]StageDef{{Name:"b",Type:StageBuild}}}, true},
		{"bad stage type", Pipeline{Name:"p", AppID:"a", Kind:KindCI, Stages:[]StageDef{{Name:"b",Type:"x"}}}, true},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if (err != nil) != c.wantErr { t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr) }
	}
}
```

- [ ] **Step 3: 跑测试**
Run: `go test ./internal/devops/pipeline/ -run TestPipelineValidate -v`
Expected: PASS。

- [ ] **Step 4: Commit**
```bash
git add internal/devops/pipeline/model.go internal/devops/pipeline/model_test.go
git commit -m "feat(pipeline): 4 实体数据模型 + Kind/Status/Type 常量"
```

---

## Task 3: PG migration 0012（pipeline 4 表）

**Files:**
- Create: `internal/storage/pg/migrations/0012_pipeline.up.sql` + `.down.sql`

**Interfaces:**
- Produces: 4 表 schema（pipeline_templates / pipelines / pipeline_runs / stage_runs），供 Task 5 pg store 使用。

- [ ] **Step 1: 写 up.sql**

```sql
-- 平台预置模板 tenant_id NULL（全租户共享）；租户自定义带 tenant_id。
CREATE TABLE IF NOT EXISTS pipeline_templates (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT,          -- NULL=平台预置
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    stages      JSONB NOT NULL DEFAULT '[]',
    builtin     BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pipeline_templates_name_tenant
    ON pipeline_templates (name, COALESCE(tenant_id, ''));

CREATE TABLE IF NOT EXISTS pipelines (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    template_id TEXT NOT NULL DEFAULT '',
    stages     JSONB NOT NULL DEFAULT '[]',
    trigger    JSONB NOT NULL DEFAULT '{}',
    disabled   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_pipelines_tenant_app ON pipelines (tenant_id, app_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_pipelines_name_tenant_app ON pipelines (tenant_id, app_id, name);

CREATE TABLE IF NOT EXISTS pipeline_runs (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    app_id        TEXT NOT NULL,
    pipeline_id   TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    commit        TEXT NOT NULL DEFAULT '',
    trigger       TEXT NOT NULL,
    trigger_ref   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    current_stage INT NOT NULL DEFAULT 0,
    version       TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_pipeline_runs_tenant_app ON pipeline_runs (tenant_id, app_id);
CREATE INDEX IF NOT EXISTS ix_pipeline_runs_pipeline ON pipeline_runs (pipeline_id, created_at DESC);
-- 同一 Pipeline 同时只允许一个 running/paused run（CI 单实例串行）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_pipeline_runs_active
    ON pipeline_runs (pipeline_id) WHERE status IN ('running', 'paused');

CREATE TABLE IF NOT EXISTS stage_runs (
    id           BIGSERIAL PRIMARY KEY,
    pipeline_run_id TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    stage_index  INT NOT NULL,
    type         TEXT NOT NULL,
    name         TEXT NOT NULL,
    status       TEXT NOT NULL,
    input        JSONB NOT NULL DEFAULT '{}',
    output       JSONB NOT NULL DEFAULT '{}',
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    error        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_stage_runs_run ON stage_runs (pipeline_run_id, stage_index);
```

- [ ] **Step 2: 写 down.sql**

```sql
DROP TABLE IF EXISTS stage_runs;
DROP TABLE IF EXISTS pipeline_runs;
DROP TABLE IF EXISTS pipelines;
DROP TABLE IF EXISTS pipeline_templates;
```

- [ ] **Step 3: 验证 migration 跑通**

Run: `PAAS_TEST_PG_URL=postgres://... go test ./internal/storage/pg/ -run TestMigrate -tags=integration -v`（若已有 migration 全量测试；否则 Task 5 集成测试覆盖）。
Expected: 无错误。

- [ ] **Step 4: Commit**
```bash
git add internal/storage/pg/migrations/0012_*
git commit -m "feat(pipeline): PG migration 0012（pipeline 4 表 + 单实例运行唯一约束）"
```

---

## Task 4: Pipeline Repository 接口 + memory 实现

**Files:**
- Create: `internal/devops/pipeline/repository.go`
- Create: `internal/devops/pipeline/store_memory.go`
- Create: `internal/devops/pipeline/store_memory_test.go`

**Interfaces:**
- Produces:
  - `Repository`（Pipeline CRUD）: `ListPipelines(ctx,appID) []Pipeline` / `GetPipeline(ctx,id)` / `CreatePipeline(ctx,Pipeline)` / `UpdatePipeline(ctx,Pipeline)` / `DeletePipeline(ctx,id)`
  - `RunRepository`（PipelineRun）: `ListRuns(ctx,appID,pipelineID,status) []PipelineRun` / `GetRun(ctx,id)` / `CreateRun(ctx,PipelineRun)` / `UpdateRun(ctx,PipelineRun)`（推进进度）/ `HasActiveRun(ctx,pipelineID) bool`
  - `TemplateRepository`: `ListTemplates(ctx) []PipelineTemplate` / `GetTemplate(ctx,id)` / `CreateTemplate(ctx,PipelineTemplate)`
  - 错误 sentinel：`ErrPipelineNotFound` / `ErrPipelineExists` / `ErrRunNotFound` / `ErrActiveRunExists`（HTTP 映射）
- 所有方法 ctx 取租户强制过滤（缺租户拒）。

- [ ] **Step 1: 写 repository.go 接口**

```go
package pipeline

import "context"

type Repository interface {
	ListPipelines(ctx context.Context, appID string) ([]Pipeline, error)
	GetPipeline(ctx context.Context, id string) (Pipeline, error)
	CreatePipeline(ctx context.Context, p Pipeline) (Pipeline, error)
	UpdatePipeline(ctx context.Context, p Pipeline) (Pipeline, error)
	DeletePipeline(ctx context.Context, id string) error
}

type RunRepository interface {
	ListRuns(ctx context.Context, appID, pipelineID, status string) ([]PipelineRun, error)
	GetRun(ctx context.Context, id string) (PipelineRun, error)
	CreateRun(ctx context.Context, r PipelineRun) (PipelineRun, error)
	UpdateRun(ctx context.Context, r PipelineRun) (PipelineRun, error)
	HasActiveRun(ctx context.Context, pipelineID string) (bool, error)
}

type TemplateRepository interface {
	ListTemplates(ctx context.Context) ([]PipelineTemplate, error)
	GetTemplate(ctx context.Context, id string) (PipelineTemplate, error)
	CreateTemplate(ctx context.Context, t PipelineTemplate) (PipelineTemplate, error)
}
```

- [ ] **Step 2: 写 store_memory.go**

参照 `internal/devops/memory/store.go` 模式：`sync.RWMutex` + `map[string]X` + 深拷贝（Stages []StageDef / StageRuns / Trigger 复制，防 race）。租户从 ctx 取（`tenant.TenantFrom(ctx)`，参照 application store）。

```go
package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

type memoryStore struct {
	mu   sync.RWMutex
	pipes    map[string]Pipeline
	runs     map[string]PipelineRun
	templates map[string]PipelineTemplate
}

func NewMemoryStore() *memoryStore {
	return &memoryStore{
		pipes: map[string]Pipeline{}, runs: map[string]PipelineRun{}, templates: map[string]PipelineTemplate{},
	}
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid := tenant.TenantFrom(ctx)
	if tid == "" { return "", ErrNoTenant }
	return tid, nil
}

func (s *memoryStore) ListPipelines(ctx context.Context, appID string) ([]Pipeline, error) {
	tid, err := tenantOrErr(ctx); if err != nil { return nil, err }
	s.mu.RLock(); defer s.mu.RUnlock()
	var out []Pipeline
	for _, p := range s.pipes {
		if p.TenantID == tid && (appID == "" || p.AppID == appID) {
			out = append(out, clonePipeline(p))
		}
	}
	return out, nil
}

func (s *memoryStore) GetPipeline(ctx context.Context, id string) (Pipeline, error) {
	tid, err := tenantOrErr(ctx); if err != nil { return Pipeline{}, err }
	s.mu.RLock(); defer s.mu.RUnlock()
	p, ok := s.pipes[id]
	if !ok || p.TenantID != tid { return Pipeline{}, ErrPipelineNotFound }
	return clonePipeline(p), nil
}

func (s *memoryStore) CreatePipeline(ctx context.Context, p Pipeline) (Pipeline, error) {
	tid, err := tenantOrErr(ctx); if err != nil { return Pipeline{}, err }
	p.TenantID = tid // 以 ctx 为准，忽略请求体
	if p.ID == "" { p.ID = fmt.Sprintf("pipe-%d", time.Now().UnixNano()) }
	p.CreatedAt = time.Now()
	if err := p.Validate(); err != nil { return Pipeline{}, err }
	s.mu.Lock(); defer s.mu.Unlock()
	for _, ex := range s.pipes {
		if ex.TenantID == tid && ex.AppID == p.AppID && ex.Name == p.Name {
			return Pipeline{}, ErrPipelineExists
		}
	}
	s.pipes[p.ID] = p
	return clonePipeline(p), nil
}

// UpdatePipeline / DeletePipeline 同款：锁内校验归属 + 存在，深拷贝返回。
// DeletePipeline 同时级联清该 pipeline 的 runs（参照 devops DeleteService 级联模式）。

// ListRuns/GetRun/CreateRun/UpdateRun/HasActiveRun：同款锁 + 租户过滤。
// CreateRun 先 HasActiveRun 校验（单实例串行）。
// UpdateRun 写回 stageRuns（engine 推进时调）。

// ListTemplates/GetTemplate/CreateTemplate：模板 tenant_id=""（平台预置）或 =tid（自定义），
// ListTemplates 返 builtin + 本租户自定义（OR tenant_id IN ('', tid)）。

// clonePipeline / cloneRun 深拷贝 Stages/StageRuns/Trigger/Params（map + slice）防 race。
```

**关键**：`clonePipeline` 必须深拷贝 `Stages []StageDef`（含 `Params map[string]any`）+ `Trigger.Events`，否则 engine 读改撕裂（参照 billing 配额深拷贝模式）。

- [ ] **Step 3: 写测试**

```go
func TestPipelineCRUDMultiTenant(t *testing.T) {
	s := NewMemoryStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")

	p, _ := s.CreatePipeline(ctxA, Pipeline{Name:"p1", AppID:"a1", Kind:KindCI,
		Stages:[]StageDef{{Name:"build", Type:StageBuild}}})

	got, _ := s.GetPipeline(ctxA, p.ID)
	if got.Name != "p1" { t.Fatal("get failed") }

	// 跨租户隔离：t-globex 看不到 t-acme 的 pipeline
	if _, err := s.GetPipeline(ctxB, p.ID); err != ErrPipelineNotFound {
		t.Fatalf("cross-tenant want NotFound got %v", err)
	}

	listA, _ := s.ListPipelines(ctxA, "a1")
	if len(listA) != 1 { t.Fatalf("listA len=%d", len(listA)) }
	listB, _ := s.ListPipelines(ctxB, "a1")
	if len(listB) != 0 { t.Fatalf("listB leak") }

	// 同 (tenant,app,name) 唯一
	if _, err := s.CreatePipeline(ctxA, Pipeline{Name:"p1", AppID:"a1", Kind:KindCI,
		Stages:[]StageDef{{Name:"b",Type:StageBuild}}}); err != ErrPipelineExists {
		t.Fatalf("dup want Exists got %v", err)
	}

	// CreateRun 单实例串行：HasActiveRun 拦截
	r1, _ := s.CreateRun(ctxA, PipelineRun{ID:"r1", AppID:"a1", PipelineID:p.ID,
		Trigger:"manual", Status:RunRunning})
	_ = r1
	if active, _ := s.HasActiveRun(ctxA, p.ID); !active { t.Fatal("should have active") }
	if _, err := s.CreateRun(ctxA, PipelineRun{ID:"r2", AppID:"a1", PipelineID:p.ID,
		Trigger:"manual", Status:RunRunning}); err != ErrActiveRunExists {
		t.Fatalf("active run want ActiveRunExists got %v", err)
	}
}
```

- [ ] **Step 4: 跑测试**
Run: `go test ./internal/devops/pipeline/ -run TestPipelineCRUD -v -race`
Expected: PASS（含深拷贝防 race）。

- [ ] **Step 5: Commit**
```bash
git add internal/devops/pipeline/repository.go internal/devops/pipeline/store_memory.go internal/devops/pipeline/store_memory_test.go
git commit -m "feat(pipeline): Repository 接口 + memory 实现（多租户隔离 + 单实例串行）"
```

---

## Task 5: Pipeline Repository PG 实现

**Files:**
- Create: `internal/devops/pipeline/store_pg.go`
- Create: `internal/devops/pipeline/integration_test.go`（`//go:build integration`）

**Interfaces:**
- Consumes: Task 3 migration 表结构；Task 4 接口契约。
- Produces: `NewPGStore(db *pgxpool.Pool) *pgStore`，实现 `Repository` + `RunRepository` + `TemplateRepository` 三接口（同一 store 实例，参照 devops memory 模式）。

- [ ] **Step 1: 写 store_pg.go**

参照 `internal/devops/pg/store.go` 模式：`*pgxpool.Pool` + 全参数化 SQL（`$1,$2..`）+ JSONB 字段（Stages/Trigger/StageRun Input/Output 用 `[]byte` marshal）+ 租户过滤（`WHERE tenant_id=$1`）+ 错误映射（`IsUniqueViolation` → ErrPipelineExists/ErrActiveRunExists）。

关键方法骨架（其余对齐）：
```go
package pipeline

import (
	"context"
	"encoding/json"
	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgStore struct {
	db *pgxpool.Pool
}

func NewPGStore(db *pgxpool.Pool) *pgStore { return &pgStore{db: db} }

func (s *pgStore) ListPipelines(ctx context.Context, appID string) ([]Pipeline, error) {
	tid := tenant.TenantFrom(ctx)
	q := `SELECT id,tenant_id,app_id,name,kind,template_id,stages,trigger,disabled,created_at
	      FROM pipelines WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" { q += ` AND app_id=$2`; args = append(args, appID) }
	q += ` ORDER BY created_at`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Pipeline
	for rows.Next() {
		var p Pipeline; var stages, trig []byte
		if err := rows.Scan(&p.ID,&p.TenantID,&p.AppID,&p.Name,&p.Kind,&p.TemplateID,&stages,&trig,&p.Disabled,&p.CreatedAt); err != nil { return nil, err }
		json.Unmarshal(stages, &p.Stages)
		json.Unmarshal(trig, &p.Trigger)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *pgStore) CreatePipeline(ctx context.Context, p Pipeline) (Pipeline, error) {
	tid := tenant.TenantFrom(ctx); p.TenantID = tid
	if p.ID == "" { p.ID = pg.NewID("pipe") } // 复用 pg.NewID 或 time.UnixNano
	stagesB, _ := json.Marshal(p.Stages); trigB, _ := json.Marshal(p.Trigger)
	p.CreatedAt = pg.Now()
	if err := p.Validate(); err != nil { return Pipeline{}, err }
	_, err := s.db.Exec(ctx,
		`INSERT INTO pipelines (id,tenant_id,app_id,name,kind,template_id,stages,trigger,disabled,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		p.ID, p.TenantID, p.AppID, p.Name, p.Kind, p.TemplateID, stagesB, trigB, p.Disabled, p.CreatedAt)
	if pg.IsUniqueViolation(err) { return Pipeline{}, ErrPipelineExists }
	if err != nil { return Pipeline{}, err }
	return p, nil
}

// UpdatePipeline：UPDATE ... WHERE id=$1 AND tenant_id=$2（RowCount 0 → ErrPipelineNotFound）。
// DeletePipeline：DELETE pipelines + cascade stage_runs（FK CASCADE），runs 单独 DELETE。
// GetPipeline/ListRuns/GetRun/CreateRun/UpdateRun/HasActiveRun/ListTemplates/GetTemplate/CreateTemplate：
//   全部 WHERE tenant_id=$tid，JSONB 字段 marshal/unmarshal，单实例串行靠 ux_pipeline_runs_active 唯一索引（CreateRun INSERT 失败 → ErrActiveRunExists）。
//   UpdateRun 写回：UPDATE pipeline_runs SET status=$,current_stage=$,... + DELETE/INSERT stage_runs（事务内，参照 application_bindings 保序模式）。
```

**关键**：
- `UpdateRun` 事务内重写 stage_runs（DELETE WHERE pipeline_run_id + 批量 INSERT），与 engine 推进对应。
- `HasActiveRun`：`SELECT EXISTS(SELECT 1 FROM pipeline_runs WHERE pipeline_id=$1 AND status IN ('running','paused'))`。
- 模板 `ListTemplates`：`WHERE tenant_id IS NULL OR tenant_id=$1`。

- [ ] **Step 2: 写集成测试**

`integration_test.go`:
```go
//go:build integration

package pipeline

import ( ... "os" "testing" ... )

func newTestStore(t *testing.T) *pgStore {
	url := os.Getenv("PAAS_TEST_PG_URL")
	if url == "" { t.Skip("PAAS_TEST_PG_URL not set") }
	// 复用 devops 集成测试的 resetSchema + 连接池构造（参照 devops/pg 集成测试）
	...
	return NewPGStore(db)
}

func TestPGPipelineCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 同 memory 测试：Create/Get/List/跨租户隔离/唯一约束
	p, _ := s.CreatePipeline(ctx, Pipeline{Name:"p1", AppID:"a1", Kind:KindCI,
		Stages:[]StageDef{{Name:"build",Type:StageBuild}}})
	if _, err := s.CreatePipeline(ctx, Pipeline{Name:"p1", AppID:"a1", Kind:KindCI,
		Stages:[]StageDef{{Name:"b",Type:StageBuild}}}); err != ErrPipelineExists {
		t.Fatalf("want Exists got %v", err)
	}
	_ = p
}
```

- [ ] **Step 3: 跑集成测试**
Run: `PAAS_TEST_PG_URL=postgres://... go test ./internal/devops/pipeline/ -tags=integration -v -p 1`
Expected: PASS。

- [ ] **Step 4: Commit**
```bash
git add internal/devops/pipeline/store_pg.go internal/devops/pipeline/integration_test.go
git commit -m "feat(pipeline): PG 实现（JSONB stages/trigger + 单实例唯一索引）"
```

---

## Task 6: 平台预置 ci/cd 模板 + seed

**Files:**
- Create: `internal/devops/pipeline/templates.go`
- Modify: `cmd/core/persistence.go`（seed 调用）
- Modify: `cmd/core/seed.go`（或在 persistence.go 内联）

**Interfaces:**
- Produces: `BuiltinTemplates() []PipelineTemplate`（ci + cd 两模板）；`SeedTemplates(ctx, TemplateRepository)` 幂等灌入（builtin=true，tenant_id=""，ON CONFLICT 跳过）。

- [ ] **Step 1: 写 templates.go**

```go
package pipeline

// BuiltinTemplates 平台预置流水线模板（tenant_id="" 全租户共享，builtin=true 不可删）。
func BuiltinTemplates() []PipelineTemplate {
	return []PipelineTemplate{
		{
			ID: "tpl-ci", Name: "开发流水线", Kind: KindCI, Builtin: true,
			Description: "git push 触发：构建 → 部署 dev → 冒烟测试 → 合并主干",
			Stages: []StageDef{
				{Name: "构建", Type: StageBuild},
				{Name: "部署到开发环境", Type: StageDeploy, Params: map[string]any{
					"envId": "", "imageSource": ImagePriorBuild, "strategy": "rolling",
					// envId 留空，用户创建时填本租户 dev 环境 ID
				}},
				{Name: "冒烟测试", Type: StageTest, Params: map[string]any{
					"mode": TestSmoke, "path": "/livez",
				}},
				{Name: "写基线", Type: StageBaseline, Params: map[string]any{
					"mainBranch": "main", "versionStrategy": "auto-increment", "mergeMode": "squash",
				}},
			},
		},
		{
			ID: "tpl-cd", Name: "发布流水线", Kind: KindCD, Builtin: true,
			Description: "手动触发：审批 → 部署 prod → 写版本",
			Stages: []StageDef{
				{Name: "上线审批", Type: StageApprove, Params: map[string]any{"message": "确认发布到生产环境"}},
				{Name: "部署到生产", Type: StageDeploy, Params: map[string]any{
					"envId": "", "imageSource": ImageLatestReady, "strategy": "rolling",
				}},
				{Name: "写版本", Type: StageBaseline, Params: map[string]any{
					"mainBranch": "", "versionStrategy": "auto-increment", "mergeMode": "ff",
					// mainBranch="" 时 cd 不合并（prod 发布不自动 merge，只打版本）
				}},
			},
		},
	}
}

// SeedTemplates 幂等灌入平台预置模板（builtin 不覆盖用户改动；PG ON CONFLICT DO NOTHING / memory exists 跳过）。
func SeedTemplates(ctx context.Context, repo TemplateRepository) error {
	for _, tpl := range BuiltinTemplates() {
		if _, err := repo.CreateTemplate(ctx, tpl); err != nil {
			if errors.Is(err, ErrTemplateExists) { continue }
			return err
		}
	}
	return nil
}
```

`CreateTemplate` 在 memory/pg 实现 builtin（tenant_id="" 不受 tenantOrErr 拦截，需特判：`t.TenantID == "" || t.TenantID == tenant.TenantFrom(ctx)`）。`ErrTemplateExists` sentinel。

- [ ] **Step 2: seed 接入 cmd/core**

`persistence.go` buildAllStores 末尾（PG/memory 两路径都在 SeedIfEmpty 区域后）加：
```go
// 平台预置流水线模板（全租户共享，不门控 demo seed）
if err := pipeline.SeedTemplates(baseCtx, pipelineStore); err != nil {
	log.Printf("pipeline: seed 模板失败: %v", err)
}
```
`pipelineStore` = `pipelineStore.(pipeline.TemplateRepository)`（或 Store 聚合接口见 Task 13）。

- [ ] **Step 3: 写测试**

`templates_test.go`:
```go
func TestSeedTemplatesIdempotent(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background() // 平台级，tenant 空
	if err := SeedTemplates(ctx, s); err != nil { t.Fatal(err) }
	first, _ := s.ListTemplates(ctx)
	if len(first) != 2 { t.Fatalf("want 2 got %d", len(first)) }
	// 幂等：再 seed 不报错不翻倍
	if err := SeedTemplates(ctx, s); err != nil { t.Fatal(err) }
	second, _ := s.ListTemplates(ctx)
	if len(second) != 2 { t.Fatalf("idempotent want 2 got %d", len(second)) }
}
```

- [ ] **Step 4: 跑测试**
Run: `go test ./internal/devops/pipeline/ -run TestSeedTemplates -v`
Expected: PASS。

- [ ] **Step 5: Commit**
```bash
git add internal/devops/pipeline/templates.go internal/devops/pipeline/templates_test.go cmd/core/persistence.go
git commit -m "feat(pipeline): 平台预置 ci/cd 模板 + 幂等 seed"
```

---

## Task 7: Gitea Merge 方法（baseline stage 依赖）

**Files:**
- Modify: `internal/devops/gitea/client.go`
- Modify: `internal/devops/gitea/client_test.go`

**Interfaces:**
- Produces: `Client.Merge(ctx, owner, repo, head, base, mode string) (mergeSHA string, err error)`，mode = `ff`（fast-forward，失败返 ErrMergeConflict）/ `squash`。

- [ ] **Step 1: 加 Merge 方法**

参照 client.go 已有 CreateRepo/GetTree 的 HTTP 风格（basic auth + JSON）：
```go
// Merge 把 head 分支合并到 base（Gitea merge API）。mode: "ff"(merge) | "squash"。
// fast-forward 不可行（分叉）时 Gitea 返 409 → ErrMergeConflict。
func (c *Client) Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error) {
	body := map[string]any{"Do": modeStr(mode), "MergeTitleField": "", "MergeMessageField": ""}
	// Gitea Do 字段: "merge"(ff, create merge commit) | "squash"
	b, _ := json.Marshal(body)
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls?base=%s&head=%s",
		c.baseURL, owner, repo, base, head)
	// 先创建 PR（或用 Gitea 直接 merge 分支 API：POST /repos/{o}/{r}/git/refs 不行）
	// 推荐：POST /repos/{o}/{r}/pulls（创建 PR）→ POST /repos/{o}/{r}/pulls/{index}/merge
	// MVP 简化：用 Gitea "merge 分支" API（POST /repos/{o}/{r}/branches/{base}/merge with form）
	... // 实现经 PR-create + merge 两步（实测 Gitea API 形态）
}
```

**注意**：Gitea 合并分支的精确 API 形态需实现时核对（创建 PR → merge PR 两步是最稳路径）。实现者应先查 Gitea API 文档（`internal/devops/gitea/client.go` 顶部注释有 Gitea 版本/URL），选最简可用形态。错误分流：409 → `ErrMergeConflict`（返友好"合并冲突，请手动解决"），网络错 → `ErrGiteaUnavailable`。

- [ ] **Step 2: 写测试（fake HTTP server）**

参照 client_test.go 已有 fake server 模式：
```go
func TestMergeSuccessAndConflict(t *testing.T) {
	// fake Gitea: POST /repos/o/r/pulls 返 201 {index:1}; POST /repos/o/r/pulls/1/merge 返 200/409
	// 200 → mergeSHA 非空；409 → ErrMergeConflict
}
```

- [ ] **Step 3: 跑测试**
Run: `go test ./internal/devops/gitea/ -run TestMerge -v`
Expected: PASS（success + conflict 两分支）。

- [ ] **Step 4: Commit**
```bash
git add internal/devops/gitea/client.go internal/devops/gitea/client_test.go
git commit -m "feat(devops/gitea): 加 Merge 方法（baseline stage 合并主干）"
```

---

## Task 8: 执行引擎 — advance 框架 + build/deploy stage + stage 输出链

**Files:**
- Create: `internal/devops/pipeline/engine.go`
- Create: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Consumes（依赖倒置，构造注入）:
  - `BuildRunner`：`CreateBuildRun(ctx, appID, repoID, branch, commit string, buildArgs map[string]string) (BuildRun, error)` + `PollBuildRun(ctx, buildID string) (BuildRun, error)`（轮询到终态）—— 桥接 devops.BuildRunRepository
  - `Releaser`：`CreateRelease(ctx, input devops.ReleaseInput) (Release, error)` + `PollWorkloadReady(ctx, workloadID string) error`—— 桥接 devops.ReleaseRepository + workload readiness
  - `PipelineRepo`/`RunRepo`（Task 4 接口）
- Produces:
  - `Engine` 结构体 + `Start(ctx, runID)`（goroutine 推进）
  - 内部 `advance(ctx, run)` 循环；`resolveImage(stage, run)` / `resolvePriorDeploy(run)` 辅助

- [ ] **Step 1: 写 engine.go 骨架 + build/deploy**

```go
package pipeline

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aitoys/paas/internal/devops"
)

// BuildRunner 桥接 devops BuildRunRepository（依赖倒置，engine 不直接 import 业务包循环由 cmd/core 桥接）。
type BuildRunner interface {
	CreateBuildRun(ctx context.Context, appID, repoID, branch, commit string, buildArgs map[string]string) (devops.BuildRun, error)
	PollBuildRun(ctx context.Context, buildID string) (devops.BuildRun, error) // 阻塞到终态（success/failed）
}

// Releaser 桥接 devops ReleaseRepository + workload readiness。
type Releaser interface {
	CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error)
	PollWorkloadReady(ctx context.Context, workloadID string) error // 阻塞到 ready 或超时
	WorkloadDomain(ctx context.Context, workloadID string) string    // 拼探活 URL
}

// Engine 异步推进 PipelineRun 状态机。不碰 HTTP，由 handler 调 Start。
type Engine struct {
	Runs RunRepository
	Builds BuildRunner
	Releases Releaser
}

// Start 起 goroutine 推进 runID（不阻塞）。handler 调用后立即返回。
func (e *Engine) Start(ctx context.Context, runID string) {
	go e.advanceLoop(ctx, runID)
}

func (e *Engine) advanceLoop(ctx context.Context, runID string) {
	// 派生 ctx 不绑请求生命周期（ctx 派生自 baseCtx，进程退出 cancel），但落库用 context.WithoutCancel(ctx)
	// 参照 devops runBuild baseCtx 模式（Plan 1 简化：派生 background + timeout per stage）。
	for {
		run, err := e.Runs.GetRun(ctx, runID)
		if err != nil { return }
		if run.Status != RunRunning { return } // paused/failed/aborted/succeeded 都停
		if run.CurrentStage >= len(run.StageRuns) {
			e.markSucceeded(ctx, run); return
		}
		stage := run.Pipeline.Stages[run.CurrentStage] // Pipeline 附在 run（GetRun 联合查；或先 GetPipeline）
		sr := run.StageRuns[run.CurrentStage]
		if sr.Status != StagePending && sr.Status != StageWaiting {
			// 已经在跑/已完成（恢复场景），跳到推进
		}
		finished, err := e.execStage(ctx, run, stage, &sr)
		// execStage 内部持久化 sr 状态；返回 finished=true 表示该 stage 终态（success→推进 / failed→中止 / paused→等）
		if err != nil { e.markFailed(ctx, run, err); return }
		if !finished { return } // paused，等人 approve
		run.CurrentStage++
		e.Runs.UpdateRun(ctx, run)
	}
}

// execStage 执行单个 stage，返回 (finished, error)。
// finished=false 表示 paused（test-manual/approve），等外部 approve 恢复。
// error 非 nil 表示 failed（中止整条 run）。
func (e *Engine) execStage(ctx context.Context, run PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	sr.StartedAt = time.Now()
	sr.Status = StageRunning
	switch stage.Type {
	case StageBuild:
		return e.execBuild(ctx, run, stage, sr)
	case StageDeploy:
		return e.execDeploy(ctx, run, stage, sr)
	case StageTest, StageApprove, StagePromote, StageBaseline:
		// Task 9/10 实现
		return false, fmt.Errorf("stage type %s not yet implemented", stage.Type)
	}
	return false, fmt.Errorf("unknown stage type %s", stage.Type)
}

func (e *Engine) execBuild(ctx context.Context, run PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	buildArgs := getStringMap(stage.Params, "buildArgs")
	branch := strOr(stage.Params, "branchOverride", run.Branch)
	// repoID 从应用绑定的 CodeRepo 取（engine 注入 RepoResolver；Plan 1 简化：取该 app 默认 repo）
	repoID := run.RepoID // PipelineRun 加 RepoID 字段（run 时解析）
	br, err := e.Builds.CreateBuildRun(ctx, run.AppID, repoID, branch, run.Commit, buildArgs)
	if err != nil { sr.Error = err.Error(); return true, err }
	sr.Input = map[string]any{"buildRunId": br.ID}
	br, err = e.Builds.PollBuildRun(ctx, br.ID)
	if err != nil { sr.Error = err.Error(); return true, err }
	if br.Status != "success" { sr.Error = "build failed"; return true, fmt.Errorf("build failed") }
	sr.Output = map[string]any{OutImageID: br.ImageID}
	sr.Status = StageSuccess
	e.persistStageRun(ctx, run, *sr)
	return true, nil
}

func (e *Engine) execDeploy(ctx context.Context, run PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	imageID, err := e.resolveImage(ctx, stage, run)
	if err != nil { sr.Error = err.Error(); return true, err }
	envID := strOr(stage.Params, "envId", "")
	if envID == "" { return true, fmt.Errorf("deploy stage 缺 envId") }
	rel, err := e.Releases.CreateRelease(ctx, devops.ReleaseInput{
		AppID: run.AppID, EnvID: envID, ImageID: imageID, Strategy: strOr(stage.Params, "strategy", "rolling"),
	})
	if err != nil { sr.Error = err.Error(); return true, err }
	if err := e.Releases.PollWorkloadReady(ctx, rel.WorkloadID); err != nil {
		sr.Error = err.Error(); return true, err
	}
	domain := e.Releases.WorkloadDomain(ctx, rel.WorkloadID)
	sr.Output = map[string]any{OutReleaseID: rel.ID, OutWorkloadDomain: domain}
	sr.Status = StageSuccess
	e.persistStageRun(ctx, run, *sr)
	return true, nil
}

// resolveImage deploy stage 镜像来源解析（CI/CD 解耦核心）。
func (e *Engine) resolveImage(ctx context.Context, stage StageDef, run PipelineRun) (string, error) {
	src := strOr(stage.Params, "imageSource", ImagePriorBuild)
	switch src {
	case ImageSelected:
		id := strOr(stage.Params, "imageId", "")
		if id == "" { return "", fmt.Errorf("imageSource=selected 缺 imageId") }
		return id, nil
	case ImageLatestReady:
		return e.Releases.LatestReadyImage(ctx, run.AppID) // 桥接 ImageRepository
	case ImagePriorBuild:
		return resolvePriorOutput(run, OutImageID) // 向前扫描 StageRuns
	}
	return "", fmt.Errorf("unknown imageSource %s", src)
}

// resolvePriorOutput 从当前 stage 之前已完成 stage 的 Output 取 key（向前扫描）。
func resolvePriorOutput(run PipelineRun, key string) (string, error) {
	for i := run.CurrentStage - 1; i >= 0; i-- {
		if v, ok := run.StageRuns[i].Output[key]; ok {
			if s, ok := v.(string); ok && s != "" { return s, nil }
		}
	}
	return "", fmt.Errorf("前序 stage 无 %s 输出", key)
}
```

辅助：`getStringMap`/`strOr` 是 `map[string]any` 类型断言 helper（同文件）。`PipelineRun` 加 `RepoID` 字段（run 时从 app 绑定解析）+ `Pipeline` 联合（GetRun 返回带 stages，或 engine 先 GetPipeline）。`persistStageRun` 更新 run 的对应 StageRun + UpdateRun 落库。

**关键设计**：`PollBuildRun`/`PollWorkloadReady` 是阻塞轮询（内部 select ctx.Done + time.Sleep 退避），engine goroutine 在此等待。ctx 派生自进程 baseCtx（进程退出 cancel），落库用 `context.WithoutCancel`（参照 devops runBuild）。

- [ ] **Step 2: 写测试（build→deploy 链 + imageSource 三模式）**

```go
func TestEngineBuildDeployChain(t *testing.T) {
	// fake BuildRunner: CreateBuildRun 返 BuildRun{ID:"b1"}; PollBuildRun 返 {Status:"success", ImageID:"img-1"}
	// fake Releaser: CreateRelease 返 Release{ID:"rel-1", WorkloadID:"wl-1"}; PollWorkloadReady=nil; WorkloadDomain="wl-1.svc"
	store := NewMemoryStore()
	eng := &Engine{Runs: store, Builds: fakeBuilder{}, Releases: fakeReleaser{}}
	// 建 pipeline: [build, deploy(priorBuild)]
	// 建 run（manual，currentStage=0，stageRuns=[pending,pending]）
	// eng.advance 同步跑（测试用同步版本：advanceSync）
	// 断言：run.Status=succeeded, stageRuns[0].Output.imageId=img-1, stageRuns[1].Output.releaseId=rel-1
	//       deploy stage.Input.imageId 来自前序 build Output（priorBuild 链）
}

func TestResolveImageSources(t *testing.T) {
	// selected: stage.Params.imageId=img-x → img-x
	// latestReady: fakeReleaser.LatestReadyImage=img-y → img-y
	// priorBuild: 前序 build Output.imageId=img-1 → img-1（向前扫描）
	// priorBuild 无前序 → error
}
```

**注意**：测试用同步 `advanceSync`（不启 goroutine），engine 暴露 `Advance(ctx, runID) error` 同步方法供测试，`Start` 内部 `go Advance`。

- [ ] **Step 3: 跑测试**
Run: `go test ./internal/devops/pipeline/ -run TestEngine -v -race`
Expected: PASS（build→deploy 链 + 三 imageSource）。

- [ ] **Step 4: Commit**
```bash
git add internal/devops/pipeline/engine.go internal/devops/pipeline/engine_test.go
git commit -m "feat(pipeline): 执行引擎 advance 框架 + build/deploy stage + 输出链"
```

---

## Task 9: 执行引擎 — test/approve stage（暂停 + 恢复）

**Files:**
- Modify: `internal/devops/pipeline/engine.go`
- Modify: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Produces: `execTest`（smoke 探活 / manual 暂停）+ `execApprove`（暂停）+ `Resume(ctx, runID, stageIdx)`（handler approve 调用，标记 stage 成功后重新 advance）。

- [ ] **Step 1: 实现 test + approve stage**

```go
func (e *Engine) execTest(ctx context.Context, run PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	mode := strOr(stage.Params, "mode", TestSmoke)
	if mode == TestManual {
		// 人工确认：暂停 run，等人 approve
		sr.Status = StageWaiting
		sr.Input = map[string]any{"mode": TestManual, "message": strOr(stage.Params, "message", "请确认测试通过")}
		e.persistStageRun(ctx, run, *sr)
		e.markPaused(ctx, run) // run.Status=paused 持久化
		return false, nil      // 不推进，等 Resume
	}
	// smoke：curl prior deploy 的 domain + path
	domain, err := resolvePriorOutput(run, OutWorkloadDomain)
	if err != nil { sr.Error = err.Error(); return true, err }
	path := strOr(stage.Params, "path", "/livez")
	url := fmt.Sprintf("http://%s%s", domain, path)
	if err := pollHTTP(ctx, url, 2*time.Minute); err != nil {
		sr.Error = fmt.Sprintf("探活失败 %s: %v", url, err)
		return true, err
	}
	sr.Output = map[string]any{"result": "ok", "url": url}
	sr.Status = StageSuccess
	e.persistStageRun(ctx, run, *sr)
	return true, nil
}

func (e *Engine) execApprove(ctx context.Context, run PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	sr.Status = StageWaiting
	sr.Input = map[string]any{"message": strOr(stage.Params, "message", "等待审批")}
	e.persistStageRun(ctx, run, *sr)
	e.markPaused(ctx, run)
	return false, nil
}

// Resume 恢复 paused run 的某 stage（handler approve 调用）。
// 标记该 stage 成功 + currentStage++ + run.Status=running，再启 advance。
func (e *Engine) Resume(ctx context.Context, runID string, stageIdx int) error {
	run, err := e.Runs.GetRun(ctx, runID)
	if err != nil { return err }
	if run.Status != RunPaused { return ErrNotPaused }
	if stageIdx != run.CurrentStage { return ErrStageNotCurrent }
	run.StageRuns[stageIdx].Status = StageSuccess
	run.StageRuns[stageIdx].FinishedAt = time.Now()
	run.CurrentStage++
	run.Status = RunRunning
	if _, err := e.Runs.UpdateRun(ctx, run); err != nil { return err }
	e.Start(ctx, runID)
	return nil
}
```

`pollHTTP`：GET 轮询到 200 或超时（`net/http` + select ctx.Done，参照 observability/real HTTP client 但无 redirect 跟随）。`markPaused`：run.Status=paused + UpdateRun。sentinel `ErrNotPaused`/`ErrStageNotCurrent`。

- [ ] **Step 2: 写测试**

```go
func TestEngineTestSmokeSuccess(t *testing.T) {
	// pipeline: [deploy(test), test(smoke,/livez)]
	// fakeReleaser.WorkloadDomain="wl.svc"; pollHTTP 用 fake httptest.Server 返 200
	// advance → run.Status=succeeded, test stage.Output.result=ok
}
func TestEngineApprovePauseResume(t *testing.T) {
	// pipeline: [approve, deploy]
	// advance → run.Status=paused, approve stage.Status=waiting
	// Resume(runID, 0) → run.Status=running → advance 继续 → deploy → succeeded
}
func TestEngineTestManualPause(t *testing.T) {
	// pipeline: [test(manual)] → advance → paused, stage waiting
}
```

- [ ] **Step 3: 跑测试**
Run: `go test ./internal/devops/pipeline/ -run TestEngine -v -race`
Expected: PASS。

- [ ] **Step 4: Commit**
```bash
git add internal/devops/pipeline/engine.go internal/devops/pipeline/engine_test.go
git commit -m "feat(pipeline): engine test/approve stage（暂停 + Resume 恢复）"
```

---

## Task 10: 执行引擎 — promote/baseline stage

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（加 execPromote/execBaseline）
- Modify: `internal/devops/pipeline/engine.go`（Releaser 接口加 Promote + GiteaMerger 接口）
- Modify: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Consumes 新增:
  - `Releaser.Promote(ctx, releaseID) (Release, error)`（桥接 PromoteRelease）
  - `GiteaMerger`：`Merge(ctx, owner, repo, head, base, mode string) (string, error)`（桥接 gitea.Client）+ `ResolveRepo(ctx, appID) (owner, repo string, err error)`（取 app 绑定的 internal repo）
- Produces: `execPromote` / `execBaseline`。

- [ ] **Step 1: 实现 promote + baseline**

```go
type GiteaMerger interface {
	ResolveRepo(ctx context.Context, appID string) (owner, repo string, err error)
	Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error)
}

func (e *Engine) execPromote(ctx context.Context, run PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	srcReleaseID, err := resolvePriorOutput(run, OutReleaseID)
	if err != nil { sr.Error = err.Error(); return true, err }
	rel, err := e.Releases.Promote(ctx, srcReleaseID)
	if err != nil {
		if errors.Is(err, devops.ErrNoPromoteTarget) {
			sr.Error = "已是最高阶环境，无晋升目标"; return true, err
		}
		sr.Error = err.Error(); return true, err
	}
	sr.Output = map[string]any{OutReleaseID: rel.ID}
	sr.Status = StageSuccess
	e.persistStageRun(ctx, run, *sr)
	return true, nil
}

func (e *Engine) execBaseline(ctx context.Context, run PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	// 1. 打版本：给本次 run 关联的所有 Release 写 Version
	version := computeVersion(run, stage)
	if err := e.Releases.SetRunVersion(ctx, run.ID, version); err != nil {
		sr.Error = err.Error(); return true, err
	}
	run.Version = version
	out := map[string]any{OutVersion: version}

	// 2. 合并主干（mainBranch 非空才 merge）
	mainBranch := strOr(stage.Params, "mainBranch", "")
	if mainBranch != "" && e.Gitea != nil {
		owner, repo, err := e.Gitea.ResolveRepo(ctx, run.AppID)
		if err == nil {
			mergeSHA, err := e.Gitea.Merge(ctx, owner, repo, run.Branch, mainBranch,
				strOr(stage.Params, "mergeMode", "squash"))
			if err != nil {
				if errors.Is(err, gitea.ErrMergeConflict) {
					// 合并冲突不中止（版本已打），仅记 warning，让用户手动解决
					sr.Error = "合并冲突，请手动解决"
				} else {
					sr.Error = err.Error()
				}
			} else {
				out[OutMergeSHA] = mergeSHA
			}
		}
		// ResolveRepo 失败（external repo / 无 internal repo）跳过 merge，仅打版本
	}
	sr.Output = out
	sr.Status = StageSuccess // baseline 即使 merge 冲突也标 success（版本已打）
	e.persistStageRun(ctx, run, *sr)
	return true, nil
}

// computeVersion 版本号生成（auto-increment 优先）。
func computeVersion(run PipelineRun, stage StageDef) string {
	strategy := strOr(stage.Params, "versionStrategy", "auto-increment")
	switch strategy {
	case "manual":
		if run.Version != "" { return run.Version } // 触发时填
	case "tag":
		// 从 commit 关联的 git tag 取（Plan 1 简化：commit 非空则用 commit 短 sha）
		if run.Commit != "" { return run.Commit[:min(8, len(run.Commit))] }
	}
	// auto-increment: <branch>-<runSeq>（runSeq 取 run.ID 后 6 位）
	return fmt.Sprintf("%s-%s", run.Branch, shortID(run.ID))
}
```

`Releaser.SetRunVersion`：给本次 run 涉及的 Release（按 stageRuns Output.releaseId 收集）UPDATE version。`devops.ErrNoPromoteTarget` / `gitea.ErrMergeConflict` sentinel（Task 7 已定义 gitea 的）。

- [ ] **Step 2: 写测试**

```go
func TestEnginePromote(t *testing.T) {
	// pipeline: [deploy(test), promote]
	// fakeReleaser.Promote(rel-test-id) → Release{ID:"rel-prod-id"}
	// advance → promote stage.Output.releaseId=rel-prod-id
}
func TestEngineBaselineVersion(t *testing.T) {
	// pipeline: [deploy, baseline(auto-increment)]
	// fakeReleaser.SetRunVersion 记录调用；advance → run.Version 非空, stage.Output.version 非空
	// 不配 Gitea（e.Gitea=nil）→ 跳过 merge，仅打版本
}
func TestEngineBaselineMergeConflict(t *testing.T) {
	// fake GiteaMerger.Merge 返 ErrMergeConflict → stage 仍 success（版本已打）, sr.Error 含"合并冲突"
}
```

- [ ] **Step 3: 跑测试**
Run: `go test ./internal/devops/pipeline/ -run TestEngine -v -race`
Expected: 全 PASS（build/deploy/test/approve/promote/baseline 全覆盖）。

- [ ] **Step 4: Commit**
```bash
git add internal/devops/pipeline/engine.go internal/devops/pipeline/engine_test.go
git commit -m "feat(pipeline): engine promote/baseline stage（晋升+打版本+合并主干）"
```

---

## Task 11: Handler — Pipeline / Template CRUD

**Files:**
- Create: `internal/devops/pipeline/handler.go`
- Create: `internal/devops/pipeline/handler_test.go`

**Interfaces:**
- Consumes: Task 4 Repository/TemplateRepository；`identity` 权限。
- Produces: `Handler` 结构体 + `ServeHTTP`（按路径分发）。
- 路由：
  - `GET    /api/applications/{id}/pipelines` / `POST`（创建，可选 `templateId`）
  - `GET/PUT/DELETE /api/applications/{id}/pipelines/{pid}`
  - `GET /api/pipeline-templates`

**权限常量**（handler.go 顶部）：
```go
const (
	PermPipelineRead  = "pipeline:read"
	PermPipelineWrite = "pipeline:write"
)
```

- [ ] **Step 1: 并入 identity.BuiltinRoles**

`internal/core/identity/model.go` `BuiltinRoles()` 的 developer 角色权限列表加 `pipeline:read`、`pipeline:write`（参照已有 `release:read/write` 位置，model.go:71-90）；tenant-admin 因含 `tenant:admin` 自动通行。viewer 若有则加只读（当前 BuiltinRoles 无 viewer，跳过）。

- [ ] **Step 2: 写 handler.go Pipeline/Template 部分**

参照 `internal/devops/handler.go` 模式（composite 按路径分发 + `httputil.WriteData` + 权限 `Authorize` 函数注入）：

```go
package pipeline

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
)

type Handler struct {
	pipes    Repository
	runs     RunRepository
	templates TemplateRepository
	engine   *Engine
	// Authorize 权限校验（nil 跳过，测试场景）。
	Authorize func(r *http.Request, perm string) bool
	// EnvTypeResolver 生产 deploy 校验（pipeline run 的 deploy stage 到 prod）。
	envType   func(ctx context.Context, envID string) (string, error)
	// Audit 审计记录器（nil 跳过）。
	audit     AuditRecorder
	actorFn   func(r *http.Request) string
}

func NewHandler(pipes Repository, runs RunRepository, templates TemplateRepository, engine *Engine, opts ...HandlerOpt) *Handler {
	h := &Handler{pipes: pipes, runs: runs, templates: templates, engine: engine}
	for _, o := range opts { o(h) }
	return h
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) { return true }
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 分发 /api/applications/{id}/pipelines[...] 与 /api/pipeline-templates 与 /api/pipelineruns[...]。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/pipeline-templates"):
		h.serveTemplates(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/pipelineruns"):
		h.serveRuns(w, r) // Task 12
	case strings.HasPrefix(r.URL.Path, "/api/applications/"):
		h.serveAppPipelines(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveAppPipelines(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	// /api/applications/{id}/pipelines[/{pid}]
	if len(parts) < 2 || parts[1] != "pipelines" { httputil.WriteError(w, 404, "not found"); return }
	appID := parts[0]

	if len(parts) == 2 && r.Method == http.MethodGet {
		if !h.allow(w, r, PermPipelineRead) { return }
		list, err := h.pipes.ListPipelines(r.Context(), appID)
		if err != nil { httputil.WriteInternalError(w, err); return }
		httputil.WriteData(w, list); return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		if !h.allow(w, r, PermPipelineWrite) { return }
		var body struct {
			Name, Kind, TemplateID string
			Stages []StageDef
			Trigger PipelineTrigger
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { httputil.WriteError(w, 400, "invalid body"); return }
		p := Pipeline{AppID: appID, Name: body.Name, Kind: body.Kind, TemplateID: body.TemplateID,
			Stages: body.Stages, Trigger: body.Trigger}
		// 从模板创建：templateId 非空时 GetTemplate 复制 stages（用户可后续改）
		if body.TemplateID != "" {
			tpl, err := h.templates.GetTemplate(r.Context(), body.TemplateID)
			if err != nil { httputil.WriteServiceError(w, 404, err); return }
			p.Kind = tpl.Kind
			if len(body.Stages) == 0 { p.Stages = cloneStages(tpl.Stages) }
		}
		created, err := h.pipes.CreatePipeline(r.Context(), p)
		if err != nil { httputil.WriteServiceError(w, toHTTPStatus(err), err); return }
		h.recordAudit(r, "create", "pipeline", created.ID, "")
		httputil.WriteDataCreated(w, created); return
	}
	if len(parts) == 3 { // {pid}
		pid := parts[2]
		switch r.Method {
		case http.MethodGet:
			if !h.allow(w, r, PermPipelineRead) { return }
			p, err := h.pipes.GetPipeline(r.Context(), pid)
			if err != nil { httputil.WriteServiceError(w, 404, err); return }
			httputil.WriteData(w, p)
		case http.MethodPut:
			if !h.allow(w, r, PermPipelineWrite) { return }
			var p Pipeline; json.NewDecoder(r.Body).Decode(&p)
			existing, err := h.pipes.GetPipeline(r.Context(), pid)
			if err != nil { httputil.WriteServiceError(w, 404, err); return }
			p.ID = pid; p.TenantID = existing.TenantID; p.AppID = appID; p.CreatedAt = existing.CreatedAt
			updated, err := h.pipes.UpdatePipeline(r.Context(), p)
			if err != nil { httputil.WriteServiceError(w, toHTTPStatus(err), err); return }
			httputil.WriteData(w, updated)
		case http.MethodDelete:
			if !h.allow(w, r, PermPipelineWrite) { return }
			if err := h.pipes.DeletePipeline(r.Context(), pid); err != nil { httputil.WriteServiceError(w, 404, err); return }
			h.recordAudit(r, "delete", "pipeline", pid, "")
			httputil.WriteData(w, map[string]string{"deleted": pid})
		default:
			httputil.WriteError(w, 405, "method not allowed")
		}
		return
	}
	httputil.WriteError(w, 404, "not found")
}

func (h *Handler) serveTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { httputil.WriteError(w, 405, "method not allowed"); return }
	if !h.allow(w, r, PermPipelineRead) { return }
	list, err := h.templates.ListTemplates(r.Context())
	if err != nil { httputil.WriteInternalError(w, err); return }
	httputil.WriteData(w, list)
}
```

`toHTTPStatus(err)`：ErrPipelineExists/ErrActiveRunExists → 409，其余 → 400。`recordAudit` 经 `h.audit`（nil 跳过）。`cloneStages` 深拷贝（模板 stages 复制到 pipeline，断开引用）。

- [ ] **Step 3: 写测试**

```go
func TestPipelineCreateFromTemplate(t *testing.T) {
	h := NewHandler(store, store, store, nil) // engine=nil（CRUD 不用 engine）
	h.Authorize = func(*http.Request, string) bool { return true }
	// 先 seed tpl-ci（store.CreateTemplate）
	// POST /api/applications/a1/pipelines {templateId:"tpl-ci", name:"p1"}
	// 断言 201, body.kind=ci, body.stages 复制自模板（len>0）
}
func TestPipelineCrossTenantIsolation(t *testing.T) {
	// t-acme 建 pipeline，t-globex GET → 空列表
}
func TestPipelineCRUDPermissionDenied(t *testing.T) {
	// Authorize 返 false → 403
}
```

- [ ] **Step 4: 跑测试**
Run: `go test ./internal/devops/pipeline/ -run TestPipelineCreate -v`
Expected: PASS。

- [ ] **Step 5: Commit**
```bash
git add internal/devops/pipeline/handler.go internal/devops/pipeline/handler_test.go internal/core/identity/model.go
git commit -m "feat(pipeline): handler Pipeline/Template CRUD + 权限"
```

---

## Task 12: Handler — PipelineRun（run/approve/abort + prod:write + 审计）

**Files:**
- Modify: `internal/devops/pipeline/handler.go`（加 serveRuns）
- Modify: `internal/devops/pipeline/handler_test.go`

**Interfaces:**
- Produces: `serveRuns`（list/get/run/approve/abort）。
- 路由：
  - `POST /api/applications/{id}/pipelines/{pid}/run` body `{branch, commit?, version?}` → 建 PipelineRun + engine.Start
  - `GET /api/pipelineruns?appId=&pipelineId=&status=` / `GET /api/pipelineruns/{id}`
  - `POST /api/pipelineruns/{id}/stages/{idx}/approve`
  - `POST /api/pipelineruns/{id}/abort`

- [ ] **Step 1: 实现 serveRuns**

```go
func (h *Handler) serveRuns(w http.ResponseWriter, r *http.Request) {
	// /api/pipelineruns[/{id}[/stages/{idx}/approve|/abort]]
	rest := strings.TrimPrefix(r.URL.Path, "/api/pipelineruns")
	rest = strings.Trim(rest, "/")

	if rest == "" && r.Method == http.MethodGet {
		if !h.allow(w, r, PermPipelineRead) { return }
		appID := r.URL.Query().Get("appId")
		pipelineID := r.URL.Query().Get("pipelineId")
		status := r.URL.Query().Get("status")
		list, err := h.runs.ListRuns(r.Context(), appID, pipelineID, status)
		if err != nil { httputil.WriteInternalError(w, err); return }
		httputil.WriteData(w, list); return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]

	// GET /api/pipelineruns/{id}
	if r.Method == http.MethodGet && len(parts) == 1 {
		if !h.allow(w, r, PermPipelineRead) { return }
		run, err := h.runs.GetRun(r.Context(), id)
		if err != nil { httputil.WriteServiceError(w, 404, err); return }
		httputil.WriteData(w, run); return
	}

	// POST /api/pipelineruns/{id}/stages/{idx}/approve
	if r.Method == http.MethodPost && len(parts) == 3 && parts[1] == "stages" && parts[3-1] == "approve" {
		// parts[2] = idx；用 path 末段匹配更稳：见下方实际实现
	}
	// 实际用 lastN segments 分发：
	if len(parts) >= 2 && parts[len(parts)-1] == "approve" {
		if !h.allow(w, r, PermPipelineWrite) { return }
		stageIdx := atoiSafe(parts[len(parts)-2])
		if err := h.engine.Resume(r.Context(), id, stageIdx); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err); return
		}
		h.recordAudit(r, "approve", "pipeline_run", id, fmt.Sprintf("stage=%d", stageIdx))
		httputil.WriteData(w, map[string]string{"resumed": id}); return
	}
	if len(parts) == 2 && parts[1] == "abort" && r.Method == http.MethodPost {
		if !h.allow(w, r, PermPipelineWrite) { return }
		if err := h.engine.Abort(r.Context(), id); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err); return
		}
		h.recordAudit(r, "abort", "pipeline_run", id, "")
		httputil.WriteData(w, map[string]string{"aborted": id}); return
	}
	httputil.WriteError(w, 404, "not found")
}

// serveRun（在 serveAppPipelines 内 /pipelines/{pid}/run 分支）：
//   body {branch, commit?, version?}
//   先 GetPipeline（校验归属 + 取 stages/trigger）
//   解析 repoID（app 绑定的 CodeRepo）+ 校验 deploy stage 目标环境 prod:write
//   HasActiveRun 校验（单实例串行）
//   建 PipelineRun{Status:running, CurrentStage:0, StageRuns:[pending...], RepoID}
//   engine.Start(ctx, run.ID)
//   WriteDataCreated(run)
```

**关键横切**（run 创建时的 prod:write 校验）：
```go
// 创建 run 前，扫描 pipeline stages 的 deploy/promote stage，目标环境为 prod 时
// 要求调用者持 prod:write（防 developer 经 CI pipeline 直接 deploy prod）。
for _, s := range p.Stages {
	if s.Type == StageDeploy {
		envID := strOr(s.Params, "envId", "")
		if etype, err := h.envType(r.Context(), envID); err == nil && etype == "prod" {
			if !h.allow(w, r, PermProdWrite) { return }
		}
	}
}
```
`PermProdWrite = "prod:write"`（与 workload/billing 等同常量，统一）。

- [ ] **Step 2: engine.Abort 实现**

engine.go 加：
```go
func (e *Engine) Abort(ctx context.Context, runID string) error {
	run, err := e.Runs.GetRun(ctx, runID)
	if err != nil { return err }
	if run.Status != RunRunning && run.Status != RunPaused { return ErrNotRunning }
	run.Status = RunAborted
	run.FinishedAt = time.Now()
	_, err = e.Runs.UpdateRun(ctx, run)
	// 进行中的 BuildRun 通过 ctx cancel（engine 派生 ctx 存储 run->cancel；MVP 简化：
	// PollBuildRun 内部 select ctx.Done，Abort 时 e.cancel(runID) 调 cancel func）
	return err
}
```
（engine 维护 `map[string]context.CancelFunc` run→cancel，Start 时存入，Abort/完成时清理；cancel 传播给 PollBuildRun。）

- [ ] **Step 3: 写测试**

```go
func TestRunManualTriggerAndAdvance(t *testing.T) {
	// pipeline: [build, deploy(test, priorBuild)]
	// POST run → 201 + engine.Start
	// 轮询 GET /api/pipelineruns/{id} 直到 succeeded（fake builder/releaser 即时成功）
	// 断言 run.Status=succeeded, stageRuns 全 success
}
func TestRunApproveFlow(t *testing.T) {
	// pipeline: [approve, deploy]
	// run → paused; POST /stages/0/approve → resumed → succeeded
}
func TestRunProdWriteGuard(t *testing.T) {
	// pipeline deploy(prod); 调用者 developer（无 prod:write）→ run 创建 403
}
func TestRunSingleInstance(t *testing.T) {
	// 已有 running run；再 POST run → 409 ErrActiveRunExists
}
func TestRunAbort(t *testing.T) {
	// run running; POST abort → status=aborted
}
```

- [ ] **Step 4: 跑测试**
Run: `go test ./internal/devops/pipeline/ -v -race`
Expected: 全 PASS。

- [ ] **Step 5: Commit**
```bash
git add internal/devops/pipeline/handler.go internal/devops/pipeline/engine.go internal/devops/pipeline/handler_test.go
git commit -m "feat(pipeline): handler PipelineRun（run/approve/abort + prod:write + 审计）"
```

---

## Task 13: cmd/core 装配 + 路由注册 + 端到端 curl 验证

**Files:**
- Modify: `cmd/core/persistence.go`（构造 pipeline store + engine 依赖桥接）
- Modify: `cmd/core/main.go`（路由注册 + composite 分发 + Authorize 注入）

**Interfaces:**
- Produces:
  - `Stores.Pipelines pipeline.Repository` / `Stores.PipelineRuns pipeline.RunRepository` / `Stores.PipelineTemplates pipeline.TemplateRepository`（聚合）
  - pipeline engine + 桥接 adapter（BuildRunner/Releaser/GiteaMerger 实现，桥接 stores.DevOps* + stores.Workloads + giteaClient）

- [ ] **Step 1: 桥接 adapter（cmd/core，破除 pipeline→devops import 循环）**

`cmd/core/persistence.go` 或新文件 `cmd/core/pipeline_adapters.go`：
```go
// buildBridge 实现 pipeline.BuildRunner，桥接 devops.BuildRunRepository + CodeRepoRepository。
type buildBridge struct{ builds devops.BuildRunRepository; repos devops.CodeRepoRepository }

func (b buildBridge) CreateBuildRun(ctx context.Context, appID, repoID, branch, commit string, buildArgs map[string]string) (devops.BuildRun, error) {
	// 调 b.builds.CreateBuildRun；buildArgs 透传（devops BuildRun 需加 BuildArgs 字段——Plan 4 dogfooding 倒逼，
	// Plan 1 暂传 nil/空，PipelineRun.RepoID 从 CodeRepo.ListRepos(appID) 取第一个 internal repo）
}
func (b buildBridge) PollBuildRun(ctx context.Context, buildID string) (devops.BuildRun, error) {
	// 轮询 b.builds.GetBuildRun 直到终态（select ctx.Done + 2s 退避）
}

// releaseBridge 实现 pipeline.Releaser，桥接 devops.ReleaseRepository + workload readiness。
type releaseBridge struct{ rels devops.ReleaseRepository; images devops.ImageRepository; wl workload.Repository; status workload.StatusReader }

func (b releaseBridge) CreateRelease(ctx context.Context, in devops.ReleaseInput) (devops.Release, error) {
	return b.rels.CreateRelease(ctx, in)
}
func (b releaseBridge) PollWorkloadReady(ctx context.Context, workloadID string) error {
	// 轮询 b.status.Instances(workloadID) 或 Get readiness
}
func (b releaseBridge) WorkloadDomain(ctx context.Context, workloadID string) string {
	// 拼接 <workloadName>.<ns>.svc.cluster.local（从 workload Repo 取 name + ns）
}
func (b releaseBridge) Promote(ctx, releaseID) (devops.Release, error) { return b.rels.PromoteRelease(ctx, releaseID) }
func (b releaseBridge) LatestReadyImage(ctx, appID) (string, error) {
	// b.images.ListImages(appID) 取最新 status=ready
}
func (b releaseBridge) SetRunVersion(ctx, runID, version string) error {
	// 从 pipeline run 的 stageRuns 收集 releaseId → UPDATE releases SET version
	// （需 pipeline store 透传 stageRuns 或 ReleaseRepository 加 SetVersionByIDs）
}

// giteaBridge 实现 pipeline.GiteaMerger，桥接 gitea.Client + CodeRepoRepository。
type giteaBridge struct{ c *gitea.Client; repos devops.CodeRepoRepository }
func (b giteaBridge) ResolveRepo(ctx, appID) (owner, repo string, err error) {
	// repos.ListRepos(appID) 找 source=internal 的 CodeRepo，解析 GiteaOwner/GiteaRepo
}
func (b giteaBridge) Merge(ctx, owner, repo, head, base, mode string) (string, error) {
	return b.c.Merge(ctx, owner, repo, head, base, mode)
}
```

**注意**：`devops.BuildRun.BuildArgs` 字段未在 Plan 1 加（Plan 4 dogfooding 才需要 buildArgs 透传到 builder）。Plan 1 的 buildBridge.CreateBuildRun 暂忽略 buildArgs 参数（传空给 devops.CreateBuildRun），Task 13 端到端验证用简单镜像（无 buildArgs）。Plan 4 加 BuildArgs 字段时再透传。

- [ ] **Step 2: persistence.go 装配**

`buildAllStores` 两路径（PG/memory）构造 pipeline store：
```go
// PG 路径（line ~144 区域）：
pipeStore := pipeline.NewPGStore(db)
// memory 路径（line ~216 区域）：
pipeStore := pipeline.NewMemoryStore()
```
`Stores` 加字段：
```go
Pipelines        pipeline.Repository
PipelineRuns     pipeline.RunRepository
PipelineTemplates pipeline.TemplateRepository
pipelineStore    interface{} // 供 engine 装配引用（实现三接口的同一实例）
```
（PG/memory store 同实例实现三接口，参照 devopsRepo 模式。）两路径都 `pipeline.SeedTemplates(baseCtx, pipeStore)`（Task 6）。

- [ ] **Step 3: main.go 装配 engine + handler + 路由**

main.go（line ~440 devopsHandler 区域后）：
```go
// pipeline engine + 桥接
buildBridge := buildBridge{builds: stores.DevOpsBuilds, repos: stores.DevOpsRepos}
releaseBridge := releaseBridge{rels: stores.DevOpsReleases, images: stores.DevOpsImages, wl: stores.Workloads, status: wlStatusReader}
giteaBridge := giteaBridge{c: giteaClient, repos: stores.DevOpsRepos}
pipeEngine := &pipeline.Engine{
	Runs: stores.PipelineRuns, Builds: buildBridge, Releases: releaseBridge, Gitea: giteaBridge,
}
pipelineHandler := pipeline.NewHandler(stores.Pipelines, stores.PipelineRuns, stores.PipelineTemplates, pipeEngine,
	pipeline.WithEnvType(stores.Environment.EnvType),
	pipeline.WithAudit(&pipelineAuditAdapter{store: stores.Security}),
	pipeline.WithActor(func(r *http.Request) string { return gateway.UserIDFrom(r.Context()) }),
)
pipelineHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
```

路由注册（line ~621 区域）：
```go
mux.Handle("/api/pipeline-templates", auth(pipelineHandler))
mux.Handle("/api/pipelineruns", auth(pipelineHandler))
mux.Handle("/api/pipelineruns/", auth(pipelineHandler))
```
composite 分发（line ~598）：`/api/applications/{id}/pipelines` 与 `/api/applications/{id}/pipelines/{pid}/run` 归 pipelineHandler：
```go
if rest := strings.TrimPrefix(r.URL.Path, "/api/applications/"); rest != r.URL.Path {
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 && parts[1] == "pipelines" {
		pipelineHandler.ServeHTTP(w, r); return
	}
	// ... 既有 devopsHandler 分发
}
```

`pipelineAuditAdapter` 桥接 `security.AuditStore`（参照 `identityAuditAdapter` / `authAuditAdapter` 模式）。

- [ ] **Step 4: 端到端验证（curl）**

```bash
./bin/core &  # 或 make run
# 1. 模板就位
curl -s -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/pipeline-templates | jq '.data[].name'
# 期望：["开发流水线","发布流水线"]

# 2. 从模板创建 CI pipeline（envId 填本租户 dev 环境）
curl -s -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"templateId":"tpl-ci","name":"shop-ci","trigger":{"type":"manual"}}' \
  http://localhost:8080/api/applications/paas-shop/pipelines

# 3. 手动触发（需 app 绑定 internal CodeRepo + builder 真实/mocker）
curl -s -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"branch":"main"}' \
  http://localhost:8080/api/applications/paas-shop/pipelines/<pid>/run

# 4. 轮询 run（看 stage 推进）
curl -s -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/pipelineruns/<rid> | jq '.data | {status, currentStage, stageRuns:[.stageRuns[]|{name,status,output}]}'
# 期望最终 status=succeeded，stageRuns 全 success，build stage output.imageId 非空

# 5. approve 流程（cd pipeline）：run paused → POST /stages/0/approve → resumed
```

**Plan 1 验证边界**：端到端需 app 已绑定 internal CodeRepo（git push 过代码）+ builder 可用。完整 dogfooding 在 Plan 4；Plan 1 验证用现有 app-cs 或临时建 repo + push（mock builder 也可验证推进链路，build stage 产出 mock imageId）。**最低验证**：用 mock builder 跑通 build→deploy→test(smoke)→baseline 推进 + approve 暂停恢复。

- [ ] **Step 5: 跑全量测试**
Run: `make test`
Expected: 全绿（含 pipeline 包 + 既有测试无回归）。

- [ ] **Step 6: Commit**
```bash
git add cmd/core/persistence.go cmd/core/main.go cmd/core/pipeline_adapters.go
git commit -m "feat(pipeline): cmd/core 装配 + 路由注册 + 端到端手动触发闭环"
```

---

## Self-Review

**1. Spec coverage（对照 spec 各节）**：
- 数据模型 4 实体 + StageDef + Kind/Status/Type/imageSource/TestMode 常量 → Task 2 ✓
- Release.Version + migration → Task 1 ✓
- Pipeline 4 表 migration → Task 3 ✓
- Repository 接口 + memory/pg 双路径 → Task 4/5 ✓
- Stage 类型 build/deploy/test/approve/promote/baseline + 输出链 → Task 8/9/10 ✓
- 执行引擎状态机 + 失败/暂停/取消 → Task 8/9/10/12 ✓
- 触发器 manual（webhook/cron 占位）→ Task 12 manual + Task 2 Trigger 占位；webhook/cron 实现归 Plan 3（spec 明确 Plan 拆分）
- baseline（Version + merge）→ Task 7（gitea merge）+ Task 10（engine baseline）✓
- 权限 pipeline:read/write + prod:write 横切 + 审计 → Task 11/12 ✓
- 预置 ci/cd 模板 + seed → Task 6 ✓
- REST 端点（Pipeline/Run/Template CRUD + run/approve/abort）→ Task 11/12 ✓；webhook 端点归 Plan 3
- cmd/core 装配 + 路由 → Task 13 ✓
- **Plan 1 不含**：前端 UI（Plan 2）、webhook/cron 触发器（Plan 3）、dogfooding 含 buildArgs 透传到 builder（Plan 4）、遗留清理（Plan 5）—— 均在 spec Plan 拆分中明确

**2. Placeholder scan**：
- Task 5 pg store / Task 11 handler 用"参照 X 模式 + 骨架"——这些是"对齐既有模式的样板代码"，给出了一处完整范例 + 字段清单 + 关键差异点，非"TODO/TBD"。实现者需读参照文件（已在 task 内点名路径）。可接受（skill 禁的是无代码的占位，不是禁止引用既有模式）。
- Task 7 Gitea Merge 的精确 API 形态需实现时核对 Gitea 版本——这是真实不确定性（Gitea API 形态），task 内已指明两步 PR-create+merge 路径 + 错误分流，非占位。
- Task 13 桥接 adapter 给了完整签名 + 行为说明，非占位。

**3. Type consistency**：
- `Pipeline` / `PipelineRun` / `StageRun` / `StageDef` 字段名贯穿 Task 2→13 一致（TenantID/AppID/Stages/StageRuns/CurrentStage/Version/Trigger）。
- StageType 常量 StageBuild/Deploy/Test/Approve/Promote/Baseline 一致。
- ImageSource 常量 ImagePriorBuild/Selected/LatestReady 一致（Task 8 resolveImage + Task 2 定义）。
- Output key 常量 OutImageID/OutReleaseID/OutWorkloadDomain/OutVersion/OutMergeSHA 一致（Task 2 定义 + Task 8/9/10 使用）。
- Repository 方法名 ListPipelines/GetPipeline/CreatePipeline/UpdatePipeline/DeletePipeline + ListRuns/GetRun/CreateRun/UpdateRun/HasActiveRun + ListTemplates/GetTemplate/CreateTemplate 一致（Task 4 定义 + Task 11/12 使用）。
- Engine 方法 Start/Advance/Resume/Abort 一致（Task 8/9/12）。
- sentinel 错误：ErrPipelineNotFound/Exists/ErrRunNotFound/ErrActiveRunExists/ErrNotPaused/ErrNotRunning/ErrTemplateExists/ErrNoTenant/ErrInvalidKind/ErrNoStages/ErrInvalidStageType 一致。
- `PipelineRun.RepoID` 字段（Task 8 execBuild 用）—— 已在 Task 2 model.go PipelineRun 补 `RepoID string json:"repoId,omitempty"`（自审修复）。

**修复记录**：Task 2 PipelineRun 已补 RepoID 字段（run 时解析 app 绑定的 internal CodeRepo，build stage 用）。

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-08-pipeline-engine-core.md`. Two execution options:

1. **Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?

---

## 后续 Plan（本 plan 不含，spec 已定拆分）

- **Plan 2**：前端流水线 UI（应用流水线 tab + 设计器 + 运行视图 + DevOps 中心增强）
- **Plan 3**：触发器增强（Gitea webhook + cron scheduler）
- **Plan 4**：paas-shop dogfooding（setup 脚本走 Gitea+Pipeline + build stage buildArgs 透传到 builder）
- **Plan 5**：遗留清理（app-cs/孤儿删除 + 旧脚本清理 + README 更新）

---

---

---
