# 流水线模型重构 + 示例 dogfooding

## 背景与决策

当前 `Pipeline` 是 per-app 实体（`createPipeline(templateId)` 把模板 stages **复制**到 Pipeline.Stages）。问题：① 每个 app 重复创建；② 改模板不传播；③ 操作多（建 app -> 新建流水线 -> 编辑 -> 触发）。

参考业界（Argo WorkflowTemplate+Workflow / Tekton Pipeline+PipelineRun / GitLab include / GitHub reusable workflow）共识：**模板定义 + 实例引用 + 参数化**。重构为「模板参数化 + 应用绑定」模型，应用建好后自动有 CI/CD（零操作），模板升级自动传播。

用户决策：① 重构为模板+绑定（推荐）② 完整 dogfooding（建 Gitea 仓库 + 推码 + 真实构建）。

## 阶段 A：模型重构（模板参数化 + 应用绑定）

### A1. 领域模型（`internal/devops/pipeline/model.go`）

- `PipelineTemplate` 加 `Params []ParamDef`（参数声明：name/type/default/description，供 admin 模板编辑器用；运行时解析靠占位符，此字段文档化为主）。
- `Pipeline` 改语义：保留 id/appID/name/kind/templateID/disabled/createdAt；**去掉 Stages 字段**（运行时从 template 解析）；加 `ParamOverrides map[string]any`（app 覆盖模板默认参数）；`Trigger` 保留（CI webhook 用，现 manual）。
- `StageDef` 不变（name/type/params）。
- 占位符约定（`templates.go` 模板默认值用）：`{{app.env.test}}` / `{{app.env.prod}}` / `{{app.repo}}`（触发时解析）。

### A2. 参数化模板（`templates.go`）

- `tpl-ci` deploy.envId 默认 `{{app.env.test}}`（替代当前空串）；其余 params 字面默认。
- `tpl-cd` deploy.envId 默认 `{{app.env.prod}}`。
- 模板自带默认，app 零操作即有可用流水线。

### A3. 参数解析器（新 `resolver.go`）

- `ResolveStages(ctx, tplStages, overrides, appCtx) ([]StageDef, error)`：
  - 遍历 tplStages，对每个 stage.params 解析占位符 + 应用 overrides 覆盖。
  - `{{app.env.test}}` -> environment store 查 app 租户 type=test 环境 ID（多个取 promoteOrder 最小）。
  - `{{app.env.prod}}` -> type=prod 环境 ID。
  - `{{app.repo}}` -> codeRepo store 查 app 绑定的 internal repo ID。
  - overrides 格式 `{"<stageIndex>.<paramKey>": value}` 或嵌套 `{deploy: {envId: "x"}}`（选其一，倾向 stageIndex+key 扁平）。
- 依赖注入：`ParamResolver` 接口（envRepo + codeRepo），cmd/core 桥接（同 envTypeBridge 模式）。

### A4. engine 改造（`engine.go` Advance）

- 当前用 `pipe.Stages[run.CurrentStage]`（130-137 行）。重构后 Pipeline 无 Stages，改用 `run.StageRuns[run.CurrentStage]`：
  - 终态判断：`run.CurrentStage >= len(run.StageRuns)`。
  - 取 stage：`sr := &run.StageRuns[run.CurrentStage]`；构造 `stage := StageDef{Type: sr.Type, Name: sr.Name, Params: sr.Input}`。
  - 去掉扩容逻辑（141-146，触发时已全实例化）。
  - 去掉 `pipe, err := e.Pipelines.GetPipeline`（130 行，不再需要 Pipeline 实体）。
- `execStage` 签名不变（仍收 stage StageDef），用构造的 stage；内部 `stage.Params` 即 `sr.Input`（resolved params）。

### A5. 触发实例化（`handler.go` triggerRun）

- 当前第 444-447 用 `p.Stages` 建 StageRuns（不存 Input）。重构后：
  - 取 Pipeline（binding）+ Template + appCtx -> `ResolveStages` -> resolved stages。
  - `stageRuns[i] = StageRun{Index: i, Type: s.Type, Name: s.Name, Status: StagePending, Input: s.Params}`（**存 Input=resolved params**）。
  - resolved stages 存快照到 PipelineRun（run 自带，模板改不影响已运行）。
- engine 用 run.StageRuns 推进（A4）。

### A6. 默认绑定（建 app 自动建）

- `application` Create 成功后，cmd/core 装配层后置 hook：自动建 2 条 Pipeline（tpl-ci/tpl-cd，默认参数，name 如 `<app>-ci`/`<app>-cd`）。
- 或在 pipeline handler `ListPipelines` 时若 app 无 binding 则返虚拟默认（隐式）。**选显式**（建 app 自动建实体，可编辑/禁用/删除）。
- 失败仅 log 不阻断 app 创建（绑定是附加能力）。

### A7. store 改造（memory + pg）

- `Pipeline` 字段改：去 Stages，加 ParamOverrides。
- migration `0019_pipeline_binding`：`ALTER TABLE pipelines ADD COLUMN param_overrides JSONB NOT NULL DEFAULT '{}'`；stages 列保留（旧数据兼容，新写入空）；清理测试残留（`DELETE FROM pipelines WHERE name LIKE '111-%' OR name LIKE '222-%'`）。
- memory store 同步改字段。
- `TemplateRepository` 加 `UpdateTemplate`/`DeleteTemplate`（admin 可编辑模板；builtin 不可删）。

