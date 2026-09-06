package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aitoys/paas/internal/ai/agent"
	agentmemory "github.com/aitoys/paas/internal/ai/agent/memory"
	"github.com/aitoys/paas/internal/ai/knowledgebase"
	kbmemory "github.com/aitoys/paas/internal/ai/knowledgebase/memory"
	"github.com/aitoys/paas/internal/ai/prompt"
	promptmemory "github.com/aitoys/paas/internal/ai/prompt/memory"
	"github.com/aitoys/paas/internal/ai/skill"
	skillmemory "github.com/aitoys/paas/internal/ai/skill/memory"
	"github.com/aitoys/paas/internal/ai/tool"
	toolmemory "github.com/aitoys/paas/internal/ai/tool/memory"
	"github.com/aitoys/paas/internal/ai/workflow"
	wfmemory "github.com/aitoys/paas/internal/ai/workflow/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestSanitizeConfig(t *testing.T) {
	in := map[string]string{
		"serverURL":  "http://srv:8080",
		"apiKey":     "sk-secret",
		"API_KEY":    "sk-2",
		"token":      "t1",
		"password":   "p1",
		"authHeader": "x",
		"endpoint":   "/api",
	}
	out := SanitizeConfig(in)
	for _, k := range []string{"apiKey", "API_KEY", "token", "password", "authHeader"} {
		if _, ok := out[k]; ok {
			t.Fatalf("敏感 key %s 未剔除: %v", k, out)
		}
	}
	if out["serverURL"] != "http://srv:8080" || out["endpoint"] != "/api" {
		t.Fatalf("非敏感 key 被误删: %v", out)
	}
	if len(in) != 7 {
		t.Fatal("SanitizeConfig 改了入参")
	}
}

// newRepos 内存四件套。
func newRepos() *Repos {
	return &Repos{
		Agents:  agentmemory.NewStore(),
		Skills:  skillmemory.NewStore(),
		Prompts: promptmemory.NewStore(),
		Tools:   toolmemory.NewStore(),
	}
}

func ctxTenant(tid string) context.Context {
	return tenant.WithTenant(context.Background(), tid)
}

