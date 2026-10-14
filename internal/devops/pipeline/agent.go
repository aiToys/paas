// agent.go AI 研发 stage（agentrun 执行域接入）。
//
// 编排权唯一归 paas（spec 2026-10-11 D2）：agent stage 只调 agentrun 的单次执行
// （POST /api/runs），不使用其 AgentPipeline 编排与审批——多个 agent stage 在 paas
// 侧经 workspaceFrom 串联，等价复刻其 pipeline 能力且无双状态机冲突。
package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AgentRunner 桥接 agentrun 执行域（长任务协议 v2：Create + Poll + 显式 Cancel）。
// agent run 消耗 token，「放弃等待」不够，Abort 必须传播取消（build 的 ctx 放弃语义升级）。
// 实现方持有 PAAS_AGENTRUN_URL/TOKEN，负责 repo 解析与 HTTP（依赖倒置，cmd/core 装配）。
type AgentRunner interface {
	// CreateRun 提交任务，返回执行域句柄（name）。
	CreateRun(ctx context.Context, spec AgentTaskSpec) (AgentTaskRef, error)
	// PollRun 阻塞轮询到终态（对齐 PollBuildRun 模式）；ctx 取消时返回错误。
	PollRun(ctx context.Context, ref AgentTaskRef) (AgentTaskStatus, error)
	// CancelRun 取消执行域任务（Abort 级联）。
	CancelRun(ctx context.Context, ref AgentTaskRef) error
	// RunExists 校验执行域 run 存在性（Retry 续接前校验 workspaceFrom 引用存活，
	// 防指向已删 CR 的死引用——超时取消级联删除后 prevRunName 即失效）。
	RunExists(ctx context.Context, name string) (bool, error)
}

// AgentTaskSpec 提交语义（engine 只传领域语义，repo URL 解析归 adapter）。
type AgentTaskSpec struct {
	AppID           string
	Name            string // <runID>-<stageIdx>，可追溯且防 agentrun 侧撞名
	Request         string // 需求文本
	Runtime         string // codex | claude | test | custom（缺省 codex）
	Stage           string // agentrun 语义 analyze|implement|test|review（缺省 implement）
	BaseBranch      string // 需求基于的分支（= run.Branch）
	WorkBranch      string // 委派模式（契约 v2 §3.3）：已有变更分支，agent 在其上工作并 push 回；空=独立模式
	WorkspaceFrom   string // Retry 续接：上次 run 名（从 sr.Input 取，Input 跨 Retry 保留）
	DeadlineSeconds int
}

// AgentTaskRef 执行域句柄。
type AgentTaskRef struct{ Name string }

// AgentTaskStatus 终态归一（Phase: succeeded|failed 由 adapter 映射 agentrun phase）。
type AgentTaskStatus struct {
	Phase, Verdict, Branch, Message string
	Tokens                          int64
}

// ChangeRegistrar 桥接 change.Service（agent 产出分支注册变更，路 B——批次集成正统路径；
// spec D4：合入归 paas，由 paas 执行器注册而非 agentrun 反调，单向依赖保持）。
type ChangeRegistrar interface {
	RegisterChange(ctx context.Context, appID, branch, baseBranch, title string) error
}

// priorAgentBranch agent stage 的前序产出分支（branchSource=priorAgent 语义）；
// 非 priorAgent 或前序无产出返回空（独立模式）。
func priorAgentBranch(run PipelineRun, stage StageDef) string {
	if strOr(stage.Params, "branchSource", "") != "priorAgent" {
		return ""
	}
	b, err := resolvePriorOutput(run, OutAgentBranch)
	if err != nil {
		return ""
	}
	return b
}

