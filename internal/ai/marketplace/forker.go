package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/ai/workflow"
)

// 本文件是六类实体的发布/安装 fork 编排（依赖各实体 Repository，依赖方向单向：
// marketplace → agent/skill/prompt/tool/knowledgebase/workflow，各实体包不 import marketplace，无环）。
//
// 同名冲突：安装时目标租户已有同名实体自动加后缀 -2/-3…（与 Dify fork 模式一致）。

// Repos 聚合六实体仓储（cmd/core 装配注入）。
// FindVectorStore/FindObjectStore 是 KB 安装时的实例重绑桥接（cmd/core 闭包 dataservice
// List 按 kind 过滤取租户第一个实例）——marketplace 不 import dataservice，保持依赖单向。
type Repos struct {
	Agents          agent.Repository
	Skills          skill.Repository
	Prompts         prompt.Repository
	Tools           tool.Repository
	KBs             knowledgebase.Repository
	Workflows       workflow.Repository
	FindVectorStore func(ctx context.Context) (string, error)
	FindObjectStore func(ctx context.Context) (string, error)
}

// uniqueName 目标租户内唯一化：同名加 -2/-3… 后缀（existsFn 返回 true 表示占用）。
func uniqueName(base string, existsFn func(string) bool) string {
	if !existsFn(base) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !existsFn(candidate) {
			return candidate
		}
	}
}

// —— Skill ——

type skillForker struct{ repos *Repos }

func (f skillForker) BuildSnapshot(ctx context.Context, entityID, category string) (string, string, string, json.RawMessage, error) {
	sk, err := f.repos.Skills.Get(ctx, entityID)
	if err != nil {
		return "", "", "", nil, err
	}
	if category == "" {
		category = sk.Category
	}
	// 快照剥离租户/来源字段（fork 时重填）
	sk.TenantID, sk.InstalledFrom, sk.ID = "", "", ""
	snap, err := json.Marshal(sk)
	if err != nil {
		return "", "", "", nil, err
	}
	return sk.Name, sk.Description, category, snap, nil
}

func (f skillForker) InstallSnapshot(ctx context.Context, item Item) (InstallResult, error) {
	var sk skill.Skill
	if err := json.Unmarshal(item.Snapshot, &sk); err != nil {
		return InstallResult{}, err
	}
	list, _ := f.repos.Skills.List(ctx)
	taken := map[string]bool{}
	for _, s := range list {
		taken[s.Name] = true
	}
	sk.Name = uniqueName(sk.Name, func(n string) bool { return taken[n] })
	sk.InstalledFrom = item.ID
	created, err := f.repos.Skills.Create(ctx, sk)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntitySkill, EntityID: created.ID, Name: created.Name}, nil
}

// —— Prompt ——

type promptForker struct{ repos *Repos }

func (f promptForker) BuildSnapshot(ctx context.Context, entityID, category string) (string, string, string, json.RawMessage, error) {
	// Prompt 是多版本模型：按 ID 找到该版本，发布其所属 name 的 active 版本
	p, err := f.repos.Prompts.Get(ctx, entityID)
	if err != nil {
		return "", "", "", nil, err
	}
	if !p.Active {
		active, err := f.repos.Prompts.GetActive(ctx, p.Name)
		if err != nil {
			return "", "", "", nil, err
		}
		p = active
	}
	if category == "" {
		category = p.Category
	}
	p.TenantID, p.InstalledFrom, p.ID = "", "", ""
	snap, err := json.Marshal(p)
	if err != nil {
		return "", "", "", nil, err
	}
	return p.Name, p.Template, category, snap, nil
}

func (f promptForker) InstallSnapshot(ctx context.Context, item Item) (InstallResult, error) {
	var p prompt.Prompt
	if err := json.Unmarshal(item.Snapshot, &p); err != nil {
		return InstallResult{}, err
	}
	list, _ := f.repos.Prompts.List(ctx)
	taken := map[string]bool{}
	for _, x := range list {
		taken[x.Name] = true
	}
	p.Name = uniqueName(p.Name, func(n string) bool { return taken[n] })
	p.InstalledFrom = item.ID
	created, err := f.repos.Prompts.Create(ctx, p)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntityPrompt, EntityID: created.ID, Name: created.Name}, nil
}

