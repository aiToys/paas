package change

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aitoys/paas/internal/devops/gitea"
)

// sentinel 错误（handler 按 errors.Is 映射 HTTP 状态）。
var (
	// ErrBatchState 状态机非法转移（如非 tested 批次 approve）。
	ErrBatchState = errors.New("batch state not allowed")
	// ErrGiteaNotConfigured 未注入 git 后端（降级不 panic）。
	ErrGiteaNotConfigured = errors.New("gitea not configured")
	// ErrNoCIPipeline 找不到应用 CI 流水线。
	ErrNoCIPipeline = errors.New("no ci pipeline found")
	// ErrMergeConflictBatch 集成/发布 merge 冲突（errors.As 取 *BatchConflictError）。
	ErrMergeConflictBatch = errors.New("batch merge conflict")
)

// BatchConflictError 批次内 merge 冲突：FailedChangeID 与 PrevChangeID 冲突。
type BatchConflictError struct {
	BatchID, FailedChangeID, PrevChangeID string
}

func (e *BatchConflictError) Error() string {
	return fmt.Sprintf("变更 %s 与 %s 冲突", e.FailedChangeID, e.PrevChangeID)
}

// GiteaBrancher 变更编排对 git 后端的最小依赖（cmd/core 桥接 gitea.Client）。
type GiteaBrancher interface {
	CreateBranch(ctx context.Context, owner, repo, branch, from string) error
	GetBranch(ctx context.Context, owner, repo, branch string) (gitea.Branch, error)
	DeleteBranch(ctx context.Context, owner, repo, branch string) error
	Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error)
}

// RunTrigger 批次触发流水线 run + 流水线发现（cmd/core 桥接 pipeline 包）。
type RunTrigger interface {
	TriggerAppRun(ctx context.Context, appID, pipelineID, branch string) (runID string, err error)
	// FindPipeline 取应用指定 kind 的流水线 ID（app 第一条 ci/cd）。
	FindPipeline(ctx context.Context, appID, kind string) (string, error)
	// ReleasesOfRun 查 run 关联的发布记录 ID（cmd/core 桥接 devops ListReleases 过滤 SourceRunID）。
	ReleasesOfRun(ctx context.Context, runID string) ([]string, error)
}

// RunReader 惰性终态推进（读 run 状态）。
type RunReader interface {
	GetRunStatus(ctx context.Context, runID string) (status string, err error)
}

// RepoLookup 解析应用内置仓库（owner/repo 名/CodeRepo ID）。
type RepoLookup func(ctx context.Context, appID string) (owner, repo, repoID string, err error)

// ChangeInput 创建变更入参（handler 绑定）。
type ChangeInput struct {
	Title, Type, Branch, BaseBranch string
	CreateBranch                    bool
}

// Service 变更编排：集成批次 integrate/approve/release + 惰性状态推进。
// 状态转移均走 Get→改→Update（memory 锁内安全；pg 依赖守卫先行检查，
// 竞态窗口可接受——变更管理为低频操作）。
type Service struct {
	repo         Repository
	gitea        GiteaBrancher
	runs         RunTrigger
	readRuns     RunReader
	repoResolver RepoLookup
}

// ServiceOpt 可选依赖注入。
type ServiceOpt func(*Service)

func WithGitea(g GiteaBrancher) ServiceOpt    { return func(s *Service) { s.gitea = g } }
func WithRunTrigger(rt RunTrigger) ServiceOpt { return func(s *Service) { s.runs = rt } }
func WithRunReader(rr RunReader) ServiceOpt   { return func(s *Service) { s.readRuns = rr } }
func WithRepoLookup(f RepoLookup) ServiceOpt  { return func(s *Service) { s.repoResolver = f } }

// NewService 构造编排 service。
func NewService(repo Repository, opts ...ServiceOpt) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// lookupRepo 解析仓库归属，未注入 resolver 时报错。
func (s *Service) lookupRepo(ctx context.Context, appID string) (owner, repo, repoID string, err error) {
	if s.repoResolver == nil {
		return "", "", "", fmt.Errorf("repo lookup 未注入")
	}
	return s.repoResolver(ctx, appID)
}

