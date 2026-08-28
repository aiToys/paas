# 金丝雀验证 stage（并行验证式）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 流水线新增 `canary` stage 类型——并行验证式金丝雀（spec D3=A 定案）：部署新镜像到 `canary-<runID>` 泳道 → waiting 人工观察（指标卡 + 域名直链）→ 「确认放量」基线全量滚动 + 删 canary / 「终止」仅删 canary（基线零风险退出）。

**Architecture:** 复用三套既有机制——① approve 的 `StageWaiting + RunPaused + Resume` 暂停恢复；② 泳道并行 Deployment 底座（lane 隔离）；③ Workload.Domain + reconciler applyIngress（canary 独立验证域名）。engine 不碰 workload 具体类型（依赖倒置，Releaser 扩展 `DeployCanary`/`DeleteWorkload` 两方法，cmd/core 桥接）。

**Tech Stack:** Go（pipeline engine/handler/adapters）+ Vue 3（PipelineRunView 观察面板）。

**Spec:** `docs/superpowers/specs/2026-08-26-lane-entity-and-deploy-policy-design.md` §3.3（D3 定案：并行验证式，非按比例切流；batches 参数保留前向兼容）。

## Global Constraints

- 流水线包不 import workload/application/lane 具体实现（依赖倒置，经 Releaser/LaneEnsurer 接口）。
- 所有响应 `{data:T}` 契约（`httputil.WriteData*`）；错误经 `WriteServiceError` 脱敏。
- 生产写操作（canary promote 到 prod 基线）需 `prod:write`；受限应用需 appGuard `release`。
- 写操作记审计（`canary_promote`/`canary_terminate`，复用 handler `recordAudit`）。
- UI 文案叫「金丝雀验证」不叫「灰度放量」（诚实边界：并行验证非比例切流）。
- OpenAPI 登记新端点。
- 注释中文，与代码库一致。

---

### Task 1: canary stage 类型 + execCanary 部署等待

**Files:**
- Modify: `internal/devops/pipeline/model.go`（常量 + validate + Output key 常量）
- Modify: `internal/devops/pipeline/engine.go`（execStage 分发 + execCanary + Abort 清理）
- Test: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Consumes: 既有 `Releaser.Deploy`/`PollWorkloadReady`、`parseDeployResources`、`resolveImage`、`logf`、`StageWaiting/RunPaused`。
- Produces:
  - `StageCanary = "canary"` 常量；`StageDef.validate` 接受 canary。
  - Output keys：`OutCanaryWorkloadID = "canaryWorkloadId"`、`OutCanaryDomain = "canaryDomain"`（新增于 model.go Output 常量块）。
  - `execCanary(ctx, run, stage, sr) (bool, error)`。
  - 本 task **暂不**调 DeployCanary（Task 2 加），先用既有 `Releaser.Deploy` 以 lane=`canary-<runID>` 部署（fakeReleaser 已可跑通）；Task 2 再切换到 `DeployCanary` 拿域名。

- [ ] **Step 1: model.go 常量 + validate**

```go
// 既有常量块（StageBaseline 后）追加：
StageCanary  = "canary"  // 金丝雀验证（并行验证式：canary 泳道部署 + 人工观察 + 确认放量/终止）

// Output key 常量块追加：
OutCanaryWorkloadID = "canaryWorkloadId" // canary stage：并行验证 workload ID（promote/终止清理用）
OutCanaryDomain     = "canaryDomain"     // canary stage：验证域名（指标卡/直链）
```

`StageDef.validate()` switch 加 `case StageCanary`。

- [ ] **Step 2: engine.go execStage 分发 + execCanary**

execStage switch 加 `case StageCanary: return e.execCanary(ctx, run, stage, sr)`。

```go
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
	sr.Output = map[string]any{
		OutReleaseID:        deployment.ID,
		OutCanaryWorkloadID: deployment.WorkloadID,
		OutCanaryDomain:     domain,
	}
	sr.Status = StageWaiting
	return false, nil
}
```

注：`dns1035`/`strFromInput` 若 engine 包不存在则新建小 helper（`dns1035` 参考 workload 包同名函数语义：小写、非法字符替换 `-`、截断；`strFromInput` 取 `sr.Input` map 的 string 值）。**不要 import workload 包**（依赖倒置约束）——本体复制这个 10 行纯函数并注释来源。

