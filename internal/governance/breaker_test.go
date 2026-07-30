package governance

import (
	"testing"
	"time"
)

// TestEvaluateBreaker_Determinism 同一 (breaker, now) 多次评估结果一致。
func TestEvaluateBreaker_Determinism(t *testing.T) {
	b := CircuitBreaker{
		ID: "cb-1", Name: "b", ServiceID: "svc-1",
		Strategy: StrategyErrorRate, Threshold: 50,
		MinRequests: 10, WindowSecs: 60, Enabled: true,
	}
	now := time.Unix(1_700_000_000, 0)
	stats1, state1 := EvaluateBreaker(b, now)
	stats2, state2 := EvaluateBreaker(b, now)
	if stats1 != stats2 || state1 != state2 {
		t.Fatalf("评估不确定: (%+v,%s) vs (%+v,%s)", stats1, state1, stats2, state2)
	}
}

// TestEvaluateBreaker_Disabled 禁用的熔断器恒 closed 且统计清零。
func TestEvaluateBreaker_Disabled(t *testing.T) {
	b := CircuitBreaker{ID: "cb-x", Strategy: StrategyErrorRate, Threshold: 50,
		MinRequests: 5, WindowSecs: 60, Enabled: false}
	stats, state := EvaluateBreaker(b, time.Unix(1_700_000_000, 0))
	if state != StateClosed || stats != (WindowStats{}) {
		t.Fatalf("禁用熔断器应 closed + 空统计，got %s %+v", state, stats)
	}
}

// TestEvaluateBreaker_StatesReachable 跨足够多时间桶，三态 + 不足态都能出现。
func TestEvaluateBreaker_StatesReachable(t *testing.T) {
	b := CircuitBreaker{
		ID: "cb-reach", Name: "b", ServiceID: "svc-1",
		Strategy: StrategyErrorRate, Threshold: 50,
		MinRequests: 10, WindowSecs: 60, Enabled: true,
	}
	seen := map[string]bool{}
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5000; i++ {
		_, state := EvaluateBreaker(b, base.Add(time.Duration(i)*60*time.Second))
		seen[state] = true
	}
	for _, want := range []string{StateClosed, StateOpen, StateHalfOpen} {
		if !seen[want] {
			t.Errorf("状态未在 5000 桶内出现: %s (seen=%v)", want, seen)
		}
	}
}

// TestBreakerValidate 校验规则。
func TestBreakerValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		b    CircuitBreaker
		ok   bool
	}{
		{"valid", CircuitBreaker{Name: "b", ServiceID: "s", Strategy: StrategyErrorRate, Threshold: 50, MinRequests: 10, WindowSecs: 60}, true},
		{"valid slow", CircuitBreaker{Name: "b", ServiceID: "s", Strategy: StrategySlowCall, Threshold: 1, MinRequests: 1, WindowSecs: 1}, true},
		{"missing name", CircuitBreaker{ServiceID: "s", Strategy: StrategyErrorRate, Threshold: 50, MinRequests: 10, WindowSecs: 60}, false},
		{"missing service", CircuitBreaker{Name: "b", Strategy: StrategyErrorRate, Threshold: 50, MinRequests: 10, WindowSecs: 60}, false},
		{"bad strategy", CircuitBreaker{Name: "b", ServiceID: "s", Strategy: "bogus", Threshold: 50, MinRequests: 10, WindowSecs: 60}, false},
		{"threshold zero", CircuitBreaker{Name: "b", ServiceID: "s", Strategy: StrategyErrorRate, Threshold: 0, MinRequests: 10, WindowSecs: 60}, false},
		{"threshold over", CircuitBreaker{Name: "b", ServiceID: "s", Strategy: StrategyErrorRate, Threshold: 101, MinRequests: 10, WindowSecs: 60}, false},
		{"min requests zero", CircuitBreaker{Name: "b", ServiceID: "s", Strategy: StrategyErrorRate, Threshold: 50, MinRequests: 0, WindowSecs: 60}, false},
		{"window zero", CircuitBreaker{Name: "b", ServiceID: "s", Strategy: StrategyErrorRate, Threshold: 50, MinRequests: 10, WindowSecs: 0}, false},
	} {
		err := c.b.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: 期望通过，got %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: 期望失败，got nil", c.name)
		}
	}
}
