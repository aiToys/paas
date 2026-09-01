// Package workflow 智能体工作流编排：把 Agent/Tool 等资产串成多步流程。
// 设计见 docs/superpowers/specs/2026-08-31-agent-workflow-design.md。
//
// 模型分两层（借鉴 pipeline 引擎已验证的定义/运行分离）：
//
//	WorkflowDef  定义（节点列表 + 连线，可重复执行）
//	WorkflowRun  一次运行（输入 + 各节点执行记录，不可变审计轨迹）
package workflow

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// min 兼容占位符错误提示的长度截断（Go 1.21+ 内置 min 可直接用，此处显式以防早期版本）。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// NodeType 节点类型。
const (
	NodeStart   = "start"     // 起点（唯一，无配置）
	NodeLLM     = "llm"       // 绑定 Agent 执行一轮推理（Output=最终文本）
	NodeTool    = "tool"      // 调用 MCP 工具（Output=JSON 结果字符串）
	NodeApprove = "approve"   // 人工确认门禁（暂停等恢复）
	NodeEnd     = "end"       // 终点（Output 为空）
	NodeCond    = "condition" // 条件分支（按变量比较走 Branches/Else）
)

// 运行/节点状态。
const (
	StatusRunning   = "running"
	StatusPaused    = "paused" // approve 等待人工
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusAborted   = "aborted"
)

// 领域 sentinel。
var (
	ErrWorkflowNotFound = errors.New("工作流不存在")
	ErrWorkflowExists   = errors.New("工作流名已被占用")
	ErrRunNotFound      = errors.New("工作流运行不存在")
	ErrInvalidDef       = errors.New("工作流定义不合法")
	ErrRunNotPaused     = errors.New("运行不在等待确认状态")
	ErrNodeNotApprove   = errors.New("该节点不是人工确认节点")
	ErrActiveRunExists  = errors.New("工作流仍有进行中的运行，须先中止再删除")
)

// Branch 条件分支的一支。
type Branch struct {
	When   string `json:"when"`   // 条件表达式 "var op value"，如 "nodes.cls.output == 售后"
	NextID string `json:"nextId"` // 命中时跳转的节点
}

// NodeDef 节点定义（Config 按类型携带不同配置）。
type NodeDef struct {
	ID       string     `json:"id"`     // Def 内唯一（用户可读，如 cls/gen/review）
	Type     string     `json:"type"`   // NodeType
	Name     string     `json:"name"`   // 展示名
	NextID   string     `json:"nextId"` // 下一节点（condition 用 Branches/ElseID）
	Config   NodeConfig `json:"config"`
	Branches []Branch   `json:"branches,omitempty"` // 仅 condition
	ElseID   string     `json:"elseId,omitempty"`   // 仅 condition：全不命中走此
}

// NodeConfig 类型相关配置（json 扁平存 DB 的 JSONB）。
type NodeConfig struct {
	// llm
	AgentID       string `json:"agentId,omitempty"`       // 绑定的 Agent
	InputTemplate string `json:"inputTemplate,omitempty"` // 提示模板，支持 {{inputs.x}}/{{nodes.<id>.output}}
	// tool
	ToolID   string            `json:"toolId,omitempty"`   // 绑定的工具
	ToolName string            `json:"toolName,omitempty"` // MCP 工具方法名
	Args     map[string]string `json:"args,omitempty"`     // 参数模板（值支持占位符）
	// condition（也可直接用 Branches 表达式，Op/Var/Val 是单分支简写）
	// approve
	Message string `json:"message,omitempty"` // 展示给确认人的说明
}

