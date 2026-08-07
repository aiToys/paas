package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

// twoRoundFakeProvider 模拟 2 轮工具循环：
//   - 第 1 轮（messages 末尾非 role=tool）：返回 tool_call（lookup）
//   - 第 2 轮（messages 含 role=tool 结果）：返回最终答案「晴天」
//
// 据此验证 runtime 回放 assistant.tool_calls + 追加 role=tool 结果后正确续轮到最终答案。
type twoRoundFakeProvider struct{ calls int }

func (p *twoRoundFakeProvider) Name() string { return "two-round-fake" }
func (p *twoRoundFakeProvider) Chat(_ context.Context, req provider.ChatRequest) (<-chan provider.Chunk, error) {
	p.calls++
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		toolSeen := len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "tool"
		if !toolSeen {
			ch <- provider.Chunk{Role: "assistant"}
			ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{
				ID: "call_1", Type: "function",
				Function: provider.ToolCallFunction{Name: "lookup", Arguments: `{"q":"天气"}`},
			}}, FinishReason: "tool_calls"}
			return
		}
		ch <- provider.Chunk{Role: "assistant"}
		ch <- provider.Chunk{Content: "晴天"}
	}()
	return ch, nil
}

// alwaysToolFake 每轮都返回 tool_call（永不给最终答案），验证 MaxSteps 上限兜底。
type alwaysToolFake struct{ calls int }

func (p *alwaysToolFake) Name() string { return "always-tool" }
func (p *alwaysToolFake) Chat(_ context.Context, _ provider.ChatRequest) (<-chan provider.Chunk, error) {
	p.calls++
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{
			ID: "c", Type: "function",
			Function: provider.ToolCallFunction{Name: "lookup", Arguments: "{}"},
		}}, FinishReason: "tool_calls"}
	}()
	return ch, nil
}

// stubRepo 仅实现 Run 用到的 Get；其他方法 panic 以防误用。
type stubRepo struct{ a Agent }

func (s *stubRepo) Get(_ context.Context, _ string) (Agent, error) { return s.a, nil }
func (s *stubRepo) List(context.Context) ([]Agent, error)          { panic("unused") }
func (s *stubRepo) Create(context.Context, Agent) (Agent, error)   { panic("unused") }
func (s *stubRepo) Update(context.Context, Agent) (Agent, error)   { panic("unused") }
func (s *stubRepo) Delete(context.Context, string) error           { panic("unused") }
func (s *stubRepo) AgentsCount(context.Context) (int, error)       { panic("unused") }

// tool_call（无 invoker，走未知工具分支）→ 回放 role=tool → 续轮到最终答案。
// 验证：多轮循环正确驱动，content 流式累计，工具进度入 reasoning，2 轮结束。
func TestRunLoopMultiRoundUnknownTool(t *testing.T) {
	fp := &twoRoundFakeProvider{}
	rt := &Runtime{agents: &stubRepo{a: Agent{ID: "a", Enabled: true, Model: "m", MaxSteps: 5}}}
	var content, reasoning strings.Builder
	err := rt.runLoop(context.Background(), fp, Agent{ID: "a", Model: "m", MaxSteps: 5},
		[]provider.Message{{Role: "user", Content: "查天气"}},
		func(c provider.Chunk) {
			content.WriteString(c.Content)
			reasoning.WriteString(c.Reasoning)
		})
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if fp.calls != 2 {
		t.Fatalf("应 2 轮结束（tool_call→最终答案），实际 %d 轮", fp.calls)
	}
	if content.String() != "晴天" {
		t.Fatalf("最终答案应流式累计为「晴天」，got %q", content.String())
	}
	if !strings.Contains(reasoning.String(), "未知工具 lookup") {
		t.Fatalf("应推送未知工具提示，got %q", reasoning.String())
	}
}

// LLM 持续请求工具（永不给答案）→ MaxSteps 上限兜底正常结束，不无限循环。
func TestRunLoopMaxStepsCap(t *testing.T) {
	fp := &alwaysToolFake{}
	rt := &Runtime{agents: &stubRepo{}}
	err := rt.runLoop(context.Background(), fp, Agent{ID: "a", Model: "m", MaxSteps: 3},
		[]provider.Message{{Role: "user", Content: "x"}}, func(provider.Chunk) {})
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if fp.calls != 3 {
		t.Fatalf("应跑满 MaxSteps=3 兜底，实际 %d", fp.calls)
	}
}
