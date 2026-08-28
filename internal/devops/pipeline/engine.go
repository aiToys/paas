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
	"runtime/debug"
	"strings"
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
	PollWorkloadReady(ctx context.Context, workloadID string) error     // 阻塞到 ready 或超时
	WorkloadDomain(ctx context.Context, workloadID string) string       // 拼探活 URL
	LatestReadyImage(ctx context.Context, appID string) (string, error) // app 最新 ready Image（CD 用）
	// Promote 晋升源 release 到下一环境（adapter 内部经 environment.NextPromoteTarget 算 target）。
	Promote(ctx context.Context, srcReleaseID string) (devops.Release, error)
	// SetVersion 给本次 run 涉及的 Release 批量回填版本号（baseline stage 打版本）。
	SetVersion(ctx context.Context, releaseIDs []string, version string) error
	// Deploy 部署镜像到 env×lane×service（找/建基线 Workload + UpdateImage），产生部署记录，不打版本。
	// service 标识同 app 多服务（空=单服务）；serviceID 关联服务实体（服务模型 Phase 1，驱动 Port/Replicas 回填，
	// adapter 在 service+serviceID 均空时自动解析 app 唯一服务）；port/containerPort 非零时在新建 Workload 时设定
	// （驱动 reconciler 建 Service，供 smoke 探活/服务发现）；复用既有 Workload 时忽略（端口属 Workload 既有配置）。
	// sourceRunID 非空时回填到部署记录。resources 资源规格（新建 Workload 时写入；既有 Workload
	// 且 resources 非空时覆盖更新）。replicas 期望副本（0=沿用默认，联调泳道降级后的值）。
	Deploy(ctx context.Context, appID, envID, lane, service, serviceID, imageID string, port, containerPort int, resources DeployResources, replicas int, sourceRunID string) (deployment devops.Release, domain string, err error)
	// Publish 打版本号里程碑：Image.Version 回填 + git tag（commit 非空且仓库为 internal 时）。
	// 不部署（部署是 deploy stage 的事）。返回 tagSha（未打 tag 时为空串）。
	Publish(ctx context.Context, appID, imageID, version, commit string) (tagSha string, err error)
	// DeployCanary 金丝雀并行验证部署（canary stage）：部署 imageID 到 canary-<sourceRunID> 并行泳道
	// （replicas=1，基线不动），返回部署记录 + 验证域名（Domain 可选外部验证域名，空则集群 FQDN）。
	// adapter 责任：lane=canary-<sanitized(sourceRunID)>；PAAS_DOMAIN_SUFFIX 非空时域名为 <wl-name>.<suffix>。
	DeployCanary(ctx context.Context, appID, envID, service, serviceID, imageID string, resources DeployResources, sourceRunID string) (deployment devops.Release, domain string, err error)
	// DeleteWorkload 删除 workload（canary 终止/abort 清理并行验证负载），adapter 负责配额 -1。
	DeleteWorkload(ctx context.Context, workloadID string) error
}

// DeployResources 是 deploy stage 的容器资源规格（K8s Quantity 字符串）。
// 与 workload.ResourceSpec / application.ResourceTemplate 同构——pipeline 不 import
// workload/application（依赖倒置），cmd/core 桥接时转换。
type DeployResources struct {
	CPURequest string `json:"cpuRequest,omitempty"`
	CPULimit   string `json:"cpuLimit,omitempty"`
	MemRequest string `json:"memRequest,omitempty"`
	MemLimit   string `json:"memLimit,omitempty"`
}

// IsEmpty 四字段全空（未指定资源规格，透传空保持 Workload 现状）。
func (r DeployResources) IsEmpty() bool {
	return r.CPURequest == "" && r.CPULimit == "" && r.MemRequest == "" && r.MemLimit == ""
}

// LaneEnsurer 桥接 lane.Repository.EnsureByName（依赖倒置，pipeline 不 import lane）。
// deploy 到非 default 泳道前懒建 Lane 实体（幂等，存在返回既有）。
type LaneEnsurer interface {
	Ensure(ctx context.Context, envID, name string) error
}