// execAgent AI 研发 stage：CreateRun -> PollRun 终态 -> Output{branch,runName}；
// 成功且 params.registerChange 时注册 Change（best-effort：失败记 log 不阻断，分支仍在可手动补建）。
// ctx 取消（Abort）-> CancelRun 级联，防 agentrun 侧继续消耗 token。
func (e *Engine) execAgent(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	if e.Agents == nil {
		err := fmt.Errorf("agent stage 未配置执行域：请设置 PAAS_AGENTRUN_URL/PAAS_AGENTRUN_TOKEN")
		sr.Error = err.Error()
		return true, err
	}
	name := fmt.Sprintf("%s-%d", run.ID, sr.Index)
	// Retry 续接：Input 跨 Retry 保留（Retry 清 Output 保 Input），上次 run 名在 Input.prevRunName。
	ws := strOr(stage.Params, "workspaceFrom", "")
	if v, ok := sr.Input["prevRunName"].(string); ok && v != "" {
		ws = v
	}
	// 续接引用存活校验（2026-10-14 卡死 -r2 实测）：prevRunName 指向的 run 可能已被超时
	// 取消级联删除（pollAgent CancelRun DELETE 对方 CR）——死引用让对方 operator「引用的
	// AgentRun 不存在」无限重试永不判负，stage 空转至超时。引用不存在时连 attempt 计数一并
	// 清出凭据，按原名+全新工作区重新提交（WorkBranch 继承链不受影响，审查/构建语义不变）。
	if ws != "" {
		if ok, err := e.Agents.RunExists(ctx, ws); err == nil && !ok {
			logf(sr, "workspaceFrom %s 引用的 run 已不存在（超时取消级联删除），改为全新工作区", ws)
			ws = ""
			delete(sr.Input, "prevRunName")
			delete(sr.Input, "agentAttempt")
		}
	}
	// Retry 防撞（2026-10-13 实测 already exists×2）：对方 run 名=K8s CR 名不可复用，重跑
	// 必撞残留 CR。attempt 优先读 agentAttempt 计数（新代码提交成功后固化）；缺失时看
	// prevRunName——有即说明该 stage 曾提交过（含防撞字段上线前的存量 run，Input 里只有
	// prevRunName），重试视为第二次从 -r2 起。
	attempt := 1
	if v, ok := sr.Input["agentAttempt"].(float64); ok && v >= 1 {
		attempt = int(v) + 1
	} else if ws != "" {
		attempt = 2
	}
	if attempt > 1 {
		name = fmt.Sprintf("%s-r%d", name, attempt)
	}
	spec := AgentTaskSpec{
		AppID:      run.AppID,
		Name:       name,
		Request:    strOr(stage.Params, "request", ""),
		Runtime:    strOr(stage.Params, "runtime", "codex"),
		Stage:      strOr(stage.Params, "stage", "implement"),
		BaseBranch: run.Branch,
		// branchSource=priorAgent（多 agent stage 串联，tpl-agent-ship v2）：本 stage 在
		// 前序 agent 产出分支上工作（如 AI 审查待审分支），与 build 同款前序查找。
		// 前序无产出回落独立模式（WorkBranch 空）。
		WorkBranch:      priorAgentBranch(*run, stage),
		WorkspaceFrom:   ws,
		DeadlineSeconds: intOr(stage.Params, "deadlineSeconds", 0),
	}
	if sr.Input == nil {
		sr.Input = map[string]any{}
	}
	ref, err := e.Agents.CreateRun(ctx, spec)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		// 自愈兜底（2026-10-13 run-4c69ae55d9fa 三连 already exists）：Input 兜底覆盖不到的
		// 存量场景（提交后进程滚动、attempt 字段上线前）拿干净 Input 生成同名。already exists
		// 是「该名字确实提交过」的确定性信号——直接换 -r2 重试一次，workspaceFrom 指向被撞的
		// 原名（上次提交即它，复用其工作区，已完成的改动不重做）。
		attempt = 2
		if _, ok := sr.Input["agentAttempt"]; !ok {
			sr.Input["agentAttempt"] = attempt
		}
		if sr.Input["prevRunName"] == nil || sr.Input["prevRunName"] == "" {
			sr.Input["prevRunName"] = name
		}
		spec.WorkspaceFrom = name
		spec.Name = fmt.Sprintf("%s-r2", name)
		logf(sr, "run 名已被占用（上次提交残留），换名重试 %s", spec.Name)
		ref, err = e.Agents.CreateRun(ctx, spec)
	}
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	sr.Input["agentRunName"] = ref.Name // 前端深链观测页
	sr.Input["prevRunName"] = ref.Name  // Retry 续接凭此键（Retry 清 Output 保 Input）
	sr.Input["agentAttempt"] = attempt  // Retry 防撞计数（成功提交后固化，下次 +1 后缀）
	// 打印 spec.WorkspaceFrom 而非局部 ws：already exists 自愈路径会改指被撞原名（上次工作区）
	logf(sr, "已提交 agentrun run=%s（workspaceFrom=%s）", ref.Name, spec.WorkspaceFrom)
	e.flushStageLog(ctx, run, sr) // 提交回执即时可见（agent 轮询可达 30 分钟，此前日志随终态才落库）

	st, err := e.pollAgent(ctx, ref, sr, spec.DeadlineSeconds)
	if err != nil {
		return true, err
	}
	if st.Phase != "succeeded" {
		msg := st.Verdict
		if st.Message != "" {
			if msg != "" {
				msg += ": "
			}
			msg += st.Message
		}
		err := fmt.Errorf("agent run 未通过: %s", msg)
		sr.Error = err.Error()
		return true, err
	}
	sr.Output = map[string]any{OutAgentBranch: st.Branch, OutAgentRunName: ref.Name}
	if st.Tokens > 0 {
		logf(sr, "tokens=%d", st.Tokens)
	}
	logf(sr, "产出分支 %s", st.Branch)
	// 幽灵分支防御：声称产出但 Gitea 无此分支 → 判负（防下游 review/build 在幽灵分支上
	// 空转——审查段曾挂满 15min deadline 且白烧 tokens，2026-10-13 实测对方 runner
	// summary 自述「未执行任何提交、推送」仍标 Succeeded）。
	// 回显缺失容错（2026-10-14 全链假绿实测）：对方平台 succeeded 但 producedBranch 空
	// （升级期间注解回显缺口）时，按其交付分支命名规则构造 agentrun/<runName> 直查 Gitea
	// 实物——存在则采用（链路不因对方回显缺口而断），不存在判负（假绿变显性失败）。
	branch := st.Branch
	if branch == "" {
		// 回显缺失时按已知的预期分支核实实物：委派模式推 workBranch（agent 在其上工作推回），
		// 独立模式推 agentrun/<runName>（对方平台交付分支命名规则）。
		candidate := spec.WorkBranch
		if candidate == "" {
			candidate = "agentrun/" + ref.Name
		}
		if e.AgentBranches != nil {
			if ok, berr := e.branchEventuallyExists(ctx, run, sr, run.AppID, candidate); berr == nil && ok {
				branch = candidate
				sr.Output[OutAgentBranch] = branch
				logf(sr, "回显缺失，按命名规则核实实物分支 %s", branch)
			}
		}
	}
	if e.AgentBranches != nil && branch != "" {
		ok, berr := e.branchEventuallyExists(ctx, run, sr, run.AppID, branch)
		if berr != nil {
			logf(sr, "分支校验失败（放行不阻断）: %v", berr) // 校验器自身故障不应误杀真实产出
		} else if !ok {
			err := fmt.Errorf("agent run 声称产出分支 %s，但仓库中不存在（含 push 落地等待窗口，判负）", branch)
			sr.Error = err.Error()
			return true, err
		}
	} else if branch == "" && strOr(stage.Params, "registerChange", "") == "true" {
		// 主开发段（registerChange）无任何可核实产出 → 显性失败，杜绝下游以 main 空转假绿
		err := fmt.Errorf("agent run 成功但未产出交付分支（对方平台无回显且仓库无 agentrun/%s），判负", ref.Name)
		sr.Error = err.Error()
		return true, err
	}
	// Change 注册（路 B，best-effort 不阻断）
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

