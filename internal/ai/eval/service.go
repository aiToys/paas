package eval

import (
	"context"
	"strings"
	"time"

	"github.com/aitoys/paas/pkg/provider"
)

// perCaseTimeout 单用例运行上限（防 LLM 挂起拖垮批量评估）。
const perCaseTimeout = 60 * time.Second

// Service 批量运行某 Agent 的评估用例并评分。
type Service struct {
	repo   Repository
	runner Runner
}

// NewService 构造评估服务。runner 为 nil 时 RunAll 返 error（需注入 agent.Runtime）。
func NewService(repo Repository, runner Runner) *Service { return &Service{repo: repo, runner: runner} }

// RunAll 跑某 Agent 的全部用例，返回逐条结果（不持久化）。
// 用例为空时返空切片 + nil（调用方可提示「无用例」）。
func (s *Service) RunAll(ctx context.Context, agentID string) ([]EvalResult, error) {
	if s.runner == nil {
		return nil, ErrEvalCaseNotFound // runtime 未装配，借用 sentinel 表达不可用
	}
	cases, err := s.repo.List(ctx, agentID)
	if err != nil {
		return nil, err
	}
	results := make([]EvalResult, 0, len(cases))
	for _, c := range cases {
		results = append(results, s.runOne(ctx, c))
	}
	return results, nil
}

// runOne 跑单用例：调 Runner 收集输出 -> Match 评分。
func (s *Service) runOne(ctx context.Context, c EvalCase) EvalResult {
	start := time.Now()
	// 每用例独立超时，防 LLM 挂起拖垮批量评估。
	cctx, cancel := context.WithTimeout(ctx, perCaseTimeout)
	defer cancel()

	var sb strings.Builder
	err := s.runner.Run(cctx, c.AgentID,
		[]provider.Message{{Role: "user", Content: c.Input}},
		func(chunk provider.Chunk) {
			if chunk.Content != "" {
				sb.WriteString(chunk.Content)
			}
		},
	)
	output := sb.String()
	res := EvalResult{
		CaseID:     c.ID,
		Name:       c.Name,
		Output:     output,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if err != nil {
		res.Reason = "运行失败: " + err.Error()
		return res
	}
	passed, reason := Match(c.MatchType, c.Expected, output)
	res.Passed = passed
	res.Reason = reason
	return res
}
