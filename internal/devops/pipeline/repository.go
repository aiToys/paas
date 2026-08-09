package pipeline

import (
	"context"
	"errors"
)

// sentinel 错误（本 task 定义的仓储层错误；model.go 中已定义的校验类错误不重复）。
// HTTP 层据此映射：NotFound→404 / Exists/ActiveRunExists→409 / NoTenant→400。
var (
	ErrPipelineNotFound = errors.New("pipeline not found")
	ErrPipelineExists   = errors.New("pipeline already exists")
	ErrRunNotFound      = errors.New("pipeline run not found")
	ErrRunExists        = errors.New("pipeline run already exists")        // ID 冲突
	ErrActiveRunExists  = errors.New("active pipeline run already exists") // 单实例串行：同一 pipeline 已有 running/paused 运行
	ErrNotRunning       = errors.New("pipeline run not running or paused") // Abort 守卫：仅 running/paused 可 abort
	ErrTemplateExists   = errors.New("pipeline template already exists")
	ErrNoTenant         = errors.New("missing tenant context")
)

// Repository Pipeline CRUD（应用 1:N 主线实体，租户隔离）。
type Repository interface {
	ListPipelines(ctx context.Context, appID string) ([]Pipeline, error)
	GetPipeline(ctx context.Context, id string) (Pipeline, error)
	CreatePipeline(ctx context.Context, p Pipeline) (Pipeline, error)
	UpdatePipeline(ctx context.Context, p Pipeline) (Pipeline, error)
	DeletePipeline(ctx context.Context, id string) error
}

// RunRepository PipelineRun 读写（engine 推进主要调 UpdateRun 写回 StageRuns）。
type RunRepository interface {
	ListRuns(ctx context.Context, appID, pipelineID, status string) ([]PipelineRun, error)
	GetRun(ctx context.Context, id string) (PipelineRun, error)
	CreateRun(ctx context.Context, r PipelineRun) (PipelineRun, error)
	UpdateRun(ctx context.Context, r PipelineRun) (PipelineRun, error)
	HasActiveRun(ctx context.Context, pipelineID string) (bool, error)
}

// TemplateRepository 模板（平台预置 builtin + 租户自定义）。
type TemplateRepository interface {
	ListTemplates(ctx context.Context) ([]PipelineTemplate, error)
	GetTemplate(ctx context.Context, id string) (PipelineTemplate, error)
	CreateTemplate(ctx context.Context, t PipelineTemplate) (PipelineTemplate, error)
	// UpdateTemplate 更新自定义模板（Stages/Name/Kind/Description/Params）；builtin 模板拒（ErrTemplateBuiltin）。
	UpdateTemplate(ctx context.Context, t PipelineTemplate) (PipelineTemplate, error)
	// DeleteTemplate 删除自定义模板；builtin 模板拒（ErrTemplateBuiltin）。
	DeleteTemplate(ctx context.Context, id string) error
}

// Store 聚合 Pipeline 三仓储接口（memoryStore/pgStore 同一实例实现全部）。
// cmd/core 装配时存 Stores.Pipeline，handler 按需类型断言取子接口。
type Store interface {
	Repository
	RunRepository
	TemplateRepository
}