// —— Tool ——

type toolForker struct{ repos *Repos }

func (f toolForker) BuildSnapshot(ctx context.Context, entityID, category string) (string, string, string, json.RawMessage, error) {
	t, err := f.repos.Tools.Get(ctx, entityID)
	if err != nil {
		return "", "", "", nil, err
	}
	if category == "" {
		category = t.Category
	}
	t.TenantID, t.InstalledFrom, t.ID = "", "", ""
	t.Config = SanitizeConfig(t.Config) // 凭证不进广场
	snap, err := json.Marshal(t)
	if err != nil {
		return "", "", "", nil, err
	}
	return t.Name, t.Description, category, snap, nil
}

func (f toolForker) InstallSnapshot(ctx context.Context, item Item) (InstallResult, error) {
	newID, name, err := installToolJSON(ctx, f.repos, item.Snapshot, item.ID)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntityTool, EntityID: newID, Name: name}, nil
}

// installToolJSON fork 单个 Tool 快照（toolForker 与 workflowForker 共用），返回新实体 ID + 最终名。
func installToolJSON(ctx context.Context, repos *Repos, raw json.RawMessage, itemID string) (string, string, error) {
	var t tool.Tool
	if err := json.Unmarshal(raw, &t); err != nil {
		return "", "", err
	}
	list, _ := repos.Tools.List(ctx)
	taken := map[string]bool{}
	for _, x := range list {
		taken[x.Name] = true
	}
	t.Name = uniqueName(t.Name, func(n string) bool { return taken[n] })
	t.InstalledFrom = itemID
	created, err := repos.Tools.Create(ctx, t)
	if err != nil {
		return "", "", err
	}
	return created.ID, created.Name, nil
}

// —— Agent 整包 ——

type agentForker struct{ repos *Repos }

func (f agentForker) BuildSnapshot(ctx context.Context, entityID, category string) (string, string, string, json.RawMessage, error) {
	a, err := f.repos.Agents.Get(ctx, entityID)
	if err != nil {
		return "", "", "", nil, err
	}
	if category == "" {
		category = a.Category
	}
	snap := AgentSnapshot{Agent: nil}

	// 内嵌 skills
	skList, _ := f.repos.Skills.List(ctx)
	skByID := map[string]bool{}
	for _, s := range skList {
		skByID[s.ID] = true
	}
	for _, id := range a.Skills {
		if !skByID[id] {
			continue // 引用缺失静默跳过（与 runtime 注入降级语义一致）
		}
		s, err := f.repos.Skills.Get(ctx, id)
		if err != nil {
			continue
		}
		s.TenantID, s.InstalledFrom, s.ID = "", "", ""
		raw, _ := json.Marshal(s)
		snap.Skills = append(snap.Skills, raw)
	}
	// 内嵌 active prompt（PromptRef 非空时）
	if a.PromptRef != "" {
		if p, err := f.repos.Prompts.GetActive(ctx, a.PromptRef); err == nil {
			p.TenantID, p.InstalledFrom, p.ID = "", "", ""
			raw, _ := json.Marshal(p)
			pr := json.RawMessage(raw)
			snap.Prompt = &pr
		}
	}
	// 内嵌 tools（脱敏）
	toolList, _ := f.repos.Tools.List(ctx)
	toolByID := map[string]bool{}
	for _, t := range toolList {
		toolByID[t.ID] = true
	}
	for _, id := range a.Tools {
		if !toolByID[id] {
			continue
		}
		t, err := f.repos.Tools.Get(ctx, id)
		if err != nil {
			continue
		}
		t.TenantID, t.InstalledFrom, t.ID = "", "", ""
		t.Config = SanitizeConfig(t.Config)
		raw, _ := json.Marshal(t)
		snap.Tools = append(snap.Tools, raw)
	}
	// Agent 本体（KB 引用不进广场——含租户数据，安装者自绑）
	a.TenantID, a.InstalledFrom, a.ID = "", "", ""
	a.KnowledgeBases = nil
	aRaw, _ := json.Marshal(a)
	snap.Agent = aRaw
	out, err := json.Marshal(snap)
	if err != nil {
		return "", "", "", nil, err
	}
	return a.Name, a.Description, category, out, nil
}

