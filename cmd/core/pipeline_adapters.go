// pipeline_adapters.go Pipeline engine 桥接 adapter（破除 pipeline->devops 循环 import）。
//
// 实现 pipeline.BuildRunner/Releaser/GiteaMerger/RepoResolver，包装 devops 四仓储 +
// workload readiness + environment promote + gitea client。engine 不直接 import
// devops store（避免 internal/devops/pipeline -> internal/devops 循环），经此桥接注入。
//
// identityAuditAdapter（auth_audit.go）已实现 pipeline.AuditRecorder 同源签名
// Record(ctx, tenantID, actor, action, resourceType, resourceID, detail) error，
// 直接复用，不在此重复定义。
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// 构建轮询间隔（秒级，CI runner 异步流转）。
const pipelineBuildPollInterval = 1 * time.Second

// 工作负载就绪轮询间隔 + 总超时（部署后等 Pod ready）。
const (
	pipelineReadyPollInterval = 2 * time.Second
	pipelineReadyTimeout      = 3 * time.Minute
)

// buildBridge 桥接 pipeline.BuildRunner -> devops BuildRunRepository。
type buildBridge struct{ builds devops.BuildRunRepository }

// CreateBuildRun 触发构建，返回写入后的 BuildRun（含 ID，供轮询）。
func (b *buildBridge) CreateBuildRun(ctx context.Context, appID, repoID, branch, commit string, _ map[string]string) (devops.BuildRun, error) {
	return b.builds.CreateBuildRun(ctx, devops.BuildRun{
		AppID: appID, RepoID: repoID, Branch: branch, Commit: commit, Trigger: devops.TriggerManual,
	})
}

