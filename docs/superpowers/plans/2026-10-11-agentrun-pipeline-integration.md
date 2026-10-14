# agentrun × paas pipeline 集成实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** agent 作为 pipeline 第 9 种 StageType 接入 agentrun 执行域（编排通道），并新增 2 页只读观测面（观测通道），实现「需求→AI 编码→构建→部署」一条龙流水线。

**Architecture:** 编排权唯一归 paas——agent stage 只调 agentrun `POST /api/runs` 单步执行（Create+Poll+Cancel 协议 v2），产出分支经 `StageRun.Output` 传递给后续 build/baseline；观测走 `/api/agentrun/*` 反向代理（用户 JWT 从 cookie 注入）。领域 run 归 agentrun，paas 只存句柄与 Output。

**Tech Stack:** Go（controller-runtime 仓库既有模式：engine 桥接接口 + cmd/core 装配）、Vue 3 + Element Plus（console-user）、httputil.ReverseProxy。

**Spec:** `docs/superpowers/specs/2026-10-11-agentrun-pipeline-integration-design.md`

## Global Constraints

- 注释语言与代码库一致（中文）；Go 代码过 `golangci-lint run ./...`；全部测试过 `make test`（-race）。
- 外发 HTTP 一律带超时；错误响应遵循 `{"error": "<中文>"}` 契约，底层错误脱敏（禁裸 `err.Error()` 进 5xx）。
- agentrun 凭证：env `PAAS_AGENTRUN_URL`（空=模块禁用，观测 503、agent stage 明确报错）+ `PAAS_AGENTRUN_TOKEN`（静态 token，编排通道）。
- `?ns=` 一律服务端强制 `agent`，剥离客户端传入（ns=租户边界，防越权）。
- 未联调前不得假设 agentrun 响应字段名——`branch` 字段名是契约待对齐项（spec §8.2），client 的响应 struct 集中定义便于联调时一处修正。
- 禁止 git commit（用户显式要求时除外）；本仓库工作树有大量未提交改动，`git add` 只加本任务文件。

## Review Focus

执行时最容易咬人的五类输入（每条已锚定到所属任务的测试）：

1. **ns 越权**：客户端传 `?ns=agent-t-别的租户` 必须被剥离重置为 `agent`——Task 7 步骤 1 测试。
2. **stageParams 越权改 prod**：run 级 stageParams 覆盖 deploy envId 到 prod，必须仍过 prod:write 静态预演（先 apply 再预演）——Task 4 步骤 1 测试。
3. **Abort 后 agent run 继续烧 token**：cancel 传播必须真实调用 `DELETE /api/runs/{name}`——Task 1 步骤 1（fake 断言）+ Task 6 步骤 1（HTTP 层）。
4. **Retry 清 Output 丢 workspaceFrom**：runName 必须写 `sr.Input`（Retry 保留 Input、清 Output，engine.go:252）——Task 3 步骤 1 测试。
5. **未配置 env 的空指针/空列表伪装**：`PAAS_AGENTRUN_URL` 空时代理 503 引导、execAgent 报「未配置」而非 panic——Task 1 步骤 1 + Task 7 步骤 1 测试。

---

### Task 1: AgentRunner 接口 + execAgent 引擎核心

**Files:**
- Create: `internal/devops/pipeline/agent.go`
- Modify: `internal/devops/pipeline/model.go`（StageType 枚举 + Output 键）
- Modify: `internal/devops/pipeline/engine.go`（execStage switch 加 case + Engine 加字段）
- Test: `internal/devops/pipeline/agent_test.go`

**Interfaces:**
- Consumes: `StageDef{Type, Params map[string]any}`、`StageRun{Input/Output/Error/Log}`、`logf(sr, format, args...)`、`resolvePriorOutput(run, key)`——均在 engine.go/model.go。
- Produces（后续任务依赖，签名精确）:
  - `const StageAgent = "agent"`（model.go）
  - `const OutAgentBranch = "branch"`、`OutAgentRunName = "runName"`（model.go Output 键；注意与既有 `OutImageID="imageId"` 同段追加）
  - `type AgentRunner interface { CreateRun(ctx context.Context, spec AgentTaskSpec) (AgentTaskRef, error); PollRun(ctx context.Context, ref AgentTaskRef) (AgentTaskStatus, error); CancelRun(ctx context.Context, ref AgentTaskRef) error }`
  - `type AgentTaskSpec struct { AppID, Name, Request, Runtime, Stage, BaseBranch, WorkspaceFrom string; DeadlineSeconds int }`
  - `type AgentTaskRef struct { Name string }`
  - `type AgentTaskStatus struct { Phase, Verdict, Branch, Message string; Tokens int64 }`（Phase 终态: `succeeded|failed`，adapter 负责归一）
  - `type ChangeRegistrar interface { RegisterChange(ctx context.Context, appID, branch, baseBranch, title string) error }`
  - `Engine.Agents AgentRunner`（nil=未配置）与 `Engine.AgentChanges ChangeRegistrar`（可选）

- [ ] **Step 1: 写失败测试**