- [ ] **Step 3: Abort 补 canary 清理**

`Abort` 在标 `RunAborted` 后（UpdateRun 前）追加：

```go
	// canary waiting 期 abort：清理并行验证 workload（基线从未被动过，无需回滚）。
	// best-effort：删除失败仅日志（GC 对 canary-<runID> 裸泳道 TTL 兜底回收）。
	if e.Releases != nil {
		for i := range run.StageRuns {
			if run.StageRuns[i].Type == StageCanary && run.StageRuns[i].Status == StageWaiting {
				if wlID, _ := run.StageRuns[i].Output[OutCanaryWorkloadID].(string); wlID != "" {
					if err := e.Releases.DeleteWorkload(ctx, wlID); err != nil {
						log.Printf("canary abort 清理失败 wl=%s: %v（GC 兜底）", wlID, err)
					}
				}
			}
		}
	}
```

（`DeleteWorkload` 接口方法 Task 2 加——本 step 先写好调用点，Task 2 一并编译通过；若同批实现则直接过。）

- [ ] **Step 4: 测试**

```go
// TestExecCanaryWaitsForConfirmation：stages=[canary(envId=test)]，fakeReleaser Deploy 记录 lane；
// 断言 run Paused、stage Waiting、Output 含 canaryWorkloadId/canaryDomain、Deploy lane=canary-<runID>、replicas=1。
// TestExecCanaryMissingEnvId：canary 无 envId 且 Input 无 → failed「缺 envId 参数」。
```

- [ ] **Step 5: `go test ./internal/devops/pipeline/` 通过，commit**

`git commit -m "feat(pipeline): canary stage 类型——并行泳道部署 + waiting 人工验证"`

---

### Task 2: Releaser 扩展 DeployCanary + DeleteWorkload + engine CanaryResume

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（Releaser 接口 + execCanary 切换 + CanaryResume）
- Modify: `internal/devops/pipeline/engine_test.go`（fakeReleaser 补两方法）
- Test: `internal/devops/pipeline/engine_test.go`

**Interfaces:**
- Produces（Releaser 接口新增两方法，所有实现必须补齐）：
  - `DeployCanary(ctx, appID, envID, service, serviceID, imageID string, resources DeployResources, sourceRunID string) (deployment devops.Release, domain string, err error)` —— adapter 内部：lane=`canary-<sanitized(sourceRunID)>`、replicas=1、Domain=可选外部验证域名（`PAAS_DOMAIN_SUFFIX` 非空时 `<wl-name>.<suffix>`，空则零 Domain 用集群 FQDN）。
  - `DeleteWorkload(ctx, workloadID string) error` —— 桥接 workload 删除（含配额 -1）。
  - `func (e *Engine) CanaryResume(ctx, runID string, stageIdx int, promote bool) error`。

- [ ] **Step 1: Releaser 接口加两方法**（engine.go 接口块，注释说明语义与 adapter 责任）

- [ ] **Step 2: execCanary 切换 DeployCanary**

deploy 调用改为：

```go
	deployment, domain, err := e.Releases.DeployCanary(ctx, run.AppID, envID, service, serviceID, imageID, res, run.ID)
```

（port/cport 不透传——canary 沿用基线 Workload 端口语义由 adapter 在 CreateRelease 内按既有规则处理；若新建 canary workload 需端口，adapter 复用 Deploy 的单服务自动解析。）

- [ ] **Step 3: CanaryResume**

