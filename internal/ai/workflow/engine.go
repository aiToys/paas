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
)

// Engine 工作流执行引擎：goroutine 推进 + approve 暂停恢复（单实例内存信号）。
type Engine struct {
	repo   Repository
	agents AgentRunner
	tools  ToolRunner
	mu     sync.Mutex
	resume map[string]chan struct{} // runID -> 恢复信号（approve 等待中）
}

func NewEngine(repo Repository, agents AgentRunner, tools ToolRunner) *Engine {
	return &Engine{repo: repo, agents: agents, tools: tools, resume: make(map[string]chan struct{})}
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

// advance 推进到暂停/终态。def 快照随 run 传递（定义后续修改不影响在跑的 run，
// 与 pipeline StageRun.Input 固化解耦同款取舍）。
func (e *Engine) advance(ctx context.Context, def WorkflowDef, run WorkflowRun) {
	outputs := map[string]string{} // nodeID -> output（变量传递真源）
	for i := range run.NodeRuns {
		outputs[run.NodeRuns[i].NodeID] = run.NodeRuns[i].Output
	}
	cur, _ := def.node(def.entry())
	if cur.ID == "" {
		e.fail(ctx, &run, "定义缺 start 节点")
		return
	}
	for step := 0; step < maxSteps; step++ {
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

		nextID, output, paused, err := e.execNode(ctx, cur, run.Inputs, outputs)
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
		if paused {
			run.NodeRuns[idx].Status = StatusPaused
			run.Status = StatusPaused
			e.save(ctx, run)
			// 等待 approve 信号；通道关闭（abort/恢复）后重新读 run 状态决定走向
			if ch := e.waitResume(run.ID); ch != nil {
				<-ch
			}
			fresh, err := e.repo.GetRun(ctx, run.ID)
			if err != nil || fresh.Status == StatusAborted {
				run.Status = StatusAborted
				run.FinishedAt = time.Now()
				e.save(ctx, run)
				return
			}
			// 恢复：当前节点视为成功，继续 nextID
			run = fresh
			run.NodeRuns[idx].Status = StatusSucceeded
			run.NodeRuns[idx].FinishedAt = time.Now()
			run.Status = StatusRunning
			e.save(ctx, run)
		} else {
			run.NodeRuns[idx].Status = StatusSucceeded
			e.save(ctx, run)
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

// execNode 执行单节点，返回（下一节点 ID / 节点输出 / 是否暂停等待人工 / 错误）。
// 节点输出由调用方统一写 outputs 与 NodeRun（单一真源）。
func (e *Engine) execNode(ctx context.Context, n NodeDef, inputs, outputs map[string]string) (string, string, bool, error) {
	switch n.Type {
	case NodeStart:
		return n.NextID, "", false, nil
	case NodeLLM:
		if e.agents == nil {
			return "", "", false, fmt.Errorf("llm 节点未接入执行器")
		}
		prompt, err := RenderTemplate(n.Config.InputTemplate, inputs, outputs)
		if err != nil {
			return "", "", false, err
		}
		cctx, cancel := context.WithTimeout(ctx, nodeTimeout)
		defer cancel()
		out, err := e.agents.RunNode(cctx, n.Config.AgentID, prompt)
		if err != nil {
			return "", "", false, fmt.Errorf("llm 节点 %s: %w", n.ID, err)
		}
		return n.NextID, out, false, nil
	case NodeTool:
		if e.tools == nil {
			return "", "", false, fmt.Errorf("tool 节点未接入执行器")
		}
		args := make(map[string]string, len(n.Config.Args))
		for k, v := range n.Config.Args {
			rv, err := RenderTemplate(v, inputs, outputs)
			if err != nil {
				return "", "", false, fmt.Errorf("tool 节点 %s 参数 %s: %w", n.ID, k, err)
			}
			args[k] = rv
		}
		cctx, cancel := context.WithTimeout(ctx, nodeTimeout)
		defer cancel()
		out, err := e.tools.Invoke(cctx, n.Config.ToolID, n.Config.ToolName, args)
		if err != nil {
			return "", "", false, fmt.Errorf("tool 节点 %s: %w", n.ID, err)
		}
		return n.NextID, out, false, nil
	case NodeCond:
		for _, b := range n.Branches {
			hit, err := EvalBranch(b.When, inputs, outputs)
			if err != nil {
				return "", "", false, fmt.Errorf("condition 节点 %s: %w", n.ID, err)
			}
			if hit {
				return b.NextID, "", false, nil
			}
		}
		return n.ElseID, "", false, nil
	case NodeApprove:
		return n.NextID, "", true, nil // paused：Message 已在定义中，前端展示
	default:
		return "", "", false, fmt.Errorf("未知节点类型 %s", n.Type)
	}
}

// waitResume 注册/获取等待通道；run 已有等待者返 nil（防重复）。
func (e *Engine) waitResume(runID string) <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.resume[runID]; ok {
		return nil
	}
	ch := make(chan struct{})
	e.resume[runID] = ch
	return ch
}

// Approve 恢复暂停的 run（nodeID 须是等待中的 approve 节点）。
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
	e.mu.Lock()
	ch, ok := e.resume[runID]
	delete(e.resume, runID)
	e.mu.Unlock()
	if ok {
		close(ch)
	}
	return nil
}

// Abort 中止运行：非终态置 aborted + 清 running/paused 节点（借鉴 pipeline StageAborted 教训）。
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
	if _, err := e.repo.UpdateRun(ctx, run); err != nil {
		return err
	}
	// 唤醒等待者（若在 approve 等待中）
	e.mu.Lock()
	ch, ok := e.resume[runID]
	delete(e.resume, runID)
	e.mu.Unlock()
	if ok {
		close(ch)
	}
	return nil
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
