package guardrail

import (
	"context"
	"regexp"
	"testing"
)

func TestRuleGuardEmptyAllowsAll(t *testing.T) {
	g := &RuleGuard{}
	if d := g.CheckInput(context.Background(), "anything"); !d.Allowed {
		t.Fatalf("零规则应放行，got block: %s", d.Reason)
	}
	if d := g.CheckOutput(context.Background(), "anything"); !d.Allowed {
		t.Fatalf("零规则应放行，got block: %s", d.Reason)
	}
}

func TestRuleGuardBannedWordCaseInsensitive(t *testing.T) {
	g := &RuleGuard{BannedWords: []string{"BOMB"}}
	if d := g.CheckInput(context.Background(), "正常文本无命中"); !d.Allowed {
		t.Fatalf("无命中应放行")
	}
	if d := g.CheckInput(context.Background(), "里面含有 bomb 字样"); d.Allowed {
		t.Fatalf("命中禁用词（大小写不敏感）应拦截")
	}
	// 输出同样拦截
	if d := g.CheckOutput(context.Background(), "输出 BoMb 内容"); d.Allowed {
		t.Fatalf("输出命中禁用词应拦截")
	}
}

func TestRuleGuardPattern(t *testing.T) {
	g := &RuleGuard{Patterns: []*regexp.Regexp{regexp.MustCompile(`\b\d{16}\b`)}} // 16 位数字（卡号样）
	if d := g.CheckInput(context.Background(), "卡号 1234567812345678 泄漏"); d.Allowed {
		t.Fatalf("命中正则应拦截")
	}
	if d := g.CheckInput(context.Background(), "正常文本"); !d.Allowed {
		t.Fatalf("无命中应放行")
	}
}

func TestRuleGuardMaxInputLen(t *testing.T) {
	g := &RuleGuard{MaxInputLen: 5}
	if d := g.CheckInput(context.Background(), "一二三四五"); !d.Allowed {
		t.Fatalf("等于上限应放行")
	}
	if d := g.CheckInput(context.Background(), "一二三四五六"); d.Allowed {
		t.Fatalf("超上限应拦截")
	}
	// 长度仅约束输入，不约束输出
	if d := g.CheckOutput(context.Background(), "一二三四五六七八九十"); !d.Allowed {
		t.Fatalf("输出不受长度限制")
	}
}
