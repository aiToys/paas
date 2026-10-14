package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------- fake AgentRunner / ChangeRegistrar ----------

// fakeAgentRunner 记录调用供断言；可注入终态与创建失败。
type fakeAgentRunner struct {
	created        []AgentTaskSpec
	cancelled      []string
	phase          string // PollRun 返回的终态
	verdict        string
	branch         string
	message        string
	createErr      error
	firstCreateErr error // 仅首次 CreateRun 返回（already exists 自愈测试用）
	calls          int
	missing        map[string]bool // RunExists 对其中名字返回 false（死引用测试）
}

func (f *fakeAgentRunner) RunExists(_ context.Context, name string) (bool, error) {
	return !f.missing[name], nil
}

func (f *fakeAgentRunner) CreateRun(_ context.Context, spec AgentTaskSpec) (AgentTaskRef, error) {
	f.calls++
	f.created = append(f.created, spec)
	if f.calls == 1 && f.firstCreateErr != nil {
		return AgentTaskRef{}, f.firstCreateErr
	}
	if f.createErr != nil {
		return AgentTaskRef{}, f.createErr
	}
	return AgentTaskRef{Name: spec.Name}, nil
}

func (f *fakeAgentRunner) PollRun(_ context.Context, ref AgentTaskRef) (AgentTaskStatus, error) {
	return AgentTaskStatus{Phase: f.phase, Verdict: f.verdict, Branch: f.branch, Message: f.message}, nil
}

func (f *fakeAgentRunner) CancelRun(_ context.Context, ref AgentTaskRef) error {
	f.cancelled = append(f.cancelled, ref.Name)
	return nil
}

type fakeRegistrar struct {
	gotBranch string
	gotBase   string
	gotTitle  string
	err       error
}

func (f *fakeRegistrar) RegisterChange(_ context.Context, _, branch, baseBranch, title string) error {
	f.gotBranch, f.gotBase, f.gotTitle = branch, baseBranch, title
	return f.err
}

// seedAgentRun 构造单 agent stage 的 run（execStage 直调，聚焦 stage 语义）。
func seedAgentRun(t *testing.T, params map[string]any) (*Engine, *PipelineRun, *StageRun) {
	t.Helper()
	s := NewMemoryStore()
	p, err := s.CreatePipeline(acmeCtxEngine(), Pipeline{
		Name: "agent 流水线", AppID: "app-1", Kind: KindCI, TemplateID: "tpl-test",
	})
	if err != nil {
		t.Fatalf("CreatePipeline 失败: %v", err)
	}
	r, err := s.CreateRun(acmeCtxEngine(), PipelineRun{
		PipelineID: p.ID, AppID: "app-1", Branch: "main", RepoID: "repo-1",
		Trigger: "manual", Status: RunRunning, CurrentStage: 0,
		StageRuns: []StageRun{{Index: 0, Type: StageAgent, Name: "开发", Status: StagePending, Input: params}},
	})
	if err != nil {
		t.Fatalf("CreateRun 失败: %v", err)
	}
	return &Engine{Pipelines: s, Runs: s}, &r, &r.StageRuns[0]
}

func TestExecAgentSuccessWritesOutput(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"request": "实现取消订单", "runtime": "codex"})
	fake := &fakeAgentRunner{phase: "succeeded", verdict: "pass", branch: "auto-feat-abc123"}
	e.Agents = fake

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("期望成功: done=%v err=%v", done, err)
	}
	if got := sr.Output[OutAgentBranch]; got != "auto-feat-abc123" {
		t.Errorf("Output.branch = %v, 期望 auto-feat-abc123", got)
	}
	if got := sr.Output[OutAgentRunName]; got != "run-agent-stage-0" && got != fake.created[0].Name {
		// name 规则见 execAgent（store 生成 run ID + stageIdx）；此处校验非空且与 CreateRun 一致
		t.Errorf("Output.runName = %v, 期望 %s", got, fake.created[0].Name)
	}
	if len(fake.created) != 1 || fake.created[0].Request != "实现取消订单" {
		t.Errorf("CreateRun spec = %+v", fake.created)
	}
	if fake.created[0].BaseBranch != "main" {
		t.Errorf("BaseBranch = %s, 期望 run.Branch=main", fake.created[0].BaseBranch)
	}
	if sr.Status != StageSuccess {
		t.Errorf("stage status = %s, 期望 success", sr.Status)
	}
}