### A8. handler 改造

- `POST /api/applications/{id}/pipelines`：保留（手动加自定义 binding）；建 app 自动建的不需手动。
- `PUT /api/applications/{id}/pipelines/{pid}`：body 改为 `paramOverrides`（不传 stages）；handler 校验 templateID 存在。
- `GET .../pipelines`：列表返 binding（含 templateID + resolved stages 预览 + overrides + disabled），前端直接显示可触发。
- OpenAPI 更新（body schema 改）。
- admin：`PUT/DELETE /api/pipeline-templates/{id}`（builtin 不可删）。

### A9. 前端改造（console-user）

- `AppPipelines.vue`：
  - 列表默认显示 2 条（CI/CD，建 app 自动有），直接「运行」按钮（零操作触发）。
  - 去掉「新建流水线」主按钮（默认有；保留「关联自定义模板」次入口）。
  - 「编辑」-> 参数覆盖器（覆盖 deploy.envId/strategy 等，不编辑 stages；显示模板默认值 + 占位符解析结果预览）。
- `PipelineDesigner.vue` -> `PipelineBindingEditor.vue`（参数覆盖表单，非 stage 编辑器；admin 的模板编辑器另做）。
- `PipelineRunView.vue` 不变（用 run.stageRuns）。
- `pipeline.ts` API：Pipeline 类型去 stages 加 paramOverrides；新增 updateBinding。

### A10. 清理遗留数据

- 删 4 条测试 Pipeline（111-ci/cd, 222-ci/cd）。
- 删 paas-shop 的旧 Pipeline（如有）。
- migration 清理 + app 自动重建默认 binding。

**阶段 A 验证**：go test ./... + 三套前端 build + k8s 部署 + e2e（建 app 自动有 2 条 binding + 触发 CI run + approve CD run）。

## 阶段 B：完整 dogfooding（paas-shop 端到端）

### B1. Gitea 建 paas-shop 仓库（internal）

- Gitea API `CreateRepo`（paas-bot 建 `paas-shop-examples` 仓库，private）。
- 仓库内容 = `examples/` 目录（含 paas-shop/ + vendor/ + Dockerfile.backend）。仓库根 = examples/ 内容。

### B2. 推 examples 源码到 Gitea

- `cd examples && git init && git remote add gitea <CloneURLWithAuth> && git add . && git commit && git push`。
- CloneURL 含 paas-bot:pass@（gitea client CloneURLWithAuth 生成）。
- 确认推码成功（Gitea tree API 验证）。

### B3. paas-shop 绑 internal repo

- 删旧 external repo（GitHub）。
- 建 internal repo（source=internal, giteaOwner=paas-bot, giteaRepo=paas-shop-examples, dockerfile=paas-shop/Dockerfile.backend, buildContext=.）。
- 平台 handler 建 internal repo 时回填 CloneURL（含凭证，builder clone 用）。

### B4. Dockerfile context 确认

- `Dockerfile.backend`：`COPY . .` + `go build ./paas-shop/${SERVICE}`，context=仓库根（examples/ 内容），含 paas-shop/ + vendor/。
- builder Job clone Gitea 仓库 -> `docker build -f paas-shop/Dockerfile.backend --build-arg SERVICE=product -t <img> .`。
- 确认 builder K8s 模式 clone Gitea internal repo（CloneURL 含 basic auth）。

### B5. 触发 CI 流水线验证

- paas-shop 自动有 CI binding（tpl-ci，零操作）。
- 触发 CI run：build（builder Job clone Gitea + docker build + push registry）-> deploy（K8s Deployment 落地）-> test（smoke /livez）-> baseline（合并主干 + 写版本）。
- 验证端到端 succeeded：Image 落库（真实 digest）+ Deployment Pod Running + smoke 通过 + baseline 合并回 Gitea。
- 失败路径排查（builder Job 日志 / Gitea clone / docker push）。

### B6. 触发 CD 流水线验证

- paas-shop 自动有 CD binding（tpl-cd）。
- 触发 CD run：approve（paused）-> POST approve -> deploy（prod，latestReady 用 CI 产出的 Image）-> baseline（写版本）。
- 验证 approve 流程 + prod:write 防护（developer 403，tenant-admin 通过）。

**阶段 B 验证**：CI run 全链路 succeeded（真实构建镜像 + 部署 + smoke + baseline）+ CD run approve 流程。

## 风险与取舍

- **builder K8s 模式真实构建**：已知踩坑历史（DooD + docker.sock + buildkit classic）。Dockerfile.backend 用 vendor 不需网络，降低风险。
- **Gitea 推码**：examples 含 vendor/ 体积大，推码慢但一次。
- **内存路径 PollWorkloadReady 超时**：dev trade-off，仅 K8s 可端到端 CD 闭环（已知）。
- **重构破坏性**：Pipeline 模型改语义，需清理旧数据 + 前端适配。测试数据可重建。

## 执行顺序

阶段 A（A1-A10）-> 阶段 A 验证 -> 阶段 B（B1-B6）-> 阶段 B 验证。
