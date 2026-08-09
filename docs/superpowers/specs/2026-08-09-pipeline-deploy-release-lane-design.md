# DevOps 流水线重新设计：deploy/release 分离 + 泳道联调 + 实时日志

## 背景

当前流水线（2026-08-08 落地的模板+绑定模型）落地后，dogfooding 验证暴露三个**产品模型层面**的问题，不是补功能能解决的：

1. **`deploy` stage 把「部署」和「发布」焊死**：`execDeploy` 每次部署都调 `CreateRelease`，无论目标是测试还是生产。导致测试环境每次变更都产生一条 Release 记录，版本号概念被滥用。
2. **测试环境没有正确的抽象**：测试的本质是「变更服务在独立泳道与基线服务联调」，但当前 deploy 只到「环境」维度，无泳道概念。Workload.LaneID / Instance.LaneID 字段早已铺好却标注"预留不用"。
3. **运行中流水线看不到日志**：`PipelineRunView` 只有 stage 时间线 + 输出键值 + 错误文本。GitHub Actions 的核心体验——每个 step 展开看实时日志流——完全缺失。

## 问题诊断：测试环境到底需要什么

这是本设计的核心洞察，也是 PaaS 区别于玩具平台的关键。

测试环境**不是「第二个生产」**，而是**为了验证变更并和基线服务联调而存在的临时流量隔离空间**。它的本质是**流量拓扑**：变更服务（feature 泳道）+ 未变更服务（default 基线）组合跑一条完整链路。

两个**正交维度**区分测试/生产（不要混淆）：

| 维度 | 测试环境 | 生产环境 |
|------|---------|---------|
| **版本号**（里程碑标识） | 无（修 bug 反复发，版本号无意义） | 有（v1.2.0，上线里程碑） |
| **部署记录**（回滚/追溯） | **有**（回退到上一次能跑通的测试部署） | 有 |
| **泳道**（流量形态） | 变更泳道（只含改动服务，其余降级基线联调） | 基线泳道（全量） |

业界对标结论：
- **版本管理 vs 部署执行分离**是业界共识（GitHub build artifact vs Release tag；Spinnaker Bake vs Deploy）。→ 支持 deploy/release 拆 stage。
- **多服务测试联调的底层能力**（Service Mesh 流量染色 + 降级）有，但**无产品化封装**——要开发者手写 VirtualService，门槛极高。Vercel/Render/Railway 是单服务，不涉及。→ 这是 PaaS 的蓝海，本平台数据面（/dp/ 基于 Endpoints + Instance.LaneID）已铺路。

## 核心模型

### deploy 与 release 解耦

- **`deploy` stage** = 把镜像**部署到「环境 × 泳道」**，产生一条**部署记录**（可回滚），**不打版本号**。
- **`release` stage** = **打版本号里程碑**（git tag + Image 版本标记 + 发布记录），**不部署**。

两者正交：deploy 是「运行态动作」，release 是「版本管理动作」。测试流水线只 deploy 不 release；生产上线 deploy(prod) 后 release 打版本。

### 泳道（lane）作为 deploy 的一等参数

deploy stage 带两个参数：`env`（物理环境）+ `lane`（泳道，默认 `default`）。
- 测试联调：`deploy(env=test, lane=feature-x)`
- 生产上线：`deploy(env=prod, lane=default)`

L1（本期）：数据模型把 lane 用起来（Workload 唯一性含 lane，部署带 LaneID）。
L2（紧接的独立切片）：数据面 `/dp/` 按 LaneID 过滤实例 + 跨泳道降级发现，实现真正的联调。

## stage 类型集（重新定义）

| Stage | 职责 | 产出 | 是否产生部署记录 | 是否打版本 |
|-------|------|------|------------------|-----------|
| **build** | 构建镜像 | imageId | 否 | 否 |
| **deploy** | 部署到 env×lane（找/建基线 Workload + UpdateImage + readiness） | workloadDomain, deploymentId | **是** | 否 |
| **test** | 验证（smoke HTTP 探活 / manual 人工确认） | result | 否 | 否 |
| **approve** | 人工审批门禁（暂停等恢复） | — | 否 | 否 |
| **release** | 打版本号里程碑（git tag + Image.version + 发布记录） | version, tagSha | 否 | **是** |
| **baseline** | 合并主干（merge PR 到 main） | mergeSha | 否 | 否 |