func TestExecAgentFailedVerdictMarksStageFailed(t *testing.T) {
	e, run, sr := seedAgentRun(t, nil)
	e.Agents = &fakeAgentRunner{phase: "failed", verdict: "test_failure", message: "go test 挂 2 项"}

	_, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent}, sr)
	if err == nil {
		t.Fatal("期望失败")
	}
	if sr.Error == "" {
		t.Error("verdict 信息应进 StageRun.Error")
	}
}

func TestExecAgentNotConfigured(t *testing.T) {
	e, run, sr := seedAgentRun(t, nil) // Agents=nil（PAAS_AGENTRUN_URL 空语义）
	_, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent}, sr)
	if err == nil || sr.Error == "" {
		t.Fatal("未配置 AgentRunner 应明确报错（引导检查 PAAS_AGENTRUN_URL）")
	}
}

func TestExecAgentAbortCancelsUpstream(t *testing.T) {
	e, run, sr := seedAgentRun(t, nil)
	fake := &fakeAgentRunner{phase: "running"} // 永远 running → 靠 ctx 打断
	e.Agents = fake

	ctx, cancel := context.WithCancel(acmeCtxEngine())
	cancel() // 立即取消 → execAgent 应 CancelRun 级联并返回错误
	_, err := e.execStage(ctx, run, StageDef{Name: "开发", Type: StageAgent}, sr)
	if err == nil {
		t.Fatal("ctx 取消应返回错误")
	}
	if len(fake.cancelled) != 1 {
		t.Errorf("ctx 取消后必须 CancelRun 级联一次, got %d 次", len(fake.cancelled))
	}
}

func TestExecAgentRegistersChange(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"registerChange": "true", "request": "r"})
	e.Agents = &fakeAgentRunner{phase: "succeeded", verdict: "pass", branch: "auto-x"}
	reg := &fakeRegistrar{}
	e.AgentChanges = reg

	_, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil {
		t.Fatalf("注册失败不应阻断 stage: %v", err)
	}
	if reg.gotBranch != "auto-x" || reg.gotBase != "main" {
		t.Errorf("RegisterChange 参数 = %s/%s, 期望 auto-x/main", reg.gotBranch, reg.gotBase)
	}
	if reg.gotTitle == "" {
		t.Error("Change 标题应含 agent run 名")
	}
}

func TestExecAgentRegisterFailureDoesNotBlock(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"registerChange": "true", "request": "r"})
	e.Agents = &fakeAgentRunner{phase: "succeeded", verdict: "pass", branch: "auto-x"}
	e.AgentChanges = &fakeRegistrar{err: context.DeadlineExceeded}

	_, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil {
		t.Fatalf("Change 注册失败应 best-effort（记 log 不阻断）: %v", err)
	}
	if sr.Status != StageSuccess {
		t.Errorf("stage 应成功, got %s", sr.Status)
	}
}

// ---------- agent 产出分支的消费链（build/baseline） ----------

func TestBuildBranchSourcePriorAgent(t *testing.T) {
	// run 有前序 agent stage 已产出 branch → build 取该分支
	run := PipelineRun{ID: "r", AppID: "a", Branch: "main", CurrentStage: 1, // resolvePriorOutput 从 CurrentStage-1 向前扫描
		StageRuns: []StageRun{
			{Index: 0, Type: StageAgent, Status: StageSuccess, Output: map[string]any{OutAgentBranch: "auto-x"}},
		}}
	got, err := resolveBuildBranch(run, StageDef{Type: StageBuild, Params: map[string]any{"branchSource": "priorAgent"}})
	if err != nil || got != "auto-x" {
		t.Errorf("branchSource=priorAgent 时 branch = %s err=%v, 期望 auto-x", got, err)
	}
	// 无 branchSource 参数：行为不变（branchOverride > run.Branch）
	if got, err := resolveBuildBranch(run, StageDef{Type: StageBuild}); err != nil || got != "main" {
		t.Errorf("缺省 branch = %s err=%v, 期望 main", got, err)
	}
	if got, err := resolveBuildBranch(run, StageDef{Type: StageBuild, Params: map[string]any{"branchOverride": "feat-1"}}); err != nil || got != "feat-1" {
		t.Errorf("branchOverride branch = %s err=%v, 期望 feat-1", got, err)
	}
	// branchSource=priorAgent 但前序无 agent stage：回落 run.Branch（模板复用单独跑 build 场景）
	empty := PipelineRun{ID: "r2", AppID: "a", Branch: "dev"}
	if got, err := resolveBuildBranch(empty, StageDef{Type: StageBuild, Params: map[string]any{"branchSource": "priorAgent"}}); err != nil || got != "dev" {
		t.Errorf("无前序产出时 branch = %s err=%v, 期望回落 dev", got, err)
	}
}

