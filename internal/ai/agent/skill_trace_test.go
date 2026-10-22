package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/pkg/provider"
)

// 本包测试不并行；使用独立 SDK recorder 验证实际结束的 span，不向真实后端导出。
func skillTraceRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := tracer
	tracer = tp.Tracer("test.skill")
	t.Cleanup(func() {
		tracer = previous
		_ = tp.Shutdown(context.Background())
	})
	return recorder
}

func spanAttributes(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	attrs := make(map[string]attribute.Value)
	for _, kv := range s.Attributes() {
		attrs[string(kv.Key)] = kv.Value
	}
	return attrs
}

type unavailableSkillRepo struct{ *stubSkillRepo }

func (unavailableSkillRepo) Get(context.Context, string) (skill.Skill, error) {
	return skill.Skill{}, errors.New("postgres://user:private-password@db")
}

func TestSkillTraceMatchesInjectedRevision(t *testing.T) {
	recorder := skillTraceRecorder(t)
	updated := time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC)
	repo := &stubSkillRepo{m: map[string]skill.Skill{
		"one": {ID: "one", Name: "导购", Instructions: "private instruction v1", Enabled: true, UpdatedAt: updated,
			InstalledFrom: "builtin:knowledge-answer@1.0.0#" + strings.Repeat("a", 64)},
	}}
	rt := &Runtime{skills: repo}
	for _, content := range []string{"private instruction v1", "private instruction v2"} {
		sk := repo.m["one"]
		sk.Instructions = content
		repo.m["one"] = sk
		ctx, root := tracer.Start(context.Background(), "agent.run")
		p := &echoFakeProvider{}
		err := rt.runLoop(ctx, p, Agent{ID: "agent-one", Skills: []string{"one"}, MaxSteps: 1}, nil, func(provider.Chunk) {})
		root.End()
		if err != nil || p.system != "# 能力：导购\n"+content {
			t.Fatalf("实际注入失败：%q %v", p.system, err)
		}
		ended := recorder.Ended()
		load, run := ended[len(ended)-2], ended[len(ended)-1]
		if load.Name() != "paas.skill.load" || load.Parent().SpanID() != run.SpanContext().SpanID() || load.SpanContext().TraceID() != run.SpanContext().TraceID() {
			t.Fatal("Skill 加载必须关联当前 Agent Trace")
		}
		attrs := spanAttributes(load)
		hash := sha256.Sum256([]byte(content))
		for key, want := range map[string]string{
			"paas.skill.id": "one", "gen_ai.skill.name": "导购", "gen_ai.skill.source.uri": "paas://skills/one",
			"paas.skill.instructions.sha256": hex.EncodeToString(hash[:]), "paas.skill.load.status": "loaded",
			"paas.skill.template.version": "1.0.0", "paas.skill.template.sha256": strings.Repeat("a", 64),
			"paas.skill.updated_at": updated.Format(time.RFC3339Nano),
		} {
			if attrs[key].AsString() != want {
				t.Fatalf("%s: got %q want %q", key, attrs[key].AsString(), want)
			}
		}
		for _, value := range attrs {
			if strings.Contains(value.String(), "private instruction") {
				t.Fatal("正文不得进入遥测")
			}
		}
		if _, ok := attrs["gen_ai.operation.name"]; ok {
			t.Fatal("直接指令注入不应冒充 GenAI 工具调用")
		}
		runAttrs := spanAttributes(run)
		if !runAttrs["paas.ai.context.ready"].AsBool() || runAttrs["paas.ai.skills.loaded_count"].AsInt64() != 1 {
			t.Fatal("Agent 缺少上下文准备与加载计数")
		}
	}
}

func TestSkillTraceFailureBlocksModelAndPreservesPartialEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		repo skill.Repository
		ids  []string
		kind string
	}{
		{"仓储未接入", nil, []string{"one"}, "skill_store_unavailable"},
		{"技能不存在", &stubSkillRepo{}, []string{"missing"}, "skill_not_found"},
		{"已停用", &stubSkillRepo{m: map[string]skill.Skill{"off": {Enabled: false}}}, []string{"off"}, "skill_disabled"},
		{"存储失败", unavailableSkillRepo{}, []string{"one"}, "skill_store_error"},
		{"第二项失败", &stubSkillRepo{m: map[string]skill.Skill{"one": {ID: "one", Name: "one", Instructions: "ok", Enabled: true}}}, []string{"one", "missing"}, "skill_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := skillTraceRecorder(t)
			ctx, root := tracer.Start(context.Background(), "agent.run")
			p := &echoFakeProvider{}
			err := (&Runtime{skills: tc.repo}).runLoop(ctx, p, Agent{Skills: tc.ids, MaxSteps: 1}, nil, func(provider.Chunk) {})
			root.End()
			if err == nil || p.system != "" {
				t.Fatal("技能失败必须在模型调用前阻断")
			}
			ended := recorder.Ended()
			failed := ended[len(ended)-2]
			if failed.Status().Code != codes.Error || spanAttributes(failed)["error.type"].AsString() != tc.kind {
				t.Fatal("缺少失败类型及错误状态")
			}
			if strings.Contains(failed.Status().Description, "private-password") {
				t.Fatal("底层存储错误不得写入遥测")
			}
			if _, ok := spanAttributes(failed)["paas.skill.instructions.sha256"]; ok {
				t.Fatal("失败加载不得虚构内容快照")
			}
			run := spanAttributes(ended[len(ended)-1])
			if run["paas.ai.context.ready"].AsBool() || run["paas.ai.skills.loaded_count"].AsInt64() != int64(len(tc.ids)-1) {
				t.Fatal("部分加载不应显示为上下文准备完成")
			}
		})
	}
}

