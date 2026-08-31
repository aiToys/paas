package workflowtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/ai/workflow"
	"github.com/aitoys/paas/internal/ai/workflow/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

type fakeAgents struct {
	resp map[string]string // agentID -> 回复
	err  map[string]error
}

func (f *fakeAgents) RunNode(ctx context.Context, agentID, prompt string) (string, error) {
	if err, ok := f.err[agentID]; ok {
		return "", err
	}
	if r, ok := f.resp[agentID]; ok {
		return r, nil
	}
	// 默认回复「售后」：分类节点走 approve 暂停路径（多数测试需要 paused 态）
	if strings.Contains(prompt, "分类") {
		return "售后", nil
	}
	return "ok: " + prompt, nil
}

type fakeTools struct{ resp string }

func (f *fakeTools) Invoke(ctx context.Context, toolID, toolName string, args map[string]string) (string, error) {
	return `{"query":"` + args["q"] + `","result":"mock"}`, nil
}

func ctxTenant() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func ticketDef() workflow.WorkflowDef {
	// 客服工单分流：start → llm 分类 → condition 分流（售后/售前）→ approve → end
	return workflow.WorkflowDef{
		Name: "ticket-flow", Enabled: true,
		Nodes: []workflow.NodeDef{
			{ID: "s", Type: workflow.NodeStart, NextID: "cls"},
			{ID: "cls", Type: workflow.NodeLLM, Name: "分类", NextID: "route",
				Config: workflow.NodeConfig{AgentID: "a-cls", InputTemplate: "分类以下工单（只回答 售后 或 售前）：{{inputs.ticket}}"}},
			{ID: "route", Type: workflow.NodeCond, NextID: "", Branches: []workflow.Branch{
				{When: "nodes.cls.output == 售后", NextID: "af"},
			}, ElseID: "pre"},
			{ID: "af", Type: workflow.NodeApprove, Name: "售后人工", Config: workflow.NodeConfig{Message: "售后工单需人工确认"}, NextID: "e"},
			{ID: "pre", Type: workflow.NodeLLM, Name: "售前回复", NextID: "e",
				Config: workflow.NodeConfig{AgentID: "a-pre", InputTemplate: "回复售前咨询：{{inputs.ticket}}"}},
			{ID: "e", Type: workflow.NodeEnd},
		},
	}
}

func TestDefValidate(t *testing.T) {
	d := ticketDef()
	if err := d.Validate(); err != nil {
		t.Fatalf("合法定义被拒: %v", err)
	}
	// 缺 start
	d2 := ticketDef()
	d2.Nodes = d2.Nodes[1:]
	if err := d2.Validate(); err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("缺 start 应报错，got %v", err)
	}
	// 重复 id
	d3 := ticketDef()
	d3.Nodes[2].ID = "cls"
	if err := d3.Validate(); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复 id 应报错，got %v", err)
	}
	// 悬挂连线
	d4 := ticketDef()
	d4.Nodes[1].NextID = "ghost"
	if err := d4.Validate(); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("悬挂连线应报错，got %v", err)
	}
	// 不可达节点
	d5 := ticketDef()
	d5.Nodes = append(d5.Nodes, workflow.NodeDef{ID: "orphan", Type: workflow.NodeLLM, NextID: "e",
		Config: workflow.NodeConfig{AgentID: "a", InputTemplate: "x"}})
	if err := d5.Validate(); err == nil || !strings.Contains(err.Error(), "不可达") {
		t.Fatalf("孤儿节点应报错，got %v", err)
	}
}