```go
// CanaryResume 金丝雀验证决策：promote=true 确认放量（基线全量滚动 + 删 canary + stage success），
// false 终止（仅删 canary，stage failed，基线零风险退出——spec：终止走人工回滚指针语义，但基线
// 从未被动过故无需回滚）。锁语义与 Resume 同源。
func (e *Engine) CanaryResume(ctx context.Context, runID string, stageIdx int, promote bool) error {
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
	sr := run.StageRuns[stageIdx]
	if sr.Type != StageCanary || sr.Status != StageWaiting {
		e.mu.Unlock()
		return fmt.Errorf("stage %d 不是等待中的金丝雀验证", stageIdx)
	}
	wlID, _ := sr.Output[OutCanaryWorkloadID].(string)
	imageID, _ := sr.Input["imageId"].(string)
	envID, _ := sr.Input["envId"].(string)
	service := strOr(StageDef{}, "", "") // 见下：service 从 Input 取
	service, _ = sr.Input["service"].(string)
	serviceID, _ = sr.Input["serviceId"].(string)
	e.mu.Unlock() // 部署/删除可能耗时（PollWorkloadReady），锁外执行
	if promote {
		// 确认放量 = 经典全量滚动：新镜像 Deploy 到基线泳道（lane=default），再删 canary 并行负载
		logf(sr, "确认放量：基线全量滚动 %s（env=%s lane=default）", imageID, envID)
		if _, _, err := e.Releases.Deploy(ctx, run.AppID, envID, LaneDefault, service, serviceID, imageID, 0, 0, DeployResources{}, 0, run.ID); err != nil {
			return fmt.Errorf("基线放量失败: %w", err)
		}
	} else {
		logf(sr, "终止金丝雀：基线未动，零风险退出")
	}
	if wlID != "" {
		if err := e.Releases.DeleteWorkload(ctx, wlID); err != nil {
			log.Printf("canary workload 清理失败 wl=%s: %v（GC 兜底）", wlID, err)
			logf(sr, "canary 清理失败（GC 兜底）: %v", err)
		} else {
			logf(sr, "canary workload %s 已回收", wlID)
		}
	}
	e.mu.Lock()
	run, err = e.Runs.GetRun(ctx, runID) // 重读（锁外期间状态可能变化，双检 paused+current）
	if err != nil || run.Status != RunPaused || stageIdx != run.CurrentStage {
		e.mu.Unlock()
		return ErrNotPaused
	}
	if promote {
		run.StageRuns[stageIdx].Status = StageSuccess
		run.StageRuns[stageIdx].Log = sr.Log
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
	// 终止：stage failed + run failed
	run.StageRuns[stageIdx].Status = StageFailed
	run.StageRuns[stageIdx].Log = sr.Log
	run.StageRuns[stageIdx].FinishedAt = time.Now()
	run.StageRuns[stageIdx].Error = "金丝雀验证终止（人工）"
	run.Status = RunFailed
	run.FinishedAt = time.Now()
	_, err = e.Runs.UpdateRun(ctx, run)
	e.mu.Unlock()
	return err
}
```

注意：`sr` 是锁内读的 StageRun 副本（值语义），`logf(sr, ...)` 修改副本后须回写 `run.StageRuns[stageIdx].Log = sr.Log`（上面已做）。清理实现时删掉示意性的重复 service 取值行，写干净版本。

- [ ] **Step 4: fakeReleaser 补方法 + 测试**

```go
// fakeReleaser 加 deleted []string 记录 DeleteWorkload 调用；DeployCanary 记录 lane 前缀 canary-。
// TestCanaryPromoteRollsBaselineAndCleans：canary waiting → CanaryResume(promote=true) →
//   断言 Deploy(lane=default, 同 imageId) 被调 + DeleteWorkload(canaryWl) + run Running + stage Success + 后续 stage 推进。
// TestCanaryTerminateKeepsBaseline：promote=false → run Failed + stage Failed + DeleteWorkload 调 + 基线 Deploy 未调。
// TestCanaryAbortCleansWorkload：canary waiting → Abort → DeleteWorkload 调 + run Aborted。
```

- [ ] **Step 5: `go test ./internal/devops/pipeline/` 通过，commit**

`git commit -m "feat(pipeline): canary 决策闭环——CanaryResume 放量/终止 + Abort 清理 + Releaser 扩展"`

---

### Task 3: handler canary 端点 + prod:write + allowProdFlow + 审计 + OpenAPI

**Files:**
- Modify: `internal/devops/pipeline/handler.go`（canary action 端点 + allowProdFlow/`targetsProd` 扫 canary + retry 动作判定）
- Modify: `internal/devops/pipeline/openapi.go` 或 handler 内 Operation 登记处（找到既有 approve 登记位置，同款登记）

**Interfaces:**
- Consumes: `Engine.CanaryResume`（Task 2）、`hasProdWrite` helper（handler.go:897 附近既有）、`recordAudit`、appGuard。
- Produces: `POST /api/pipelineruns/{id}/stages/{idx}/canary`，body `{"action":"promote"|"terminate"}`。

