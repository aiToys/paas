package devops

import (
	"context"
	"errors"
)

// Sentinel 错误。
var (
	// ErrTooManyActiveBuilds 租户活跃构建超限（可观测审计 C1：构建入口无并发上限，
	// 脚本化触发可打爆节点 docker daemon；per-tenant 并发闸，handler 映射 429）。
	ErrTooManyActiveBuilds = errors.New("活跃构建数超限，请等待进行中的构建完成")
	// ErrRepoExists 仓库已存在（重复绑定；handler 映射 409，错误契约审计 I8：
	// PG 唯一冲突此前无 sentinel 落 400，与 gitea.ErrRepoExists 的 409 不一致）。
	ErrRepoExists = errors.New("仓库已存在")
)

// MaxActiveBuildsPerTenant 租户并发构建上限（DooD 共享节点 docker daemon 的保护值；
// 超限 429 排队重试语义。memory/PG 双后端同源）。
const MaxActiveBuildsPerTenant = 3

// 四仓储接口方法名带实体前缀，避免单 Store 实现全部接口时方法名冲突
// （List/Get/Create 在多实体上重名）。实现必须从 ctx 取租户并强制过滤
// （缺失即拒，跨租户 not found 不泄漏存在性）。

// CodeRepoRepository 是代码仓库持久化抽象。
type CodeRepoRepository interface {
	ListRepos(ctx context.Context, appID string) ([]CodeRepo, error)
	GetRepo(ctx context.Context, id string) (CodeRepo, error)
	CreateRepo(ctx context.Context, r CodeRepo) error
	DeleteRepo(ctx context.Context, id string) error
	// ListAllRepos 跨租户列出全部仓库（不过滤 tenant，返回对象带 TenantID）。
	// 启动收敛用（存量 internal 仓库应用的 CI webhook 升级），非 HTTP 暴露。
	ListAllRepos(ctx context.Context) ([]CodeRepo, error)
}

// BuildRunRepository 是构建运行持久化抽象。
// CreateBuildRun 触发一次构建；mock CI runner 异步流转状态并产出 Image。
type BuildRunRepository interface {
	ListBuildRuns(ctx context.Context, appID string) ([]BuildRun, error)
	GetBuildRun(ctx context.Context, id string) (BuildRun, error)
	// CreateBuildRun 触发一次构建；mock CI runner 异步流转状态并产出 Image。
	// 返回写入后的 BuildRun（含生成的 ID），供调用方（pipeline engine）轮询。
	CreateBuildRun(ctx context.Context, b BuildRun) (BuildRun, error)
	// ListAllBuildRuns 跨租户列出全部构建（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllBuildRuns(ctx context.Context) ([]BuildRun, error)
}

// ImageRepository 是构建产物持久化抽象。
type ImageRepository interface {
	ListImages(ctx context.Context, appID string) ([]Image, error)
	GetImage(ctx context.Context, id string) (Image, error)
	// ListAllImages 跨租户列出全部镜像（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllImages(ctx context.Context) ([]Image, error)
	// SetVersion 给镜像回填正式版本号（release stage 打版本时调，标记该镜像已发布为某版本）。
	// 跨租户访问返 not found 不泄漏（与 GetImage 同源）。
	SetVersion(ctx context.Context, id, version string) error
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
	// MarkSourceRun 给部署记录回填触发它的 pipeline run ID（deploy stage 经 Deploy 接口写入，
	// 用于追溯到哪次流水线触发了此次部署）。跨租户访问返 not found 不泄漏。
	MarkSourceRun(ctx context.Context, id, runID string) error
	// ListAllReleases 跨租户列出全部发布（admin 平台总览，不过滤 tenant，返回对象带 TenantID）。
	ListAllReleases(ctx context.Context) ([]Release, error)
}
