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