// CreateChangeWithBranch 创建变更：CreateBranch=true 时平台代建分支（main 派生），
// 否则校验引用分支已存在。
func (s *Service) CreateChangeWithBranch(ctx context.Context, appID string, in ChangeInput) (Change, error) {
	owner, repoName, repoID, err := s.lookupRepo(ctx, appID)
	if err != nil {
		return Change{}, err
	}
	base := in.BaseBranch
	if base == "" {
		base = "main"
	}
	if in.CreateBranch {
		if s.gitea == nil {
			return Change{}, ErrGiteaNotConfigured
		}
		if err := s.gitea.CreateBranch(ctx, owner, repoName, in.Branch, base); err != nil {
			return Change{}, fmt.Errorf("创建分支失败: %w", err)
		}
	} else {
		// 引用已有分支：不存在时提前暴露（而非 integrate 时才失败）
		if s.gitea == nil {
			return Change{}, ErrGiteaNotConfigured
		}
		if _, err := s.gitea.GetBranch(ctx, owner, repoName, in.Branch); err != nil {
			return Change{}, fmt.Errorf("分支不存在: %w", err)
		}
	}
	c, err := s.repo.CreateChange(ctx, Change{
		AppID: appID, RepoID: repoID,
		Title: in.Title, Type: in.Type, Branch: in.Branch,
		BranchCreated: in.CreateBranch, BaseBranch: base,
	})
	if err != nil {
		return Change{}, err
	}
	c.BranchCreated = in.CreateBranch
	return c, nil
}

// AbandonChange 放弃变更：open→abandoned；已入批（integrated 等）拒绝，先移出批次。
func (s *Service) AbandonChange(ctx context.Context, id string) (Change, error) {
	c, err := s.repo.GetChange(ctx, id)
	if err != nil {
		return Change{}, err
	}
	if c.BatchID != "" {
		return Change{}, fmt.Errorf("%w: 变更已入批，请先移出批次", ErrBatchState)
	}
	if c.Status != ChangeOpen {
		return Change{}, fmt.Errorf("%w: 仅 open 变更可放弃", ErrBatchState)
	}
	c.Status = ChangeAbandoned
	return s.repo.UpdateChange(ctx, c)
}

// batchAllowed 批次状态 ∈ 给定集合。
func batchAllowed(b IntegrationBatch, statuses ...string) bool {
	for _, st := range statuses {
		if b.Status == st {
			return true
		}
	}
	return false
}

// AddChangeToBatch 变更入批：批次 collecting/failed（失败后重新收集）+ 变更 open 且未入批。
func (s *Service) AddChangeToBatch(ctx context.Context, batchID, changeID string) (IntegrationBatch, error) {
	b, err := s.repo.GetBatch(ctx, batchID)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if !batchAllowed(b, BatchCollecting, BatchFailed) {
		return IntegrationBatch{}, fmt.Errorf("%w: 批次 %s 状态不可收变更", ErrBatchState, b.Status)
	}
	c, err := s.repo.GetChange(ctx, changeID)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if c.Status != ChangeOpen || c.BatchID != "" {
		return IntegrationBatch{}, fmt.Errorf("%w: 仅 open 且未入批的变更可入批", ErrBatchState)
	}
	if c.RepoID != b.RepoID {
		return IntegrationBatch{}, fmt.Errorf("变更与批次不属于同一仓库")
	}
	b.ChangeIDs = append(b.ChangeIDs, changeID)
	if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
		return IntegrationBatch{}, err
	}
	c.BatchID = b.ID
	if _, err := s.repo.UpdateChange(ctx, c); err != nil {
		return IntegrationBatch{}, err
	}
	return s.repo.GetBatch(ctx, batchID)
}

// RemoveChangeFromBatch 变更出批：collecting/conflict/failed 状态可移出。
func (s *Service) RemoveChangeFromBatch(ctx context.Context, batchID, changeID string) (IntegrationBatch, error) {
	b, err := s.repo.GetBatch(ctx, batchID)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if !batchAllowed(b, BatchCollecting, BatchConflict, BatchFailed) {
		return IntegrationBatch{}, fmt.Errorf("%w: 批次 %s 状态不可移出变更", ErrBatchState, b.Status)
	}
	// 断言变更确属本批次：否则「影子出批」破坏他批数据（审计第 7 轮 I1）
	inBatch := false
	for _, id := range b.ChangeIDs {
		if id == changeID {
			inBatch = true
			break
		}
	}
	if !inBatch {
		return IntegrationBatch{}, fmt.Errorf("%w: 变更 %s 不在批次 %s 中", ErrBatchState, changeID, batchID)
	}
	ids := make([]string, 0, len(b.ChangeIDs))
	for _, id := range b.ChangeIDs {
		if id != changeID {
			ids = append(ids, id)
		}
	}
	b.ChangeIDs = ids
	if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
		return IntegrationBatch{}, err
	}
	if c, err := s.repo.GetChange(ctx, changeID); err == nil {
		c.BatchID = ""
		c.ConflictWith = ""
		if c.Status == ChangeIntegrated {
			c.Status = ChangeOpen // 冲突移出后可修改重新入批
		}
		if _, err := s.repo.UpdateChange(ctx, c); err != nil {
			return IntegrationBatch{}, err
		}
	}
	return s.repo.GetBatch(ctx, batchID)
}