// AppResourceLookup 桥接 application.Repository（依赖倒置）：取应用级资源规格模板，
// deploy stage 未显式指定 resources 时作默认值。
type AppResourceLookup interface {
	Template(ctx context.Context, appID string) (DeployResources, error)
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
	// LaneEnsurer 可选：非 default 泳道 deploy 前懒建 Lane 实体；nil 时跳过（仅按名部署，无实体记录）。
	LaneEnsurer LaneEnsurer
	// AppResourceLookup 可选：deploy 未显式指定 resources 时取应用级资源模板；nil 或失败降级空（best-effort）。
	AppResourceLookup AppResourceLookup
	// EnvType 联调泳道副本降级的 prod 判定（nil 时保守按 prod 处理，不降级副本）。
	EnvType EnvTypeResolver

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// Start 起 goroutine 推进 runID（不阻塞，handler 调后立即返回）。
// 从 ctx 提取 tenant 注入 background（store 强制 tenant 过滤）；派生 cancelCtx 存入 cancels，
// Abort 时 cancel 传播给 PollBuildRun 阻塞调用。
func (e *Engine) Start(ctx context.Context, runID string) {
	tid, _ := tenant.TenantFrom(ctx)
	runCtx, cancel := context.WithCancel(context.Background()) //nolint:gosec // run 生命周期独立于触发请求；Abort 显式 cancel
	runCtx = tenant.WithTenant(runCtx, tid)
	e.mu.Lock()
	if e.cancels == nil {
		e.cancels = map[string]context.CancelFunc{}
	}
	e.cancels[runID] = cancel
	e.mu.Unlock()
	go func() {
		defer e.releaseCancel(runID)
		// panic recover：goroutine panic 会杀掉整个 core 进程（一个 run 的 bug 不应拖垮平台），
		// 捕获后用脱离取消的 ctx 标 run failed，防卡 running 占串行槽位（审计第 2 轮 I2）。
		defer func() {
			if p := recover(); p != nil {
				log.Printf("pipeline: advance run %s panic: %v\n%s", runID, p, debug.Stack()) //nolint:gosec // runID 平台生成
				markCtx := tenant.WithTenant(context.WithoutCancel(runCtx), tid)
				run, err := e.Runs.GetRun(markCtx, runID)
				if err != nil {
					return
				}
				if run.Status == RunRunning || run.Status == RunPaused {
					run.Status = RunFailed
					run.FinishedAt = time.Now()
					for i := range run.StageRuns {
						if run.StageRuns[i].Status == StageRunning {
							run.StageRuns[i].Status = StageFailed
							run.StageRuns[i].Error = fmt.Sprintf("内部错误: %v", p)
							run.StageRuns[i].FinishedAt = time.Now()
						}
					}
					_, _ = e.Runs.UpdateRun(markCtx, run)
				}
			}
		}()
		if err := e.Advance(runCtx, runID); err != nil {
			log.Printf("pipeline: advance run %s 失败: %v", runID, err) //nolint:gosec // runID 平台生成
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
// 清理残留 StageRunning 的 stage_runs 标 StageAborted（数据一致：否则 run=aborted 但 stage=running）。
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
	// 清理残留非终态 stage_runs：running（advance 退出时未标终态）+ waiting（Paused 时等待 approve/test-manual）。
	// 否则 run=aborted 但 stage=running/waiting，前端时间线显示不一致。
	for i := range run.StageRuns {
		s := run.StageRuns[i].Status
		if s == StageRunning || s == StageWaiting {
			run.StageRuns[i].Status = StageAborted
			run.StageRuns[i].FinishedAt = time.Now()
		}
	}
	// canary waiting 期 abort：清理并行验证 workload（基线从未被动过，无需回滚）。
	// best-effort：删除失败仅日志（GC 对 canary-<runID> 裸泳道 TTL 兜底回收）。
	if e.Releases != nil {
		for i := range run.StageRuns {
			if run.StageRuns[i].Type == StageCanary && run.StageRuns[i].Status == StageAborted {
				if wlID, _ := run.StageRuns[i].Output[OutCanaryWorkloadID].(string); wlID != "" {
					if err := e.Releases.DeleteWorkload(ctx, wlID); err != nil {
						log.Printf("canary abort 清理失败 wl=%s: %v（GC 兜底）", wlID, err)
					}
				}
			}
		}
	}
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

// Retry 重试失败的 run：从失败 stage（CurrentStage）重新推进。
// 仅 RunFailed 可 retry；重置该 stage 状态（Status=Pending，清 Error/Output/FinishedAt）+ run.Status=Running，
// 然后 Start 异步推进。成功/暂停/运行中的 run 拒绝（ErrNotFailed）。串行约束天然满足（failed 非 active）。
func (e *Engine) Retry(ctx context.Context, runID string) error {
	run, err := e.Runs.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != RunFailed {
		return ErrNotFailed
	}
	// 重置失败 stage：CurrentStage 指向失败的那一步（markFailed 不递增 CurrentStage）。
	idx := run.CurrentStage
	if idx < 0 || idx >= len(run.StageRuns) {
		return ErrStageNotCurrent
	}
	sr := &run.StageRuns[idx]
	sr.Status = StagePending
	sr.Error = ""
	sr.Output = nil
	sr.StartedAt = time.Time{}
	sr.FinishedAt = time.Time{}
	run.Status = RunRunning
	run.FinishedAt = time.Time{}
	if _, err := e.Runs.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.Start(ctx, runID)
	return nil
}

// Advance 同步推进 runID 到终态或暂停点。返回 nil 表示 run 已到终态（succeeded/failed/paused）。
// 测试直接调；Start 内部 go Advance。
func (e *Engine) Advance(ctx context.Context, runID string) error {
	for {
		// ctx cancel（Abort/进程退出）立即退出循环，防 cancel 后多推进一个同步 stage。
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
	case StageRelease:
		return e.execRelease(ctx, run, stage, sr)
	case StageTest:
		return e.execTest(ctx, run, stage, sr)
	case StageApprove:
		return e.execApprove(ctx, run, stage, sr)
	case StagePromote:
		return e.execPromote(ctx, run, stage, sr)
	case StageBaseline:
		return e.execBaseline(ctx, run, stage, sr)
	case StageCanary:
		return e.execCanary(ctx, run, stage, sr)
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
	logf(sr, "构建提交 buildRunId=%s", br.ID)
	// 合并而非覆盖：保留初始 Input（buildArgs/branchOverride 等解析参数，retry 时仍需），
	// 追加 buildRunId（前端拉构建日志依赖 stage.input.buildRunId）。
	// 覆盖整 map 会丢初始参数，retry 必失败。
	if sr.Input == nil {
		sr.Input = map[string]any{}
	}
	sr.Input["buildRunId"] = br.ID
	br, err = e.Builds.PollBuildRun(ctx, br.ID)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	logf(sr, "构建 %s", br.Status)
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

// execDeploy 部署 stage：resolveImage -> Releaser.Deploy(env×lane) -> PollWorkloadReady -> Output.releaseId+workloadDomain。
// 与 release 解耦：deploy 只部署 + 产生部署记录，不打版本（版本归 release stage）。
// lane 参数标识部署到哪条泳道（default=基线，其他=联调/灰度泳道），透传到 Workload.LaneID（L1 数据模型，L2 联调消费）。
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
	lane := strOr(stage.Params, "lane", LaneDefault)
	// service 标识部署到哪个服务（同 app 多服务场景，如 paas-shop product/recommend/...）。
	// 模板不填（app-specific），应用经 Pipeline.paramOverrides 覆盖（如 paas-shop product 各一条 pipeline）。
	// 空=单服务（向后兼容，CreateRelease 查找按 app×env×lane×service，service 空匹配单服务 Workload）。
	service := strOr(stage.Params, "service", "")
	// serviceId 关联服务实体（服务模型 Phase 1）：驱动 CreateRelease 回填 Port/Replicas + Workload.ServiceID。
	// 模板不填；应用经 paramOverrides 覆盖。service+serviceId 均空时 adapter 自动采用 app 唯一服务（单服务应用零操作）。
	serviceID := strOr(stage.Params, "serviceId", "")
	// port/containerPort 透传给新建 Workload（驱动 reconciler 建 Service）。复用既有 Workload 忽略。
	// 模板不填（app-specific），应用经 Pipeline.paramOverrides 覆盖（如 paas-shop product=8081）。
	port := intOr(stage.Params, "port", 0)
	cport := intOr(stage.Params, "containerPort", port)
	// 泳道实体化（Lane 一等公民）：非 default 泳道 deploy 前懒建 Lane 实体（幂等，存在返回既有）。
	// Ensure 失败 stage failed（实体是泳道管理/TTL 回收的真源，缺失致泳道成为黑盒）。
	if lane != LaneDefault && e.LaneEnsurer != nil {
		if err := e.LaneEnsurer.Ensure(ctx, envID, lane); err != nil {
			err := fmt.Errorf("泳道实体创建失败 %s: %w", lane, err)
			sr.Error = err.Error()
			return true, err
		}
		logf(sr, "泳道实体就绪 %s", lane)
	}
	// 资源规格解析（优先级）：① stage params 显式 resources > ② 应用级 ResourceTemplate > ③ 空（保持现状）。
	// Lookup 失败 best-effort 降级空（应用查不到/未注入不应阻断部署——资源规格是优化项非正确性项）。
	res := parseDeployResources(stage.Params)
	if res.IsEmpty() && e.AppResourceLookup != nil {
		if tpl, err := e.AppResourceLookup.Template(ctx, run.AppID); err == nil {
			res = tpl
		} else {
			logf(sr, "应用资源模板读取失败（降级空）: %v", err)
		}
	}
	// 标准基线「生产禁 BestEffort」：目标环境明确为 prod 且三级来源兜底仍空时 fail-fast
	//（终审 I2：workload handler 直建路径有拒建护栏，流水线是更重要的生产入口，不能裸奔）。
	// EnvType 未注入（测试/旧装配）跳过检查——与 allowProdFlow 的「nil 跳过」同语义，
	// 生产装配必注入 EnvType（cmd/core pipeline_adapters）。
	if e.EnvType != nil {
		if et, err := e.EnvType(ctx, envID); err == nil && et == "prod" && res.IsEmpty() {
			return false, fmt.Errorf("生产环境部署必须有资源规格（cpuRequest/memRequest 至少一项）：请在流水线 deploy stage 配 resources，或在应用配置 tab 设置资源规格默认值")
		}
	}
	// 联调泳道副本降级：非 default 泳道 + 非 prod 环境 + 显式 replicas>1 时截断为 1
	//（联调泳道只需验证功能，省资源；prod 环境不降级——灰度泳道按显式副本走）。
	// EnvType nil 时保守按 prod 处理不降级（fail-closed，与 allowProdFlow 同源语义）。
	replicas := intOr(stage.Params, "replicas", 0)
	if lane != LaneDefault && !e.laneIsProd(ctx, envID) {
		// 联调泳道单副本验证：显式 >1 截断为 1；未显式（0）也置 1——
		// 0 会让 store 回退服务定义 Replicas（如 3），逃逸泳道降级语义（审查 Important 修复）。
		if replicas == 0 {
			replicas = 1
			logf(sr, "联调泳道未显式副本，单副本验证（不沿用服务定义副本）")
		} else if replicas > 1 {
			logf(sr, "联调泳道副本降级 %d -> 1（非 prod 环境泳道单副本验证）", replicas)
			replicas = 1
		}
	}
	logf(sr, "部署镜像 %s 到 env=%s lane=%s service=%s serviceId=%s", imageID, envID, lane, service, serviceID)
	// prod 环境写受 prod:write 保护（adapter 内 CreateRelease 走 EnvTypeResolver）
	deployment, domain, err := e.Releases.Deploy(ctx, run.AppID, envID, lane, service, serviceID, imageID, port, cport, res, replicas, run.ID)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	// 合并而非覆盖：保留初始 Input（envId/lane/imageSource 等，retry 时 execDeploy 仍需 envId，
	// 覆盖丢 envId 会导致 retry fail-fast「deploy stage 缺 envId 参数」）。
	// 追加 imageId（priorBuild 链供下游 stage + 前端日志）；releaseId 走 Output（OutReleaseID）不写 Input。
	if sr.Input == nil {
		sr.Input = map[string]any{}
	}
	sr.Input["imageId"] = imageID
	logf(sr, "等待 Workload %s 就绪", deployment.WorkloadID)
	if err := e.Releases.PollWorkloadReady(ctx, deployment.WorkloadID); err != nil {
		sr.Error = err.Error()
		return true, err
	}
	logf(sr, "Workload 就绪，访问地址 %s", domain)
	sr.Output = map[string]any{OutReleaseID: deployment.ID, OutWorkloadDomain: domain}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// execRelease 发布版本号里程碑：打 git tag + Image.Version + 给本 run 部署记录回填 version。
// 不部署（部署是 deploy stage 的事）。version 缺省由 computeVersion 生成。
func (e *Engine) execRelease(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	version := computeVersion(*run, stage)
	logf(sr, "发布版本 %s", version)
	// 取前序 deploy 产出的 imageId（release 标记的是这个镜像）
	imageID, err := resolvePriorOutput(*run, OutImageID)
	if err == nil {
		tagSha, perr := e.Releases.Publish(ctx, run.AppID, imageID, version, run.Commit)
		if perr != nil {
			sr.Error = perr.Error()
			return true, perr
		}
		if tagSha != "" {
			logf(sr, "已打 git tag %s @ %s", version, tagSha[:min(8, len(tagSha))])
		}
	} else {
		logf(sr, "无前序镜像，跳过 tag（仅记录版本号）")
	}
	// 给本 run 涉及的部署记录回填 version（复用 SetVersion）
	var releaseIDs []string
	for i := 0; i < run.CurrentStage && i < len(run.StageRuns); i++ {
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
	sr.Output = map[string]any{OutVersion: version}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// logf 追加 stage 日志事件（append-only，前端展开查看）。时间戳前缀便于排序。
func logf(sr *StageRun, format string, args ...any) {
	sr.Log += time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...) + "\n"
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
	logf(sr, "探活 %s", url)
	// 两段探活：主路径先短探（15s，快速失败给降级让路），未过且非显式 / 时降级试根路径
	//（静态页应用无 /healthz，白等 2 分钟才降级的体验优化），降级也未过再回到主路径长探
	//（覆盖「应用启动慢」场景——短探失败可能是还没起完，不是路径不存在）。
	err = pollHTTP(ctx, url, 15*time.Second)
	if err != nil && path != "/" {
		fallback := fmt.Sprintf("http://%s/", domain)
		logf(sr, "%s 短探未过，降级尝试 %s", path, fallback)
		if err2 := pollHTTP(ctx, fallback, 30*time.Second); err2 == nil {
			err = nil
			url = fallback
		}
	}
	if err != nil {
		logf(sr, "回到主路径长探（最多 2 分钟，等待应用完全启动）")
		err = pollHTTP(ctx, url, 2*time.Minute)
	}
	if err != nil {
		// F6：失败信息带行动指引（检查服务端口/健康路径），不只是裸报错。
		sr.Error = fmt.Sprintf("探活失败 %s: %v（排查：确认服务监听端口与流水线一致、健康路径存在；可在「服务」tab 修改端口后在「流水线」paramOverrides 覆盖探活 path）", url, err)
		return true, err
	}
	logf(sr, "探活通过")
	sr.Output = map[string]any{"result": "ok", "url": url}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// execCanary 金丝雀验证 stage（并行验证式，spec D3=A）：部署新镜像到 canary-<runID> 并行泳道
// （1 副本，不动基线）→ StageWaiting 暂停等人工「确认放量/终止」。
// 诚实边界：这是 preview 式并行验证，非按比例切流——真切流依赖生产流量权重（D1 留后续）。
// batches/observationMins 参数保留前向兼容（本实现不消费）。
func (e *Engine) execCanary(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	imageID, err := e.resolveImage(ctx, stage, *run)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	envID := strOr(stage.Params, "envId", "")
	if envID == "" {
		// 未显式指定时沿用前序 deploy 的 envId（canary 通常紧跟 deploy stage 验证其产物）
		envID = strFromInput(sr.Input, "envId")
		if envID == "" {
			err := fmt.Errorf("canary stage 缺 envId 参数")
			sr.Error = err.Error()
			return true, err
		}
	}
	service := strOr(stage.Params, "service", "")
	serviceID := strOr(stage.Params, "serviceId", "")
	lane := "canary-" + dns1035(run.ID) // 并行验证泳道，随 run 命名（Abort/GC 兜底回收）
	// 资源规格：三级来源复用 deploy 同款解析（canary 与基线同规格验证才有意义）
	res := parseDeployResources(stage.Params)
	if res.IsEmpty() && e.AppResourceLookup != nil {
		if tpl, err := e.AppResourceLookup.Template(ctx, run.AppID); err == nil {
			res = tpl
		}
	}
	// 泳道实体懒建（与 deploy 同语义：Lane 实体是泳道管理/TTL 回收真源）。
	if e.LaneEnsurer != nil {
		if err := e.LaneEnsurer.Ensure(ctx, envID, lane); err != nil {
			err := fmt.Errorf("泳道实体创建失败 %s: %w", lane, err)
			sr.Error = err.Error()
			return true, err
		}
	}
	logf(sr, "金丝雀验证：部署 %s 到并行泳道 %s（基线不动）", imageID, lane)
	deployment, domain, err := e.Releases.Deploy(ctx, run.AppID, envID, lane, service, serviceID, imageID, 0, 0, res, 1, run.ID)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	logf(sr, "等待金丝雀 Workload %s 就绪", deployment.WorkloadID)
	if err := e.Releases.PollWorkloadReady(ctx, deployment.WorkloadID); err != nil {
		sr.Error = err.Error()
		return true, err
	}
	logf(sr, "金丝雀就绪，验证地址 %s —— 观察指标/日志后「确认放量」或「终止」", domain)
	if sr.Input == nil {
		sr.Input = map[string]any{}
	}
	sr.Input["imageId"] = imageID
	sr.Input["envId"] = envID
	sr.Input["service"] = service
	sr.Input["serviceId"] = serviceID
	sr.Output = map[string]any{
		OutReleaseID:        deployment.ID,
		OutCanaryWorkloadID: deployment.WorkloadID,
		OutCanaryDomain:     domain,
	}
	sr.Status = StageWaiting
	return false, nil
}

// dns1035 清洗为 K8s Service 名合法字符（DNS-1035：小写字母数字与 -，首字母，≤63）。
// 复制自 internal/devops model.go 同名函数（pipeline 不 import workload/devops store 侧，
// 依赖倒置约束——10 行纯函数本体复制）。
func dns1035(name string) string {
	var b []byte
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b = append(b, byte(r))
		case r >= 'A' && r <= 'Z':
			b = append(b, byte(r-'A'+'a'))
		default:
			b = append(b, '-')
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	out := strings.Trim(string(b), "-")
	if out != "" && (out[0] < 'a' || out[0] > 'z') {
		out = "n" + out
	}
	return out
}

// strFromInput 取 StageRun.Input map 的 string 值（类型断言失败返空串）。
func strFromInput(input map[string]any, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

// execApprove 审批 stage：暂停 run 等外部 Resume（通过）或 Abort（拒绝）。
func (e *Engine) execApprove(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	message := strOr(stage.Params, "message", "等待审批")
	logf(sr, "等待审批：%s", message)
	sr.Status = StageWaiting
	sr.Input = map[string]any{"message": message}
	return false, nil
}

// execPromote 晋升 stage：取前序 deploy 的 releaseId -> Releaser.Promote 晋升到下一环境基线。
// adapter 内部经 environment.NextPromoteTarget 算 target + CreateRelease(LaneDefault) 走基线泳道。
// 已是最高阶环境 -> ErrNoPromoteTarget 标 failed。
func (e *Engine) execPromote(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	srcReleaseID, err := resolvePriorOutput(*run, OutReleaseID)
	if err != nil {
		sr.Error = err.Error()
		return true, err
	}
	logf(sr, "晋升发布 %s 到下一阶序环境", srcReleaseID)
	rel, err := e.Releases.Promote(ctx, srcReleaseID)
	if err != nil {
		if errors.Is(err, environment.ErrNoPromoteTarget) {
			sr.Error = "已是最高阶环境，无晋升目标"
		} else {
			sr.Error = err.Error()
		}
		return true, err
	}
	logf(sr, "已晋升到 %s", rel.ID)
	sr.Output = map[string]any{
		OutReleaseID:      rel.ID,
		OutWorkloadDomain: e.Releases.WorkloadDomain(ctx, rel.WorkloadID),
	}
	sr.Status = StageSuccess
	sr.FinishedAt = time.Now()
	return true, nil
}

// execBaseline 基线 stage：只合并主干（版本号已归 release stage）。
// mainBranch 空 / Gitea 未注入 -> 跳过合并直接 success（兼容无 release stage 的旧模板）。
// merge 冲突仅记 sr.Error 警告让用户手动解决，stage 仍 success 不中止 run。
func (e *Engine) execBaseline(ctx context.Context, run *PipelineRun, stage StageDef, sr *StageRun) (bool, error) {
	out := map[string]any{}
	mainBranch := strOr(stage.Params, "mainBranch", "")
	if mainBranch == "" {
		logf(sr, "未配 mainBranch，跳过合并")
		sr.Output = out
		sr.Status = StageSuccess
		sr.FinishedAt = time.Now()
		return true, nil
	}
	if e.Gitea == nil {
		logf(sr, "未接入 Gitea，跳过合并")
		sr.Output = out
		sr.Status = StageSuccess
		sr.FinishedAt = time.Now()
		return true, nil
	}
	// run 分支与主干相同（如直接从 main 触发 CD）时无变更可合，明确跳过——
	// 避免对 Gitea 发 head==base 的 PR 拿 422「无差异」，错误吞进 sr.Error 造成 stage 假绿、日志仅一行。
	if run.Branch == mainBranch {
		logf(sr, "run 分支 %s 与主干相同，无变更可合并，跳过", run.Branch)
		sr.Output = out
		sr.Status = StageSuccess
		sr.FinishedAt = time.Now()
		return true, nil
	}
	logf(sr, "合并 %s -> %s", run.Branch, mainBranch)
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
			logf(sr, "已合并 %s", mergeSHA[:min(8, len(mergeSHA))])
		}
	}
	// ResolveRepo 失败（external repo / 无 internal repo）跳过 merge，仅记录
	sr.Output = out
	sr.Status = StageSuccess // 冲突也标 success（merge 可手动补）
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
//
// 并发安全：两次并发 approve 会都读到同一份 paused 快照。用 e.mu 把「校验 paused + 转 running +
// UpdateRun」串行化——第二个 Resume 进入时 run 已是 running，ErrNotPaused 拒绝，避免起两个
// advance goroutine + cancels map 互相覆盖。Start 的 cancels 注册在锁外（其内部自锁，防自死锁）。
func (e *Engine) Resume(ctx context.Context, runID string, stageIdx int) error {
	e.mu.Lock()
	run, err := e.Runs.GetRun(ctx, runID)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if run.Status != RunPaused {
		e.mu.Unlock()
		return ErrNotPaused
	}
	if stageIdx != run.CurrentStage {
		e.mu.Unlock()
		return ErrStageNotCurrent
	}
	run.StageRuns[stageIdx].Status = StageSuccess
	run.StageRuns[stageIdx].FinishedAt = time.Now()
	run.CurrentStage++
	run.Status = RunRunning
	_, err = e.Runs.UpdateRun(ctx, run)
	e.mu.Unlock()
	if err != nil {
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
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // URL 为引擎内部拼出的 workload FQDN，非用户输入
		if err != nil {
			return err
		}
		resp, err := client.Do(req) //nolint:gosec // URL 为引擎内部拼出的 workload FQDN，非用户输入
		if err == nil {
			_ = resp.Body.Close() //nolint:gosec
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

// intOr 从 params 取 int（deploy stage port/containerPort 等）。支持 JSON 反序列化的
// float64（json.Unmarshal 把数字统一解成 float64）+ int + 字符串数字。缺失/非法返 def。
func intOr(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i
		}
	}
	return def
}

// parseDeployResources 从 stage params 解析资源规格（同 buildArgs 的 getStringMap 模式）。
// key：cpuRequest/cpuLimit/memRequest/memLimit；非字符串值跳过。
func parseDeployResources(params map[string]any) DeployResources {
	m := getStringMap(params, "resources")
	return DeployResources{
		CPURequest: m["cpuRequest"],
		CPULimit:   m["cpuLimit"],
		MemRequest: m["memRequest"],
		MemLimit:   m["memLimit"],
	}
}

// laneIsProd 判定目标环境是否 prod（联调泳道副本降级用）。EnvType 未注入或解析失败时
// 保守返 true（按 prod 处理不降级，fail-closed 防误降级生产灰度泳道副本）。
func (e *Engine) laneIsProd(ctx context.Context, envID string) bool {
	if e.EnvType == nil {
		return true
	}
	etype, err := e.EnvType(ctx, envID)
	if err != nil || etype == environment.TypeProd {
		return true
	}
	return false
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