// agentPollTimeout agent stage 总超时兜底（对齐 buildBridge 的 pipelineBuildPollTimeout 语义）：
// 上游永 Running（agentrun 丢失/僵死）时防 engine goroutine 永挂占死单实例串行槽位。
// deadlineSeconds 参数大于 0 时以参数 +5min buffer 为准（尊重模板配置），缺省 30min。
const agentPollTimeout = 30 * time.Minute

// pollAgent 轮询到终态；ctx.Done 或总超时时 CancelRun 级联（脱离取消的 ctx 调用，
// 防取消后无法下发 DELETE）并返回错误（Abort 传播）。
func (e *Engine) pollAgent(ctx context.Context, ref AgentTaskRef, sr *StageRun, deadlineSeconds int) (AgentTaskStatus, error) {
	ticker := time.NewTicker(2 * time.Second) // 对齐 PollBuildRun 轮询节奏
	defer ticker.Stop()
	timeout := agentPollTimeout
	if deadlineSeconds > 0 {
		// buffer=5 个轮询周期：agentrun 侧 deadline 到期后 phase 转终态可能有自身 GC 延迟，
		// 留少量余量防误杀；超时兜底是防丢失闸，不是精确闹钟。
		timeout = time.Duration(deadlineSeconds)*time.Second + 10*time.Second
	}
	deadline := time.After(timeout)
	for {
		st, err := e.Agents.PollRun(ctx, ref)
		if err == nil {
			switch st.Phase {
			case "succeeded", "failed":
				return st, nil
			}
		} else if ctx.Err() == nil {
			// 上游真实错误（非取消）：stage 失败（与 buildBridge 同款语义）
			sr.Error = err.Error()
			return st, err
		}
		select {
		case <-ctx.Done():
			if cerr := e.Agents.CancelRun(context.WithoutCancel(ctx), ref); cerr != nil {
				logf(sr, "级联取消失败: %v", cerr)
			}
			sr.Error = "已中止"
			return st, fmt.Errorf("agent run 已中止")
		case <-deadline:
			if cerr := e.Agents.CancelRun(context.WithoutCancel(ctx), ref); cerr != nil {
				logf(sr, "级联取消失败: %v", cerr)
			}
			sr.Error = fmt.Sprintf("agent run 超时（%s 未到终态）", timeout)
			return st, fmt.Errorf("%s", sr.Error)
		case <-ticker.C:
		}
	}
}

