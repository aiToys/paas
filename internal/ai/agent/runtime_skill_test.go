package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/pkg/provider"
)

// echoFakeProvider 单轮直出：把收到的 system prompt 原样返回（用于断言注入内容）。
type echoFakeProvider struct{ system string }

func (p *echoFakeProvider) Name() string { return "echo-fake" }
func (p *echoFakeProvider) Chat(_ context.Context, req provider.ChatRequest) (<-chan provider.Chunk, error) {
	for _, m := range req.Messages {
		if m.Role == "system" {
			p.system = m.Content
		}
	}
	ch := make(chan provider.Chunk, 1)
	ch <- provider.Chunk{Content: "ok"}
	close(ch)
	return ch, nil
}

// stubSkillRepo 仅实现 Get（含启用/禁用分支）。
type stubSkillRepo struct{ m map[string]skill.Skill }

func (s *stubSkillRepo) List(context.Context) ([]skill.Skill, error)        { panic("unused") }
func (s *stubSkillRepo) ListAll(context.Context) ([]skill.Skill, error)     { panic("unused") }
func (s *stubSkillRepo) Get(_ context.Context, id string) (skill.Skill, error) {
	sk, ok := s.m[id]
	if !ok {
		return skill.Skill{}, skill.ErrSkillNotFound
	}
	return sk, nil
}
func (s *stubSkillRepo) Create(context.Context, skill.Skill) (skill.Skill, error) { panic("unused") }
func (s *stubSkillRepo) Update(context.Context, skill.Skill) (skill.Skill, error) { panic("unused") }
func (s *stubSkillRepo) Delete(context.Context, string) error                   { panic("unused") }
func (s *stubSkillRepo) SkillsCount(context.Context) (int, error)               { panic("unused") }

// Skill 指令注入 system prompt：启用的 skill 内容出现，禁用/缺失的静默跳过。
func TestBuildSystemInjectsSkills(t *testing.T) {
	rt := &Runtime{skills: &stubSkillRepo{m: map[string]skill.Skill{
		"sk-on":   {ID: "sk-on", Name: "写周报", Instructions: "每周五输出 markdown 周报", Enabled: true},
		"sk-off":  {ID: "sk-off", Name: "禁用项", Instructions: "不应出现", Enabled: false},
	}}}
	fp := &echoFakeProvider{}
	a := Agent{
		ID: "a", Enabled: true, Model: "m", MaxSteps: 1,
		SystemPrompt: "你是助手",
		Skills:       []string{"sk-on", "sk-off", "sk-missing"},
	}
	err := rt.runLoop(context.Background(), fp, a, []provider.Message{{Role: "user", Content: "hi"}}, func(provider.Chunk) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.system, "写周报") || !strings.Contains(fp.system, "每周五输出 markdown 周报") {
		t.Errorf("启用的 skill 未注入 system prompt: %q", fp.system)
	}
	if strings.Contains(fp.system, "不应出现") {
		t.Errorf("禁用的 skill 被注入: %q", fp.system)
	}
}