// TestBuildBranchSourcePriorAgentNoOutput 防假绿第二道闸（2026-10-14 全链假绿实测）：
// 前序有 agent stage 但无产出分支（对方平台回显缺口+push 未落）时，build 必须**报错**
// 而非静默回落 main 构建初始模板——曾致 8 段全绿但泳道页面是初始页。
func TestBuildBranchSourcePriorAgentNoOutput(t *testing.T) {
	run := PipelineRun{ID: "r3", AppID: "a", Branch: "main", CurrentStage: 1,
		StageRuns: []StageRun{
			{Index: 0, Type: StageAgent, Status: StageSuccess, Output: map[string]any{OutAgentRunName: "run-x"}}, // 无 branch
		}}
	got, err := resolveBuildBranch(run, StageDef{Type: StageBuild, Params: map[string]any{"branchSource": "priorAgent"}})
	if err == nil {
		t.Fatalf("前序 agent 无产出分支应报错防假绿, got branch=%s", got)
	}
	if !strings.Contains(err.Error(), "拒绝回落") {
		t.Errorf("错误文案应说明拒绝回落: %v", err)
	}
}

func TestBaselineMergeFromPriorAgent(t *testing.T) {
	s := NewMemoryStore()
	p, err := s.CreatePipeline(acmeCtxEngine(), Pipeline{Name: "bp", AppID: "app-1", Kind: KindCD, TemplateID: "tpl-test"})
	if err != nil {
		t.Fatalf("CreatePipeline 失败: %v", err)
	}
	r, err := s.CreateRun(acmeCtxEngine(), PipelineRun{
		PipelineID: p.ID, AppID: "app-1", Branch: "main", Trigger: "manual",
		Status: RunRunning, CurrentStage: 1,
		StageRuns: []StageRun{
			{Index: 0, Type: StageAgent, Status: StageSuccess, Output: map[string]any{OutAgentBranch: "auto-y"}},
			{Index: 1, Type: StageBaseline, Name: "合主干", Status: StagePending,
				Input: map[string]any{"mainBranch": "main", "mergeFrom": "priorAgent"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateRun 失败: %v", err)
	}
	gm := &fakeGiteaMerger{mergeSHA: "sha-1"}
	eng := &Engine{Pipelines: s, Runs: s, Gitea: gm}
	done, err := eng.execStage(acmeCtxEngine(), &r, StageDef{Name: "合主干", Type: StageBaseline, Params: r.StageRuns[1].Input}, &r.StageRuns[1])
	if err != nil || !done {
		t.Fatalf("期望成功: done=%v err=%v", done, err)
	}
	if gm.gotHead != "auto-y" {
		t.Errorf("mergeFrom=priorAgent 时 Merge head = %s, 期望 auto-y（agent 产出分支）", gm.gotHead)
	}
}

func TestRetryAgentWorkspaceContinuity(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"request": "r"})
	fake := &fakeAgentRunner{phase: "failed", verdict: "compile_error"}
	e.Agents = fake
	_, _ = e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if len(fake.created) != 1 {
		t.Fatalf("首次执行应创建 1 个 run, got %d", len(fake.created))
	}
	// 模拟 Retry：清 Status/Error/Output 保留 Input（对齐 engine.Retry 真实语义，engine.go Retry）
	sr.Status, sr.Error, sr.Output = StagePending, "", nil
	fake.phase = "succeeded"
	fake.branch = "auto-fixed"
	_, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
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

func TestExecAgentPollTimeoutCascades(t *testing.T) {
	// 上游永远 Running（agentrun 丢失/僵死）→ 必须有总超时兜底，防 engine goroutine
	// 永挂占死单实例串行槽位（HasActiveRun 409 后续 run 全拒）；超时同样级联取消。
	e, run, sr := seedAgentRun(t, map[string]any{"request": "r", "deadlineSeconds": 1})
	fake := &fakeAgentRunner{phase: "running"}
	e.Agents = fake
	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err == nil || done != true {
		t.Fatalf("期望超时失败: done=%v err=%v", done, err)
	}
	if len(fake.cancelled) != 1 {
		t.Errorf("超时后必须 CancelRun 级联（上游僵死也发 DELETE）, got %d", len(fake.cancelled))
	}
}

// TestExecAgentRetrySuffix 锁 Retry 防撞语义（2026-10-13 实测 already exists）：
// 对方 run 名=K8s CR 名不可复用，Retry（Input 保留 prevRunName/agentAttempt，Output 清空）
// 二次执行时名字加 -rN 后缀 + workspaceFrom 复用上次工作区。
func TestExecAgentRetrySuffix(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{
		"request":      "实现取消订单",
		"prevRunName":  "run-r1stage0-0",
		"agentAttempt": float64(1), // JSON round-trip 后数字为 float64
	})
	fake := &fakeAgentRunner{phase: "succeeded", branch: "auto-feat-abc"}
	e.Agents = fake

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("期望成功: done=%v err=%v", done, err)
	}
	want := run.ID + "-0-r2"
	if fake.created[0].Name != want {
		t.Errorf("CreateRun name = %s, 期望 %s（-r2 防撞）", fake.created[0].Name, want)
	}
	if fake.created[0].WorkspaceFrom != "run-r1stage0-0" {
		t.Errorf("WorkspaceFrom = %q, 期望复用上次工作区 run-r1stage0-0", fake.created[0].WorkspaceFrom)
	}
	// 内存实现回写 int（经 PG JSON round-trip 后为 float64，execAgent 两侧都兼容）
	if v, _ := sr.Input["agentAttempt"].(int); v != 2 {
		t.Errorf("agentAttempt = %v, 期望固化为 2", sr.Input["agentAttempt"])
	}
}

// TestExecAgentRetryLegacyInput 锁存量兜底（2026-10-13 实测二次 already exists）：防撞字段
// 上线前提交过的 stage，Input 只有 prevRunName 没有 agentAttempt——重试仍须从 -r2 起。
func TestExecAgentRetryLegacyInput(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{
		"request":     "实现取消订单",
		"prevRunName": "run-legacy-0-1", // 上次已提交，但无 agentAttempt（存量 Input 形态）
	})
	fake := &fakeAgentRunner{phase: "succeeded", branch: "auto-feat-x"}
	e.Agents = fake

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "审查", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("期望成功: done=%v err=%v", done, err)
	}
	want := run.ID + "-0-r2"
	if fake.created[0].Name != want {
		t.Errorf("CreateRun name = %s, 期望 %s（存量 Input 也须防撞）", fake.created[0].Name, want)
	}
	if fake.created[0].WorkspaceFrom != "run-legacy-0-1" {
		t.Errorf("WorkspaceFrom = %q, 期望 run-legacy-0-1", fake.created[0].WorkspaceFrom)
	}
}

