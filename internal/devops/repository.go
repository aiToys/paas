package devops

import "context"

// 四仓储接口方法名带实体前缀，避免单 Store 实现全部接口时方法名冲突
// （List/Get/Create 在多实体上重名）。实现必须从 ctx 取租户并强制过滤
// （缺失即拒，跨租户 not found 不泄漏存在性）。

// CodeRepoRepository 是代码仓库持久化抽象。
type CodeRepoRepository interface {
	ListRepos(ctx context.Context, appID string) ([]CodeRepo, error)
	GetRepo(ctx context.Context, id string) (CodeRepo, error)
	CreateRepo(ctx context.Context, r CodeRepo) error
	DeleteRepo(ctx context.Context, id string) error
}

// BuildRunRepository 是构建运行持久化抽象。
// CreateBuildRun 触发一次构建；mock CI runner 异步流转状态并产出 Image。
type BuildRunRepository interface {
	ListBuildRuns(ctx context.Context, appID string) ([]BuildRun, error)
	GetBuildRun(ctx context.Context, id string) (BuildRun, error)
	CreateBuildRun(ctx context.Context, b BuildRun) error
	// ListAllBuildRuns 跨租户列出全部构建（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllBuildRuns(ctx context.Context) ([]BuildRun, error)
}

// ImageRepository 是构建产物持久化抽象。
type ImageRepository interface {
	ListImages(ctx context.Context, appID string) ([]Image, error)
	GetImage(ctx context.Context, id string) (Image, error)
	// ListAllImages 跨租户列出全部镜像（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllImages(ctx context.Context) ([]Image, error)
}

// ReleaseRepository 是发布持久化 + 编排抽象。
// CreateRelease/RollbackRelease 内部完成 Workload 编排（依赖倒置：编排需要 Workload
// 仓储，由实现注入，接口本身不暴露 Workload 依赖）。
type ReleaseRepository interface {
	ListReleases(ctx context.Context, appID string) ([]Release, error)
	GetRelease(ctx context.Context, id string) (Release, error)
	// CreateRelease 编排发布：取镜像、找/建目标环境基线 Workload、更新镜像、记录回滚指针。
	CreateRelease(ctx context.Context, input ReleaseInput) (Release, error)
	// RollbackRelease 回滚到上一镜像，返回新建的回滚 Release。
	RollbackRelease(ctx context.Context, releaseID string) (Release, error)
	// PromoteRelease 把源 release 的镜像发布到目标环境（发布流水线逐级提升），
	// 复用 CreateRelease 编排，新 release 记 PromotedFrom=源 release ID（晋升链可追溯）。
	// targetEnvID 由调用方（handler）经 environment.NextPromoteTarget 算出并完成 prod:write 校验。
	PromoteRelease(ctx context.Context, srcReleaseID, targetEnvID string) (Release, error)
	// SetReleaseVersion 给已存在的发布单回填版本号（baseline stage 打版本时调）。
	// 跨租户访问返 not found 不泄漏（与 GetRelease 同源）。
	SetReleaseVersion(ctx context.Context, id, version string) error
	// ListAllReleases 跨租户列出全部发布（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllReleases(ctx context.Context) ([]Release, error)
}
