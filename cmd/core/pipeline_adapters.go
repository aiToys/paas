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
	"errors"
	"fmt"
	"time"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/service"
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
func (b *buildBridge) CreateBuildRun(ctx context.Context, appID, repoID, branch, commit string, buildArgs map[string]string) (devops.BuildRun, error) {
	return b.builds.CreateBuildRun(ctx, devops.BuildRun{
		AppID: appID, RepoID: repoID, Branch: branch, Commit: commit, Trigger: devops.TriggerManual, BuildArgs: buildArgs,
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

// giteaTagger 是 releaseBridge.Publish 打 git tag 所需的最小接口（解耦 *gitea.Client 直接依赖）。
// giteaBridge 实现此接口（已有 ResolveRepo + 新增 CreateTag 委托 gitea.Client）。
type giteaTagger interface {
	ResolveRepo(ctx context.Context, appID string) (owner, repo string, err error)
	CreateTag(ctx context.Context, owner, repo, tag, commit string) (string, error)
}

// releaseBridge 桥接 pipeline.Releaser -> devops ReleaseRepository +
// workload readiness + Image + environment promote。
type releaseBridge struct {
	releases  devops.ReleaseRepository
	images    devops.ImageRepository
	workloads workload.Repository
	envs      environment.Repository
	gitea     giteaTagger           // 可选，nil 时 Publish 跳过 git tag（仅回填 Image.Version）
	status    workload.StatusReader // 可选，nil 时 PollWorkloadReady 透传 store 原 Ready（集群外降级）
	services  service.Repository    // 可选，nil 时单服务自动解析降级（service/serviceID 均空保持向后兼容）
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
		// K8s 实时状态填充 Ready（PG Ready 反向同步留后续，PollWorkloadReady 须读实时非 store 原值）。
		// FillStatus 原地修改 slice 元素（值语义），须从 slice 读回填充后的 wl。
		if r.status != nil {
			wls := []workload.Workload{wl}
			_ = r.status.FillStatus(ctx, wls)
			wl = wls[0]
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
// FQDN host 用 reconciler 建的 Service 名（wl.Name 优先，空则 wl.ID），与 applyService 命名一致；
// port 非 0 且非 80 时显式带端口（Service 监听 wl.Port，smoke 默认走 80 会连不上）。
func (r *releaseBridge) WorkloadDomain(ctx context.Context, workloadID string) string {
	tid, _ := tenant.TenantFrom(ctx)
	if wl, err := r.workloads.Get(ctx, workloadID); err == nil {
		if wl.Domain != "" {
			return wl.Domain
		}
		host := wl.Name
		if host == "" {
			host = workloadID
		}
		fqdn := fmt.Sprintf("%s.%s.svc.cluster.local", host, tenant.Namespace(tid))
		if wl.Port > 0 && wl.Port != 80 {
			fqdn = fmt.Sprintf("%s:%d", fqdn, wl.Port)
		}
		return fqdn
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local", workloadID, tenant.Namespace(tid))
}

// LatestReadyImage 返回 app 最新 ready Image ID（CD deploy 阶段 imageSource=latestReady 用）。
func (r *releaseBridge) LatestReadyImage(ctx context.Context, appID string) (string, error) {
	list, err := r.images.ListImages(ctx, appID)
	if err != nil {
		return "", err
	}
	// ListImages 按 BuiltAt 倒序（新→旧，memory/pg 一致），正序取第一条 ready 即最新。
	for i := range list {
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

// Deploy 部署镜像到 env×lane×service（找/建基线 Workload + UpdateImage），产生部署记录，不打版本。
// port/containerPort 仅新建 Workload 时设定（驱动 reconciler 建 Service）。sourceRunID 非空回填追溯。
// serviceID 关联服务实体（服务模型 Phase 1）；service+serviceID 均空时自动采用 app 唯一服务
// （单服务应用零操作——默认绑定 pipeline 不写 overrides 也能带出 Port/Replicas/ServiceID）。
func (r *releaseBridge) Deploy(ctx context.Context, appID, envID, lane, service, serviceID, imageID string, port, containerPort int, sourceRunID string) (devops.Release, string, error) {
	if service == "" && serviceID == "" && r.services != nil {
		// 单服务自动采用：恰 1 个服务实体时零操作打通 Port/ServiceID；多服务/无服务保持空（向后兼容）。
		if svcs, err := r.services.List(ctx, appID); err == nil && len(svcs) == 1 {
			service, serviceID = svcs[0].Name, svcs[0].ID
		}
	}
	rel, err := r.releases.CreateRelease(ctx, devops.ReleaseInput{
		AppID: appID, EnvID: envID, LaneID: lane, Service: service, ServiceID: serviceID, ImageID: imageID,
		Port: port, ContainerPort: containerPort,
	})
	if err != nil {
		return devops.Release{}, "", err
	}
	if sourceRunID != "" {
		// 回填失败仅降级（部署已成功，追溯字段非关键），不阻断 deploy。
		// 错误透传策略：此处故意吞错，避免因 source_run_id 列未就绪（migration 0022 前）拖垮整个 deploy。
		_ = r.releases.MarkSourceRun(ctx, rel.ID, sourceRunID)
		rel.SourceRunID = sourceRunID
	}
	return rel, r.WorkloadDomain(ctx, rel.WorkloadID), nil
}

// Publish 打版本号里程碑：Image.Version 回填 + git tag（commit 非空且仓库为 internal 时）。
// 不部署（部署是 deploy stage 的事）。返回 tagSha（未打 tag 时为空串）。
// external repo 或未注入 gitea 时仅回填 Image.Version，跳过 tag（不报错）。
func (r *releaseBridge) Publish(ctx context.Context, appID, imageID, version, commit string) (string, error) {
	if err := r.images.SetVersion(ctx, imageID, version); err != nil {
		return "", err
	}
	if commit == "" || r.gitea == nil {
		return "", nil
	}
	owner, repo, err := r.gitea.ResolveRepo(ctx, appID)
	if err != nil {
		// external repo / 无 internal repo：跳过 tag，不报错（版本号已回填）
		return "", nil
	}
	return r.gitea.CreateTag(ctx, owner, repo, version, commit)
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

// CreateTag 经 gitea client 在指定 commit 上打 tag（release stage 打版本号里程碑）。
// gitea client 未配置返错（release stage 显式知道 tag 不可用，与 baseline 跳过策略区分）。
func (g *giteaBridge) CreateTag(ctx context.Context, owner, repo, tag, commit string) (string, error) {
	if g.gitea == nil {
		return "", fmt.Errorf("gitea client 未配置，release tag 不可用")
	}
	return g.gitea.CreateTag(ctx, owner, repo, tag, commit)
}

var _ pipeline.GiteaMerger = (*giteaBridge)(nil)
var _ giteaTagger = (*giteaBridge)(nil)

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

// paramResolverBridge 桥接 pipeline.ParamResolver -> environment（按 app 租户查 type 环境）+ codeRepo（internal repo）。
// 用于模板占位符 {{app.env.test}}/{{app.env.prod}}/{{app.repo}} 解析。
type paramResolverBridge struct {
	apps  application.Repository
	envs  environment.Repository
	repos devops.CodeRepoRepository
}

// EnvByType 返回 app 租户内指定 type 的环境 ID（多个取 promoteOrder 最小；无返错）。
func (p *paramResolverBridge) EnvByType(ctx context.Context, appID, envType string) (string, error) {
	app, err := p.apps.Get(ctx, appID)
	if err != nil {
		return "", err
	}
	// environment store 按 ctx tenant 过滤，需切到 app 所属租户
	ctx = tenant.WithTenant(ctx, app.TenantID)
	envs, err := p.envs.List(ctx)
	if err != nil {
		return "", err
	}
	var best *environment.Environment
	for i := range envs {
		e := &envs[i]
		if e.Type != envType {
			continue
		}
		if best == nil || e.PromoteOrder < best.PromoteOrder {
			best = e
		}
	}
	if best == nil {
		return "", fmt.Errorf("应用租户无 %s 环境（app=%s）", envType, appID)
	}
	return best.ID, nil
}

// InternalRepoID 返回 app 绑定的 internal CodeRepo ID（无则空串，build stage 失败提示）。
func (p *paramResolverBridge) InternalRepoID(ctx context.Context, appID string) (string, error) {
	return (&repoResolverBridge{repos: p.repos}).ResolveInternalRepo(ctx, appID)
}

var _ pipeline.ParamResolver = (*paramResolverBridge)(nil)

// defaultPipelineBinder 建 app 时自动建默认流水线绑定（tpl-ci/tpl-cd），best-effort。
// 同名已存在（ErrPipelineExists）忽略（幂等）；模板缺失跳过。
func defaultPipelineBinder(pipes pipeline.Repository, templates pipeline.TemplateRepository) func(context.Context, string) error {
	return func(ctx context.Context, appID string) error {
		for _, tplID := range []string{"tpl-ci", "tpl-cd"} {
			tpl, err := templates.GetTemplate(ctx, tplID)
			if err != nil {
				continue // 模板不存在跳过（dev 未 seed 或生产门控）
			}
			_, err = pipes.CreatePipeline(ctx, pipeline.Pipeline{
				AppID: appID, Name: appID + "-" + tpl.Kind, Kind: tpl.Kind, TemplateID: tplID,
				Trigger: pipeline.PipelineTrigger{Type: "manual", Branch: "main"},
			})
			if err != nil && !errors.Is(err, pipeline.ErrPipelineExists) {
				return err
			}
		}
		return nil
	}
}

// serviceLookupBridge 桥接 service.Repository -> devops.ServiceLookup（依赖倒置，
// devops 不 import internal/service；Release 编排新建基线 Workload 时取 Port/Replicas）。
type serviceLookupBridge struct{ repos service.Repository }

func (b serviceLookupBridge) GetService(ctx context.Context, appID, serviceID string) (devops.ServiceDef, error) {
	s, err := b.repos.Get(ctx, appID, serviceID)
	if err != nil {
		return devops.ServiceDef{}, err
	}
	return devops.ServiceDef{ID: s.ID, Name: s.Name, Port: s.Port, Replicas: s.Replicas}, nil
}
