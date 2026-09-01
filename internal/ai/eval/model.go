// Package eval 实现 Agent 评估（P4）：为 Agent 定义测试用例，批量运行评分。
//
// 一个 EvalCase = (输入, 期望, 匹配方式)。Run 对某 Agent 的全部用例各跑一次 Agent，
// 收集输出按匹配方式判 pass/fail。用于回归验证 Agent 改动（换模型/调 prompt）不退化。
//
// 匹配方式：
//   - contains：输出包含期望（大小写不敏感，最宽松，适合「提到 X」）
//   - exact：输出 trim 后等于期望（最严格）
//   - regex：期望为正则，输出匹配即通过
//
// 租户私有；不绑物理环境（无 prod:write）。EvalRun 持久化历史（对标 LangSmith 评估记录：
// 回归趋势 + 改动前后对比），每 Agent 保留最近 N 次防膨胀。
package eval

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/aitoys/paas/pkg/provider"
)

// MatchType 常量：匹配方式。
const (
	MatchContains = "contains"
	MatchExact    = "exact"
	MatchRegex    = "regex"
)

// EvalCase 评估用例（租户私有，归属某 Agent）。
type EvalCase struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	AgentID   string    `json:"agentId"`
	Name      string    `json:"name"`      // 用例名（租户内 Agent 下唯一便于识别）
	Input     string    `json:"input"`     // 用户输入
	Expected  string    `json:"expected"`  // 期望（contains 子串 / exact 全等 / regex 正则）
	MatchType string    `json:"matchType"` // contains | exact | regex
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EvalRun 一次批量评估的历史记录（持久化，趋势/对比用）。
type EvalRun struct {
	ID         string       `json:"id"`
	TenantID   string       `json:"tenantId"`
	AgentID    string       `json:"agentId"`
	Total      int          `json:"total"`
	Passed     int          `json:"passed"`
	Results    []EvalResult `json:"results"` // JSONB
	DurationMs int64        `json:"durationMs"`
	CreatedAt  time.Time    `json:"createdAt"`
}

// EvalResult 单用例运行结果（随 EvalRun 持久化）。
type EvalResult struct {
	CaseID     string `json:"caseId"`
	Name       string `json:"name"`
	Passed     bool   `json:"passed"`
	Output     string `json:"output"` // Agent 实际输出
	Reason     string `json:"reason"` // 失败原因（Passed=false 时）
	DurationMs int64  `json:"durationMs"`
}

// Runner 执行 Agent（依赖倒置，由 agent.Runtime 实现，避免 eval->agent import）。
type Runner interface {
	Run(ctx context.Context, agentID string, msgs []provider.Message, onChunk func(provider.Chunk)) error
}

// sentinel 错误。
var (
	ErrEvalCaseNotFound = errors.New("评估用例不存在")
	ErrEvalUnavailable  = errors.New("评估服务未装配执行器")
	ErrEvalCaseExists   = errors.New("评估用例已存在")
	ErrEvalRunNotFound  = errors.New("评估记录不存在")
)

// Validate 校验用例。
func (c EvalCase) Validate() error {
	if c.AgentID == "" {
		return fieldErr("agentId 不能为空")
	}
	if c.Input == "" {
		return fieldErr("input 不能为空")
	}
	if c.Expected == "" {
		return fieldErr("expected 不能为空")
	}
	switch c.MatchType {
	case MatchContains, MatchExact, MatchRegex:
	default:
		return fieldErr("matchType 必须是 contains/exact/regex")
	}
	if c.MatchType == MatchRegex {
		if _, err := regexp.Compile(c.Expected); err != nil {
			return fieldErr("regex 期望非法: " + err.Error())
		}
	}
	return nil
}

// Match 按匹配方式判定输出是否通过。
func Match(matchType, expected, output string) (bool, string) {
	switch matchType {
	case MatchExact:
		if strings.TrimSpace(output) == expected {
			return true, ""
		}
		return false, "输出与期望不完全相等"
	case MatchRegex:
		re, err := regexp.Compile(expected)
		if err != nil {
			return false, "期望正则非法: " + err.Error()
		}
		if re.MatchString(output) {
			return true, ""
		}
		return false, "输出未匹配期望正则"
	default: // MatchContains
		if strings.Contains(strings.ToLower(output), strings.ToLower(expected)) {
			return true, ""
		}
		return false, "输出未包含期望子串"
	}
}

type fieldErr string

func (e fieldErr) Error() string { return string(e) }

func IsFieldErr(err error) bool {
	_, ok := err.(fieldErr)
	return ok
}
