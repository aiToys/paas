package eval

import (
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		matchType, expected, output string
		wantPass                     bool
	}{
		{MatchContains, "天气", "今天天气不错", true},
		{MatchContains, "RAIN", "it is raining", true}, // 大小写不敏感
		{MatchContains, "雪", "今天天气不错", false},
		{MatchExact, "你好", "你好", true},
		{MatchExact, "你好", " 你好 ", true}, // trim
		{MatchExact, "你好", "你好吗", false},
		{MatchRegex, `\d{4}`, "年份2026年", true},
		{MatchRegex, `^\d+$`, "abc", false},
	}
	for i, c := range cases {
		pass, _ := Match(c.matchType, c.expected, c.output)
		if pass != c.wantPass {
			t.Errorf("[%d] %s %q vs %q: want %v got %v", i, c.matchType, c.expected, c.output, c.wantPass, pass)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := (EvalCase{}).Validate(); err == nil {
		t.Fatal("空用例应校验失败")
	}
	c := EvalCase{AgentID: "a", Input: "q", Expected: "x", MatchType: MatchContains}
	if err := c.Validate(); err != nil {
		t.Fatalf("合法用例应通过: %v", err)
	}
	c.MatchType = "bogus"
	if err := c.Validate(); err == nil {
		t.Fatal("非法 matchType 应校验失败")
	}
	c.MatchType = MatchRegex
	c.Expected = "(" // 非法正则
	if err := c.Validate(); err == nil {
		t.Fatal("非法正则应校验失败")
	}
}