- [ ] **Step 1: 端点实现**（approve 分支后追加，结构复用 approve 的守卫序列）

```go
	// POST /{id}/stages/{idx}/canary  金丝雀验证决策（确认放量/终止）
	if r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "stages" && parts[3] == "canary" {
		if !h.allow(w, r, PermPipelineWrite) {
			return
		}
		if h.engine == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "engine not configured")
			return
		}
		var in struct{ Action string `json:"action"` }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Action != "promote" && in.Action != "terminate" {
			httputil.WriteError(w, http.StatusBadRequest, "action 必须是 promote 或 terminate")
			return
		}
		run, err := h.runs.GetRun(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		// 受限应用：金丝雀放量是生产发布动作，需 release（app-maintainer+）
		if h.appGuard != nil && !h.appGuard.Allow(r, run.AppID, application.AppActionRelease) {
			httputil.WriteError(w, http.StatusForbidden, "forbidden: 无该应用的应用级权限（release）")
			return
		}
		stageIdx := atoiSafe(parts[2])
		// promote 到生产基线需 prod:write（approve 端点无此校验是既有语义；canary promote
		// 直接改生产 Workload 镜像，必须有——比 approve 更强的写操作）。
		if in.Action == "promote" && h.envTypeOf != nil { // 用 handler 既有 EnvTypeResolver 注入名
			if envID, _ := run.StageRuns[stageIdx].Input["envId"].(string); envID != "" {
				if et, err := h.envTypeOf(r.Context(), envID); err == nil && et == "prod" && !h.hasProdWrite(r) {
					httputil.WriteError(w, http.StatusForbidden, "forbidden: missing prod:write")
					return
				}
			}
		}
		if err := h.engine.CanaryResume(r.Context(), id, stageIdx, in.Action == "promote"); err != nil {
			httputil.WriteServiceError(w, toHTTPStatus(err), err)
			return
		}
		h.recordAudit(r, "canary_"+in.Action, "pipeline_run", id, fmt.Sprintf("stage=%d", stageIdx))
		httputil.WriteData(w, map[string]string{"resumed": id})
		return
	}
```

（实现时按 handler 实际字段名对齐：EnvTypeResolver 注入字段名、`hasProdWrite` 是否已抽成方法——若未抽，按 handler.go:897 的既有判定内联。）

- [ ] **Step 2: targetsProd/allowProdFlow 扫 canary**

`targetsProd`（handler.go:846 附近）静态扫描 stages 的 switch 加 `case StageCanary`（canary 隐含对目标环境的部署/放量，与 deploy 同判）。`retry` 的动作判定循环里 `sr.Type == StageCanary` 也归 `AppActionRelease`。

- [ ] **Step 3: OpenAPI 登记**（找到既有 approve/abort 的 `reg.Operation` 登记处，同款加 canary 操作，Perm `pipeline:write`，WithReqBody 上述匿名结构体）。

- [ ] **Step 4: 测试**（handler_test.go）

```go
// TestCanaryActionPromoteAndTerminate：paused canary run → promote 200 + 审计；
// terminate 200；非法 action 400；非 canary stage 调用 → 引擎错误映射。
// TestCanaryPromoteProdRequiresProdWrite：prod envId + developer → 403；admin → 200。
```

- [ ] **Step 5: `go test ./internal/devops/pipeline/` 通过，commit**

`git commit -m "feat(pipeline): canary 决策端点——prod:write 护栏 + 审计 + OpenAPI"`

---

### Task 4: tpl-cd v2 加 canary + notifications 区分

**Files:**
- Modify: `internal/devops/pipeline/templates.go`（tpl-cd Version 2）
- Modify: `internal/devops/change/notifications.go`（RunStatusItem 加 StageType + canary 文案）
- Modify: `cmd/core/pipeline_adapters.go`（runTriggerBridge.ListRunStatuses 补 StageType）

**Interfaces:**
- Consumes: builtin 版本升级机制（SeedTemplates 版本比对 + ReplaceBuiltinTemplate，templates.go:84-107）。
- Produces: `RunStatusItem.StageType string`（change 包接口字段扩展，bridge 同步填充）。

- [ ] **Step 1: tpl-cd v2**

stages 改为：approve → deploy(prod, lane=default) → **canary(envId 同 deploy，message 可选)** → release → baseline；`Version: 2`（启动 seed 自动覆盖 v1）。canary params 最小集 `{"envId": "{{app.env.prod}}", "imageSource": "priorBuild"}`。