```go
// agent_test.go
package pipeline

import (
	"context"
	"errors"
	"testing"
)

// fakeAgentRunner 记录调用供断言；可注入终态与延迟。
type fakeAgentRunner struct {
	created   []AgentTaskSpec
	cancelled []string
	phase     string // PollRun 返回的终态
	verdict   string
	branch    string
	createErr error
}

func (f *fakeAgentRunner) CreateRun(_ context.Context, spec AgentTaskSpec) (AgentTaskRef, error) {
	f.created = append(f.created, spec)
	if f.createErr != nil {
		return AgentTaskRef{}, f.createErr
	}
	return AgentTaskRef{Name: spec.Name}, nil
}
func (f *fakeAgentRunner) PollRun(_ context.Context, ref AgentTaskRef) (AgentTaskStatus, error) {
	return AgentTaskStatus{Phase: f.phase, Verdict: f.verdict, Branch: f.branch}, nil
}
func (f *fakeAgentRunner) CancelRun(_ context.Context, ref AgentTaskRef) error {
	f.cancelled = append(f.cancelled, ref.Name)
	return nil
}

func testRunAgent() (*Engine, *PipelineRun) {
	run := &PipelineRun{ID: "run-1", AppID: "app-1", Branch: "main", Status: RunRunning,
		StageRuns: []StageRun{{Index: 0, Type: StageAgent, Name: "开发", Status: StagePending}}}
	run.CurrentStage = 0
	return &Engine{Runs: newMemRuns(run), Pipelines: newMemPipes()}, run // 复用包内测试 helper；若无则见 Step 3 说明
}

func TestExecAgentSuccessWritesOutput(t *testing.T) {
	e, run := testRunAgent()
	fake := &fakeAgentRunner{phase: "succeeded", verdict: "pass", branch: "auto-feat-abc123"}
	e.Agents = fake
	done, err := e.execStage(context.Background(), run, StageDef{Name: "开发", Type: StageAgent, Params: map[string]any{
		"request": "实现取消订单", "runtime": "codex",
	}}, &run.StageRuns[0])
	if err != nil || !done {
		t.Fatalf("期望成功: done=%v err=%v", done, err)
	}
	if got := run.StageRuns[0].Output[OutAgentBranch]; got != "auto-feat-abc123" {
		t.Errorf("Output.branch = %v, 期望 auto-feat-abc123", got)
	}
	if got := run.StageRuns[0].Output[OutAgentRunName]; got != "run-1-0" { // name 规则 <runID>-<stageIdx>，见 Step 3
		t.Errorf("Output.runName = %v", got)
	}
	if len(fake.created) != 1 || fake.created[0].Request != "实现取消订单" {
		t.Errorf("CreateRun spec = %+v", fake.created)
	}
	if fake.created[0].BaseBranch != "main" {
		t.Errorf("BaseBranch = %s, 期望 run.Branch", fake.created[0].BaseBranch)
	}
}

func TestExecAgentFailedVerdictMarksStageFailed(t *testing.T) {
	e, run := testRunAgent()
	e.Agents = &fakeAgentRunner{phase: "failed", verdict: "test_failure", message: "go test 挂 2 项"}
	_, err := e.execStage(context.Background(), run, StageDef{Name: "开发", Type: StageAgent}, &run.StageRuns[0])
	if err == nil {
		t.Fatal("期望失败")
	}
	if run.StageRuns[0].Error == "" {
		t.Error("verdict 信息应进 StageRun.Error")
	}
}

func TestExecAgentNotConfigured(t *testing.T) {
	e, run := testRunAgent() // Agents=nil（PAAS_AGENTRUN_URL 空语义）
	_, err := e.execStage(context.Background(), run, StageDef{Name: "开发", Type: StageAgent}, &run.StageRuns[0])
	if err == nil || run.StageRuns[0].Error == "" {
		t.Fatal("未配置 AgentRunner 应明确报错（引导检查 PAAS_AGENTRUN_URL）")
	}
}

func TestExecAgentAbortCancelsUpstream(t *testing.T) {
	e, run := testRunAgent()
	fake := &fakeAgentRunner{phase: "running"}
	e.Agents = fake
	// PollRun 返回非终态时应阻塞轮询；此处用 cancel ctx 模拟 Abort 打断
	ctx, cancel := context.WithCancel(context.Background())
	go func() { cancel() }() // 立即取消 → execAgent 应调 CancelRun 并返回
	_, _ = e.execStage(ctx, run, StageDef{Name: "开发", Type: StageAgent}, &run.StageRuns[0])
	if len(fake.cancelled) != 1 {
		t.Errorf("ctx 取消后必须 CancelRun 级联，got %d 次", len(fake.cancelled))
	}
}

func TestExecAgentRegistersChange(t *testing.T) {
	e, run := testRunAgent()
	e.Agents = &fakeAgentRunner{phase: "succeeded", verdict: "pass", branch: "auto-x"}
	reg := &fakeRegistrar{}
	e.AgentChanges = reg
	_, err := e.execStage(context.Background(), run, StageDef{Name: "开发", Type: StageAgent, Params: map[string]any{
		"registerChange": true, "request": "r",
	}}, &run.StageRuns[0])
	if err != nil {
		t.Fatalf("注册失败不应阻断 stage: %v", err)
	}
	if reg.gotBranch != "auto-x" || reg.gotBase != "main" {
		t.Errorf("RegisterChange 参数 = %s/%s", reg.gotBranch, reg.gotBase)
	}
}
```

（`fakeRegistrar` 与内存 store helper 若包内无现成——`store_memory_test.go`/`engine_test.go` 已有内存 repo fixture，复用之；缺什么补什么，模式对齐现有测试。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/pipeline/ -run TestExecAgent -v`
Expected: 编译失败 `undefined: StageAgent / AgentRunner`。

- [ ] **Step 3: 最小实现**

`model.go`——StageType 枚举段追加 + Output 键段追加：

```go
StageAgent    = "agent" // AI 研发（agentrun 执行域）：需求→代码分支，产出经 Output.branch 衔接后续 build/baseline
```

```go
OutAgentBranch = "branch"  // agent stage：产出分支名（build branchSource=priorAgent / Change 注册消费）
OutAgentRunName = "runName" // agent stage：agentrun 侧 run 名（观测深链 / Retry workspaceFrom）
```

`agent.go`（新文件，完整实现）：

```go
package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AgentRunner 桥接 agentrun 执行域（协议 v2：Create+Poll+显式 Cancel——agent run 消耗 token，
// 「放弃等待」不够，Abort 必须传播取消）。实现方持有 PAAS_AGENTRUN_URL/TOKEN，负责 repo 解析与 HTTP。
type AgentRunner interface {
	CreateRun(ctx context.Context, spec AgentTaskSpec) (AgentTaskRef, error)
	PollRun(ctx context.Context, ref AgentTaskRef) (AgentTaskStatus, error) // 阻塞轮询到终态（对齐 PollBuildRun）
	CancelRun(ctx context.Context, ref AgentTaskRef) error
}