// TestExecAgentAlreadyExistsSelfHeal 锁换名自愈（2026-10-13 run-4c69ae55d9fa 三连撞名）：
// Input 兜底覆盖不到的存量场景拿干净 Input 生成同名，CreateRun 返回 already exists 时
// 自动换 -r2 重试一次，workspaceFrom 指向被撞原名（复用上次工作区）。
func TestExecAgentAlreadyExistsSelfHeal(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"request": "r"}) // 干净 Input（存量形态）
	fake := &fakeAgentRunner{phase: "succeeded", branch: "auto-x", firstCreateErr: errors.New(`agentruns.orchestra.wang.dd "run-x-0" already exists`)}
	e.Agents = fake

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "审查", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("期望自愈后成功: done=%v err=%v", done, err)
	}
	if len(fake.created) != 2 {
		t.Fatalf("CreateRun 调用次数 = %d, 期望 2（撞名重试一次）", len(fake.created))
	}
	if fake.created[0].Name != run.ID+"-0" || fake.created[1].Name != run.ID+"-0-r2" {
		t.Errorf("names = [%s, %s], 期望第二次带 -r2", fake.created[0].Name, fake.created[1].Name)
	}
	if fake.created[1].WorkspaceFrom != run.ID+"-0" {
		t.Errorf("WorkspaceFrom = %q, 期望被撞原名 %s", fake.created[1].WorkspaceFrom, run.ID+"-0")
	}
	if sr.Output[OutAgentRunName] != run.ID+"-0-r2" {
		t.Errorf("Output.runName = %v, 期望 -r2", sr.Output[OutAgentRunName])
	}
}

