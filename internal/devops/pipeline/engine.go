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
	"fmt"
	"log"
	"time"

	"github.com/aitoys/paas/internal/devops"
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
}

// Engine 异步推进 PipelineRun 状态机。
// Pipelines 取 stage 定义（run 不嵌入 Pipeline，按 PipelineID 联合查）；
// Runs 读写 run 状态；Builds/Releases 桥接 devops 执行体。
type Engine struct {
	Pipelines Repository
	Runs      RunRepository
	Builds    BuildRunner
	Releases  Releaser
}

// Start 起 goroutine 推进 runID（不阻塞，handler 调后立即返回）。
// ctx 派生自 background（进程级，不绑请求生命周期）；进程退出 goroutine 终止，
// run 卡 Running（留后续 SweepInterrupted 兜底，同 devops runBuild 模式）。
func (e *Engine) Start(ctx context.Context, runID string) {
	go func() {
		if err := e.Advance(context.Background(), runID); err != nil {
			log.Printf("pipeline: advance run %s 失败: %v", runID, err)
		}
	}()
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
		pipe, err := e.Pipelines.GetPipeline(ctx, run.PipelineID)
		if err != nil {
			return e.markFailed(ctx, run, fmt.Errorf("加载 pipeline 失败: %w", err))
		}
		if run.CurrentStage >= len(pipe.Stages) {
			return e.markSucceeded(ctx, run)
		}
		stage := pipe.Stages[run.CurrentStage]
		// run.StageRuns 可能少于 pipe.Stages（恢复场景）；按需扩容
		for len(run.StageRuns) <= run.CurrentStage {
			run.StageRuns = append(run.StageRuns, StageRun{
				Index: len(run.StageRuns), Type: stage.Type, Name: stage.Name, Status: StagePending,
			})
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
	case StageTest, StageApprove, StagePromote, StageBaseline:
		err := fmt.Errorf("stage 类型 %s 未实现（Task 9/10）", stage.Type)
		sr.Error = err.Error()
		return true, err
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
	run.Status = RunFailed
	run.FinishedAt = time.Now()
	// cause 已在 execStage 写入对应 sr.Error；此处只落 run 状态
	_, err := e.Runs.UpdateRun(ctx, run)
	return err
}

func (e *Engine) markSucceeded(ctx context.Context, run PipelineRun) error {
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
