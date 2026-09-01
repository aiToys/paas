package workflow

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// AgentRunner LLM 节点执行器（依赖倒置，cmd/core 桥接 agent.Runtime）。
type AgentRunner interface {
	// RunNode 执行一轮 Agent 推理，返回最终文本（不含推理过程）。
	RunNode(ctx context.Context, agentID, prompt string) (string, error)
}

// ToolRunner tool 节点执行器（依赖倒置，cmd/core 桥接 mcp client）。
type ToolRunner interface {
	// Invoke 调用工具，返回 JSON 结果字符串。
	Invoke(ctx context.Context, toolID, toolName string, args map[string]string) (string, error)
}

// 计划执行的超时与推进上限。
const (
	nodeTimeout = 120 * time.Second // 单节点执行上限（LLM/工具）
	maxSteps    = 100               // 推进步数上限（condition 环定义防死循环，节点数≤50 之外再兜底）
	resumePoll  = 1 * time.Second   // paused 等待的 DB 轮询间隔（Approve/Abort 落库为唯一真源）
)

// Engine 工作流执行引擎：goroutine 推进 + approve 暂停恢复。
// 恢复信号以 DB 状态为唯一真源（paused→running 由 Approve 落库，引擎轮询感知）——
// 跨副本/进程重启天然安全（重启后 Approve 检测无本进程等待者时直接拉起续跑）。
type Engine struct {
	repo   Repository
	agents AgentRunner
	tools  ToolRunner

	mu      sync.Mutex
	waiting map[string]bool // runID -> 本进程是否有 advance goroutine 在等（防 Approve 双重拉起）
}

func NewEngine(repo Repository, agents AgentRunner, tools ToolRunner) *Engine {
	return &Engine{repo: repo, agents: agents, tools: tools, waiting: make(map[string]bool)}
}

