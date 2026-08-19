package service

import "context"

// Repository 是服务实体的持久化接口（租户隔离，跨租户 not found）。
type Repository interface {
	List(ctx context.Context, appID string) ([]Service, error)
	Get(ctx context.Context, appID, id string) (Service, error)
	Create(ctx context.Context, s Service) error
	Update(ctx context.Context, s Service) error
	Delete(ctx context.Context, appID, id string) error
	// GetOrCreateByName 供存量回填：按 (app, name) 取，无则建（幂等）。
	GetOrCreateByName(ctx context.Context, appID, name, typ string, fill func(*Service)) (Service, error)
}