// AgentBranchChecker agent 产出分支存在性校验（幽灵分支防御，2026-10-13）：
// 对方 runner 曾多次「succeeded 但未执行 git push」（summary 自述「未执行任何提交、推送」），
// 幽灵分支让下游 review/build 空转（审查段挂满 deadline 15min + 白烧 tokens）。实现方桥接
// Gitea GetBranch（cmd/core 装配），nil 时跳过校验（单测/无 Gitea 场景）。
type AgentBranchChecker interface {
	BranchExists(ctx context.Context, appID, branch string) (bool, error)
}

// branchPushWaitInterval / branchPushWaitAttempts 分支校验的 push 落地等待窗口：
// agentrun run 转 Succeeded 的瞬间其 push Pod 才开始跑（镜像拉取+git clone+commit+push
// 实测可达 4 分钟——run-195d355648ef-0 判负时分支尚未落地，数分钟后 Gitea 实物齐全，
// 连更早被判「改动丢失」的 -r2 分支也最终存在），paas 立即校验会把真实产出误判成幽灵分支。
// 30s×10=最多 5 分钟等待窗口，落地即提前返回；真幽灵分支最多多等一个窗口才判负。
var (
	branchPushWaitInterval = 30 * time.Second
	branchPushWaitAttempts = 10
)

// branchEventuallyExists 分支存在性带等待重试（真幽灵分支最多多等一个窗口才判负，
// 竞态中的真实产出不再被误杀）。校验器自身故障立即上抛（放行语义归调用方）。
func (e *Engine) branchEventuallyExists(ctx context.Context, run *PipelineRun, sr *StageRun, appID, branch string) (bool, error) {
	for i := 0; ; i++ {
		ok, err := e.AgentBranches.BranchExists(ctx, appID, branch)
		if err != nil {
			return false, err
		}
		if ok {
			if i > 0 {
				logf(sr, "分支 %s 经 %d 次等待后落地（push 竞态）", branch, i)
			}
			return true, nil
		}
		if i >= branchPushWaitAttempts-1 {
			return false, nil
		}
		logf(sr, "分支 %s 未找到，等待 push 落地（%d/%d）", branch, i+1, branchPushWaitAttempts-1)
		e.flushStageLog(ctx, run, sr) // 等待进度即时可见（长 stage 日志即时落库语义）
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(branchPushWaitInterval):
		}
	}
}