变化点：
- **新增 `release` stage**：纯版本管理，不部署。打 git tag + Image 记 version + PipelineRun.version。
- **`baseline` 瘦身**：原 baseline = 打版本 + 合并主干。打版本移到 release，baseline 只剩合并主干。
- **`promote` 重新定义**（保留，不删）：原 promote = 晋升 release 到下一环境。新语义 = 「把本 run 已部署的镜像，部署到下一阶序环境的基线泳道」。本质是 deploy 的「自动算下一环境」语法糖（复用 deploy 逻辑，env 由 `environment.NextPromoteTarget` 算，lane=default）。保留它是因为「提升到下一环境」是高频操作，显式 stage 比让用户手填 env 更符合直觉。
- deploy 新增 `lane` 参数。

## 数据模型变化

### Workload 唯一性扩到含 lane（L1 核心）
- 当前基线 Workload 找/建维度：(tenant, app, env)。
- L1 后：(tenant, app, env, **lane**)。同一 app×env 可有 default 基线 + 多个 feature 泳道 Workload。
- `Workload.LaneID` 字段已存在，本期开始真正写入（deploy stage 传 lane → 找/建对应 lane 的 Workload）。

### Release 实体 = 部署记录（正名，不改结构）
当前 `devops.Release` 实体的真实语义就是**部署记录**（绑 EnvID/WorkloadID/PreviousImageID）。本设计**不改其结构**，仅在文档/UI 上澄清「这是部署记录，不是版本发布」。加一个字段：
- `LaneID string` — 本次部署的泳道（default 或 feature-x）。
- `SourceRunID string` — 由哪次 pipeline run 部署（追溯）。

版本号字段 `Release.Version` 仍保留：deploy 时不填，release stage 时给「本 run 涉及的部署记录」批量回填（复用现有 `SetVersion` 机制）。

### Image 加 Version（release stage 写入点）
- `Image.Version string` — 该镜像被发布为的正式版本号（release stage 打）。空 = 未发布（测试构建产物）。

### StageRun 加 Log（实时日志载体）
- `StageRun.Log string` — 执行过程关键事件 append（开始/调用的子操作/结果摘要）。
- build stage 的日志**不重复存**：前端展开 build stage 时直接调既有 `GET /api/buildruns/{id}`（含全量 BuildRun.Log）。StageRun.Log 只放其他 stage 的轻量事件 + build 的 BuildRunID 指针。

## 实时日志方案

### 后端
- engine `execXX` 在关键节点 append 到 `sr.Log`（用 `logf(sr, fmt, args...)` helper）：
  - build：`构建已提交 buildRunId=xxx` + 轮询进度点。
  - deploy：`解析镜像 imageId=xxx` + `部署到 env=test lane=feature-x` + `Workload wl-xxx 就绪`。
  - test：`探活 http://domain/livez ... 200 OK`。
  - release：`打 tag v1.2.0 @ commit xxx`。
- build 全量日志走独立端点（已有），避免 StageRun.Log 膨胀。

### 前端
- `PipelineRunView.vue`：每个 stage 卡片可展开，展开区 = 日志区（monospace，自动滚到底）。
- build stage 展开时额外拉 `GET /api/buildruns/{buildRunId}` 显示全量构建日志。
- 运行中 stage 日志随 5s 轮询追加（与现有轮询复用，不加新通道）。

## 典型流水线模板

### tpl-ci（变更验证 + 联调，高频无版本）
```
build → deploy(env={{app.env.test}}, lane={{run.branch}}) → test(smoke)
```
- lane 用 `{{run.branch}}` 占位符：每个分支独立泳道，互不干扰，联调完合并即销毁/闲置。
- 不含 release（测试不打版本）。

### tpl-cd（正式上线，低频打版本）
```
deploy(env={{app.env.prod}}, lane=default, imageSource=priorBuild) 
  → release(versionStrategy=auto-increment) 
  → baseline(mainBranch=main)
```
- 部署到生产基线 → 打版本号 → 合并主干。

### tpl-full（一条龙，可选）
```
build → deploy(test, lane={{run.branch}}) → test(smoke) 
     → approve(人工确认上线) 
     → deploy(prod, default) → release → baseline
```

## L2 泳道联调（独立切片，本 spec 锁接口不实现）

L2 范围（下一个 spec）：数据面 `/dp/` + 服务发现实现跨泳道联调。
- `/dp/instances?service=x&lane=feature-x`：优先返 feature-x 泳道实例，缺失则降级返 default 实例。
- 流量染色：请求带 `x-paas-lane: feature-x` header，网关/sidecar 据此路由（L3 全链路 mesh 的演进点）。
- 前端：泳道可视化（环境详情页看泳道拓扑 + 流量）。

