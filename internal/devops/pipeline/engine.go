// engine.go Pipeline 执行引擎：异步推进 PipelineRun 状态机。
//
// 借鉴 Spinnaker（stage 序列 + 异步推进）+ ArgoCD（artifact source 解耦）。
// engine 不碰 HTTP，由 handler 调 Start 起 goroutine 推进；Advance 同步推进供测试。
//
// 依赖倒置：BuildRunner/Releaser 接口桥接 devops 业务包（engine 不直接 import
// devops store，避免循环；cmd/core 装配时实现 adapter 注入）。
//
// stage 输出链：build.Output.imageId -> deploy(priorBuild) -> deploy.Output.releaseId ->
// promote/baseline。resolvePriorOutput 向前扫描已完成 stage 的 Output 取 key。
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/pkg/tenant"
)

// BuildRunner 桥接 devops BuildRunRepository（依赖倒置）。
// PollBuildRun 阻塞轮询到终态（success/failed），engine goroutine 在此等待。
type BuildRunner interface {
	CreateBuildRun(ctx context.Context, appID, repoID, branch, commit string, buildArgs map[string]string) (devops.BuildRun, error)
	PollBuildRun(ctx context.Context, buildID string) (devops.BuildRun, error)
}

// Releaser 桥接 devops ReleaseRepository + workload readiness + ImageRepository。
type Releaser interface {
	CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error)
	PollWorkloadReady(ctx context.Context, workloadID string) error // 阻塞到 ready 或超时
	WorkloadDomain(ctx context.Context, workloadID string) string    // 拼探活 URL
	LatestReadyImage(ctx context.Context, appID string) (string, error) // app 最新 ready Image（CD 用）
	// Promote 晋升源 release 到下一环境（adapter 内部经 environment.NextPromoteTarget 算 target）。
	Promote(ctx context.Context, srcReleaseID string) (devops.Release, error)
	// SetVersion 给本次 run 涉及的 Release 批量回填版本号（baseline stage 打版本）。
	SetVersion(ctx context.Context, releaseIDs []string, version string) error
}

// GiteaMerger 桥接 gitea.Client（baseline stage 合并主干）。
// ResolveRepo 取 app 绑定的 internal CodeRepo 的 owner/repo；external repo 返错（跳过 merge）。
type GiteaMerger interface {
	ResolveRepo(ctx context.Context, appID string) (owner, repo string, err error)
	Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error)
}