// Integrate 集成编排：重建集成分支 → 按 ChangeIDs 顺序 merge → 触发 CI run → testing。
// 冲突时批次落 conflict + 返回 *BatchConflictError。
func (s *Service) Integrate(ctx context.Context, batchID string) (IntegrationBatch, error) {
	if s.gitea == nil {
		return IntegrationBatch{}, ErrGiteaNotConfigured
	}
	if s.runs == nil {
		return IntegrationBatch{}, ErrGiteaNotConfigured
	}
	b, err := s.repo.GetBatch(ctx, batchID)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if !batchAllowed(b, BatchCollecting, BatchConflict, BatchFailed) {
		return IntegrationBatch{}, fmt.Errorf("%w: 批次 %s 状态不可集成", ErrBatchState, b.Status)
	}
	if len(b.ChangeIDs) == 0 {
		return IntegrationBatch{}, fmt.Errorf("%w: 空批次不可集成", ErrBatchState)
	}
	owner, repoName, _, err := s.lookupRepo(ctx, b.AppID)
	if err != nil {
		return IntegrationBatch{}, err
	}

	// 集成分支重建（先删后建，幂等重跑）。基线取批内第一个变更的 BaseBranch
	// （默认 main；变更可指定其他基线，批次跟随首个变更，与平台代建分支语义一致——审计第 7 轮 M2）。
	baseBranch := "main"
	if len(b.ChangeIDs) > 0 {
		if c, cerr := s.repo.GetChange(ctx, b.ChangeIDs[0]); cerr == nil && c.BaseBranch != "" {
			baseBranch = c.BaseBranch
		}
	}
	if err := s.gitea.DeleteBranch(ctx, owner, repoName, b.Branch); err != nil && !errors.Is(err, gitea.ErrBranchNotFound) {
		return IntegrationBatch{}, fmt.Errorf("删除集成分支失败: %w", err)
	}
	if err := s.gitea.CreateBranch(ctx, owner, repoName, b.Branch, baseBranch); err != nil {
		return IntegrationBatch{}, fmt.Errorf("重建集成分支失败: %w", err)
	}

	// 有序 merge；冲突即停 + 标记冲突变更
	var prev string
	for _, cid := range b.ChangeIDs {
		c, err := s.repo.GetChange(ctx, cid)
		if err != nil {
			return IntegrationBatch{}, err
		}
		if _, err := s.gitea.Merge(ctx, owner, repoName, c.Branch, b.Branch, "merge"); err != nil {
			if errors.Is(err, gitea.ErrMergeConflict) {
				c.ConflictWith = prev
				if _, uerr := s.repo.UpdateChange(ctx, c); uerr != nil {
					return IntegrationBatch{}, uerr
				}
				b.Status = BatchConflict
				if _, uerr := s.repo.UpdateBatch(ctx, b); uerr != nil {
					return IntegrationBatch{}, uerr
				}
				return IntegrationBatch{}, &BatchConflictError{BatchID: b.ID, FailedChangeID: cid, PrevChangeID: prev}
			}
			return IntegrationBatch{}, fmt.Errorf("merge %s 失败: %w", c.Branch, err)
		}
		prev = cid
	}

	// 全成功：触发 CI run（app 第一条 ci 流水线，branch=集成分支）
	pid, err := s.runs.FindPipeline(ctx, b.AppID, "ci")
	if err != nil {
		return IntegrationBatch{}, err
	}
	if pid == "" {
		return IntegrationBatch{}, fmt.Errorf("%w: app=%s", ErrNoCIPipeline, b.AppID)
	}
	runID, err := s.runs.TriggerAppRun(ctx, b.AppID, pid, b.Branch)
	if err != nil {
		return IntegrationBatch{}, fmt.Errorf("触发 CI 失败: %w", err)
	}
	b.Status = BatchTesting
	b.PipelineID = pid
	b.RunID = runID
	if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
		return IntegrationBatch{}, err
	}
	// 批内变更全部 integrated（清 ConflictWith）
	for _, cid := range b.ChangeIDs {
		c, err := s.repo.GetChange(ctx, cid)
		if err != nil {
			return IntegrationBatch{}, err
		}
		c.Status = ChangeIntegrated
		c.ConflictWith = ""
		if _, err := s.repo.UpdateChange(ctx, c); err != nil {
			return IntegrationBatch{}, err
		}
	}
	return s.repo.GetBatch(ctx, batchID)
}