// fakeBranchChecker 幽灵分支防御测试用。existsAfter=前 N 次查询返回不存在（push 落地竞态模拟）。
type fakeBranchChecker struct {
	exists, errOnCheck bool
	existsAfter        int
	calls              int
}

func (f *fakeBranchChecker) BranchExists(_ context.Context, _, _ string) (bool, error) {
	f.calls++
	if f.errOnCheck {
		return false, errors.New("gitea down")
	}
	if f.existsAfter > 0 {
		return f.calls > f.existsAfter, nil
	}
	return f.exists, nil
}

func shrinkBranchWait() { // 测试压缩 push 落地等待窗口（20s×4 不可进单测）
	branchPushWaitInterval = time.Millisecond
}

// TestExecAgentGhostBranchRejected 锁幽灵分支防御（2026-10-13）：对方 runner「succeeded
// 但未推送」（summary 自述未执行 git 操作）时，paas 边界判负——防下游 review/build 空转。
func TestExecAgentGhostBranchRejected(t *testing.T) {
	shrinkBranchWait()
	e, run, sr := seedAgentRun(t, map[string]any{"request": "r"})
	e.Agents = &fakeAgentRunner{phase: "succeeded", branch: "ghost-branch"}
	e.AgentBranches = &fakeBranchChecker{exists: false}

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err == nil || !done {
		t.Fatalf("幽灵分支应判负: done=%v err=%v", done, err)
	}
	if !strings.Contains(sr.Error, "不存在") {
		t.Errorf("Error = %q, 期望含「不存在」", sr.Error)
	}
	if sr.Status == StageSuccess {
		t.Error("stage 不应标 success")
	}
}

// TestExecAgentBranchLandsAfterWait push 落地竞态容错（2026-10-14 实测误杀）：
// run 转 Succeeded 瞬间 push Pod 才开跑，paas 立即校验时分支尚未落地——
// 前几次查不到、窗口内落地 → 采用该分支，不误杀真实产出。
func TestExecAgentBranchLandsAfterWait(t *testing.T) {
	shrinkBranchWait()
	e, run, sr := seedAgentRun(t, map[string]any{"registerChange": "true", "request": "r"})
	fake := &fakeAgentRunner{phase: "succeeded", branch: ""} // 回显缺失
	e.Agents = fake
	e.AgentBranches = &fakeBranchChecker{existsAfter: 2} // 第 3 次查询才落地

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("窗口内落地的分支应被采用: done=%v err=%v", done, err)
	}
	want := "agentrun/" + fake.created[0].Name
	if sr.Output[OutAgentBranch] != want {
		t.Errorf("Output.branch = %v, 期望 %s", sr.Output[OutAgentBranch], want)
	}
	if !strings.Contains(sr.Log, "push 竞态") {
		t.Errorf("Log 应记录竞态落地: %s", sr.Log)
	}
}

// TestExecAgentBranchCheckErrorTolerant：校验器自身故障放行不阻断（不误杀真实产出）。
func TestExecAgentBranchCheckErrorTolerant(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"request": "r"})
	e.Agents = &fakeAgentRunner{phase: "succeeded", branch: "real-branch"}
	e.AgentBranches = &fakeBranchChecker{errOnCheck: true}

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("校验器故障应放行: done=%v err=%v", done, err)
	}
	if sr.Output[OutAgentBranch] != "real-branch" {
		t.Errorf("Output.branch = %v, 期望 real-branch", sr.Output[OutAgentBranch])
	}
}