- [ ] **Step 2: notifications**

`RunStatusItem` 加 `StageType string`；paused 分支：

```go
		case "paused":
			title := fmt.Sprintf("流水线等待审批（%s）", r.Current)
			if r.StageType == "canary" {
				title = fmt.Sprintf("金丝雀验证中，等待确认（%s）", r.Current)
			}
```

- [ ] **Step 3: bridge 补字段**（cmd/core runTriggerBridge ListRunStatuses 填 StageType=当前 stage 的 Type）。

- [ ] **Step 4: 测试 + commit**

`TestSeedTemplatesUpgradesBuiltinOnVersionBump` 风格断言 v1→v2 覆盖（已有机制测试可参照；若模板结构测试断言 stages 数需同步改）。commit：`git commit -m "feat(pipeline): tpl-cd v2 金丝雀验证 + 通知区分 canary 等待"`

---

### Task 5: cmd/core 桥接 DeployCanary/DeleteWorkload + 域名后缀

**Files:**
- Modify: `cmd/core/pipeline_adapters.go`（releaseBridge 两方法）

**Interfaces:**
- Consumes: `workload.Repository.Delete`、`devops.ReleaseRepository.CreateRelease`、`tenant.Namespace`、workload handler 的配额回退模式。
- Produces: Releaser 接口完整实现。

- [ ] **Step 1: DeleteWorkload**

```go
// DeleteWorkload 删除工作负载（canary 清理）：repo.Delete（级联清 CR → K8s 资源随 OwnerRef 删）+ 配额回退 -1。
func (r *releaseBridge) DeleteWorkload(ctx context.Context, workloadID string) error {
	wl, err := r.workloads.Get(ctx, workloadID)
	if err != nil {
		return err // 含 NotFound：canary workload 已不存在视为已清理，调用方可忽略或上抛
	}
	if err := r.workloads.Delete(ctx, workloadID); err != nil {
		return err
	}
	if r.quotaDec != nil {
		_ = r.quotaDec(ctx) // billing 工作负载配额 -1（cmd/core 注入，best-effort）
	}
	_ = wl // wl 仅用于存在性确认
	return nil
}
```

（`quotaDec` 为 releaseBridge 新增可选字段，cmd/core 装配处注入 `billing.ResWorkloads` 的 CheckAndInc(-1) 闭包——与 workload handler QuotaCheck 同源。）

- [ ] **Step 2: DeployCanary**

```go
// DeployCanary 金丝雀并行部署：lane=canary-<sanitized(runID)>、replicas=1、可选外部验证域名。
// 域名：PAAS_DOMAIN_SUFFIX 非空时 <wl-name>.<suffix>（reconciler applyIngress 建独立验证入口），
// 空则不设 Domain（集群内 FQDN 验证，WorkloadDomain 兜底拼接）。
func (r *releaseBridge) DeployCanary(ctx context.Context, appID, envID, service, serviceID, imageID string, resources pipeline.DeployResources, sourceRunID string) (devops.Release, string, error) {
	if service == "" && serviceID == "" && r.services != nil { // 单服务自动解析（与 Deploy 同源）
		if svcs, err := r.services.List(ctx, appID); err == nil && len(svcs) == 1 {
			service, serviceID = svcs[0].Name, svcs[0].ID
		}
	}
	lane := "canary-" + dns1035Sanitize(sourceRunID)
	rel, err := r.releases.CreateRelease(ctx, devops.ReleaseInput{
		AppID: appID, EnvID: envID, LaneID: lane, Service: service, ServiceID: serviceID, ImageID: imageID,
		Resources: devops.ResourceSpecInput{CPURequest: resources.CPURequest, CPULimit: resources.CPULimit,
			MemRequest: resources.MemRequest, MemLimit: resources.MemLimit},
		Replicas: 1,
	})
	if err != nil {
		return devops.Release{}, "", err
	}
	if sourceRunID != "" {
		_ = r.releases.MarkSourceRun(ctx, rel.ID, sourceRunID)
		rel.SourceRunID = sourceRunID
	}
	// 可选验证域名（env 后缀未配则跳过——集群内 FQDN 已可验证）
	if suffix := os.Getenv("PAAS_DOMAIN_SUFFIX"); suffix != "" {
		domain := fmt.Sprintf("%s.%s", rel.WorkloadID, suffix) // 用 WorkloadID 保证全局唯一
		// CreateRelease 不透传 Domain：经 Update 写回（workload.Repository.Update 已有）
		if wl, err := r.workloads.Get(ctx, rel.WorkloadID); err == nil {
			wl.Domain = domain
			_ = r.workloads.Update(ctx, wl)
		}
		return rel, domain, nil
	}
	return rel, r.WorkloadDomain(ctx, rel.WorkloadID), nil
}
```