// Approve 审批通过：tested→releasing（prod:write 由 handler 校验）。
func (s *Service) Approve(ctx context.Context, batchID string) (IntegrationBatch, error) {
	b, err := s.repo.GetBatch(ctx, batchID)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if b.Status != BatchTested {
		return IntegrationBatch{}, fmt.Errorf("%w: 仅 tested 批次可审批", ErrBatchState)
	}
	// 清 RunID：留旧 CI run 会让 SyncBatchStatus 在 releasing 态读到 succeeded 提前判
	// released（审计第 2 轮 I1）；空 RunID 守卫使其不推进，等 Release 触发 CD run 后再判。
	b.Status = BatchReleasing
	b.RunID = ""
	b.PipelineID = ""
	if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
		return IntegrationBatch{}, err
	}
	return b, nil
}

// Release 发布编排（releasing 态，Approve 后调）：逐个 merge 到 main →
// 触发 CD run（branch=main）+ RunID 覆盖（批次只留当前活跃 run）。
// 冲突即停 + 批次回 tested + *BatchConflictError；重试由用户解决后重新 Release。
//
// 注意（已知边界）：已合并分支再 merge Gitea 返 409，当前按冲突处理；
// no-diff merge 的 409 文案实测后再区分，暂不做已合并检测。
func (s *Service) Release(ctx context.Context, batchID string) (IntegrationBatch, error) {
	if s.gitea == nil || s.runs == nil {
		return IntegrationBatch{}, ErrGiteaNotConfigured
	}
	b, err := s.repo.GetBatch(ctx, batchID)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if b.Status != BatchReleasing {
		return IntegrationBatch{}, fmt.Errorf("%w: 仅 releasing 批次可发布（先 approve）", ErrBatchState)
	}
	// fail-fast 预检 CD pipeline：在 merge 之前发现「无 CD 流水线」，避免 merge 全部成功后
	// trigger 失败停留 releasing → 重试被 409 误判冲突的半程失败（审计 I-2）。
	pid, err := s.runs.FindPipeline(ctx, b.AppID, "cd")
	if err != nil {
		return IntegrationBatch{}, err
	}
	if pid == "" {
		return IntegrationBatch{}, fmt.Errorf("%w: app=%s kind=cd", ErrNoCIPipeline, b.AppID)
	}
	owner, repoName, _, err := s.lookupRepo(ctx, b.AppID)
	if err != nil {
		return IntegrationBatch{}, err
	}

	var prev string
	for _, cid := range b.ChangeIDs {
		c, err := s.repo.GetChange(ctx, cid)
		if err != nil {
			return IntegrationBatch{}, err
		}
		if _, err := s.gitea.Merge(ctx, owner, repoName, c.Branch, "main", "merge"); err != nil {
			if errors.Is(err, gitea.ErrMergeConflict) {
				c.ConflictWith = prev
				if _, uerr := s.repo.UpdateChange(ctx, c); uerr != nil {
					return IntegrationBatch{}, uerr
				}
				b.Status = BatchTested // 回 tested，用户解决后可重试 Release
				if _, uerr := s.repo.UpdateBatch(ctx, b); uerr != nil {
					return IntegrationBatch{}, uerr
				}
				return IntegrationBatch{}, &BatchConflictError{BatchID: b.ID, FailedChangeID: cid, PrevChangeID: prev}
			}
			return IntegrationBatch{}, fmt.Errorf("merge %s 到 main 失败: %w", c.Branch, err)
		}
		prev = cid
	}

	// 全成功：触发 CD run（branch=main）。
	// trigger 失败显式回 tested：重试 Release 的 merge 已合并分支会 409 误判冲突，
	// 回 tested 让用户走 SyncBatchStatus 同款重试语义（可重新 Release）。
	runID, err := s.runs.TriggerAppRun(ctx, b.AppID, pid, "main")
	if err != nil {
		b.Status = BatchTested
		if _, uerr := s.repo.UpdateBatch(ctx, b); uerr != nil {
			return IntegrationBatch{}, fmt.Errorf("触发 CD 失败且回退状态失败: trigger=%v rollback=%w", err, uerr)
		}
		return IntegrationBatch{}, fmt.Errorf("触发 CD 失败（批次已回 tested 可重试）: %w", err)
	}
	b.PipelineID = pid
	b.RunID = runID // 覆盖 CI run，批次只留当前活跃 run
	if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
		return IntegrationBatch{}, err
	}
	return s.repo.GetBatch(ctx, batchID)
}