// WorkflowDef 工作流定义。
type WorkflowDef struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Name      string    `json:"name"` // 租户内唯一
	Desc      string    `json:"desc,omitempty"`
	Nodes     []NodeDef `json:"nodes"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NodeRun 单节点执行记录。
type NodeRun struct {
	NodeID     string    `json:"nodeId"`
	Status     string    `json:"status"`
	Output     string    `json:"output,omitempty"` // llm=最终文本 / tool=JSON 字符串
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// WorkflowRun 一次运行。
type WorkflowRun struct {
	ID         string            `json:"id"`
	TenantID   string            `json:"tenantId"`
	WorkflowID string            `json:"workflowId"`
	Status     string            `json:"status"`
	Inputs     map[string]string `json:"inputs"`
	NodeRuns   []NodeRun         `json:"nodeRuns"`
	CreatedAt  time.Time         `json:"createdAt"`
	FinishedAt time.Time         `json:"finishedAt,omitempty"`
}

// —— 定义校验（结构完整性；Agent/Tool 存在性由引擎执行期校验）——

const MaxNodes = 50 // 防失控定义

// Validate 校验定义：单一 start/end、连线指向存在节点、类型专属字段非空、无孤立节点。
func (d *WorkflowDef) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("%w: name 不能为空", ErrInvalidDef)
	}
	if len(d.Nodes) == 0 || len(d.Nodes) > MaxNodes {
		return fmt.Errorf("%w: 节点数须在 1..%d", ErrInvalidDef, MaxNodes)
	}
	ids := map[string]bool{}
	var starts, ends int
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.ID == "" {
			return fmt.Errorf("%w: 第 %d 个节点缺 id", ErrInvalidDef, i+1)
		}
		if ids[n.ID] {
			return fmt.Errorf("%w: 节点 id 重复 %s", ErrInvalidDef, n.ID)
		}
		ids[n.ID] = true
		switch n.Type {
		case NodeStart:
			starts++
		case NodeEnd:
			ends++
		case NodeLLM:
			if n.Config.AgentID == "" || n.Config.InputTemplate == "" {
				return fmt.Errorf("%w: llm 节点 %s 需 agentId + inputTemplate", ErrInvalidDef, n.ID)
			}
		case NodeTool:
			if n.Config.ToolID == "" || n.Config.ToolName == "" {
				return fmt.Errorf("%w: tool 节点 %s 需 toolId + toolName", ErrInvalidDef, n.ID)
			}
		case NodeCond:
			if len(n.Branches) == 0 || n.ElseID == "" {
				return fmt.Errorf("%w: condition 节点 %s 需 branches + elseId", ErrInvalidDef, n.ID)
			}
			for _, b := range n.Branches {
				if strings.TrimSpace(b.When) == "" {
					return fmt.Errorf("%w: condition 节点 %s 存在空条件表达式", ErrInvalidDef, n.ID)
				}
				if b.NextID == "" {
					return fmt.Errorf("%w: condition 节点 %s 分支缺 nextId", ErrInvalidDef, n.ID)
				}
			}
		case NodeApprove:
			// Message 可选
		default:
			return fmt.Errorf("%w: 未知节点类型 %s", ErrInvalidDef, n.Type)
		}
	}
	if starts != 1 || ends < 1 {
		return fmt.Errorf("%w: 需恰好 1 个 start 与 ≥1 个 end", ErrInvalidDef)
	}
	// 连线指向存在节点
	for i := range d.Nodes {
		n := &d.Nodes[i]
		targets := []string{n.NextID, n.ElseID}
		for _, b := range n.Branches {
			targets = append(targets, b.NextID)
		}
		for _, t := range targets {
			if t != "" && !ids[t] {
				return fmt.Errorf("%w: 节点 %s 指向不存在的 %s", ErrInvalidDef, n.ID, t)
			}
		}
		if n.Type != NodeCond && n.Type != NodeEnd && n.NextID == "" {
			return fmt.Errorf("%w: 节点 %s 缺 nextId", ErrInvalidDef, n.ID)
		}
	}
	// 可达性：从 start 沿连线遍历，存在不可达节点报错（防孤儿定义）。
	reach := map[string]bool{}
	d.reachable(d.entry(), reach)
	for _, n := range d.Nodes {
		if !reach[n.ID] {
			return fmt.Errorf("%w: 节点 %s 从 start 不可达", ErrInvalidDef, n.ID)
		}
	}
	// 环检测（DFS 三色标记）：环定义在运行期只能靠 maxSteps 兜底——每步都是真实
	// LLM/工具调用（费用放大），必须在创建期拦截。
	onStack := map[string]bool{}
	seen := map[string]bool{}
	var visit func(id string) error
	visit = func(id string) error {
		if onStack[id] {
			return fmt.Errorf("%w: 节点 %s 处于环路中", ErrInvalidDef, id)
		}
		if !reach[id] || seen[id] {
			return nil
		}
		seen[id] = true
		onStack[id] = true
		n, ok := d.node(id)
		if !ok {
			return nil
		}
		for _, t := range d.targets(n) {
			if err := visit(t); err != nil {
				return err
			}
		}
		onStack[id] = false
		return nil
	}
	if err := visit(d.entry()); err != nil {
		return err
	}
	return nil
}

// targets 节点的全部出边目标。
func (d *WorkflowDef) targets(n NodeDef) []string {
	out := []string{}
	if n.NextID != "" {
		out = append(out, n.NextID)
	}
	if n.ElseID != "" {
		out = append(out, n.ElseID)
	}
	for _, b := range n.Branches {
		if b.NextID != "" {
			out = append(out, b.NextID)
		}
	}
	return out
}

func (d *WorkflowDef) entry() string {
	for _, n := range d.Nodes {
		if n.Type == NodeStart {
			return n.ID
		}
	}
	return ""
}

func (d *WorkflowDef) reachable(id string, seen map[string]bool) {
	if seen[id] || id == "" {
		return
	}
	seen[id] = true
	n, ok := d.node(id)
	if !ok {
		return
	}
	d.reachable(n.NextID, seen)
	d.reachable(n.ElseID, seen)
	for _, b := range n.Branches {
		d.reachable(b.NextID, seen)
	}
}

func (d *WorkflowDef) node(id string) (NodeDef, bool) {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return NodeDef{}, false
}

// RenderTemplate 渲染 {{inputs.x}} / {{nodes.<id>.output}} 占位符。
// 未定义引用报错（fail-fast，防静默空串流向下游）。
func RenderTemplate(tpl string, inputs map[string]string, nodes map[string]string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tpl); {
		open := strings.Index(tpl[i:], "{{")
		if open < 0 {
			b.WriteString(tpl[i:])
			break
		}
		close_ := strings.Index(tpl[i+open:], "}}")
		if close_ < 0 {
			// 未闭合占位符报错（fail-fast，防字面量 {{xxx 静默流向 LLM 产生错误结果）
			return "", fmt.Errorf("占位符未闭合：{{ 后缺少 }}（模板片段 %q）", tpl[i:i+open+min(len(tpl[i+open:]), 32)])
		}
		b.WriteString(tpl[i : i+open])
		ref := strings.TrimSpace(tpl[i+open+2 : i+open+close_])
		val, err := resolveRef(ref, inputs, nodes)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		i = i + open + close_ + 2
	}
	return b.String(), nil
}

func resolveRef(ref string, inputs map[string]string, nodes map[string]string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "inputs."):
		k := strings.TrimPrefix(ref, "inputs.")
		if v, ok := inputs[k]; ok {
			return v, nil
		}
		return "", fmt.Errorf("输入变量 %s 未定义", k)
	case strings.HasPrefix(ref, "nodes.") && strings.HasSuffix(ref, ".output"):
		id := strings.TrimSuffix(strings.TrimPrefix(ref, "nodes."), ".output")
		if v, ok := nodes[id]; ok {
			return v, nil
		}
		return "", fmt.Errorf("节点 %s 无输出", id)
	default:
		return "", fmt.Errorf("无法解析占位符 {{%s}}", ref)
	}
}

// EvalBranch 条件比较：expr 形如 "var op value"，op ∈ == != contains。
// var 是占位符引用（inputs.x / nodes.id.output），value 是字面量。
func EvalBranch(expr string, inputs map[string]string, nodes map[string]string) (bool, error) {
	for _, op := range []string{"==", "!=", "contains"} {
		if idx := strings.Index(expr, op); idx > 0 {
			ref := strings.TrimSpace(expr[:idx])
			lit := strings.TrimSpace(expr[idx+len(op):])
			val, err := resolveRef(ref, inputs, nodes)
			if err != nil {
				return false, err
			}
			switch op {
			case "==":
				return val == lit, nil
			case "!=":
				return val != lit, nil
			case "contains":
				return strings.Contains(val, lit), nil
			}
		}
	}
	return false, fmt.Errorf("无法解析条件表达式 %q", expr)
}