// AgentTaskSpec 提交语义（engine 只传领域语义，repo URL 解析归 adapter 依赖倒置）。
type AgentTaskSpec struct {
	AppID           string
	Name            string // <runID>-<stageIdx>，可追溯且防 agentrun 侧撞名
	Request         string // 需求文本
	Runtime         string // codex | claude | test | custom（缺省 codex）
	Stage           string // agentrun 语义 analyze|implement|test|review（缺省 implement）
	BaseBranch      string // 需求基于的分支（= run.Branch）
	WorkspaceFrom   string // Retry 续接：上次 run 名（从 sr.Input 取，Input 跨 Retry 保留）
	DeadlineSeconds int
}

type AgentTaskRef struct{ Name string }

// AgentTaskStatus 终态归一（Phase: succeeded|failed 由 adapter 映射 agentrun phase）。
type AgentTaskStatus struct {
	Phase, Verdict, Branch, Message string
	Tokens                          int64
}

// ChangeRegistrar 桥接 change.Service（agent 产出分支注册变更，路 B——批次集成正统路径）。
type ChangeRegistrar interface {
	RegisterChange(ctx context.Context, appID, branch, baseBranch, title string) error
}

// execAgent AI 研发 stage：CreateRun -> PollRun 终态 -> Output{branch,runName}；
// 成功且 params.registerChange 时注册 Change（best-effort：失败记 log 不阻断，分支仍在可手动补建）。
// ctx 取消（Abort）→ CancelRun 级联，防 agentrun 侧继续消耗 token。
func (e *Engine) execAgent(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	if e.Agents == nil {
		err := fmt.Errorf("agent stage 未配置执行域：请设置 PAAS_AGENTRUN_URL/PAAS_AGENTRUN_TOKEN")
		sr.Error = err.Error()
		return true, err
	}
	str := func(k, def string) string { if v, ok := strOr(stage.Params, k, def).(string); ok { return v }; return def }
	// strOr 已返回 string；直接用 strOr(stage.Params, k, def)
	name := fmt.Sprintf("%s-%d", run.ID, sr.Index)
	// Retry 续接：Input 跨 Retry 保留（Retry 清 Output 保 Input，engine.go Retry），上次 run 名在 Input.prevRunName
	ws := strOr(stage.Params, "workspaceFrom", "")
	if v, ok := sr.Input["prevRunName"].(string); ok && v != "" {
		ws = v
	}
	spec := AgentTaskSpec{
		AppID: run.AppID, Name: name,
		Request: strOr(stage.Params, "request", ""), Runtime: strOr(stage.Params, "runtime", "codex"),
		Stage: strOr(stage.Params, "stage", "implement"), BaseBranch: run.Branch, WorkspaceFrom: ws,
		DeadlineSeconds: intOr(stage.Params, "deadlineSeconds", 0),
	}
	ref, err := e.Agents.CreateRun(ctx, spec)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	if sr.Input == nil {
		sr.Input = map[string]any{}
	}
	sr.Input["agentRunName"] = ref.Name // 前端深链观测页；Retry 续接读 prevRunName（Step 见 Task 3）
	logf(sr, "已提交 agentrun run=%s（workspaceFrom=%s）", ref.Name, ws)

	st, err := e.pollAgent(ctx, ref, sr)
	if err != nil {
		return true, err
	}
	if st.Phase != "succeeded" {
		msg := st.Verdict
		if st.Message != "" { msg = msg + ": " + st.Message }
		err := fmt.Errorf("agent run 未通过: %s", msg)
		sr.Error = err.Error()
		return true, err
	}
	sr.Output = map[string]any{OutAgentBranch: st.Branch, OutAgentRunName: ref.Name}
	if st.Tokens > 0 {
		logf(sr, "tokens=%d", st.Tokens)
	}
	logf(sr, "产出分支 %s", st.Branch)
	// Change 注册（路 B，best-effort）
	if strOr(stage.Params, "registerChange", "") == "true" && e.AgentChanges != nil && st.Branch != "" {
		if rerr := e.AgentChanges.RegisterChange(ctx, run.AppID, st.Branch, run.Branch, "agent: "+ref.Name); rerr != nil {
			logf(sr, "Change 注册失败（不阻断，可手动补建）: %v", rerr)
		} else {
			logf(sr, "已注册 Change（分支 %s）", st.Branch)
		}
	}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// pollAgent 轮询到终态；ctx.Done 时 CancelRun 级联并返回错误（Abort 传播）。
func (e *Engine) pollAgent(ctx context.Context, ref AgentTaskRef, sr *StageRun) (AgentTaskStatus, error) {
	ticker := time.NewTicker(2 * time.Second) // 对齐 PollBuildRun 轮询节奏
	defer ticker.Stop()
	for {
		st, err := e.Agents.PollRun(ctx, ref)
		if err != nil {
			if ctx.Err() != nil {
				if _, cerr := e.Agents.CancelRun(context.WithoutCancel(ctx), ref); cerr != nil {
					logf(sr, "级联取消失败: %v", cerr)
				}
				sr.Error = "已中止"
				return st, fmt.Errorf("agent run 已中止")
			}
			sr.Error = err.Error()
			return st, err
		}
		switch st.Phase {
		case "succeeded", "failed":
			return st, nil
		}
		select {
		case <-ctx.Done():
			if _, cerr := e.Agents.CancelRun(context.WithoutCancel(ctx), ref); cerr != nil {
				logf(sr, "级联取消失败: %v", cerr)
			}
			sr.Error = "已中止"
			return st, fmt.Errorf("agent run 已中止")
		case <-ticker.C:
		}
	}
}
```

`engine.go`——`Engine` struct 的 `EnvType` 字段后追加：

```go
	// Agents 桥接 agentrun 执行域（agent stage）；nil=未配置（execAgent 明确报错，不 panic）。
	Agents AgentRunner
	// AgentChanges 可选：agent 产出分支注册 Change（路 B）；nil 时 registerChange 参数跳过。
	AgentChanges ChangeRegistrar
```

`execStage` switch 追加：

```go
	case StageAgent:
		return e.execAgent(ctx, run, stage, sr)
```

注意：`strOr`/`intOr` 若无 `intOr` 则在 agent.go 补（`func intOr(m map[string]any, k string, def int) int`）。`strings` import 按需删减。

- [ ] **Step 4: 跑测试通过**

Run: `go test ./internal/devops/pipeline/ -run TestExecAgent -v`
Expected: PASS 全绿。修掉 `str` 闭包冗余（示例代码里演示了两種写法，实现时统一用 `strOr`）。

- [ ] **Step 5: 全包回归**

Run: `go test ./internal/devops/pipeline/ && golangci-lint run ./internal/devops/pipeline/`
Expected: PASS / 无新告警。

---

### Task 2: build branchSource + baseline mergeFrom 消费 agent 产出分支

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（execBuild :368 分支来源、execBaseline :761 head 来源）
- Test: `internal/devops/pipeline/agent_test.go`（追加）

**Interfaces:**
- Consumes: Task 1 的 `OutAgentBranch`/`resolvePriorOutput`。
- Produces: stage params 新键——build: `branchSource: "priorAgent"`（缺省原 branchOverride/run.Branch 行为不变）；baseline: `mergeFrom: "priorAgent"`。

- [ ] **Step 1: 写失败测试**

```go
func TestBuildBranchSourcePriorAgent(t *testing.T) {
	// run 有前序 agent stage 已产出 branch → build 取该分支
	run := &PipelineRun{ID: "r", AppID: "a", Branch: "main",
		StageRuns: []StageRun{
			{Index: 0, Type: StageAgent, Status: StageSuccess, Output: map[string]any{OutAgentBranch: "auto-x"}},
		}}
	run.CurrentStage = 1
	got := resolveBuildBranch(*run, StageDef{Type: StageBuild, Params: map[string]any{"branchSource": "priorAgent"}})
	if got != "auto-x" {
		t.Errorf("branch = %s, 期望 auto-x", got)
	}
	// 无 branchSource 参数：行为不变（branchOverride > run.Branch）
	if got := resolveBuildBranch(*run, StageDef{Type: StageBuild}); got != "main" {
		t.Errorf("缺省 branch = %s, 期望 main", got)
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/devops/pipeline/ -run TestBuildBranchSource -v` → `undefined: resolveBuildBranch`。

- [ ] **Step 3: 实现**

engine.go 抽小函数并接入两处：

```go
// resolveBuildBranch build 分支来源：branchSource=priorAgent 取前序 agent 产出分支；
// 缺省/branchOverride 保持原行为（静态覆盖 > run.Branch）。
func resolveBuildBranch(run PipelineRun, stage StageDef) string {
	if strOr(stage.Params, "branchSource", "") == "priorAgent" {
		if b, err := resolvePriorOutput(run, OutAgentBranch); err == nil {
			return b
		}
		// 前序无 agent 产出（如模板复用单独跑 build）：回落 run.Branch
	}
	return strOr(stage.Params, "branchOverride", run.Branch)
}
```

execBuild :368 改 `branch := resolveBuildBranch(*run, stage)`；execBaseline 中 `run.Branch` 作为 head 的位置（:780 附近 `run.Branch == mainBranch` 比较与 Merge 调用）改为：

```go
	head := run.Branch
	if strOr(stage.Params, "mergeFrom", "") == "priorAgent" {
		if b, err := resolvePriorOutput(*run, OutAgentBranch); err == nil {
			head = b
		}
	}
```

（后续 `run.Branch == mainBranch` 判断与 `e.Gitea.Merge(..., head, ...)` 全部用 `head`；baseline 测试补一条 mergeFrom=priorAgent 断言，fake GiteaMerger 已有先例。）

- [ ] **Step 4: 回归**

Run: `go test ./internal/devops/pipeline/ -run 'TestBuildBranch|TestBaseline' -v` → PASS；再全包。

---

### Task 3: Retry 经 Input.prevRunName 续接 workspaceFrom

**Files:**
- Modify: `internal/devops/pipeline/agent.go`（execAgent 提交成功后写 `prevRunName`）
- Test: `internal/devops/pipeline/agent_test.go`（追加）

**Interfaces:**
- Consumes: Task 1 execAgent；Retry 语义（清 Output/Status/Error、保留 Input，engine.go:244-258）。
- Produces: `sr.Input["prevRunName"]`（Retry 后 execAgent 自动作为 WorkspaceFrom）。

- [ ] **Step 1: 写失败测试**

```go
func TestRetryAgentWorkspaceContinuity(t *testing.T) {
	e, run := testRunAgent()
	fake := &fakeAgentRunner{phase: "failed", verdict: "compile_error"}
	e.Agents = fake
	_, _ = e.execStage(context.Background(), run, StageDef{Name: "开发", Type: StageAgent, Params: map[string]any{"request": "r"}}, &run.StageRuns[0])
	// 模拟 Retry：清 Status/Error/Output 保留 Input（对齐 engine.Retry 真实语义）
	sr := &run.StageRuns[0]
	sr.Status, sr.Error, sr.Output = StagePending, "", nil
	fake.phase = "succeeded"
	fake.branch = "auto-fixed"
	_, err := e.execStage(context.Background(), run, StageDef{Name: "开发", Type: StageAgent, Params: map[string]any{"request": "r"}}, sr)
	if err != nil {
		t.Fatal(err)
	}
	if fake.created[1].WorkspaceFrom == "" {
		t.Error("Retry 重跑必须带 WorkspaceFrom（迭代修复而非从头再来）")
	}
	if fake.created[1].WorkspaceFrom != fake.created[0].Name {
		t.Errorf("WorkspaceFrom=%s, 期望上次 run 名 %s", fake.created[1].WorkspaceFrom, fake.created[0].Name)
	}
}
```

- [ ] **Step 2: 确认失败** → Run 同包 `-run TestRetryAgent` → FAIL（WorkspaceFrom 空）。

- [ ] **Step 3: 实现**——execAgent 中 `sr.Input["agentRunName"] = ref.Name` 一行后追加：

```go
	sr.Input["prevRunName"] = ref.Name // Retry 清 Output 保 Input，续接凭此键（防作弊 hash 校验失败从头跑费 token）
```

- [ ] **Step 4: 通过 + 回归** → PASS。

---

### Task 4: 触发 API run 级 stageParams（需求文本载体）

**Files:**
- Modify: `internal/devops/pipeline/handler.go`（triggerRun body + 应用 + 预演顺序）
- Test: `internal/devops/pipeline/handler_test.go`（追加）

**Interfaces:**
- Consumes: `ResolveStages`、`applyOverrides`（resolver.go，key 格式 `"<stageIdx>.<paramKey>"`）、prod:write 静态预演 `allowProdFlow`。
- Produces: 触发端点 body 新增 `stageParams map[string]any`（run 级一次性覆盖，优先于 Pipeline.ParamOverrides；webhook 路径不传）。

- [ ] **Step 1: 写失败测试**

```go
// 两个断言：① stageParams 注入 0.request；② stageParams 把 deploy envId 改到 prod 时仍被 prod:write 预演拦截
func TestTriggerRunStageParams(t *testing.T) { /* httptest + memory store，对齐现有 trigger 测试模式：
   body {"branch":"main","stageParams":{"0.request":"实现XX"}}
   → 断言 run.StageRuns[0].Input["request"]...（实现以 resolved stages 为准，断言 GetRun 后 StageRuns[0].Input 或触发后 fake engine 收到的 resolved）
   再造一个 deploy 到 prod 的 pipeline：body stageParams {"1.envId":"<prodEnvID>"}，developer（无 prod:write）触发 → 403 */ }
```

（测试骨架对齐 handler_test.go 现有 triggerRun 用例的 fixture 搭法；关键断言两处如注释。）

- [ ] **Step 2: 确认失败** → 编译失败或字段被忽略。

- [ ] **Step 3: 实现**——triggerRun body struct 追加 `StageParams map[string]any \`json:"stageParams"\``；`ResolveStages(...)` 之后、`allowProdFlow` 预演与 `triggerRunInternal` 之前插入：

```go
	// run 级一次性 stage 参数（需求文本载体）：后于 ParamOverrides 应用（run 级优先），
	// 先于 prod 预演（防经 stageParams 把 deploy 改指 prod 绕过静态预演——权限审计同款防线）。
	if len(body.StageParams) > 0 {
		applyOverridesToResolved(resolved, body.StageParams) // 新 helper：for i := range resolved { applyOverrides(resolved[i].Params, body.StageParams, i) }
	}
```

`applyOverridesToResolved` 放 resolver.go（复用 applyOverrides 单 stage 逻辑；注意 applyOverridesToStages 是模板→实例的整段版本，勿混用）。webhook 路径（webhookTrigger）不接此参数。

- [ ] **Step 4: 通过 + 全 handler 回归**。

---

### Task 5: builtin 模板 tpl-agent-ship / tpl-agent-review

**Files:**
- Modify: `internal/devops/pipeline/templates.go`（+ model.go 若 Kind 枚举需扩展）
- Test: `internal/devops/pipeline/templates_test.go`（追加）

**Interfaces:**
- Consumes: `BuiltinTemplates()`、`Version` 比对 seed（ReplaceBuiltinTemplate，改 builtin 只需 Version+1）。
- Produces: `KindAgent = "agent"`（model.go Kind 枚举，若现有只有 KindCI/KindCD）；模板 ID `tpl-agent-ship`/`tpl-agent-review`。

- [ ] **Step 1: 写失败测试**——断言两模板存在、stages 序符合 spec §4.4：

```go
func TestBuiltinAgentTemplates(t *testing.T) {
	tpls := BuiltinTemplates()
	var ship *PipelineTemplate
	for i := range tpls {
		if tpls[i].ID == "tpl-agent-ship" { ship = &tpls[i] }
	}
	if ship == nil { t.Fatal("缺 tpl-agent-ship") }
	if ship.Stages[0].Type != StageAgent { t.Errorf("首 stage 应为 agent") }
	// ship: agent(develop,registerChange) → approve → build(branchSource=priorAgent) → deploy(测试泳道) → test(smoke)
	// review: agent(review, stage=review) 单 stage
	// 断言 build stage Params["branchSource"]=="priorAgent"；deploy 的 envId=="{{app.env.test}}"、lane=="{{run.branch}}"
}
```

- [ ] **Step 2: 确认失败**。

- [ ] **Step 3: 实现**——templates.go `BuiltinTemplates()` 追加两模板（Version: 1）：

```go
	{
		ID: "tpl-agent-ship", Name: "AI 研发交付流水线", Kind: KindAgent, Builtin: true, Version: 1,
		Description: "需求→AI编码→人工审批→构建→测试泳道部署→冒烟（产出分支自动注册 Change）",
		Stages: []StageDef{
			{Name: "AI 开发", Type: StageAgent, Params: map[string]any{
				"request": "在此填写需求", "runtime": "codex", "registerChange": "true", "deadlineSeconds": 1800,
			}},
			{Name: "合入审批", Type: StageApprove, Params: map[string]any{"message": "确认 AI 产出可进入构建"}},
			{Name: "构建", Type: StageBuild, Params: map[string]any{"branchSource": "priorAgent"}},
			{Name: "部署到测试泳道", Type: StageDeploy, Params: map[string]any{
				"envId": "{{app.env.test}}", "lane": "{{run.branch}}", "imageSource": ImagePriorBuild, "strategy": "rolling",
			}},
			{Name: "冒烟联调", Type: StageTest, Params: map[string]any{"mode": TestSmoke, "path": "/healthz"}},
		},
	},
	{
		ID: "tpl-agent-review", Name: "AI 代码审查", Kind: KindAgent, Builtin: true, Version: 1,
		Description: "对当前分支发起 AI 审查（产出结论与建议，不部署）",
		Stages: []StageDef{
			{Name: "AI 审查", Type: StageAgent, Params: map[string]any{
				"stage": "review", "runtime": "codex", "request": "审查当前分支变更", "deadlineSeconds": 900,
			}},
		},
	},
```

model.go Kind 枚举补 `KindAgent = "agent"`（对齐现有 KindCI/KindCD 声明处）。

- [ ] **Step 4: 通过 + 回归**（seed 幂等测试若断言模板数需同步计数）。

---

### Task 6: internal/agentrun 编排通道 client

**Files:**
- Create: `internal/agentrun/client.go`
- Test: `internal/agentrun/client_test.go`

**Interfaces:**
- Consumes: Task 1 的 `AgentTaskSpec/Ref/Status`（pipeline 包导出类型——client 返回/接收它们，产生 pipeline→agentrun 单向依赖；**勿反向 import**：agentrun 包 import pipeline 包获取类型，cmd/core 装配把 client 注入 Engine.Agents）。
- Produces:

```go
package agentrun

// RepoResolver app→git 仓库 URL（cmd/core 桥接 giteaBridge：owner/repo + gitea base URL 组装 .git 地址）。
type RepoResolver interface { GitURL(ctx context.Context, appID string) (string, error) }

func NewClient(baseURL, token string, repos RepoResolver) *Client
func (c *Client) CreateRun(ctx context.Context, spec pipeline.AgentTaskSpec) (pipeline.AgentTaskRef, error)
func (c *Client) PollRun(ctx context.Context, ref pipeline.AgentTaskRef) (pipeline.AgentTaskStatus, error)
func (c *Client) CancelRun(ctx context.Context, ref pipeline.AgentTaskRef) error
```

- [ ] **Step 1: 写失败测试**（httptest 假 agentrun）——断言：

```go
// ① POST /api/runs?ns=agent：body 含 request/runtime/stage/project{git,ref}、workspaceFrom、deadlineSeconds；
//    Authorization: Bearer <静态token>；白名单外字段不发送
// ② GET /api/runs/{name}?ns=agent 轮询：phase=Pending/Running → 继续轮；Succeeded+branch → 归一 Phase=succeeded
// ③ DELETE /api/runs/{name}?ns=agent 调用路径与 token
// ④ 非 2xx {"error":"..."} → 错误透传中文文案；连接拒绝 → 脱敏为「Agent 编排平台不可达」
// ⑤ baseURL 空串 → NewClient 返回 nil 或 CreateRun 返回「未配置」（对齐 Engine.Agents=nil 语义，装配层约定）
```

- [ ] **Step 2: 确认失败**（编译错）。

- [ ] **Step 3: 实现**——契约 §4.1 body 组装（白名单：name/runtime/mode/stage/request/priority/retries/deadlineSeconds/workspaceFrom/agentRef/project）；mode 固定 `real`；响应 struct 集中一处：

```go
// runResp agentrun GET /api/runs/{name} 响应（契约待对齐项：产出分支字段名 branch，
// 联调若有出入只改此 struct 的 json tag——集中定义防散落）。
type runResp struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"`   // Pending|Running|Succeeded|Failed（归一到小写比较）
	Verdict string `json:"verdict"`
	Branch  string `json:"branch"`
	Tokens  int64  `json:"tokens"`
	Message string `json:"message,omitempty"`
}
```

HTTP client 用 `httputil.NewClient(30 * time.Second)` 同款超时 Transport（不自建）；PollRun 内 2s 间隔轮询到终态或 ctx.Done（返回 error，Cancel 归 engine 的 pollAgent 管——PollRun 只负责等待）。

- [ ] **Step 4: 通过 + lint**。

---

### Task 7: internal/agentrun 观测代理 + 权限 + 装配

**Files:**
- Create: `internal/agentrun/proxy.go`
- Modify: `internal/core/identity/model.go`（BuiltinRoles）
- Modify: `cmd/core/main.go`（装配：client 注入 Engine、proxy 注册、ChangeRegistrar 桥接、审计）
- Test: `internal/agentrun/proxy_test.go`

**Interfaces:**
- Consumes: `auth.AccessCookieName`（cookie 值即 access JWT）、`gateway.RequestAllowed`、`apiroute` Operation 登记、`change.Service.CreateChangeWithBranch(ChangeInput{Title,Type,Branch,BaseBranch,CreateBranch:false})`。
- Produces: `func NewProxy(baseURL string, authorize func(r *http.Request, perm string) bool, audit func(r *http.Request, action string)) http.Handler`；路由 `/api/agentrun/*`。

- [ ] **Step 1: 写失败测试**（httptest 假上游 + proxy 实例）——五个断言对应 Review Focus 1/5：

```go
// ① 上游收到 ?ns=agent：客户端请求带 ?ns=agent-t-x 或不带 → 上游 Query.Get("ns")=="agent"
// ② 上游收到 Authorization: Bearer <paas_access cookie 值>（请求带 cookie）；无 cookie 且无 Bearer → 401 {"error"}
//    带 Bearer JWT（含两个 '.'）→ 原样透传；Bearer sk-xxx（API Key）→ 401 明确文案「需要登录会话」
// ③ baseURL 空 → 503 {"error":"Agent 编排未配置"}；上游连接拒绝 → 502 脱敏
// ④ GET 走 read 权限、POST/DELETE 走 write 权限（authorize 记录请求的 perm 断言）
// ⑤ 路径清理：/api/agentrun/runs/x → 上游 /api/runs/x（strip 前缀，防 path traversal 逃逸 —— director 只做前缀剥离 + RawQuery 重建）
```

- [ ] **Step 2: 确认失败**。

- [ ] **Step 3: 实现**——`httputil.ReverseProxy{Director}`：

```go
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.baseURL == "" { httputil.WriteError(w, http.StatusServiceUnavailable, "Agent 编排未配置（PAAS_AGENTRUN_URL）"); return }
	perm := PermRead; if r.Method != http.MethodGet && r.Method != http.MethodHead { perm = PermWrite }
	if p.authorize != nil && !p.authorize(r, perm) { httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm); return }
	token := bearerFrom(r) // Authorization 含两个 '.' 的 JWT 原样；否则空
	if token == "" { if c, err := r.Cookie(auth.AccessCookieName); err == nil && strings.Contains(c.Value, ".") { token = c.Value } }
	if token == "" { httputil.WriteError(w, http.StatusUnauthorized, "Agent 编排需要登录会话（API Key 不支持）"); return }
	// ns 服务端强制：剥离客户端传入（ns=租户边界）
	q := r.URL.Query(); q.Del("ns"); q.Set("ns", "agent")
	target := *p.target // url.URL 预解析
	target.Path = "/api" + strings.TrimPrefix(r.URL.Path, "/api/agentrun")
	target.RawQuery = q.Encode()
	if p.audit != nil && perm == PermWrite { p.audit(r, r.Method+" "+target.Path) }
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Del("Cookie") // 凭证最小化：不外发平台会话 cookie
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(...))) // rp.ReverseProxy ErrorHandler → 502 脱敏
}
```

（`PermRead="agentrun:read"`/`PermWrite="agentrun:write"` 包内常量；ErrorHandler 写 `{"error":"Agent 编排平台不可达"}`。）

identity/model.go：developer 列表追加 `"agentrun:read", "agentrun:write"`；viewer 追加 `"agentrun:read"`。

cmd/core/main.go 装配（pipeline engine 构建段 :669 附近）：

```go
	// agentrun 编排通道（静态 token）+ 观测代理（用户 JWT）——env 空=双通道禁用。
	agentrunURL := os.Getenv("PAAS_AGENTRUN_URL")
	var agentRunner pipeline.AgentRunner
	if agentrunURL != "" {
		agentRunner = agentrun.NewClient(agentrunURL, os.Getenv("PAAS_AGENTRUN_TOKEN"), &agentrunRepoBridge{gitea: giteaBridgeInst, giteaURL: os.Getenv("PAAS_GITEA_URL")})
	}
	pipeEngine.Agents = agentRunner
	pipeEngine.AgentChanges = &changeRegistrarBridge{svc: changeSvc} // 桥接 change.Service.CreateChangeWithBranch（Title="agent: "+runName, Type 对齐 change 模块合法枚举，实现时以 change/model.go 为准）
```

`agentrunRepoBridge.GitURL`：经 `giteaBridge.ResolveRepo(appID)` 得 owner/repo，与 Gitea base URL 组装 `http://.../owner/repo.git`。代理注册（mux 段，对齐现有 auth() 包装）：

```go
	if agentrunURL != "" {
		proxy := agentrun.NewProxy(agentrunURL,
			func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) },
			func(r *http.Request, action string) { auditAdapter.Record(r, "agentrun", "proxy", "", action) }) // 对齐 identityAuditAdapter 模式
		mux.Handle("/api/agentrun/", auth(proxy))
		reg.Operation("GET", "/api/agentrun/runs", apiroute.Tags("AgentRun"), apiroute.Summary("Agent 运行列表（代理 agentrun）"), apiroute.Perm("agentrun:read"), apiroute.WithResp(nil))
		// 同款登记 POST/DELETE runs（WithResp(nil) 透传契约）
	}
```

（审计 adapter 名以 main.go 现有 identityAuditAdapter 实际形态为准，对齐即接。）

- [ ] **Step 4: 通过 + `go build ./cmd/core` + make test**。

---

### Task 8: 前端——api 封装 + 2 观测页 + 路由导航 + 触发表单需求文本

**Files:**
- Create: `frontend/console-user/src/api/agentrun.ts`
- Create: `frontend/console-user/src/views/AgentRuns.vue`、`frontend/console-user/src/views/AgentRunDetail.vue`
- Modify: `frontend/console-user/src/router.ts`、`frontend/console-user/src/App.vue`（nav）
- Modify: DevOps 触发处（`DevOps.vue` 内触发 run 的表单/对话框——实现时以 `grep -n "触发" DevOps.vue` 定位）

**Interfaces:**
- Consumes: `fetchAuth`/`fetchJSON`（src/api.ts；fetchJSON 智能解包兼容 agentrun 裸 JSON）；Element Plus 表格模式（对齐 `Pulls.vue` 列表 / `WorkflowRunView.vue` 详情）。
- Produces: 路由 `/devops/agent-runs`、`/devops/agent-runs/:name`。

- [ ] **Step 1: api/agentrun.ts**（类型手写——本模块尚无 types.gen 迁移，对齐 workflow.ts 手写 interface 现状）：

```ts
// agentrun 观测 API（经 /api/agentrun 代理，用户 JWT 由 cookie 自动携带）。
import { fetchJSON } from '@/api'

export interface AgentRunItem {
  name: string; runtime: string; mode: string; stage: string
  phase: string; verdict: string; priority: number; tokens: number
  createdAt?: string; finishedAt?: string; branch?: string; message?: string
}
export const listRuns = () => fetchJSON<AgentRunItem[]>('/api/agentrun/runs')
export const getRun = (name: string) => fetchJSON<AgentRunItem>(`/api/agentrun/runs/${encodeURIComponent(name)}`)
export const cancelRun = (name: string) => fetchJSON<void>(`/api/agentrun/runs/${encodeURIComponent(name)}`, { method: 'DELETE' })
export const listArtifacts = (name: string) => fetchJSON<{key: string; size?: number}[]>(`/api/agentrun/runs/${encodeURIComponent(name)}/artifacts`)
export const getArtifact = (name: string, key: string) => fetchJSON<string>(`/api/agentrun/runs/${encodeURIComponent(name)}/artifacts/${encodeURIComponent(key)}`)
```

- [ ] **Step 2: AgentRuns.vue**——列表（列：名称/阶段/模式/状态 phase tag/判定 verdict/tokens/创建时间），行点击 `router.push` 详情；10s 轮询 + `onUnmounted` 清理（对齐现有轮询页模式）；**404/503 时 catch 显示引导态卡片**（「Agent 编排未配置或不可达」+ PAAS_AGENTRUN_URL 提示）。
- [ ] **Step 3: AgentRunDetail.vue**——状态 KV descriptions + message/结论 + 产物列表（点开 dialog 显示内容文本，diff 类内容 `<pre>` 展示）+ 取消按钮（非终态显示，confirm 后 cancelRun）。
- [ ] **Step 4: router.ts** 两路由（`meta` 对齐现有 requiresAuth 写法）+ App.vue nav DevOps 分组加「Agent 运行」项（对齐现有 nav 数组结构，group 归 DevOps 同组）。
- [ ] **Step 5: DevOps 触发表单加「需求文本」**——触发 agent 模板 pipeline 时的可选 textarea，非空则 body 带 `stageParams: {"0.request": 文本}`（stage 索引取模板首个 agent stage；实现时按触发表单现有字段拼接方式扩展）。
- [ ] **Step 6: 构建验证**——`cd frontend && pnpm build`（三套构建过；ESLint 无 `as any`）。

---

### Task 9: 部署配套 + 全量回归 + e2e

**Files:**
- Modify: `deploy/charts/paas/templates/core-deployment.yaml`（env 段，对齐 PAAS_GITEA_URL :117 模式）
- Modify: `deploy/charts/paas/templates/secret.yaml` + `values.yaml`（PAAS_AGENTRUN_TOKEN，对齐 PAAS_AIROUTER_API_KEY :29 模式）
- Modify: `CLAUDE.md`（模块地图加摘要 + spec/plan 链接，按仓库约定「新增内容写摘要+链接」）

- [ ] **Step 1: chart env**——core-deployment.yaml 追加：

```yaml
            # Agent 编排执行域（agentrun）。空=agent stage 与 /api/agentrun 观测代理整体禁用。
            - name: PAAS_AGENTRUN_URL
              value: {{ .Values.agentrun.url | quote }}
```

secret.yaml 追加 `PAAS_AGENTRUN_TOKEN: {{ .Values.agentrun.token | quote }}` + core-deployment 对应 valueFrom secretKeyRef（对齐 PAAS_AIROUTER_API_KEY 的两段式）；values.yaml 加 `agentrun: {url: "", token: ""}` 默认空。

- [ ] **Step 2: 全量回归**——`make test && make lint`；`cd frontend && pnpm build`。
- [ ] **Step 3: 部署 dev 集群**——`./scripts/deploy-k8s.sh`（常驻授权）；core env 配 `PAAS_AGENTRUN_URL=http://agent-console.agent.svc` + token（经 helm --set 或 values overlay）。
- [ ] **Step 4: e2e 按 spec §9**——无 agentrun 联调环境下先验证：模块禁用态（503 引导、agent stage 报错文案）、其余功能零回归、观测页路由可达；**联调项**（demo run 全链、Abort 级联、Retry workspaceFrom、Change 发车）待契约修订对齐（产出分支字段名）后在 agentrun 可达环境执行，逐项勾 spec §9 清单。
- [ ] **Step 5: CLAUDE.md 模块地图**——DevOps CI/CD 节追加两行摘要：agent stage（协议 v2/编排通道）+ /api/agentrun 观测代理（双通道），链接本 spec 与 plan。

---

## Self-Review 记录

1. **Spec 覆盖**：§3 九步流程→Task 1-5（②③⑤⑥⑦⑧ 步）+ Task 4（①）+ Task 8（⑨）；§4.1 协议→Task 1/6；§4.2 清单→Task 1-7；§4.3 前端→Task 8；§4.4 模板→Task 5；§4.5 部署→Task 9；§8 契约修订→不在本计划（文档级，联调时同步 agentrun 侧）；§9 验收→Task 9 Step 4。无缺口。
2. **占位符**：Task 4/7/8 的测试骨架给出断言要点而非全量代码（fixture 搭法指明对齐现有用例）——属可执行指引，非 TBD；其余任务代码完整。
3. **类型一致**：`AgentTaskSpec/Ref/Status`、`OutAgentBranch="branch"`、`resolveBuildBranch`、`prevRunName`、Perm 常量跨任务核对一致；Task 6 client 消费 pipeline 包导出类型（单向依赖方向已注明）。
4. **Review Focus**：五条均已锚定测试（Task 7 S1①⑤ / Task 4 S1② / Task 1 S1+Task 6 S1③ / Task 3 S1④ / Task 1 S1+Task 7 S1⑤）。
