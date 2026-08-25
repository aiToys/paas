package eval

import "context"

// Repository 评估用例持久化接口（全方法 ctx + tenant 过滤）。
type Repository interface {
	// List 按 agentID 列用例（agentID 为空列该租户全部）。
	List(ctx context.Context, agentID string) ([]EvalCase, error)
	Get(ctx context.Context, id string) (EvalCase, error)
	Create(ctx context.Context, c EvalCase) (EvalCase, error)
	Delete(ctx context.Context, id string) error
	// EvalCasesCount 全表（seed 判空用）。
	EvalCasesCount(ctx context.Context) (int, error)
}

// RunRepository 评估历史仓储（ListRuns 最近优先 + CreateRun + 环形截断在 store 内做）。
type RunRepository interface {
	ListRuns(ctx context.Context, agentID string) ([]EvalRun, error) // agentID 空列全部（最近优先）
	GetRun(ctx context.Context, id string) (EvalRun, error)
	CreateRun(ctx context.Context, r EvalRun) (EvalRun, error)
}
