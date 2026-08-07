// Package guardrail 实现 AI 护栏（P4）：对 Agent 的输入/输出做安全检查。
//
// 两道闸（runtime 在调 LLM 前后各调一次）：
//   - CheckInput：拒超长输入（防滥用/DoS）+ 拒命中禁用词/模式的提示
//   - CheckOutput：拒命中禁用词/模式的生成内容（流式逐段检，命中即截断）
//
// 默认实现 RuleGuard 基于规则（平台级，env 配置：PAAS_GUARD_BANNED 逗号分隔禁用词、
// PAAS_GUARD_MAX_INPUT 输入字符上限）。空配置 = 全放行（NoopGuard 语义）。
// 完整策略引擎（向量分类/LLM 审查/租户级策略）留后续。
package guardrail

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ErrBlocked 输入或输出被护栏拦截（runtime 映射为 422 给客户端）。
var ErrBlocked = errors.New("内容被护栏拦截")

// Decision 护栏判定结果。
type Decision struct {
	Allowed bool   // true 放行；false 拦截
	Reason  string // 拦截原因（Allowed=false 时填充，可展示给客户端）
}

// Allow / Block 是便捷构造。
func Allow() Decision             { return Decision{Allowed: true} }
func Block(reason string) Decision { return Decision{Allowed: false, Reason: reason} }

// Guard 输入/输出护栏抽象（依赖倒置，runtime 注入；nil 视为全放行）。
type Guard interface {
	CheckInput(ctx context.Context, input string) Decision
	CheckOutput(ctx context.Context, output string) Decision
}

// RuleGuard 基于规则的护栏：禁用词（大小写不敏感）+ 正则模式 + 输入长度上限。
// 所有规则为空时全放行（等价 NoopGuard）。
type RuleGuard struct {
	BannedWords []string         // 命中即拒（子串匹配，大小写不敏感）
	Patterns    []*regexp.Regexp // 命中即拒
	MaxInputLen int              // 输入 rune 数上限，<=0 不限
}

// CheckInput 检用户输入：长度 + 禁用词 + 模式。
func (g *RuleGuard) CheckInput(_ context.Context, input string) Decision {
	if g.MaxInputLen > 0 && len([]rune(input)) > g.MaxInputLen {
		return Block("输入超出长度限制")
	}
	return g.checkText(input)
}

// CheckOutput 检生成内容：仅禁用词 + 模式（长度不限，模型自定）。
func (g *RuleGuard) CheckOutput(_ context.Context, output string) Decision {
	return g.checkText(output)
}

func (g *RuleGuard) checkText(s string) Decision {
	if len(g.BannedWords) == 0 && len(g.Patterns) == 0 {
		return Allow()
	}
	lower := strings.ToLower(s)
	for _, w := range g.BannedWords {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(lower, w) {
			return Block("内容包含禁用词")
		}
	}
	for _, p := range g.Patterns {
		if p != nil && p.MatchString(s) {
			return Block("内容命中禁用模式")
		}
	}
	return Allow()
}

// NewFromEnv 从环境变量构建平台级护栏（cmd/core 装配时调）。
//   - PAAS_GUARD_BANNED：逗号分隔禁用词（大小写不敏感子串匹配）
//   - PAAS_GUARD_PATTERNS：逗号分隔正则（任一命中即拒；非法正则忽略）
//   - PAAS_GUARD_MAX_INPUT：输入 rune 上限（<=0 或缺省=0 不限）
//
// 全空 = 全放行（RuleGuard 零规则即 NoopGuard 语义）。
func NewFromEnv() *RuleGuard {
	g := &RuleGuard{}
	if v := os.Getenv("PAAS_GUARD_BANNED"); v != "" {
		for _, w := range strings.Split(v, ",") {
			if w = strings.TrimSpace(w); w != "" {
				g.BannedWords = append(g.BannedWords, w)
			}
		}
	}
	if v := os.Getenv("PAAS_GUARD_PATTERNS"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if re, err := regexp.Compile(p); err == nil {
				g.Patterns = append(g.Patterns, re)
			}
		}
	}
	if v := os.Getenv("PAAS_GUARD_MAX_INPUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			g.MaxInputLen = n
		}
	}
	return g
}
