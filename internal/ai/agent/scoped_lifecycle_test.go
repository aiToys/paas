package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

func TestScopedLoopHardToolLimitAndUsage(t *testing.T) {
	rt := &Runtime{}
	fake := &alwaysToolFake{}
	tools, settled := 0, 0
	hooks := &ToolHooks{MaxToolCalls: 1, BeforeModel: func(_ context.Context, reserve int) (func(provider.Usage, string), error) {
		if reserve <= 0 {
			t.Fatal("必须先预留本轮额度")
		}
		return func(u provider.Usage, source string) {
			settled++
			if source != "estimated" {
				t.Fatalf("fake 未返回实际 usage: %s", source)
			}
		}, nil
	}}
	_, err := rt.toolLoop(context.Background(), fake, Agent{Model: "m", MaxSteps: 10}, []provider.Message{{Role: "user", Content: "检查"}}, nil, map[string]toolInvoker{"lookup": func(context.Context, string) (string, error) { tools++; return "ok", nil }}, nil, hooks)
	if err == nil || tools != 1 || fake.calls != 2 || settled != 2 {
		t.Fatalf("工具或用量限制失效 err=%v tools=%d model=%d settled=%d", err, tools, fake.calls, settled)
	}
}

func TestBudgetRejectsBeforeProviderCall(t *testing.T) {
	rt := &Runtime{}
	fake := &alwaysToolFake{}
	denied := errors.New("budget exhausted")
	_, err := rt.toolLoop(context.Background(), fake, Agent{Model: "m", MaxSteps: 2}, nil, nil, nil, nil, &ToolHooks{BeforeModel: func(context.Context, int) (func(provider.Usage, string), error) { return nil, denied }})
	if !errors.Is(err, denied) || fake.calls != 0 {
		t.Fatalf("超预算仍调用供应商 err=%v calls=%d", err, fake.calls)
	}
}
