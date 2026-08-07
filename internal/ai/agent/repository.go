package agent

import "context"

// Repository agent 持久化接口（全方法 ctx + tenant 过滤）。
type Repository interface {
	List(ctx context.Context) ([]Agent, error)
	Get(ctx context.Context, id string) (Agent, error)
	Create(ctx context.Context, a Agent) (Agent, error)
	Update(ctx context.Context, a Agent) (Agent, error)
	Delete(ctx context.Context, id string) error
	AgentsCount(ctx context.Context) (int, error) // 全表，seed 判空
}
