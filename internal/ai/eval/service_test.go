package eval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/ai/eval"
	"github.com/aitoys/paas/internal/ai/eval/memory"
	"github.com/aitoys/paas/pkg/provider"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeRunner 收集输出到固定串，模拟 agent.Runtime.Run。
type fakeRunner struct{ output string }

func (f fakeRunner) Run(_ context.Context, _ string, _ []provider.Message, onChunk func(provider.Chunk)) error {
	onChunk(provider.Chunk{Role: "assistant"})
	onChunk(provider.Chunk{Content: f.output})
	return nil
}

// RunAll 跑多用例：收集输出 + 按 matchType 评分。
func TestServiceRunAll(t *testing.T) {
	store := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-a")
	// 两条用例：一条 contains 通过，一条 exact 失败。
	store.Create(ctx, eval.EvalCase{AgentID: "ag", Name: "c1", Input: "q1", Expected: "天气", MatchType: eval.MatchContains})
	store.Create(ctx, eval.EvalCase{AgentID: "ag", Name: "c2", Input: "q2", Expected: "晴朗", MatchType: eval.MatchExact})

	svc := eval.NewService(store, fakeRunner{output: "今天天气不错"})
	results, err := svc.RunAll(ctx, "ag")
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("应 2 条结果，got %d", len(results))
	}
	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	if passed != 1 {
		t.Fatalf("应 1 通过 1 失败，got passed=%d", passed)
	}
	// 失败条目应有 reason + output 回填。
	var failed eval.EvalResult
	for _, r := range results {
		if !r.Passed {
			failed = r
		}
	}
	if failed.Output != "今天天气不错" || !strings.Contains(failed.Reason, "不完全相等") {
		t.Fatalf("失败结果回填错: %+v", failed)
	}
}

// Runner 为 nil 时 RunAll 返错误（runtime 未装配保护）。
func TestServiceRunAllNoRunner(t *testing.T) {
	store := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-a")
	store.Create(ctx, eval.EvalCase{AgentID: "ag", Input: "q", Expected: "x", MatchType: eval.MatchContains})
	svc := eval.NewService(store, nil)
	if _, err := svc.RunAll(ctx, "ag"); err == nil {
		t.Fatal("runner nil 应返错")
	}
}

// 无用例时返空切片 + nil（不报错）。
func TestServiceRunAllEmpty(t *testing.T) {
	store := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-a")
	svc := eval.NewService(store, fakeRunner{output: "x"})
	results, err := svc.RunAll(ctx, "ag")
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("无用例应空切片，got %d", len(results))
	}
}