func TestAgentTraceSeparatesLoadedSkillsFromContextFailure(t *testing.T) {
	recorder := skillTraceRecorder(t)
	models := maas.NewMemoryStore()
	ctx := context.Background()
	if err := models.CreateModel(ctx, &provider.Model{ID: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := models.CreateChannel(ctx, "echo", &provider.Channel{ID: "echo-channel", Type: maas.ProviderEcho}); err != nil {
		t.Fatal(err)
	}
	a := Agent{ID: "agent-one", Name: "客服", Enabled: true, Model: "echo", MaxSteps: 1,
		Skills: []string{"one"}, KnowledgeBases: []string{"missing"}}
	rt := NewRuntime(&stubRepo{a: a}, models, nil, nil, nil, nil).WithSkills(&stubSkillRepo{m: map[string]skill.Skill{
		"one": {ID: "one", Name: "导购", Instructions: "read-only", Enabled: true},
	}})
	if err := rt.Run(ctx, a.ID, []provider.Message{{Role: "user", Content: "hi"}}, func(provider.Chunk) {
		t.Error("KB 准备失败不得调用模型")
	}); err == nil {
		t.Fatal("预期上下文依赖失败")
	}
	spans := recorder.Ended()
	if len(spans) != 2 || spans[0].Name() != "paas.skill.load" || spans[1].Name() != "agent.run" {
		t.Fatalf("缺失真实 Run 产生的父子 span: %+v", spans)
	}
	attrs := spanAttributes(spans[1])
	if spans[1].Status().Code != codes.Error || attrs["paas.ai.skills.loaded_count"].AsInt64() != 1 || attrs["paas.ai.context.ready"].AsBool() || attrs["gen_ai.agent.name"].AsString() != a.Name {
		t.Fatal("已加载 Skill 不能掩盖上下文失败")
	}
}

func TestSkillTraceDoesNotCaptureUntrustedOriginOrInventVersion(t *testing.T) {
	recorder := skillTraceRecorder(t)
	//nolint:gosec // 合成凭证仅用于验证敏感来源不会进入遥测，不连接该地址。
	rt := &Runtime{skills: &stubSkillRepo{m: map[string]skill.Skill{"one": {
		ID: "one", Name: "one", Instructions: "safe", Enabled: true,
		InstalledFrom: "https://user:secret@example.com/skill?token=secret",
	}}}}
	if _, err := rt.loadSkillInstructions(context.Background(), Agent{}, "one"); err != nil {
		t.Fatal(err)
	}
	attrs := spanAttributes(recorder.Ended()[0])
	if _, ok := attrs["paas.skill.template.version"]; ok {
		t.Fatal("自建/未知来源不得推测模板版本")
	}
	for _, value := range attrs {
		if strings.Contains(value.String(), "secret") {
			t.Fatal("任意来源文本或凭证不得进入遥测")
		}
	}
}

type traceToolProvider struct{ calls int }

func (*traceToolProvider) Name() string { return "local-tool-test" }
func (p *traceToolProvider) Chat(context.Context, provider.ChatRequest) (<-chan provider.Chunk, error) {
	p.calls++
	ch := make(chan provider.Chunk, 1)
	if p.calls == 1 {
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{ID: "call-from-model", Type: "function", Function: provider.ToolCallFunction{Name: "target_tool", Arguments: "{}"}}}}
	} else {
		ch <- provider.Chunk{Content: "done"}
	}
	close(ch)
	return ch, nil
}

func TestToolTraceRecordsStandardOperationAndProviderCallID(t *testing.T) {
	recorder := skillTraceRecorder(t)
	srv := &fakeMCPServer{}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()
	rt := &Runtime{tools: &fakeToolRepo{t: tool.Tool{
		ID: "tool-one", Name: "target_tool", Type: tool.TypeMCP, Enabled: true,
		Config: map[string]string{tool.CfgMCPServerURL: ts.URL},
	}}}
	p := &traceToolProvider{}
	if err := rt.runLoop(context.Background(), p, Agent{Tools: []string{"tool-one"}, MaxSteps: 2}, nil, func(provider.Chunk) {}); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "tool.call" || p.calls != 2 {
		t.Fatalf("工具调用或续轮错误：%d spans %d model calls", len(spans), p.calls)
	}
	attrs := spanAttributes(spans[0])
	if attrs["gen_ai.operation.name"].AsString() != "execute_tool" || attrs["gen_ai.tool.call.id"].AsString() != "call-from-model" {
		t.Fatal("缺少工具标准操作或原始调用 ID")
	}
}
