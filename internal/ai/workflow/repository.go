package workflow

import "context"

// Repository 工作流定义 + 运行存储（tenant 强制隔离，跨租户统一 not found）。
type Repository interface {
	// 定义
	List(ctx context.Context) ([]WorkflowDef, error)
	Get(ctx context.Context, id string) (WorkflowDef, error)
	Create(ctx context.Context, d WorkflowDef) (WorkflowDef, error)
	Update(ctx context.Context, d WorkflowDef) (WorkflowDef, error)
	Delete(ctx context.Context, id string) error
	WorkflowsCount(ctx context.Context) (int, error) // 全表，seed 判空

	// 运行
	CreateRun(ctx context.Context, r WorkflowRun) (WorkflowRun, error)
	GetRun(ctx context.Context, id string) (WorkflowRun, error)
	UpdateRun(ctx context.Context, r WorkflowRun) (WorkflowRun, error)
	ListRuns(ctx context.Context, workflowID string) ([]WorkflowRun, error)
	ListActiveRuns(ctx context.Context) ([]WorkflowRun, error) // 全表 running/paused（Sweep 启动恢复）
}
