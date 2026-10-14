package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aitoys/paas/internal/agentrun"
	"github.com/aitoys/paas/internal/devops/change"
	"github.com/aitoys/paas/internal/devops/gitea"
)

// agentrunRepoBridge 桥接 giteaBridge -> agentrun.RepoResolver：
// app 绑定的内置仓库 owner/repo + Gitea base URL 组装 git clone 地址（agentrun source-init 用）。
type agentrunRepoBridge struct {
	resolve  func(ctx context.Context, appID string) (owner, repo string, err error)
	giteaURL string
}

func (b *agentrunRepoBridge) GitURL(ctx context.Context, appID string) (string, error) {
	owner, repo, err := b.resolve(ctx, appID)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(b.giteaURL, "/")
	return fmt.Sprintf("%s/%s/%s.git", base, owner, repo), nil
}

// changeRegistrarBridge 桥接 change.Service -> pipeline.ChangeRegistrar：
// agent stage 成功后把产出分支注册为 Change（open），自然汇入批次集成/发布流。
// spec D4：合入归 paas——由 paas 执行器注册，非 agentrun 反调（单向依赖保持）。
type changeRegistrarBridge struct {
	svc *change.Service
}

func (b *changeRegistrarBridge) RegisterChange(ctx context.Context, appID, branch, baseBranch, title string) error {
	_, err := b.svc.CreateChangeWithBranch(ctx, appID, change.ChangeInput{
		Title: title, Type: change.ChangeFeat,
		Branch: branch, BaseBranch: baseBranch, CreateBranch: false, // 分支已由 agentrun 创建，仅引用
	})
	return err
}

// changeDelegatorBridge 桥接 agentrun client -> change.AgentRunDelegator：
// 变更页「智能研发」按钮（契约 v2 §3.3 委派模式），透传用户 JWT（归因 claims.sub）。
// 两阶段 pipeline（analyze→implement）：implement 硬依赖前序 analyze 产物（执行域既有契约），
// 单 run implement 会失败；编排归 agentrun（质量链归执行域），paas 零状态机。
// 返回 pipeline 名（回写 Change.AgentRunName，观测详情走 /pipelines/{name}）。
type changeDelegatorBridge struct{ c *agentrun.Client }

func (b changeDelegatorBridge) Delegate(ctx context.Context, userJWT string, spec change.AgentDelegateSpec) (string, error) {
	return b.c.CreatePipelineAsUser(ctx, userJWT, agentrun.AgentPipelineSpec{
		AppID: spec.AppID, Request: spec.Request,
		WorkBranch: spec.WorkBranch, BaseBranch: spec.BaseBranch,
		Stages: []string{"analyze", "implement"},
	})
}

// agentBranchCheckerBridge 桥接 giteaBridge -> pipeline.AgentBranchChecker：
// agent 产出分支存在性校验（幽灵分支防御，2026-10-13）——对方 runner「succeeded 但未推送」
// 违约在 paas 边界判负，防下游 review/build 空转烧 token。gitea nil 时放行（功能降级）。
type agentBranchCheckerBridge struct{ gb *giteaBridge }

func (b *agentBranchCheckerBridge) BranchExists(ctx context.Context, appID, branch string) (bool, error) {
	if b.gb == nil || b.gb.gitea == nil {
		return true, nil // 无 Gitea 装配（单测/降级）：放行不阻断
	}
	owner, repo, err := b.gb.ResolveRepo(ctx, appID)
	if err != nil {
		return false, err
	}
	_, err = b.gb.gitea.GetBranch(ctx, owner, repo, branch)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gitea.ErrBranchNotFound) {
		return false, nil
	}
	return false, err
}