// Start 创建运行记录并异步推进。定义未启用返错（不静默）。
func (e *Engine) Start(ctx context.Context, def WorkflowDef, inputs map[string]string) (WorkflowRun, error) {
	if !def.Enabled {
		return WorkflowRun{}, fmt.Errorf("工作流未启用")
	}
	tid, _ := tenant.TenantFrom(ctx)
	run, err := e.repo.CreateRun(ctx, WorkflowRun{
		TenantID:   tid,
		WorkflowID: def.ID,
		Status:     StatusRunning,
		Inputs:     inputs,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		return WorkflowRun{}, err
	}
	go e.advance(runTenant(ctx, tid), def, run)
	return run, nil
}

func runTenant(ctx context.Context, tid string) context.Context {
	return tenant.WithTenant(context.WithoutCancel(ctx), tid)
}

// Sweep 启动恢复（devops SweepInterrupted 同款）：进程重启后孤儿 run 处置。
// running 孤儿（推进 goroutine 已消失）→ failed「进程重启中断」；
// paused 保留——Approve 时经 waiting 检测无本进程等待者会拉起续跑。
func (e *Engine) Sweep(ctx context.Context) {
	runs, err := e.repo.ListActiveRuns(ctx)
	if err != nil {
		log.Printf("[workflow] Sweep 查询活动运行失败: %v", err)
		return
	}
	for _, run := range runs {
		if run.Status != StatusRunning {
			continue
		}
		run.Status = StatusFailed
		run.FinishedAt = time.Now()
		if n := len(run.NodeRuns); n > 0 && run.NodeRuns[n-1].Status == StatusRunning {
			run.NodeRuns[n-1].Status = StatusFailed
			run.NodeRuns[n-1].FinishedAt = run.FinishedAt
			run.NodeRuns[n-1].Error = "进程重启中断"
		}
		if _, err := e.repo.UpdateRun(tenant.WithTenant(ctx, run.TenantID), run); err != nil {
			log.Printf("[workflow] Sweep 处置运行 %s 失败: %v", run.ID, err)
		}
	}
}

// advance 推进到暂停/终态。def 快照随 run 传递（定义后续修改不影响在跑的 run，
// 与 pipeline StageRun.Input 固化解耦同款取舍）。
func (e *Engine) advance(ctx context.Context, def WorkflowDef, run WorkflowRun) {
	outputs := map[string]string{} // nodeID -> output（变量传递真源）
	for i := range run.NodeRuns {
		outputs[run.NodeRuns[i].NodeID] = run.NodeRuns[i].Output
	}
	// 续跑入口（Approve 拉起的重启/跨副本 run）：从最后节点推进；新 run 从 start 进。
	cur, err := e.resumeCursor(def, run, outputs)
	if err != nil {
		e.fail(ctx, &run, err.Error())
		return
	}
	for step := 0; step < maxSteps; step++ {
		if cur.ID == "" {
			e.fail(ctx, &run, "定义缺 start 节点")
			return
		}
		if cur.Type == NodeEnd {
			// end 也记录 NodeRun（审计轨迹完整：每次到达终点的路径可追溯）
			run.NodeRuns = append(run.NodeRuns, NodeRun{NodeID: cur.ID, Status: StatusSucceeded,
				StartedAt: time.Now(), FinishedAt: time.Now()})
			run.Status = StatusSucceeded
			run.FinishedAt = time.Now()
			e.save(ctx, run)
			return
		}
		nr := NodeRun{NodeID: cur.ID, Status: StatusRunning, StartedAt: time.Now()}
		run.NodeRuns = append(run.NodeRuns, nr)
		idx := len(run.NodeRuns) - 1

		output, paused, err := e.execNode(ctx, cur, run.Inputs, outputs)
		run.NodeRuns[idx].FinishedAt = time.Now()
		outputs[cur.ID] = output // 统一回写（outputs + NodeRun.Output 单一真源，防双写覆盖）
		run.NodeRuns[idx].Output = output
		if err != nil {
			run.NodeRuns[idx].Status = StatusFailed
			run.NodeRuns[idx].Error = err.Error()
			run.Status = StatusFailed
			run.FinishedAt = time.Now()
			e.save(ctx, run)
			return
		}
		// 节点执行期间状态可能已被外部改变（Abort 落库 aborted / Sweep 清扫 failed）——
		// 重读校验，任何非 running 状态都不再推进覆盖（残余窗口仅 GetRun 与 save 之间，毫秒级，接受）
		if fresh, gerr := e.repo.GetRun(ctx, run.ID); gerr == nil && fresh.Status != StatusRunning {
			if fresh.Status == StatusAborted || fresh.Status == StatusFailed {
				return
			}
		}
		if paused {
			run.NodeRuns[idx].Status = StatusPaused
			run.Status = StatusPaused
			e.save(ctx, run)
			if !e.waitResume(ctx, &run) {
				return // aborted 或 run 已被删除（定义级联）：终态由 Abort/删除方落库，不覆盖
			}
			// Approve 已把该节点标 succeeded + status=running，继续推进
			run.Status = StatusRunning
		} else {
			run.NodeRuns[idx].Status = StatusSucceeded
			e.save(ctx, run)
		}
		nextID, err := nextIDOf(def, cur, run.Inputs, outputs)
		if err != nil {
			e.fail(ctx, &run, err.Error())
			return
		}
		nxt, ok := def.node(nextID)
		if !ok {
			e.fail(ctx, &run, fmt.Sprintf("节点 %s 指向不存在的 %s", cur.ID, nextID))
			return
		}
		cur = nxt
	}
	e.fail(ctx, &run, fmt.Sprintf("超过最大步数 %d（疑似环路定义）", maxSteps))
}

// resumeCursor 计算推进起点：无执行记录 → start；有记录 → 最后节点的后继
// （condition 断点续跑时无法从记录恢复命中支，按已重建的 inputs/outputs 重新求值——纯函数幂等）。
func (e *Engine) resumeCursor(def WorkflowDef, run WorkflowRun, outputs map[string]string) (NodeDef, error) {
	if len(run.NodeRuns) == 0 {
		n, _ := def.node(def.entry())
		return n, nil
	}
	last := run.NodeRuns[len(run.NodeRuns)-1]
	n, ok := def.node(last.NodeID)
	if !ok {
		return NodeDef{}, fmt.Errorf("运行记录引用了不存在的节点 %s（定义已变更）", last.NodeID)
	}
	nextID, err := nextIDOf(def, n, run.Inputs, outputs)
	if err != nil {
		return NodeDef{}, err
	}
	nxt, ok := def.node(nextID)
	if !ok {
		return NodeDef{}, fmt.Errorf("节点 %s 指向不存在的 %s", n.ID, nextID)
	}
	return nxt, nil
}

// execNode 执行单节点，返回（节点输出 / 是否暂停等待人工 / 错误）。
// 节点输出由调用方统一写 outputs 与 NodeRun（单一真源）。
func (e *Engine) execNode(ctx context.Context, n NodeDef, inputs, outputs map[string]string) (string, bool, error) {
	switch n.Type {
	case NodeStart:
		return "", false, nil
	case NodeLLM:
		if e.agents == nil {
			return "", false, fmt.Errorf("llm 节点未接入执行器")
		}
		prompt, err := RenderTemplate(n.Config.InputTemplate, inputs, outputs)
		if err != nil {
			return "", false, err
		}
		cctx, cancel := context.WithTimeout(ctx, nodeTimeout)
		defer cancel()
		out, err := e.agents.RunNode(cctx, n.Config.AgentID, prompt)
		if err != nil {
			return "", false, fmt.Errorf("llm 节点 %s: %w", n.ID, err)
		}
		return out, false, nil
	case NodeTool:
		if e.tools == nil {
			return "", false, fmt.Errorf("tool 节点未接入执行器")
		}
		args := make(map[string]string, len(n.Config.Args))
		for k, v := range n.Config.Args {
			rv, err := RenderTemplate(v, inputs, outputs)
			if err != nil {
				return "", false, fmt.Errorf("tool 节点 %s 参数 %s: %w", n.ID, k, err)
			}
			args[k] = rv
		}
		cctx, cancel := context.WithTimeout(ctx, nodeTimeout)
		defer cancel()
		out, err := e.tools.Invoke(cctx, n.Config.ToolID, n.Config.ToolName, args)
		if err != nil {
			return "", false, fmt.Errorf("tool 节点 %s: %w", n.ID, err)
		}
		return out, false, nil
	case NodeCond:
		// 分支求值统一走 nextIDOf（首跑与断点续跑同一真源）
		return "", false, nil
	case NodeApprove:
		return "", true, nil // paused：Message 已在定义中，前端展示
	default:
		return "", false, fmt.Errorf("未知节点类型 %s", n.Type)
	}
}

// nextIDOf 计算节点执行后的下一节点 ID（condition 按当前变量求值）。
func nextIDOf(def WorkflowDef, n NodeDef, inputs, outputs map[string]string) (string, error) {
	if n.Type == NodeCond {
		for _, b := range n.Branches {
			hit, err := EvalBranch(b.When, inputs, outputs)
			if err != nil {
				return "", fmt.Errorf("condition 节点 %s: %w", n.ID, err)
			}
			if hit {
				return b.NextID, nil
			}
		}
		return n.ElseID, nil
	}
	return n.NextID, nil
}

// waitResume 轮询 DB 等待 Approve（paused→running）/ Abort（→aborted）/ 定义删除（run 级联消失）。
// 返回 false 表示不再推进（终态已由对方落库）。
func (e *Engine) waitResume(ctx context.Context, run *WorkflowRun) bool {
	e.mu.Lock()
	e.waiting[run.ID] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.waiting, run.ID)
		e.mu.Unlock()
	}()
	for {
		time.Sleep(resumePoll)
		fresh, err := e.repo.GetRun(ctx, run.ID)
		if err != nil {
			return false // run 已删除（定义删除级联清 runs）
		}
		switch fresh.Status {
		case StatusAborted:
			*run = fresh
			return false
		case StatusRunning: // Approve 已恢复（节点由 Approve 标 succeeded）
			*run = fresh
			return true
		}
		// 仍 paused：继续等
	}
}