// TestExecAgentMissingEchoAdoptsRealBranch 回显缺失容错（2026-10-14 全链假绿实测）：
// 对方平台 succeeded 但 producedBranch 空（operator 升级期注解回显缺口）时，按交付分支
// 命名规则构造 agentrun/<runName> 直查 Gitea 实物——存在则采用，链路不因回显缺口而断。
func TestExecAgentMissingEchoAdoptsRealBranch(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"request": "r"})
	fake := &fakeAgentRunner{phase: "succeeded", branch: ""} // 回显缺失
	e.Agents = fake
	e.AgentBranches = &fakeBranchChecker{exists: true}

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("实物分支存在应采用容错: done=%v err=%v", done, err)
	}
	want := "agentrun/" + fake.created[0].Name
	if sr.Output[OutAgentBranch] != want {
		t.Errorf("Output.branch = %v, 期望按命名规则核实 %s", sr.Output[OutAgentBranch], want)
	}
}

// TestExecAgentNoOutputRegisterChangeFails 主开发段（registerChange）零产出判负：
// 无回显且 Gitea 无实物分支（含 push 落地等待窗口）→ 显性失败，
// 杜绝下游 build 以 main 空转假绿（8 段全绿假象根因）。
func TestExecAgentNoOutputRegisterChangeFails(t *testing.T) {
	shrinkBranchWait()
	e, run, sr := seedAgentRun(t, map[string]any{"registerChange": "true", "request": "r"})
	e.Agents = &fakeAgentRunner{phase: "succeeded", branch: ""}
	e.AgentBranches = &fakeBranchChecker{exists: false}

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err == nil || !done {
		t.Fatalf("零产出应判负: done=%v err=%v", done, err)
	}
	if !strings.Contains(sr.Error, "未产出交付分支") {
		t.Errorf("Error = %q, 期望含「未产出交付分支」", sr.Error)
	}
}

// TestExecAgentDeadWorkspaceFromRef 死引用自愈（2026-10-14 卡死 -r2 实测）：
// Retry 的 prevRunName 指向已被超时取消级联删除的 run → 提交前清出死引用
// （workspaceFrom 空 + attempt 计数清零），按原名+全新工作区重新提交——
// WorkBranch 继承链不受影响，不再让对方 operator 对失效引用无限重试。
func TestExecAgentDeadWorkspaceFromRef(t *testing.T) {
	e, run, sr := seedAgentRun(t, map[string]any{"request": "r"})
	sr.Input["prevRunName"] = "run-dead-ref" // 已被 CancelRun 删除
	sr.Input["agentAttempt"] = float64(3)    // 存量 attempt 计数一并清
	fake := &fakeAgentRunner{phase: "succeeded", verdict: "pass", branch: "auto-x", missing: map[string]bool{"run-dead-ref": true}}
	e.Agents = fake

	done, err := e.execStage(acmeCtxEngine(), run, StageDef{Name: "开发", Type: StageAgent, Params: sr.Input}, sr)
	if err != nil || !done {
		t.Fatalf("死引用应自愈成功: done=%v err=%v", done, err)
	}
	spec := fake.created[0]
	if spec.WorkspaceFrom != "" {
		t.Errorf("WorkspaceFrom = %q, 期望空（死引用已清）", spec.WorkspaceFrom)
	}
	if strings.HasSuffix(spec.Name, "-r4") || strings.HasSuffix(spec.Name, "-r2") {
		t.Errorf("Name = %q, 期望原名（attempt 计数已清，原 CR 已删无撞名）", spec.Name)
	}
	if sr.Input["prevRunName"] != nil && sr.Input["prevRunName"] != "" {
		if _, isDead := fake.missing[sr.Input["prevRunName"].(string)]; isDead {
			t.Errorf("Input.prevRunName 仍指向死引用: %v", sr.Input["prevRunName"])
		}
	}
}
