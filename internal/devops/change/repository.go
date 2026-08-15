package change

import (
	"context"
	"errors"
)

// sentinel 错误（handler 按 errors.Is 映射 HTTP 状态）。
var (
	ErrChangeNotFound = errors.New("change not found")
	ErrChangeExists   = errors.New("change already exists")
	ErrBatchNotFound  = errors.New("integration batch not found")
	ErrBatchExists    = errors.New("integration batch already exists")
	ErrNoTenant       = errors.New("missing tenant in context")
)

// Repository 变更 + 集成批次仓储（单接口，Store 聚合）。
// 全方法强制按 ctx 租户过滤；跨租户访问统一 NotFound 不泄漏存在性。
type Repository interface {
	ListChanges(ctx context.Context, appID, status string) ([]Change, error)
	GetChange(ctx context.Context, id string) (Change, error)
	CreateChange(ctx context.Context, c Change) (Change, error)
	UpdateChange(ctx context.Context, c Change) (Change, error)
	ListBatches(ctx context.Context, appID, status string) ([]IntegrationBatch, error)
	GetBatch(ctx context.Context, id string) (IntegrationBatch, error)
	CreateBatch(ctx context.Context, b IntegrationBatch) (IntegrationBatch, error)
	UpdateBatch(ctx context.Context, b IntegrationBatch) (IntegrationBatch, error)
}