// PollBuildRun 阻塞轮询到终态（success/failed）；ctx cancel 退出（Abort 传播）。
func (b *buildBridge) PollBuildRun(ctx context.Context, buildID string) (devops.BuildRun, error) {
	ticker := time.NewTicker(pipelineBuildPollInterval)
	defer ticker.Stop()
	for {
		if br, err := b.builds.GetBuildRun(ctx, buildID); err != nil {
			return devops.BuildRun{}, err
		} else if br.Status == devops.BuildSuccess || br.Status == devops.BuildFailed {
			return br, nil
		}
		select {
		case <-ctx.Done():
			return devops.BuildRun{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

var _ pipeline.BuildRunner = (*buildBridge)(nil)

// releaseBridge 桥接 pipeline.Releaser -> devops ReleaseRepository +
// workload readiness + Image + environment promote。
type releaseBridge struct {
	releases  devops.ReleaseRepository
	images    devops.ImageRepository
	workloads workload.Repository
	envs      environment.Repository
}

// CreateRelease 编排发布（取镜像 + 找/建基线 Workload + 更新镜像 + 回滚指针）。
func (r *releaseBridge) CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error) {
	return r.releases.CreateRelease(ctx, input)
}

// PollWorkloadReady 阻塞到 Ready>=Replicas && Replicas>0，超时返错（smoke 探活前置）。
// 内存/集群外（无 K8s 反向同步 Ready）会一直轮询到超时——可接受（mock 场景 Ready 不会涨）。
func (r *releaseBridge) PollWorkloadReady(ctx context.Context, workloadID string) error {
	deadline := time.Now().Add(pipelineReadyTimeout)
	ticker := time.NewTicker(pipelineReadyPollInterval)
	defer ticker.Stop()
	for {
		wl, err := r.workloads.Get(ctx, workloadID)
		if err != nil {
			return err
		}
		if wl.Replicas > 0 && wl.Ready >= wl.Replicas {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("工作负载就绪超时: %s（期望 %d，就绪 %d）", workloadID, wl.Replicas, wl.Ready)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// WorkloadDomain 拼探活域名：workload.Domain 优先，否则集群内 FQDN（同 ns DNS 可达）。
func (r *releaseBridge) WorkloadDomain(ctx context.Context, workloadID string) string {
	if wl, err := r.workloads.Get(ctx, workloadID); err == nil && wl.Domain != "" {
		return wl.Domain
	}
	tid, _ := tenant.TenantFrom(ctx)
	return fmt.Sprintf("%s.%s.svc.cluster.local", workloadID, tenant.Namespace(tid))
}

// LatestReadyImage 返回 app 最新 ready Image ID（CD deploy 阶段 imageSource=latestReady 用）。
func (r *releaseBridge) LatestReadyImage(ctx context.Context, appID string) (string, error) {
	list, err := r.images.ListImages(ctx, appID)
	if err != nil {
		return "", err
	}
	for i := len(list) - 1; i >= 0; i-- { // 倒序取最新一条 ready
		if list[i].Status == devops.ImageReady {
			return list[i].ID, nil
		}
	}
	return "", fmt.Errorf("无可用镜像（app=%s）", appID)
}

// Promote 晋升源 release 到下一阶序环境（adapter 内部经 environment.NextPromoteTarget 算 target）。
func (r *releaseBridge) Promote(ctx context.Context, srcReleaseID string) (devops.Release, error) {
	src, err := r.releases.GetRelease(ctx, srcReleaseID)
	if err != nil {
		return devops.Release{}, err
	}
	target, err := r.envs.NextPromoteTarget(ctx, src.EnvID)
	if err != nil {
		return devops.Release{}, err
	}
	return r.releases.PromoteRelease(ctx, srcReleaseID, target.ID)
}

// SetVersion 给本次 run 涉及的 Release 批量回填版本号（baseline stage 打版本）。
func (r *releaseBridge) SetVersion(ctx context.Context, releaseIDs []string, version string) error {
	for _, id := range releaseIDs {
		if err := r.releases.SetReleaseVersion(ctx, id, version); err != nil {
			return err
		}
	}
	return nil
}

var _ pipeline.Releaser = (*releaseBridge)(nil)

// giteaBridge 桥接 pipeline.GiteaMerger -> gitea.Client + devops CodeRepo（解析 internal repo owner/name）。
type giteaBridge struct {
	repos devops.CodeRepoRepository
	gitea *gitea.Client // nil 时 baseline merge 返错（功能降级）
}

// ResolveRepo 取 app 绑定的内置仓库 owner/repo；external repo 报错（baseline merge 跳过）。
func (g *giteaBridge) ResolveRepo(ctx context.Context, appID string) (owner, repo string, err error) {
	list, err := g.repos.ListRepos(ctx, appID)
	if err != nil {
		return "", "", err
	}
	for _, r := range list {
		if r.Source == devops.RepoSourceInternal && r.GiteaOwner != "" && r.GiteaRepo != "" {
			return r.GiteaOwner, r.GiteaRepo, nil
		}
	}
	return "", "", fmt.Errorf("应用未绑定内置仓库（无法合并主干）: %s", appID)
}

// Merge 经 gitea client 合并 head→base（baseline stage 把变更合并回主干）。
func (g *giteaBridge) Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error) {
	if g.gitea == nil {
		return "", fmt.Errorf("gitea client 未配置，baseline merge 不可用")
	}
	return g.gitea.Merge(ctx, owner, repo, head, base, mode)
}

var _ pipeline.GiteaMerger = (*giteaBridge)(nil)

// repoResolverBridge 桥接 pipeline.RepoResolver -> devops CodeRepo（解析 internal repo ID 供 build stage）。
type repoResolverBridge struct{ repos devops.CodeRepoRepository }

// ResolveInternalRepo 返回 app 绑定的内置仓库 ID；无内置仓库返空串（build stage repoID 空，构建会失败提示）。
func (r *repoResolverBridge) ResolveInternalRepo(ctx context.Context, appID string) (string, error) {
	list, err := r.repos.ListRepos(ctx, appID)
	if err != nil {
		return "", err
	}
	for _, rp := range list {
		if rp.Source == devops.RepoSourceInternal {
			return rp.ID, nil
		}
	}
	return "", nil
}

var _ pipeline.RepoResolver = (*repoResolverBridge)(nil)

// envTypeBridge 构造 EnvTypeResolver（生产 deploy 校验），桥接 environment.Repository.Get。
func envTypeBridge(envs environment.Repository) pipeline.EnvTypeResolver {
	return func(ctx context.Context, envID string) (string, error) {
		env, err := envs.Get(ctx, envID)
		if err != nil {
			return "", err
		}
		return env.Type, nil
	}
}

// promoteTargetTypeBridge 构造 PromoteTargetTypeResolver（promote 到 prod 校验）：
// envID 的下一阶环境（NextPromoteTarget）类型。triggerRun 静态预演 promote 链用。
func promoteTargetTypeBridge(envs environment.Repository) pipeline.PromoteTargetTypeResolver {
	return func(ctx context.Context, envID string) (string, error) {
		target, err := envs.NextPromoteTarget(ctx, envID)
		if err != nil {
			return "", err
		}
		return target.Type, nil
	}
}