// hasWaiter 本进程是否有 advance 在等该 run（Approve 决定是否需要拉起续跑）。
func (e *Engine) hasWaiter(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waiting[runID]
}

// Approve 恢复暂停的 run（nodeID 须是等待中的 approve 节点）。
// 状态翻转直接落库（DB 唯一真源）：本进程有等待者则其轮询感知续跑；
// 无等待者（进程重启后/其它副本暂停的）由此处拉起 advance 接管。
func (e *Engine) Approve(ctx context.Context, runID, nodeID string) error {
	run, err := e.repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != StatusPaused {
		return ErrRunNotPaused
	}
	// 校验 nodeID 是最后一个 paused 节点（防历史节点 ID 混淆）
	if len(run.NodeRuns) == 0 || run.NodeRuns[len(run.NodeRuns)-1].NodeID != nodeID {
		return ErrNodeNotApprove
	}
	for _, nr := range run.NodeRuns {
		if nr.NodeID == nodeID && nr.Status != StatusPaused {
			return ErrNodeNotApprove
		}
	}
	run.NodeRuns[len(run.NodeRuns)-1].Status = StatusSucceeded
	run.NodeRuns[len(run.NodeRuns)-1].FinishedAt = time.Now()
	run.Status = StatusRunning
	if _, err := e.repo.UpdateRun(ctx, run); err != nil {
		return err
	}
	if e.hasWaiter(runID) {
		return nil // 本进程等待者轮询感知后自行续跑
	}
	// 无等待者：拉起续跑（需定义快照；定义已删则 run 应已被级联清，GetRun 失败路径）
	def, err := e.repo.Get(ctx, run.WorkflowID)
	if err != nil {
		return fmt.Errorf("恢复失败：工作流定义不存在: %w", err)
	}
	go e.advance(runTenant(ctx, run.TenantID), def, run)
	return nil
}

