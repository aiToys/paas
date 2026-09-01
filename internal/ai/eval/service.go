package eval

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/aitoys/paas/pkg/provider"
)

// perCaseTimeout 单用例运行上限（防 LLM 挂起拖垮批量评估）。
const perCaseTimeout = 60 * time.Second

// Service 批量运行某 Agent 的评估用例并评分，历史落 RunRepository（无则跳过持久化）。
type Service struct {
	repo   Repository
	runs   RunRepository
	runner Runner
}

// NewService 构造评估服务。runner 为 nil 时 RunAll 返 error（需注入 agent.Runtime）。
func NewService(repo Repository, runner Runner) *Service { return &Service{repo: repo, runner: runner} }

// WithRuns 注入评估历史仓储（启用 EvalRun 持久化）。
func (s *Service) WithRuns(r RunRepository) *Service { s.runs = r; return s }

// RunAll 跑某 Agent 的全部用例，返回逐条结果（不持久化）。
// 用例为空时返空切片 + nil（调用方可提示「无用例」）。
func (s *Service) RunAll(ctx context.Context, agentID string) ([]EvalResult, error) {
	if s.runner == nil {
		return nil, ErrEvalUnavailable // runtime 未装配（503 语义，不误导为 404）
	}
	cases, err := s.repo.List(ctx, agentID)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	results := make([]EvalResult, 0, len(cases))
	passed := 0
	for _, c := range cases {
		// 调用方取消（客户端断连）即停止，不再跑后续用例（防 LLM token 空耗）。
		if err := ctx.Err(); err != nil {
			return results, err
		}
		res := s.runOne(ctx, c)
		if res.Passed {
			passed++
		}
		results = append(results, res)
	}
	// 历史落库（仓储未注入跳过；失败记日志不阻断返回结果——best-effort）
	if s.runs != nil {
		if _, err := s.runs.CreateRun(ctx, EvalRun{
			AgentID: agentID, Total: len(results), Passed: passed,
			Results: results, DurationMs: time.Since(start).Milliseconds(),
		}); err != nil {
			log.Printf("[eval] 评估历史落库失败 agent=%s: %v", agentID, err) //nolint:gosec // agentID 来自内部实体
		}
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