**L1 必须为 L2 锁定的接口**（L2 实现时直接消费，不改流水线）：
1. deploy stage 产出的 Workload 带 `LaneID`（非 default 即泳道）。
2. Release（部署记录）带 `LaneID` + `SourceRunID`。
3. 泳道生命周期：测试联调完（合并主干后），feature 泳道 Workload 可回收（baseline stage 或独立清理任务）。

## 影响范围

### 后端
- `internal/devops/pipeline/engine.go`：
  - `execDeploy` 加 lane 参数，改调 `Releaser.Deploy(ctx, appID, envID, lane, imageID)`（新方法，找/建对应 lane 的基线 Workload + UpdateImage + PollReady，产生部署记录但不打版本）。
  - 新增 `execRelease`（打 git tag + Image.Version + PipelineRun.version + 给本 run 部署记录回填 Version）。
  - `execBaseline` 瘦身（去掉打版本，只合并主干；打版本归 release）。
  - `execPromote` 重定义为 deploy 的「自动算下一环境」变体。
  - 所有 execXX 加 `logf(sr, ...)` 日志事件。
- `Releaser` 接口（`pipeline_adapters.go`）：
  - 新增 `Deploy(ctx, appID, envID, lane, imageID) (deployment devops.Release, domain string, err error)`。
  - `CreateRelease` 保留（= Deploy 的别名或弃用，devops handler 仍用）。
  - 新增 `Publish(ctx, appID, imageID, version, commit) (tagSha string, err error)`（打 git tag + Image.Version）。
- `internal/devops/memory/store.go` + `pg/store.go`：
  - 找/建基线 Workload 逻辑加 lane 维度。
  - Release 加 LaneID + SourceRunID 列（migration 增量）。
- `internal/devops/model.go`：Release 加 LaneID/SourceRunID；Image 加 Version。
- `internal/workload`：Repository 找/建 Workload 按 (app, env, lane)。
- `internal/devops/pipeline/templates.go`：tpl-ci/tpl-cd 重写（lane 占位符 + release stage）。
- `internal/devops/pipeline/model.go`：StageRun 加 Log；StageType 加 `StageRelease`；lane 常量。

### 前端（console-user）
- `PipelineRunView.vue`：stage 卡片可展开 + 日志区 + build 拉全量 BuildRun.Log + lane 展示。
- `PipelineDesigner.vue`：deploy stage 参数加 lane（默认 default，可选 `{{run.branch}}`）。
- `AppPipelines.vue`：触发 CI 时显示「部署到 feature 泳道」语义。

### 测试
- engine 测：execDeploy 带 lane、execRelease 打版本、execBaseline 瘦身后只合并。
- store 测：找/建 (app,env,lane) Workload、Release 带 LaneID。
- 回归：现有流水线 e2e（paas-shop CI/CD）适配新模板。

## 不做项（YAGNI）

- **L2 泳道联调的流量机制**（数据面染色降级）：独立切片，本 spec 只锁接口。
- **L3 全链路 mesh 泳道**（Istio/zeus 请求级染色）：远期。
- **独立的「版本发布记录」实体**（含 changelog/artifacts）：MVP 用 Image.Version + PipelineRun.version + git tag 表达，够用。后续需要 release notes/制品库再升级。
- **promote 跨级跳迁**（test→prod 直达）：逐级更安全。
- **webhook/cron 自动触发**：仍 manual（独立切片）。

## 实施顺序建议

1. **后端 stage 语义**（engine + Releaser 接口 + model 字段）—— 地基，测试驱动。
2. **lane 维度接入**（Workload 找/建含 lane + deploy 传 lane）—— L1 数据模型。
3. **release stage 实现**（Publish + Image.Version + git tag）。
4. **实时日志**（StageRun.Log + logf helper + 前端展开）。
5. **模板重写 + dogfooding 适配**（tpl-ci/tpl-cd + paas-shop 重新验证）。
6. **L2 切片立项**（独立 spec：数据面泳道联调）。

## 验证

- 后端单测：engine 各 stage 新语义 + store lane 维度 + release 打版本。
- `go test ./...` 全绿。
- 前端：`pnpm build` 三套通过。
- k8s e2e：paas-shop CI（build→deploy test lane→test）无版本号部署 + CD（deploy prod→release v1.x→baseline）打版本上线 + 运行视图实时日志可见。
