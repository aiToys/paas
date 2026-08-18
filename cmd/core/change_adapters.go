// change_adapters.go 变更管理编排桥接 adapter（cmd/core 装配专用）。
//
// 实现 change.GiteaBrancher / change.RunTrigger / change.RunReader / change.RepoLookup，
// 包装 gitea.Client + pipeline 三仓储 + devops CodeRepo/Release。change 包不直接
// import pipeline/gitea client（依赖倒置，测试可注入 fake）。
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/change"
	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/pkg/tenant"
)

// giteaBrancherBridge 桥接 change.GiteaBrancher -> gitea.Client（四方法直传）。
type giteaBrancherBridge struct{ client *gitea.Client }

func (b *giteaBrancherBridge) CreateBranch(ctx context.Context, owner, repo, branch, from string) error {
	return b.client.CreateBranch(ctx, owner, repo, branch, from)
}

func (b *giteaBrancherBridge) GetBranch(ctx context.Context, owner, repo, branch string) (gitea.Branch, error) {
	return b.client.GetBranch(ctx, owner, repo, branch)
}

func (b *giteaBrancherBridge) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	return b.client.DeleteBranch(ctx, owner, repo, branch)
}

func (b *giteaBrancherBridge) Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error) {
	return b.client.Merge(ctx, owner, repo, head, base, mode)
}

var _ change.GiteaBrancher = (*giteaBrancherBridge)(nil)

// runTriggerBridge 实现 change.RunTrigger + change.RunReader。
// TriggerAppRun 组 run 与 pipeline handler triggerRunInternal 同构：
// GetPipeline → GetTemplate → ResolveStages → HasActiveRun 单实例检查 →
// 建 StageRuns → CreateRun → engine.Start。
type runTriggerBridge struct {
	pipes     pipeline.Repository
	runs      pipeline.RunRepository
	templates pipeline.TemplateRepository
	resolver  pipeline.ParamResolver // nil 时占位符原样（与 pipeline handler 降级一致）
	repos     pipeline.RepoResolver  // nil 时 repoID 空
	engine    *pipeline.Engine
	rels      devops.ReleaseRepository // ListReleases 过滤 SourceRunID
}

// TriggerAppRun 触发应用流水线 run（branch 注入 {{run.branch}}），返回 runID。
func (b *runTriggerBridge) TriggerAppRun(ctx context.Context, appID, pipelineID, branch string) (string, error) {
	p, err := b.pipes.GetPipeline(ctx, pipelineID)
	if err != nil {
		return "", err
	}
	tpl, err := b.templates.GetTemplate(ctx, p.TemplateID)
	if err != nil {
		return "", err
	}
	resolved, err := pipeline.ResolveStages(ctx, tpl.Stages, p.ParamOverrides, b.resolver, appID, branch)
	if err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	// 单实例串行：同 pipeline 已有 running/paused run 拒绝
	active, err := b.runs.HasActiveRun(ctx, pipelineID)
	if err != nil {
		return "", err
	}
	if active {
		return "", pipeline.ErrActiveRunExists
	}
	repoID := ""
	if b.repos != nil {
		if rid, rerr := b.repos.ResolveInternalRepo(ctx, appID); rerr == nil {
			repoID = rid
		}
	}
	stageRuns := make([]pipeline.StageRun, len(resolved))
	for i, s := range resolved {
		stageRuns[i] = pipeline.StageRun{Index: i, Type: s.Type, Name: s.Name, Status: pipeline.StagePending, Input: s.Params}
	}
	run, err := b.runs.CreateRun(ctx, pipeline.PipelineRun{
		PipelineID: pipelineID, AppID: appID, Branch: branch,
		RepoID: repoID, Trigger: pipeline.TriggerManual,
		Status: pipeline.RunRunning, CurrentStage: 0, StageRuns: stageRuns,
	})
	if err != nil {
		return "", err
	}
	if b.engine != nil {
		b.engine.Start(ctx, run.ID)
	}
	return run.ID, nil
}

// GetRunStatus 读 run 当前状态（惰性终态推进用）。
func (b *runTriggerBridge) GetRunStatus(ctx context.Context, runID string) (string, error) {
	run, err := b.runs.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	return run.Status, nil
}

// ListRunStatuses 全租户 failed/paused/running run 状态列表（change.Notifications 通知聚合用）。
func (b *runTriggerBridge) ListRunStatuses(ctx context.Context) ([]change.RunStatusItem, error) {
	out := []change.RunStatusItem{}
	for _, st := range []string{"failed", "paused", "running"} {
		runs, err := b.runs.ListRuns(ctx, "", "", st)
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			cur := ""
			if r.CurrentStage >= 0 && r.CurrentStage < len(r.StageRuns) {
				cur = r.StageRuns[r.CurrentStage].Name
			}
			out = append(out, change.RunStatusItem{
				ID: r.ID, AppID: r.AppID, Status: r.Status, Current: cur,
				At: r.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return out, nil
}

// FindPipeline 取应用指定 kind 的流水线 ID（app 第一条匹配 kind）。
// 未找到返空串（service 层转 ErrNoCIPipeline）。
func (b *runTriggerBridge) FindPipeline(ctx context.Context, appID, kind string) (string, error) {
	list, err := b.pipes.ListPipelines(ctx, appID)
	if err != nil {
		return "", err
	}
	for _, p := range list {
		if p.Kind == kind && !p.Disabled {
			return p.ID, nil
		}
	}
	return "", nil
}

// ReleasesOfRun 查 run 关联的发布记录 ID（ListReleases 过滤 SourceRunID）。
func (b *runTriggerBridge) ReleasesOfRun(ctx context.Context, runID string) ([]string, error) {
	// ListReleases 需 ctx 租户过滤；run 持租户，先取 run 派生租户 ctx。
	run, err := b.runs.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	wctx := tenant.WithTenant(ctx, run.TenantID)
	list, err := b.rels.ListReleases(wctx, run.AppID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, r := range list {
		if r.SourceRunID == runID {
			out = append(out, r.ID)
		}
	}
	return out, nil
}

var _ change.RunTrigger = (*runTriggerBridge)(nil)
var _ change.RunReader = (*runTriggerBridge)(nil)

// changeRepoLookup 解析应用内置仓库（owner/repo 名/CodeRepo ID），
// 供 change.Service 定位变更分支归属仓库。
func changeRepoLookup(repos devops.CodeRepoRepository) change.RepoLookup {
	return func(ctx context.Context, appID string) (string, string, string, error) {
		list, err := repos.ListRepos(ctx, appID)
		if err != nil {
			return "", "", "", err
		}
		for _, r := range list {
			if r.Source == devops.RepoSourceInternal && r.GiteaOwner != "" && r.GiteaRepo != "" {
				return r.GiteaOwner, r.GiteaRepo, r.ID, nil
			}
		}
		return "", "", "", fmt.Errorf("应用未绑定内置仓库（变更管理需内置 Git 仓库）: %s", appID)
	}
}