func (f agentForker) InstallSnapshot(ctx context.Context, item Item) (InstallResult, error) {
	newID, name, err := installAgentBundle(ctx, f.repos, item.Snapshot, item.ID)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntityAgent, EntityID: newID, Name: name}, nil
}

// installAgentBundle fork Agent 整包（agentForker 与 workflowForker 共用），
// 返回新 Agent ID + 最终名。嵌套 skills/prompt/tools 全部 fork 并重写引用。
func installAgentBundle(ctx context.Context, repos *Repos, snapRaw json.RawMessage, itemID string) (string, string, error) {
	var snap AgentSnapshot
	if err := json.Unmarshal(snapRaw, &snap); err != nil {
		return "", "", err
	}
	var a agent.Agent
	if err := json.Unmarshal(snap.Agent, &a); err != nil {
		return "", "", err
	}
	// fork 嵌套 skills，直接收集新 ID（快照无 ID，引用列表重建为 fork 后的新 ID）
	newSkills := []string{}
	for _, raw := range snap.Skills {
		var sk skill.Skill
		if err := json.Unmarshal(raw, &sk); err != nil {
			continue
		}
		list, _ := repos.Skills.List(ctx)
		taken := map[string]bool{}
		for _, x := range list {
			taken[x.Name] = true
		}
		sk.Name = uniqueName(sk.Name, func(n string) bool { return taken[n] })
		sk.InstalledFrom = itemID
		created, err := repos.Skills.Create(ctx, sk)
		if err != nil {
			return "", "", err
		}
		newSkills = append(newSkills, created.ID)
	}
	// fork 嵌套 prompt（PromptRef 按 name 引用，直接用 fork 后的名称）
	if snap.Prompt != nil {
		var p prompt.Prompt
		if err := json.Unmarshal(*snap.Prompt, &p); err == nil {
			list, _ := repos.Prompts.List(ctx)
			taken := map[string]bool{}
			for _, x := range list {
				taken[x.Name] = true
			}
			p.Name = uniqueName(p.Name, func(n string) bool { return taken[n] })
			p.InstalledFrom = itemID
			if _, err := repos.Prompts.Create(ctx, p); err == nil {
				a.PromptRef = p.Name
			}
		}
	}
	// fork 嵌套 tools（复用 installToolJSON；快照自产自销，坏 JSON 走 fail-fast）
	newTools := []string{}
	for _, raw := range snap.Tools {
		id, _, err := installToolJSON(ctx, repos, raw, itemID)
		if err != nil {
			return "", "", err
		}
		newTools = append(newTools, id)
	}
	a.Skills = newSkills
	a.Tools = newTools
	a.InstalledFrom = itemID
	agents, _ := repos.Agents.List(ctx)
	taken := map[string]bool{}
	for _, x := range agents {
		taken[x.Name] = true
	}
	a.Name = uniqueName(a.Name, func(n string) bool { return taken[n] })
	created, err := repos.Agents.Create(ctx, a)
	if err != nil {
		return "", "", err
	}
	return created.ID, created.Name, nil
}

// —— 知识库（配置骨架） ——

type kbForker struct{ repos *Repos }

// kbDefaultCategory KB 实体无 Category 字段，发布未显式选分类时兜底。
const kbDefaultCategory = "general"

func (f kbForker) BuildSnapshot(ctx context.Context, entityID, category string) (string, string, string, json.RawMessage, error) {
	kb, err := f.repos.KBs.Get(ctx, entityID)
	if err != nil {
		return "", "", "", nil, err
	}
	if category == "" {
		category = kbDefaultCategory
	}
	// 数据服务引用是租户数据不进快照（与 Tool 凭证同语义），安装时重绑安装者实例
	kb.TenantID, kb.AppID, kb.ID = "", "", ""
	kb.VectorStoreRef, kb.ObjectStoreRef = "", ""
	kb.CreatedAt, kb.UpdatedAt = time.Time{}, time.Time{}
	snap, err := json.Marshal(kb)
	if err != nil {
		return "", "", "", nil, err
	}
	// KB 无描述字段，按关键配置自动派生（广场卡片/详情展示）
	desc := fmt.Sprintf("嵌入 %s · %d 维 · TopK %d（配置模板，文档与实例引用不随安装带入）",
		kb.EmbeddingModel, kb.EmbeddingDim, kb.RetrieverConfig.TopK)
	return kb.Name, desc, category, snap, nil
}