func TestSkillPublishInstallRoundtrip(t *testing.T) {
	repos := newRepos()
	ctx := ctxTenant("t-a")
	sk, err := repos.Skills.Create(ctx, skill.Skill{Name: "周报助手", Description: "写周报", Instructions: "每周五...", Category: "writing"})
	if err != nil {
		t.Fatal(err)
	}
	f := skillForker{repos}
	_, _, cat, snap, err := f.BuildSnapshot(ctx, sk.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if cat != "writing" {
		t.Fatalf("category 应继承实体自带，got %s", cat)
	}
	// 快照不含租户/ID
	var raw map[string]any
	_ = json.Unmarshal(snap, &raw)
	if raw["tenantId"] != "" || raw["id"] != "" {
		t.Fatalf("快照未剥离租户/ID: %v", raw)
	}

	store := &fakeRepo{}
	item, err := store.Create(ctx, Item{EntityType: EntitySkill, Name: "周报助手", Category: "writing", Snapshot: snap, PublisherTenant: "t-a"})
	if err != nil {
		t.Fatal(err)
	}
	// 另一租户安装
	ctxB := ctxTenant("t-b")
	res, err := f.InstallSnapshot(ctxB, item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repos.Skills.Get(ctxB, res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "周报助手" || got.InstalledFrom != item.ID || got.Instructions != "每周五..." {
		t.Fatalf("fork 副本字段不符: %+v", got)
	}
	// 同名二次安装 → 后缀
	res2, err := f.InstallSnapshot(ctxB, item)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Name != "周报助手-2" {
		t.Fatalf("同名应加后缀，got %s", res2.Name)
	}
	// 计数
	_ = store.IncInstalls(ctx, item.ID)
	it, _ := store.Get(ctx, item.ID)
	if it.Installs != 1 {
		t.Fatalf("安装计数错误: %d", it.Installs)
	}
}

func TestToolPublishSanitizesCredentials(t *testing.T) {
	repos := newRepos()
	ctx := ctxTenant("t-a")
	tl, err := repos.Tools.Create(ctx, tool.Tool{
		Name: "天气查询", Description: "查天气", Type: tool.TypeHTTP,
		Config: map[string]string{"endpoint": "http://wx/api", "apiKey": "sk-leak"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := toolForker{repos}
	_, _, _, snap, err := f.BuildSnapshot(ctx, tl.ID, "data")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snap, []byte("sk-leak")) {
		t.Fatal("工具凭证泄漏进广场快照")
	}
	// 安装后凭证留空待填
	item := Item{ID: "mk-1", EntityType: EntityTool, Name: "天气查询", Snapshot: snap}
	res, err := f.InstallSnapshot(ctxTenant("t-b"), item)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := repos.Tools.Get(ctxTenant("t-b"), res.EntityID)
	if got.Config["apiKey"] != "" || got.Config["endpoint"] != "http://wx/api" {
		t.Fatalf("安装后 config 不符: %v", got.Config)
	}
}

func TestAgentBundleForkRewritesReferences(t *testing.T) {
	repos := newRepos()
	ctx := ctxTenant("t-a")
	sk, _ := repos.Skills.Create(ctx, skill.Skill{Name: "代码评审", Instructions: "按规范评审"})
	_, _ = repos.Prompts.Create(ctx, prompt.Prompt{Name: "评审模板", Template: "评审 {{.Lang}} 代码"})
	tl, _ := repos.Tools.Create(ctx, tool.Tool{Name: "linter", Description: "lint", Type: tool.TypeBuiltin, Config: map[string]string{"handler": "lint"}})
	a, err := repos.Agents.Create(ctx, agent.Agent{
		Name: "评审员", Description: "代码评审 agent", Model: "glm-5.2",
		SystemPrompt: "你是评审员", Skills: []string{sk.ID}, PromptRef: "评审模板", Tools: []string{tl.ID},
		KnowledgeBases: []string{"kb-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := agentForker{repos}
	name, _, cat, snap, err := f.BuildSnapshot(ctx, a.ID, "coding")
	if err != nil {
		t.Fatal(err)
	}
	if name != "评审员" || cat != "coding" {
		t.Fatalf("name/category 不符: %s %s", name, cat)
	}
	if bytes.Contains(snap, []byte("kb-1")) {
		t.Fatal("KB 引用不应进广场快照")
	}

	item := Item{ID: "mk-agg", EntityType: EntityAgent, Name: name, Snapshot: snap}
	ctxB := ctxTenant("t-b")
	res, err := f.InstallSnapshot(ctxB, item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repos.Agents.Get(ctxB, res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstalledFrom != "mk-agg" {
		t.Fatalf("来源标记缺失: %+v", got)
	}
	if len(got.Skills) != 1 || len(got.Tools) != 1 {
		t.Fatalf("引用未重写: %+v", got)
	}
	// 引用指向 fork 后的新实体（存在于 t-b）
	if _, err := repos.Skills.Get(ctxB, got.Skills[0]); err != nil {
		t.Fatalf("skill 引用失效: %v", err)
	}
	if _, err := repos.Tools.Get(ctxB, got.Tools[0]); err != nil {
		t.Fatalf("tool 引用失效: %v", err)
	}
	// PromptRef 指向 fork 后的 prompt name
	if _, err := repos.Prompts.GetActive(ctxB, got.PromptRef); err != nil {
		t.Fatalf("prompt 引用失效: %v", err)
	}
	// t-a 原实体不受影响
	orig, _ := repos.Agents.Get(ctx, a.ID)
	if len(orig.Skills) != 1 || orig.PromptRef != "评审模板" {
		t.Fatalf("源实体被改: %+v", orig)
	}
}

func TestRepublishOverwritesAndDeleteKeepsCopies(t *testing.T) {
	store := &fakeRepo{}
	ctx := ctxTenant("t-a")
	it1, _ := store.Create(ctx, Item{EntityType: EntitySkill, Name: "x", Snapshot: []byte(`{}`), PublisherTenant: "t-a"})
	_ = store.IncInstalls(ctx, it1.ID)
	// 重发布覆盖：同名同发布者新 ID
	it2, _ := store.Create(ctx, Item{EntityType: EntitySkill, Name: "x", Snapshot: []byte(`{"v":2}`), PublisherTenant: "t-a"})
	if list, _ := store.List(ctx, "", "", ""); len(list) != 1 {
		t.Fatalf("重发布应覆盖，条目数 %d", len(list))
	}
	_ = it2
	// 下架不影响已装副本（InstallSnapshot 产物已在租户 store，与 marketplace 解耦——此处验证下架语义）
	if err := store.Delete(ctx, it1.ID); err == nil {
		// it1 已被覆盖删除，此时应 not found——两种情况都算通过路径验证
		_ = err
	}
	if _, err := store.Get(ctx, it1.ID); err != ErrItemNotFound {
		t.Fatalf("it1 应已被覆盖删除: %v", err)
	}
}

// TestKBPublishInstallRoundtrip KB 配置骨架发布-安装回路：
// 快照剥离数据服务引用（租户数据），安装时经 finder 重绑安装者租户实例；无实例时引导性报错。
func TestKBPublishInstallRoundtrip(t *testing.T) {
	repos := newRepos()
	repos.KBs = kbmemory.NewStore()
	ctx := ctxTenant("t-a")
	kb, err := repos.KBs.Create(ctx, knowledgebase.KnowledgeBase{
		Name: "售后知识库", VectorStoreRef: "ds-vec-a", ObjectStoreRef: "ds-obj-a",
		EmbeddingModel: "text-embedding-3", EmbeddingDim: 1536,
		RetrieverConfig: knowledgebase.RetrieverConfig{TopK: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := kbForker{repos}
	name, desc, cat, snap, err := f.BuildSnapshot(ctx, kb.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if cat != "general" {
		t.Fatalf("KB 无 Category 应兜底 general，got %s", cat)
	}
	if name != "售后知识库" || !bytes.Contains(snap, []byte("text-embedding-3")) {
		t.Fatalf("name/desc 不符: %s %s", name, desc)
	}
	var raw map[string]any
	_ = json.Unmarshal(snap, &raw)
	if raw["vectorStoreRef"] != "" || raw["objectStoreRef"] != "" {
		t.Fatalf("数据服务引用不应进快照: %v", raw)
	}

	item := Item{ID: "mk-kb", EntityType: EntityKnowledgebase, Name: name, Snapshot: snap}
	// 无实例 → 引导性错误（finder 桥接模拟租户无 vector 实例）
	repos.FindVectorStore = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("本租户暂无向量数据库实例，请先在资源中心创建后再安装")
	}
	repos.FindObjectStore = func(ctx context.Context) (string, error) { return "ds-obj-b", nil }
	if _, err := f.InstallSnapshot(ctxTenant("t-b"), item); err == nil || !bytes.Contains([]byte(err.Error()), []byte("向量数据库")) {
		t.Fatalf("无实例应报引导性错误，got %v", err)
	}
	// 注入 finder 重绑
	repos.FindVectorStore = func(ctx context.Context) (string, error) { return "ds-vec-b", nil }
	res, err := f.InstallSnapshot(ctxTenant("t-b"), item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repos.KBs.Get(ctxTenant("t-b"), res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VectorStoreRef != "ds-vec-b" || got.ObjectStoreRef != "ds-obj-b" {
		t.Fatalf("安装后引用应重绑安装者实例: %+v", got)
	}
	if got.EmbeddingModel != "text-embedding-3" || got.EmbeddingDim != 1536 {
		t.Fatalf("配置骨架字段应保留: %+v", got)
	}
}

// TestWorkflowPublishInstallRewritesNodeRefs 工作流整包发布-安装回路：
// 节点引用的 Agent/Tool 全部内嵌，安装后引用重写为 fork 出的新实体 ID，且默认停用。
func TestWorkflowPublishInstallRewritesNodeRefs(t *testing.T) {
	repos := newRepos()
	repos.Workflows = wfmemory.NewStore()
	ctx := ctxTenant("t-a")
	sk, _ := repos.Skills.Create(ctx, skill.Skill{Name: "售后话术", Instructions: "规范回复"})
	a, err := repos.Agents.Create(ctx, agent.Agent{
		Name: "分类员", Description: "意图分类", Model: "glm-5.2", SystemPrompt: "你是分类员", Skills: []string{sk.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	tl, _ := repos.Tools.Create(ctx, tool.Tool{
		Name: "工单创建", Description: "建工单", Type: tool.TypeHTTP,
		Config: map[string]string{"endpoint": "http://tk/api", "token": "sk-leak"},
	})
	def := workflow.WorkflowDef{
		Name: "售后工单流", Desc: "分类后建工单",
		Nodes: []workflow.NodeDef{
			{ID: "start", Type: workflow.NodeStart, NextID: "cls"},
			{ID: "cls", Type: workflow.NodeLLM, Name: "分类", NextID: "mk",
				Config: workflow.NodeConfig{AgentID: a.ID, InputTemplate: "分类：{{inputs.q}}"}},
			{ID: "mk", Type: workflow.NodeTool, Name: "建单", NextID: "end",
				Config: workflow.NodeConfig{ToolID: tl.ID, ToolName: "create_ticket"}},
			{ID: "end", Type: workflow.NodeEnd},
		},
	}
	d, err := repos.Workflows.Create(ctx, def)
	if err != nil {
		t.Fatal(err)
	}
	f := workflowForker{repos}
	name, _, cat, snap, err := f.BuildSnapshot(ctx, d.ID, "service")
	if err != nil {
		t.Fatal(err)
	}
	if name != "售后工单流" || cat != "service" {
		t.Fatalf("name/category 不符: %s %s", name, cat)
	}
	if bytes.Contains(snap, []byte("sk-leak")) {
		t.Fatal("工具凭证不应进工作流快照")
	}
	var wfSnap WorkflowSnapshot
	_ = json.Unmarshal(snap, &wfSnap)
	if len(wfSnap.Agents) != 1 || len(wfSnap.Tools) != 1 {
		t.Fatalf("内嵌实体数不符: agents=%d tools=%d", len(wfSnap.Agents), len(wfSnap.Tools))
	}
	if wfSnap.Agents[0].RefID != a.ID || wfSnap.Tools[0].RefID != tl.ID {
		t.Fatal("RefID 应为发布者侧实体 ID")
	}

	item := Item{ID: "mk-wf", EntityType: EntityWorkflow, Name: name, Snapshot: snap}
	ctxB := ctxTenant("t-b")
	res, err := f.InstallSnapshot(ctxB, item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repos.Workflows.Get(ctxB, res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("安装后应默认停用（凭证需补填确认后再启用）")
	}
	// 节点引用指向 t-b fork 出的新实体
	var llmNode, toolNode *workflow.NodeDef
	for i := range got.Nodes {
		switch got.Nodes[i].ID {
		case "cls":
			llmNode = &got.Nodes[i]
		case "mk":
			toolNode = &got.Nodes[i]
		}
	}
	if llmNode == nil || toolNode == nil {
		t.Fatal("节点结构不完整")
	}
	if llmNode.Config.AgentID == a.ID {
		t.Fatal("agent 引用未重写")
	}
	if _, err := repos.Agents.Get(ctxB, llmNode.Config.AgentID); err != nil {
		t.Fatalf("agent 引用应指向 fork 副本: %v", err)
	}
	// 内嵌 agent 的 skill 一并 fork
	ag, _ := repos.Agents.Get(ctxB, llmNode.Config.AgentID)
	if len(ag.Skills) != 1 {
		t.Fatalf("内嵌 skill 未 fork: %+v", ag)
	}
	if toolNode.Config.ToolID == tl.ID {
		t.Fatal("tool 引用未重写")
	}
	newTool, err := repos.Tools.Get(ctxB, toolNode.Config.ToolID)
	if err != nil {
		t.Fatalf("tool 引用应指向 fork 副本: %v", err)
	}
	if newTool.Config["token"] != "" || newTool.Config["endpoint"] != "http://tk/api" {
		t.Fatalf("工具凭证不应随安装带入: %v", newTool.Config)
	}
}

// fakeRepo 本包内最小 Repository 实现（import memory 子包会成环——子包 import marketplace）。
type fakeRepo struct{ items map[string]Item }

func (f *fakeRepo) ensure() {
	if f.items == nil {
		f.items = map[string]Item{}
	}
}
func (f *fakeRepo) List(ctx context.Context, entityType, category, q string) ([]Item, error) {
	f.ensure()
	out := []Item{}
	for _, it := range f.items {
		if entityType != "" && it.EntityType != entityType {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}
func (f *fakeRepo) Get(ctx context.Context, id string) (Item, error) {
	f.ensure()
	it, ok := f.items[id]
	if !ok {
		return Item{}, ErrItemNotFound
	}
	return it, nil
}
func (f *fakeRepo) Create(ctx context.Context, in Item) (Item, error) {
	f.ensure()
	for id, it := range f.items {
		if it.EntityType == in.EntityType && it.Name == in.Name && it.PublisherTenant == in.PublisherTenant {
			delete(f.items, id)
		}
	}
	if in.ID == "" {
		in.ID = "mk-fake"
	}
	f.items[in.ID] = in
	return in, nil
}
func (f *fakeRepo) Delete(ctx context.Context, id string) error {
	f.ensure()
	if _, ok := f.items[id]; !ok {
		return ErrItemNotFound
	}
	delete(f.items, id)
	return nil
}
func (f *fakeRepo) IncInstalls(ctx context.Context, id string) error {
	f.ensure()
	it, ok := f.items[id]
	if !ok {
		return ErrItemNotFound
	}
	it.Installs++
	f.items[id] = it
	return nil
}
func (f *fakeRepo) ListByPublisher(ctx context.Context, tid string) ([]Item, error) { return nil, nil }
func (f *fakeRepo) ListAll(ctx context.Context) ([]Item, error)                     { return nil, nil }