// SyncBatchStatus 惰性推进（GET 详情时调）：
//   - testing：CI 终态 succeeded→tested（change→tested）；failed/aborted→failed（change 回 open 出批）
//   - releasing：CD 终态 succeeded→released（change→released + FinishedAt + ReleaseIDs 回填）；
//     failed/aborted→回 tested（可重试 Release）
func (s *Service) SyncBatchStatus(ctx context.Context, batchID string) (IntegrationBatch, error) {
	b, err := s.repo.GetBatch(ctx, batchID)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if s.readRuns == nil || s.runs == nil || b.RunID == "" {
		return b, nil // 无活跃 run，不推进
	}
	if !batchAllowed(b, BatchTesting, BatchReleasing) {
		return b, nil
	}
	status, err := s.readRuns.GetRunStatus(ctx, b.RunID)
	if err != nil {
		return b, err // 读失败保持现状（下次再推）
	}

	switch b.Status {
	case BatchTesting:
		switch status {
		case "succeeded":
			// 仅推进 status：用单列 SetBatchStatus，防与并发入批的全量 UpdateBatch 互相覆盖
			if err := s.repo.SetBatchStatus(ctx, b.ID, BatchTested); err != nil {
				return IntegrationBatch{}, err
			}
			for _, cid := range b.ChangeIDs {
				c, err := s.repo.GetChange(ctx, cid)
				if err != nil {
					return IntegrationBatch{}, err
				}
				c.Status = ChangeTested
				if _, err := s.repo.UpdateChange(ctx, c); err != nil {
					return IntegrationBatch{}, err
				}
			}
		case "failed", "aborted":
			b.Status = BatchFailed
			if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
				return IntegrationBatch{}, err
			}
			// 批内变更全部回 open 出批（可重新入批）
			for _, cid := range b.ChangeIDs {
				c, err := s.repo.GetChange(ctx, cid)
				if err != nil {
					return IntegrationBatch{}, err
				}
				c.Status = ChangeOpen
				c.BatchID = ""
				c.ConflictWith = "" // 上轮冲突标记不残留到下次入批（审计第 7 轮 M1）
				if _, err := s.repo.UpdateChange(ctx, c); err != nil {
					return IntegrationBatch{}, err
				}
			}
			b.ChangeIDs = nil
			if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
				return IntegrationBatch{}, err
			}
		}
	case BatchReleasing:
		switch status {
		case "succeeded":
			relIDs, err := s.runs.ReleasesOfRun(ctx, b.RunID)
			if err != nil {
				return IntegrationBatch{}, err
			}
			b.Status = BatchReleased
			b.ReleaseIDs = relIDs
			b.FinishedAt = time.Now().UTC()
			if _, err := s.repo.UpdateBatch(ctx, b); err != nil {
				return IntegrationBatch{}, err
			}
			for _, cid := range b.ChangeIDs {
				c, err := s.repo.GetChange(ctx, cid)
				if err != nil {
					return IntegrationBatch{}, err
				}
				c.Status = ChangeReleased
				if _, err := s.repo.UpdateChange(ctx, c); err != nil {
					return IntegrationBatch{}, err
				}
			}
		case "failed", "aborted":
			// CD 失败停 releasing，回 tested 可重试（单列推进，不覆盖并发写字段）
			if err := s.repo.SetBatchStatus(ctx, b.ID, BatchTested); err != nil {
				return IntegrationBatch{}, err
			}
		}
	}
	return s.repo.GetBatch(ctx, batchID)
}