func (f kbForker) InstallSnapshot(ctx context.Context, item Item) (InstallResult, error) {
	var kb knowledgebase.KnowledgeBase
	if err := json.Unmarshal(item.Snapshot, &kb); err != nil {
		return InstallResult{}, err
	}
	if f.repos.FindVectorStore == nil || f.repos.FindObjectStore == nil {
		return InstallResult{}, fmt.Errorf("广场未注入数据服务发现桥接，无法安装知识库")
	}
	vs, err := f.repos.FindVectorStore(ctx)
	if err != nil {
		return InstallResult{}, err
	}
	os, err := f.repos.FindObjectStore(ctx)
	if err != nil {
		return InstallResult{}, err
	}
	kb.VectorStoreRef, kb.ObjectStoreRef = vs, os
	list, _ := f.repos.KBs.List(ctx)
	taken := map[string]bool{}
	for _, x := range list {
		taken[x.Name] = true
	}
	kb.Name = uniqueName(kb.Name, func(n string) bool { return taken[n] })
	created, err := f.repos.KBs.Create(ctx, kb)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntityKnowledgebase, EntityID: created.ID, Name: created.Name}, nil
}

// —— 工作流整包 ——

type workflowForker struct{ repos *Repos }

func (f workflowForker) BuildSnapshot(ctx context.Context, entityID, category string) (string, string, string, json.RawMessage, error) {
	d, err := f.repos.Workflows.Get(ctx, entityID)
	if err != nil {
		return "", "", "", nil, err
	}
	if category == "" {
		category = kbDefaultCategory
	}
	snap := WorkflowSnapshot{}
	agentF := agentForker(f) // 同一字段布局，类型转换即可（staticcheck S1016）
	// 存在性预检（同 agentForker 先例：List 建 ID 集，缺失引用静默不进快照）
	agList, _ := f.repos.Agents.List(ctx)
	agByID := map[string]bool{}
	for _, a := range agList {
		agByID[a.ID] = true
	}
	tlList, _ := f.repos.Tools.List(ctx)
	toolByID := map[string]bool{}
	for _, t := range tlList {
		toolByID[t.ID] = true
	}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Config.AgentID != "" && agByID[n.Config.AgentID] && !snap.hasAgent(n.Config.AgentID) {
			// 复用 agentForker 快照（AgentSnapshot 含内嵌 skills/prompt/tools，全脱敏）
			_, _, _, raw, err := agentF.BuildSnapshot(ctx, n.Config.AgentID, "")
			if err == nil {
				snap.Agents = append(snap.Agents, EmbeddedAgent{RefID: n.Config.AgentID, Snap: unmarshalAgentSnapshot(raw)})
			}
		}
		if n.Config.ToolID != "" && toolByID[n.Config.ToolID] && !snap.hasTool(n.Config.ToolID) {
			t, err := f.repos.Tools.Get(ctx, n.Config.ToolID)
			if err == nil {
				t.TenantID, t.InstalledFrom, t.ID = "", "", ""
				t.Config = SanitizeConfig(t.Config)
				if raw, err := json.Marshal(t); err == nil {
					snap.Tools = append(snap.Tools, EmbeddedTool{RefID: n.Config.ToolID, Tool: raw})
				}
			}
		}
	}
	d.TenantID, d.ID = "", ""
	wfRaw, _ := json.Marshal(d)
	snap.Workflow = wfRaw
	out, err := json.Marshal(snap)
	if err != nil {
		return "", "", "", nil, err
	}
	nodeTypes := map[string]int{}
	for _, n := range d.Nodes {
		nodeTypes[n.Type]++
	}
	parts := []string{}
	for _, t := range []string{workflow.NodeLLM, workflow.NodeTool, workflow.NodeCond, workflow.NodeApprove} {
		if nodeTypes[t] > 0 {
			parts = append(parts, fmt.Sprintf("%d×%s", nodeTypes[t], nodeTypeLabel(t)))
		}
	}
	desc := d.Desc
	if desc == "" {
		desc = strings.Join(parts, " + ")
	}
	return d.Name, desc, category, out, nil
}