// Abort 中止运行：非终态置 aborted + 清 running/paused 节点（借鉴 pipeline StageAborted 教训）。
// 状态落库后，节点执行中的 advance 重读发现 aborted 即停止推进（不覆盖终态）。
func (e *Engine) Abort(ctx context.Context, runID string) error {
	run, err := e.repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	switch run.Status {
	case StatusSucceeded, StatusFailed, StatusAborted:
		return fmt.Errorf("运行已终态")
	}
	for i := range run.NodeRuns {
		if run.NodeRuns[i].Status == StatusRunning || run.NodeRuns[i].Status == StatusPaused {
			run.NodeRuns[i].Status = StatusAborted
			run.NodeRuns[i].FinishedAt = time.Now()
		}
	}
	run.Status = StatusAborted
	run.FinishedAt = time.Now()
	_, err = e.repo.UpdateRun(ctx, run)
	return err
}

func (e *Engine) save(ctx context.Context, run WorkflowRun) {
	if _, err := e.repo.UpdateRun(ctx, run); err != nil {
		log.Printf("[workflow] 保存运行 %s 失败: %v", run.ID, err)
	}
}

func (e *Engine) fail(ctx context.Context, run *WorkflowRun, msg string) {
	run.Status = StatusFailed
	run.FinishedAt = time.Now()
	if len(run.NodeRuns) > 0 && run.NodeRuns[len(run.NodeRuns)-1].Error == "" {
		run.NodeRuns[len(run.NodeRuns)-1].Error = msg
		run.NodeRuns[len(run.NodeRuns)-1].Status = StatusFailed
		run.NodeRuns[len(run.NodeRuns)-1].FinishedAt = time.Now()
	}
	e.save(ctx, *run)
}