func TestRenderTemplateAndBranch(t *testing.T) {
	inputs := map[string]string{"ticket": "退款"}
	nodes := map[string]string{"cls": "售后"}
	out, err := workflow.RenderTemplate("工单={{inputs.ticket}} 分类={{nodes.cls.output}}", inputs, nodes)
	if err != nil || out != "工单=退款 分类=售后" {
		t.Fatalf("渲染错误: %q %v", out, err)
	}
	if _, err := workflow.RenderTemplate("{{inputs.missing}}", inputs, nodes); err == nil {
		t.Fatal("未定义变量应报错")
	}
	hit, err := workflow.EvalBranch("nodes.cls.output == 售后", inputs, nodes)
	if err != nil || !hit {
		t.Fatalf("条件应命中: %v %v", hit, err)
	}
	hit, _ = workflow.EvalBranch("nodes.cls.output contains 售", inputs, nodes)
	if !hit {
		t.Fatal("contains 应命中")
	}
}

// 全链路：分类=售后 → approve 暂停 → 恢复 → end。
func TestEngineTicketAfterSalesPath(t *testing.T) {
	repo := memory.NewStore()
	agents := &fakeAgents{resp: map[string]string{"a-cls": "售后"}}
	eng := workflow.NewEngine(repo, agents, &fakeTools{})
	ctx := ctxTenant()
	def, err := repo.Create(ctx, ticketDef())
	if err != nil {
		t.Fatal(err)
	}
	run, err := eng.Start(ctx, def, map[string]string{"ticket": "要求退款"})
	if err != nil {
		t.Fatal(err)
	}
	// 等暂停
	waitStatus(t, repo, run.ID, workflow.StatusPaused)
	// approve 恢复（node=af）
	if err := eng.Approve(ctx, run.ID, "af"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	waitStatus(t, repo, run.ID, workflow.StatusSucceeded)
	fresh, _ := repo.GetRun(ctx, run.ID)
	// 节点序列：s → cls → route → af → e
	var seq []string
	for _, nr := range fresh.NodeRuns {
		seq = append(seq, nr.NodeID)
	}
	want := "s,cls,route,af,e"
	if strings.Join(seq, ",") != want {
		t.Fatalf("节点序列 = %s, want %s", strings.Join(seq, ","), want)
	}
}

// 售前分支：分类=售前 → pre llm → end（无 approve）。
func TestEnginePreSalesPath(t *testing.T) {
	repo := memory.NewStore()
	agents := &fakeAgents{resp: map[string]string{"a-cls": "售前", "a-pre": "您好，很高兴为您介绍"}}
	eng := workflow.NewEngine(repo, agents, &fakeTools{})
	ctx := ctxTenant()
	def, _ := repo.Create(ctx, ticketDef())
	run, err := eng.Start(ctx, def, map[string]string{"ticket": "怎么买"})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, repo, run.ID, workflow.StatusSucceeded)
	fresh, _ := repo.GetRun(ctx, run.ID)
	if strings.Join(nodeIDs(fresh), ",") != "s,cls,route,pre,e" {
		t.Fatalf("售前分支序列错误: %v", nodeIDs(fresh))
	}
	// pre 节点 output 是 fake 回复
	for _, nr := range fresh.NodeRuns {
		if nr.NodeID == "pre" && nr.Output != "您好，很高兴为您介绍" {
			t.Fatalf("pre output = %q", nr.Output)
		}
	}
}

// abort：paused 状态中止 → 清 paused 节点。
func TestEngineAbortWhilePaused(t *testing.T) {
	repo := memory.NewStore()
	agents := &fakeAgents{resp: map[string]string{"a-cls": "售后"}}
	eng := workflow.NewEngine(repo, agents, &fakeTools{})
	ctx := ctxTenant()
	def, _ := repo.Create(ctx, ticketDef())
	run, _ := eng.Start(ctx, def, map[string]string{"ticket": "退款"})
	waitStatus(t, repo, run.ID, workflow.StatusPaused)
	if err := eng.Abort(ctx, run.ID); err != nil {
		t.Fatalf("abort: %v", err)
	}
	waitStatus(t, repo, run.ID, workflow.StatusAborted)
	fresh, _ := repo.GetRun(ctx, run.ID)
	last := fresh.NodeRuns[len(fresh.NodeRuns)-1]
	if last.NodeID != "af" || last.Status != workflow.StatusAborted {
		t.Fatalf("af 节点应为 aborted，got %+v", last)
	}
	// 已终态再 abort 报错
	if err := eng.Abort(ctx, run.ID); err == nil {
		t.Fatal("终态 abort 应报错")
	}
}