// hasAgent/hasTool 防同一引用重复内嵌（多节点引用同一 agent）。
func (s *WorkflowSnapshot) hasAgent(refID string) bool {
	for _, a := range s.Agents {
		if a.RefID == refID {
			return true
		}
	}
	return false
}

func (s *WorkflowSnapshot) hasTool(refID string) bool {
	for _, t := range s.Tools {
		if t.RefID == refID {
			return true
		}
	}
	return false
}

// unmarshalAgentSnapshot 把 agentForker 产出的 AgentSnapshot JSON 反序列化回结构
// （复用 BuildSnapshot 产物的唯一途径；失败返回零值由调用方 err 分支忽略——自产 JSON 实际不会失败）。
func unmarshalAgentSnapshot(raw json.RawMessage) AgentSnapshot {
	var snap AgentSnapshot
	_ = json.Unmarshal(raw, &snap)
	return snap
}

func nodeTypeLabel(t string) string {
	switch t {
	case workflow.NodeLLM:
		return "LLM"
	case workflow.NodeTool:
		return "工具"
	case workflow.NodeCond:
		return "条件"
	case workflow.NodeApprove:
		return "确认"
	}
	return t
}

func (f workflowForker) InstallSnapshot(ctx context.Context, item Item) (InstallResult, error) {
	var snap WorkflowSnapshot
	if err := json.Unmarshal(item.Snapshot, &snap); err != nil {
		return InstallResult{}, err
	}
	var d workflow.WorkflowDef
	if err := json.Unmarshal(snap.Workflow, &d); err != nil {
		return InstallResult{}, err
	}
	agentMap := map[string]string{} // 旧 RefID → fork 后新 ID
	for _, ea := range snap.Agents {
		raw, err := json.Marshal(ea.Snap)
		if err != nil {
			continue
		}
		newID, _, err := installAgentBundle(ctx, f.repos, raw, item.ID)
		if err != nil {
			return InstallResult{}, err
		}
		agentMap[ea.RefID] = newID
	}
	toolMap := map[string]string{}
	for _, et := range snap.Tools {
		newID, _, err := installToolJSON(ctx, f.repos, et.Tool, item.ID)
		if err != nil {
			return InstallResult{}, err
		}
		toolMap[et.RefID] = newID
	}
	// 重写节点引用（快照未覆盖的引用保留原 ID：运行期 fail-fast 提示换绑，不静默丢节点）
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.Config.AgentID != "" {
			if newID, ok := agentMap[n.Config.AgentID]; ok {
				n.Config.AgentID = newID
			}
		}
		if n.Config.ToolID != "" {
			if newID, ok := toolMap[n.Config.ToolID]; ok {
				n.Config.ToolID = newID
			}
		}
	}
	list, _ := f.repos.Workflows.List(ctx)
	taken := map[string]bool{}
	for _, x := range list {
		taken[x.Name] = true
	}
	d.Name = uniqueName(d.Name, func(n string) bool { return taken[n] })
	d.Enabled = false // 安装后默认停用：引用 agent/工具的凭证需补填，用户确认后再启用
	created, err := f.repos.Workflows.Create(ctx, d)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntityWorkflow, EntityID: created.ID, Name: created.Name}, nil
}

// RegisterAllForkers 注册全部六类实体编排（cmd/core 装配入口）。
func RegisterAllForkers(h *Handler, repos *Repos) {
	h.RegisterForker(EntitySkill, skillForker{repos})
	h.RegisterForker(EntityPrompt, promptForker{repos})
	h.RegisterForker(EntityTool, toolForker{repos})
	h.RegisterForker(EntityAgent, agentForker{repos})
	h.RegisterForker(EntityKnowledgebase, kbForker{repos})
	h.RegisterForker(EntityWorkflow, workflowForker{repos})
}