（`dns1035Sanitize` 与 engine 侧同语义的本包实现；`ReleaseInput` 若无 Domain 字段则用上述 Update 写回路径——实现时确认 `workload.Repository.Update` 签名按实际调整。）

- [ ] **Step 3: 装配注入 quotaDec + `go build ./...` + commit**

`git commit -m "feat(core): releaseBridge 桥接 DeployCanary/DeleteWorkload + 可选验证域名"`

---

### Task 6: 前端——canary 观察面板 + 决策按钮

**Files:**
- Modify: `frontend/console-user/src/api/pipeline.ts`（canaryAction API + 类型）
- Modify: `frontend/console-user/src/views/app-tabs/PipelineRunView.vue`（canary 面板）
- Modify: `frontend/console-user/src/views/app-tabs/PipelineDesigner.vue`（stage 类型下拉加 canary）

**Interfaces:**
- Consumes: Task 3 端点；`/api/observability/metrics?targetType=workload&targetId=`（既有）；`confirmDangerous`（既有）；fetchAuth。
- Produces: `canaryAction(runId, stageIdx, action)` API 函数。

- [ ] **Step 1: api**

```ts
export function canaryAction(runId: string, stageIdx: number, action: 'promote' | 'terminate') {
  return fetchAuth(`/api/pipelineruns/${runId}/stages/${stageIdx}/canary`, {
    method: 'POST', body: JSON.stringify({ action }),
  })
}
```

- [ ] **Step 2: PipelineRunView canary 面板**

`canApprove` 逻辑旁加 `canCanary`（`run.status==='paused' && cur.type==='canary'`）。canary waiting 时当前 stage 卡片展开区渲染：
- 验证地址直链（`cur.output.canaryDomain`，外链 target=_blank）+ 「在可观测中查看」跳 `/platform/observability?targetType=workload&targetId=<canaryWorkloadId>`
- 4 指标卡（CPU/内存/RPS/延迟，10s 轮询调 metrics API，onUnmounted 清理——复制 AppObservability 内联模式）
- 按钮「✅ 确认放量」（生产环境走 `confirmDangerous({action:'确认放量', requireNameConfirm:true, isProd})`）+「⛔ 终止」（普通 confirm）→ 调 canaryAction → 刷新 run
- stage 图标映射加 canary（⚠️ 或 🐤 风格，与现有 stageIcon 映射一致）

- [ ] **Step 3: PipelineDesigner**：stage 类型 select options 加 `{ value: 'canary', label: '金丝雀验证' }`；canary 选中时 params 面板显 envId/message。

- [ ] **Step 4: `pnpm --filter console-user build` 通过 + commit**

`git commit -m "feat(console-user): 金丝雀验证观察面板——指标卡 + 域名直链 + 放量/终止"`

---

### Task 7: 全量验证 + 部署 e2e

- [ ] `go test ./... -race` 全绿
- [ ] `pnpm build` 三套前端通过
- [ ] `./scripts/deploy-k8s.sh` 部署（记得 export NODE_IP；脚本已含 kubectl apply CRD）
- [ ] e2e：tpl-cd v2 seed 覆盖（GET /api/pipeline-templates/tpl-cd 断言 version=2 含 canary stage）→ 触发 CD run（approve → deploy prod → canary waiting）→ 通知列表见「金丝雀验证中」→ canary 面板指标可见 → terminate 验证零风险退出 → 再触发一轮 promote 验证基线镜像更新 + canary workload 回收 → 审计 canary_promote/canary_terminate 落库
- [ ] CLAUDE.md 加「金丝雀验证 stage」章节 + 基线表更新（生产发布策略缺口改「并行验证式已落地；按比例切流仍缺」）