// approve 校验：非暂停状态 / 错误节点 ID 拒绝。
func TestApproveGuards(t *testing.T) {
	repo := memory.NewStore()
	eng := workflow.NewEngine(repo, &fakeAgents{}, &fakeTools{})
	ctx := ctxTenant()
	def, _ := repo.Create(ctx, ticketDef())
	run, _ := eng.Start(ctx, def, map[string]string{"ticket": "退款"})
	waitStatus(t, repo, run.ID, workflow.StatusPaused)
	if err := eng.Approve(ctx, run.ID, "cls"); err == nil {
		t.Fatal("非 approve 节点应拒绝")
	}
	// 用不存在 run 试 not paused
	if err := eng.Approve(ctx, "wfr-ghost", "af"); err == nil {
		t.Fatal("不存在 run 应报错")
	}
}

// 失败传播：llm 节点失败 → run failed + Error 记录。
func TestEngineFailurePropagates(t *testing.T) {
	repo := memory.NewStore()
	agents := &fakeAgents{err: map[string]error{"a-cls": context.DeadlineExceeded}}
	eng := workflow.NewEngine(repo, agents, &fakeTools{})
	ctx := ctxTenant()
	def, _ := repo.Create(ctx, ticketDef())
	run, _ := eng.Start(ctx, def, map[string]string{"ticket": "退款"})
	waitStatus(t, repo, run.ID, workflow.StatusFailed)
	fresh, _ := repo.GetRun(ctx, run.ID)
	found := false
	for _, nr := range fresh.NodeRuns {
		if nr.NodeID == "cls" && nr.Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("cls 节点应记录 Error")
	}
}

// 工具节点 + 变量传递。
func TestEngineToolNode(t *testing.T) {
	repo := memory.NewStore()
	def := workflow.WorkflowDef{
		Name: "tool-flow", Enabled: true,
		Nodes: []workflow.NodeDef{
			{ID: "s", Type: workflow.NodeStart, NextID: "q"},
			{ID: "q", Type: workflow.NodeTool, Name: "搜索", NextID: "e",
				Config: workflow.NodeConfig{ToolID: "tool-1", ToolName: "search", Args: map[string]string{"q": "{{inputs.kw}}"}}},
			{ID: "e", Type: workflow.NodeEnd},
		},
	}
	eng := workflow.NewEngine(repo, &fakeAgents{}, &fakeTools{})
	ctx := ctxTenant()
	saved, _ := repo.Create(ctx, def)
	run, _ := eng.Start(ctx, saved, map[string]string{"kw": "手机"})
	waitStatus(t, repo, run.ID, workflow.StatusSucceeded)
	fresh, _ := repo.GetRun(ctx, run.ID)
	for _, nr := range fresh.NodeRuns {
		if nr.NodeID == "q" && !strings.Contains(nr.Output, "手机") {
			t.Fatalf("工具参数未渲染: %q", nr.Output)
		}
	}
}

func nodeIDs(r workflow.WorkflowRun) []string {
	out := make([]string, 0, len(r.NodeRuns))
	for _, nr := range r.NodeRuns {
		out = append(out, nr.NodeID)
	}
	return out
}

func waitStatus(t *testing.T, repo workflow.Repository, runID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := repo.GetRun(ctxTenant(), runID)
		if err == nil && r.Status == want {
			// succeeded/failed/aborted 是终态直接返；paused 需确保 goroutine 已 save
			if want != workflow.StatusPaused {
				return
			}
			// paused：等 advance 真正阻塞（NodeRuns 末尾已 paused）——再查一次稳定即返
			time.Sleep(20 * time.Millisecond)
			r2, _ := repo.GetRun(ctxTenant(), runID)
			if r2.Status == workflow.StatusPaused {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待状态 %s 超时", want)
}