// Engine 异步推进 PipelineRun 状态机。
// Pipelines 取 stage 定义（run 不嵌入 Pipeline，按 PipelineID 联合查）；
// Runs 读写 run 状态；Builds/Releases 桥接 devops 执行体；Gitea 可选（baseline merge）。
// cancels 维护 run->cancel，Abort 时 cancel 传播给进行中的 PollBuildRun。
type Engine struct {
	Pipelines Repository
	Runs      RunRepository
	Builds    BuildRunner
	Releases  Releaser
	Gitea     GiteaMerger // 可选，nil 时 baseline 跳过 merge 仅打版本

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// Start 起 goroutine 推进 runID（不阻塞，handler 调后立即返回）。
// 从 ctx 提取 tenant 注入 background（store 强制 tenant 过滤）；派生 cancelCtx 存入 cancels，
// Abort 时 cancel 传播给 PollBuildRun 阻塞调用。
func (e *Engine) Start(ctx context.Context, runID string) {
	tid, _ := tenant.TenantFrom(ctx)
	runCtx, cancel := context.WithCancel(context.Background())
	runCtx = tenant.WithTenant(runCtx, tid)
	e.mu.Lock()
	if e.cancels == nil {
		e.cancels = map[string]context.CancelFunc{}
	}
	e.cancels[runID] = cancel
	e.mu.Unlock()
	go func() {
		defer e.releaseCancel(runID)
		if err := e.Advance(runCtx, runID); err != nil {
			log.Printf("pipeline: advance run %s 失败: %v", runID, err)
		}
	}()
}

func (e *Engine) releaseCancel(runID string) {
	e.mu.Lock()
	delete(e.cancels, runID)
	e.mu.Unlock()
}

// Abort 终止 run：标 aborted + cancel 进行中的 advance（PollBuildRun 因 ctx.Done 退出）。
// 仅 running/paused 可 abort；已终态（succeeded/failed/aborted）返 ErrNotRunning。
func (e *Engine) Abort(ctx context.Context, runID string) error {
	run, err := e.Runs.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != RunRunning && run.Status != RunPaused {
		return ErrNotRunning
	}
	run.Status = RunAborted
	run.FinishedAt = time.Now()
	if _, err := e.Runs.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.mu.Lock()
	if cancel, ok := e.cancels[runID]; ok {
		cancel()
	}
	e.mu.Unlock()
	return nil
}

// Advance 同步推进 runID 到终态或暂停点。返回 nil 表示 run 已到终态（succeeded/failed/paused）。
// 测试直接调；Start 内部 go Advance。
func (e *Engine) Advance(ctx context.Context, runID string) error {
	for {
		run, err := e.Runs.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Status != RunRunning {
			return nil // paused/failed/aborted/succeeded 都停
		}
		// stages 已在触发时实例化到 run.StageRuns（Input=resolved params）。
		// 绑定模型：Pipeline 无 Stages，运行时用 run.StageRuns（不再加载 Pipeline 实体）。
		if run.CurrentStage >= len(run.StageRuns) {
			return e.markSucceeded(ctx, run)
		}
		sr := &run.StageRuns[run.CurrentStage]
		// 跳过已完成的 stage（恢复场景）
		if sr.Status == StageSuccess || sr.Status == StageSkipped {
			run.CurrentStage++
			if _, err := e.Runs.UpdateRun(ctx, run); err != nil {
				return err
			}
			continue
		}
		stage := StageDef{Type: sr.Type, Name: sr.Name, Params: sr.Input}
		finished, err := e.execStage(ctx, &run, stage, sr)
		if err != nil {
			sr.Status = StageFailed // execStage 已写 sr.Error，此处补 stage 终态
			return e.markFailed(ctx, run, err)
		}
		if !finished {
			// paused（test-manual/approve），标 Paused 等外部恢复
			return e.markPaused(ctx, run)
		}
		// stage 成功，推进
		run.CurrentStage++
		if _, err := e.Runs.UpdateRun(ctx, run); err != nil {
			return err
		}
	}
}

// execStage 执行单个 stage，返回 (finished, error)。
// finished=false 表示 paused（test-manual/approve），等外部恢复；
// error 非 nil 表示 failed（中止整条 run，cause 已写 sr.Error）。
func (e *Engine) execStage(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	sr.StartedAt = time.Now()
	sr.Status = StageRunning
	switch stage.Type {
	case StageBuild:
		return e.execBuild(ctx, run, stage, sr)
	case StageDeploy:
		return e.execDeploy(ctx, run, stage, sr)
	case StageTest:
		return e.execTest(ctx, run, stage, sr)
	case StageApprove:
		return e.execApprove(ctx, run, stage, sr)
	case StagePromote:
		return e.execPromote(ctx, run, stage, sr)
	case StageBaseline:
		return e.execBaseline(ctx, run, stage, sr)
	}
	err := fmt.Errorf("未知 stage 类型 %s", stage.Type)
	sr.Error = err.Error()
	return true, err
}

// execBuild 构建 stage：CreateBuildRun -> PollBuildRun 到终态 -> Output.imageId。
func (e *Engine) execBuild(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	buildArgs := getStringMap(stage.Params, "buildArgs")
	branch := strOr(stage.Params, "branchOverride", run.Branch)
	br, err := e.Builds.CreateBuildRun(ctx, run.AppID, run.RepoID, branch, run.Commit, buildArgs)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	sr.Input = map[string]any{"buildRunId": br.ID}
	br, err = e.Builds.PollBuildRun(ctx, br.ID)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	if br.Status != devops.BuildSuccess {
		err := fmt.Errorf("构建失败: %s", br.Status)
		sr.Error = err.Error()
		return true, err
	}
	sr.Output = map[string]any{OutImageID: br.ImageID}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// execDeploy 部署 stage：resolveImage -> CreateRelease -> PollWorkloadReady -> Output.releaseId+workloadDomain。
func (e *Engine) execDeploy(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	imageID, err := e.resolveImage(ctx, stage, *run)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	envID := strOr(stage.Params, "envId", "")
	if envID == "" {
		err := fmt.Errorf("deploy stage 缺 envId 参数")
		sr.Error = err.Error()
		return true, err
	}
	rel, err := e.Releases.CreateRelease(ctx, devops.ReleaseInput{
		AppID:    run.AppID,
		EnvID:    envID,
		ImageID:  imageID,
		Strategy: strOr(stage.Params, "strategy", "rolling"),
	})
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	sr.Input = map[string]any{"releaseId": rel.ID, "imageId": imageID}
	if err := e.Releases.PollWorkloadReady(ctx, rel.WorkloadID); err != nil {
		sr.Error = err.Error()
		return true, err
	}
	domain := e.Releases.WorkloadDomain(ctx, rel.WorkloadID)
	sr.Output = map[string]any{OutReleaseID: rel.ID, OutWorkloadDomain: domain}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// execTest 测试 stage：smoke（HTTP 探活自动）/ manual（人工确认暂停）。
// smoke 取前序 deploy 的 workloadDomain + path 轮询 2xx；manual 暂停等 Resume。
func (e *Engine) execTest(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	mode := strOr(stage.Params, "mode", TestSmoke)
	if mode == TestManual {
		// 人工确认：暂停 run，等外部 Resume
		sr.Status = StageWaiting
		sr.Input = map[string]any{"mode": TestManual, "message": strOr(stage.Params, "message", "请确认测试通过")}
		return false, nil
	}
	// smoke：探活前序 deploy 的 domain + path
	domain, err := resolvePriorOutput(*run, OutWorkloadDomain)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	path := strOr(stage.Params, "path", "/livez")
	url := fmt.Sprintf("http://%s%s", domain, path)
	if err := pollHTTP(ctx, url, 2*time.Minute); err != nil {
		sr.Error = fmt.Sprintf("探活失败 %s: %v", url, err)
		return true, err
	}
	sr.Output = map[string]any{"result": "ok", "url": url}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// execApprove 审批 stage：暂停 run 等外部 Resume（通过）或 Abort（拒绝，Task 12）。
func (e *Engine) execApprove(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	sr.Status = StageWaiting
	sr.Input = map[string]any{"message": strOr(stage.Params, "message", "等待审批")}
	return false, nil
}

// execPromote 晋升 stage：取前序 deploy 的 releaseId -> Releaser.Promote 晋升到下一环境。
// adapter 内部经 environment.NextPromoteTarget 算 target；已是最高阶返 ErrNoPromoteTarget。
func (e *Engine) execPromote(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	srcReleaseID, err := resolvePriorOutput(*run, OutReleaseID)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	rel, err := e.Releases.Promote(ctx, srcReleaseID)
	if err != nil {
		if errors.Is(err, environment.ErrNoPromoteTarget) {
			sr.Error = "已是最高阶环境，无晋升目标"
		} else {
			sr.Error = err.Error()
		}
		return true, err
	}
	sr.Output = map[string]any{OutReleaseID: rel.ID}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// execBaseline 基线 stage：1. 打版本（回填本次 run 涉及的所有 Release.Version）+ 2. 合并主干（可选）。
// merge 冲突不中止（版本已打），仅记 sr.Error 警告，让用户手动解决。
func (e *Engine) execBaseline(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	version := computeVersion(*run, stage)
	// 收集本次 run 涉及的所有 releaseID（deploy/promote stage 输出，向前扫描）
	var releaseIDs []string
	for i := 0; i <= run.CurrentStage && i < len(run.StageRuns); i++ {
		if id, ok := run.StageRuns[i].Output[OutReleaseID].(string); ok && id != "" {
			releaseIDs = append(releaseIDs, id)
		}
	}
	if len(releaseIDs) > 0 {
		if err := e.Releases.SetVersion(ctx, releaseIDs, version); err != nil {
			sr.Error = err.Error()
			return true, err
		}
	}
	run.Version = version
	out := map[string]any{OutVersion: version}

	// 合并主干（mainBranch 非空 + Gitea 注入才 merge）
	mainBranch := strOr(stage.Params, "mainBranch", "")
	if mainBranch != "" && e.Gitea != nil {
		owner, repo, err := e.Gitea.ResolveRepo(ctx, run.AppID)
		if err == nil {
			mergeSHA, merr := e.Gitea.Merge(ctx, owner, repo, run.Branch, mainBranch,
				strOr(stage.Params, "mergeMode", "squash"))
			if merr != nil {
				if errors.Is(merr, gitea.ErrMergeConflict) {
					sr.Error = "合并冲突，请手动解决"
				} else {
					sr.Error = merr.Error()
				}
			} else {
				out[OutMergeSHA] = mergeSHA
			}
		}
		// ResolveRepo 失败（external repo / 无 internal repo）跳过 merge，仅打版本
	}
	sr.Output = out
	sr.Status = StageSuccess // baseline 即使 merge 冲突也标 success（版本已打）
	sr.FinishedAt = time.Now()
	return true, nil
}

// computeVersion 版本号生成。
// auto-increment=<branch>-<runID 后 6 位>；manual=触发时填的 run.Version；tag=commit 短 sha。
func computeVersion(run PipelineRun, stage StageDef) string {
	switch strOr(stage.Params, "versionStrategy", "auto-increment") {
	case "manual":
		if run.Version != "" {
			return run.Version
		}
	case "tag":
		if run.Commit != "" {
			n := 8
			if len(run.Commit) < n {
				n = len(run.Commit)
			}
			return run.Commit[:n]
		}
	}
	return fmt.Sprintf("%s-%s", run.Branch, shortID(run.ID))
}

// shortID 取 ID 后 6 位（runSeq 占位，无全局序列时的确定性派生）。
func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[len(id)-6:]
}

// Resume 恢复 paused run 的某 stage（handler approve 调用）。
// 标记该 stage 成功 + currentStage++ + run.Status=running，再启 advance 异步推进。
// 拒绝审批走 Abort（Task 12），此处只处理通过路径。
func (e *Engine) Resume(ctx context.Context, runID string, stageIdx int) error {
	run, err := e.Runs.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != RunPaused {
		return ErrNotPaused
	}
	if stageIdx != run.CurrentStage {
		return ErrStageNotCurrent
	}
	run.StageRuns[stageIdx].Status = StageSuccess
	run.StageRuns[stageIdx].FinishedAt = time.Now()
	run.CurrentStage++
	run.Status = RunRunning
	if _, err := e.Runs.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.Start(ctx, runID)
	return nil
}

// pollHTTP GET 轮询 url 到 2xx 或超时，用于 smoke 探活。
// 不跟随重定向（CheckRedirect=ErrUseLastResponse，与平台出站 client SSRF 防护一致）。
func pollHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("探活超时")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// resolveImage deploy stage 镜像来源解析（CI/CD 解耦核心）。
// selected=指定 imageId；latestReady=app 最新 ready Image；priorBuild=前序 build stage 产出。
func (e *Engine) resolveImage(ctx context.Context, stage StageDef, run PipelineRun) (string, error) {
	switch strOr(stage.Params, "imageSource", ImagePriorBuild) {
	case ImageSelected:
		id := strOr(stage.Params, "imageId", "")
		if id == "" {
			return "", fmt.Errorf("imageSource=selected 缺 imageId 参数")
		}
		return id, nil
	case ImageLatestReady:
		return e.Releases.LatestReadyImage(ctx, run.AppID)
	case ImagePriorBuild:
		return resolvePriorOutput(run, OutImageID)
	}
	return "", fmt.Errorf("未知 imageSource")
}

// resolvePriorOutput 从当前 stage 之前已完成 stage 的 Output 取 key（向前扫描）。
// 用于 build.Output.imageId -> deploy(priorBuild) 等输出链。
func resolvePriorOutput(run PipelineRun, key string) (string, error) {
	for i := run.CurrentStage - 1; i >= 0; i-- {
		if v, ok := run.StageRuns[i].Output[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("前序 stage 无 %s 输出", key)
}

// ---------- 状态机落库 ----------

func (e *Engine) markFailed(ctx context.Context, run PipelineRun, cause error) error {
	if ctx.Err() != nil {
		return nil // Abort 已标 aborted，不覆盖
	}
	run.Status = RunFailed
	run.FinishedAt = time.Now()
	// cause 已在 execStage 写入对应 sr.Error；此处只落 run 状态
	_, err := e.Runs.UpdateRun(ctx, run)
	return err
}

func (e *Engine) markSucceeded(ctx context.Context, run PipelineRun) error {
	if ctx.Err() != nil {
		return nil // Abort 已标 aborted，不覆盖
	}
	run.Status = RunSucceeded
	run.FinishedAt = time.Now()
	_, err := e.Runs.UpdateRun(ctx, run)
	return err
}

func (e *Engine) markPaused(ctx context.Context, run PipelineRun) error {
	run.Status = RunPaused
	_, err := e.Runs.UpdateRun(ctx, run)
	return err
}

// ---------- map[string]any 类型断言 helper ----------

// strOr 从 params 取 string，空或缺失返 def。
func strOr(params map[string]any, key, def string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

// getStringMap 从 params 取 map[string]string（buildArgs 等字符串映射）。
// params 值是 map[string]any（JSON 解析），逐项断言 string。
func getStringMap(params map[string]any, key string) map[string]string {
	out := map[string]string{}
	if v, ok := params[key]; ok {
		if m, ok := v.(map[string]any); ok {
			for k, val := range m {
				if s, ok := val.(string); ok {
					out[k] = s
				}
			}
		}
	}
	return out
}
