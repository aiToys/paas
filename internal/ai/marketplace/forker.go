package marketplace

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/tool"
)

// 本文件是四类实体的发布/安装 fork 编排（依赖各实体 Repository，依赖方向单向：
// marketplace → agent/skill/prompt/tool，各实体包不 import marketplace，无环）。
//
// 同名冲突：安装时目标租户已有同名实体自动加后缀 -2/-3…（与 Dify fork 模式一致）。

// Repos 聚合四实体仓储（cmd/core 装配注入）。
type Repos struct {
	Agents  agent.Repository
	Skills  skill.Repository
	Prompts prompt.Repository
	Tools   tool.Repository
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
	var t tool.Tool
	if err := json.Unmarshal(item.Snapshot, &t); err != nil {
		return InstallResult{}, err
	}
	list, _ := f.repos.Tools.List(ctx)
	taken := map[string]bool{}
	for _, x := range list {
		taken[x.Name] = true
	}
	t.Name = uniqueName(t.Name, func(n string) bool { return taken[n] })
	t.InstalledFrom = item.ID
	created, err := f.repos.Tools.Create(ctx, t)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntityTool, EntityID: created.ID, Name: created.Name}, nil
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
	var snap AgentSnapshot
	if err := json.Unmarshal(item.Snapshot, &snap); err != nil {
		return InstallResult{}, err
	}
	var a agent.Agent
	if err := json.Unmarshal(snap.Agent, &a); err != nil {
		return InstallResult{}, err
	}
	// fork 嵌套 skills，直接收集新 ID（快照无 ID，引用列表重建为 fork 后的新 ID）
	newSkills := []string{}
	for _, raw := range snap.Skills {
		var sk skill.Skill
		if err := json.Unmarshal(raw, &sk); err != nil {
			continue
		}
		list, _ := f.repos.Skills.List(ctx)
		taken := map[string]bool{}
		for _, x := range list {
			taken[x.Name] = true
		}
		sk.Name = uniqueName(sk.Name, func(n string) bool { return taken[n] })
		sk.InstalledFrom = item.ID
		created, err := f.repos.Skills.Create(ctx, sk)
		if err != nil {
			return InstallResult{}, err
		}
		newSkills = append(newSkills, created.ID)
	}
	// fork 嵌套 prompt（PromptRef 按 name 引用，直接用 fork 后的名称）
	if snap.Prompt != nil {
		var p prompt.Prompt
		if err := json.Unmarshal(*snap.Prompt, &p); err == nil {
			list, _ := f.repos.Prompts.List(ctx)
			taken := map[string]bool{}
			for _, x := range list {
				taken[x.Name] = true
			}
			p.Name = uniqueName(p.Name, func(n string) bool { return taken[n] })
			p.InstalledFrom = item.ID
			if _, err := f.repos.Prompts.Create(ctx, p); err == nil {
				a.PromptRef = p.Name
			}
		}
	}
	// fork 嵌套 tools
	newTools := []string{}
	for _, raw := range snap.Tools {
		var t tool.Tool
		if err := json.Unmarshal(raw, &t); err != nil {
			continue
		}
		list, _ := f.repos.Tools.List(ctx)
		taken := map[string]bool{}
		for _, x := range list {
			taken[x.Name] = true
		}
		t.Name = uniqueName(t.Name, func(n string) bool { return taken[n] })
		t.InstalledFrom = item.ID
		created, err := f.repos.Tools.Create(ctx, t)
		if err != nil {
			return InstallResult{}, err
		}
		newTools = append(newTools, created.ID)
	}
	a.Skills = newSkills
	a.Tools = newTools
	a.InstalledFrom = item.ID
	agents, _ := f.repos.Agents.List(ctx)
	taken := map[string]bool{}
	for _, x := range agents {
		taken[x.Name] = true
	}
	a.Name = uniqueName(a.Name, func(n string) bool { return taken[n] })
	created, err := f.repos.Agents.Create(ctx, a)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{EntityType: EntityAgent, EntityID: created.ID, Name: created.Name}, nil
}

// RegisterAllForkers 注册全部四类实体编排（cmd/core 装配入口）。
func RegisterAllForkers(h *Handler, repos *Repos) {
	h.RegisterForker(EntitySkill, skillForker{repos})
	h.RegisterForker(EntityPrompt, promptForker{repos})
	h.RegisterForker(EntityTool, toolForker{repos})
	h.RegisterForker(EntityAgent, agentForker{repos})
}
