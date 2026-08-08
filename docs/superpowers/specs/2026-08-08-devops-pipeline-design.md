# DevOps 流水线（Pipeline 引擎）+ 示例 dogfooding 设计

> 状态：设计阶段（2026-08-08）
> 范围：平台级 Pipeline 编排能力 + paas-shop 示例改走完整平台流程 + 遗留数据清理
> 前置设计：`docs/superpowers/specs/2026-08-07-admin-management-foundation-design.md`（admin 横切）、CLAUDE.md「DevOps CI/CD」「DevOps 发布体验改造」两节

## 背景

当前平台 DevOps 子系统有 4 实体（`CodeRepo` / `BuildRun` / `Image` / `Release`），代码→构建→镜像→发布→部署各环节本身已具备，但缺**把它们串起来的声明式编排抽象**：

- **无 Pipeline / Stage 实体**：唯一"流水线"= `Release.PromotedFrom` 链 + `Environment.PromoteOrder` 阶序（固定 test→prod 逐级 promote），用户无法自定义 stage 结构。
- **用户操作碎片化**：从代码变更到部署最少 4 步跨 2 个页面（应用详情触发构建/创建发布 + DevOps 中心 promote），无一键入口。
- **dev/prod 流程未区分**：开发测试（高频自动）与生产发布（低频重审批）是不同流程，但平台只有一条 promote 链表达。

同时示例服务 `paas-shop` **完全绕过平台 DevOps 流程**：

- 7 个微服务全用人工预推镜像（`192.168.41.122:30050/paas-shop/xxx:vN`）；
- `setup-paas-shop.sh §9` 的 BuildRun 是演示性默认 Mock，从不产出真实镜像，产物也从不被消费；
- `Release` API **0 调用**，workload 直接写死外部镜像字符串；
- product/recommend/bff 连 Pod 都没有，只是治理注册条目。

平台还存在**遗留无用数据**：`app-cs`（早期 busybox 玩具构建，3 个 release）、`app-1785719131840599575`（时间戳孤儿 id）、以及指向废弃演示的旧脚本三件套（`create-resources.sh` / `create-workloads.sh` / `verify.sh`）。

## 目标

1. **Pipeline 引擎**：引入 `PipelineTemplate` / `Pipeline` / `PipelineRun` / `StageRun` 4 实体，支持用户声明式编排 stage（build/deploy/test/approve/promote/baseline），异步状态机推进，stage 间输出自动传递，一键跑完或中间人工卡点。
2. **Application 1:N Pipeline**：一个应用关联多条流水线，`Kind` 区分 ci/cd/custom，CI/CD 通过 `Image` 实体解耦。借鉴 Spinnaker / ArgoCD 的 artifact source 模型。
3. **paas-shop dogfooding**：示例改走完整平台流程（Gitea 建仓 → Pipeline → build → deploy → test → baseline），成为平台的真实用户。
4. **遗留清理**：删废弃应用数据 + 旧脚本。

## 非目标（YAGNI，明确不做）

- **多泳道（非 default Lane）+ 泳道级基线环境**：本期"基线环境"用 default lane 表达，Lane 实体/路由/染色归后续服务治理切片。
- **CI→CD 自动触发链**：本期手动衔接（CD 手动选 Image 发布），`onImageReady` / `onPipelineSucceeded` 事件触发留后续。
- **失败自动回滚**：失败中止 PipelineRun，回滚是显式独立操作（复用 Release rollback）。
- **test-pod（跑测试 Job Pod）**：test stage 只做 HTTP 冒烟 + 人工确认。
- **semver 自动推导**：版本号用自增 build 号 / git tag / 手动指定，语义化版本自动推导留后续。
- **蓝绿/金丝雀真实编排**：Strategy 字段保留，真实流量切分耦合泳道归后续。
- **Pipeline matrix/并行 stage**：本期 stage 严格串行，并行/矩阵构建留后续。

## 整体架构

借鉴 **Spinnaker**（Application 1:N Pipeline，每条独立 trigger + stage 序列）+ **ArgoCD Workflows**（artifact source，流水线间通过产物解耦）+ **GitLab CI**（按分支路由不同 pipeline）。

```
Application (paas-shop)
  ├── Pipeline (ci)   trigger=webhook[feature/*]  build→deploy(dev)→test(smoke)→baseline(merge)
  ├── Pipeline (cd)   trigger=manual              approve→deploy(staging)→test→approve→deploy(prod)→baseline(版本)
  └── Pipeline (custom) ...

CI Pipeline  ──产出 Image──▶  平台 Image 仓库  ◀──消费 Image──  CD Pipeline
(高频自动)                     (不可变 digest)                  (低频人工)
```

**核心解耦**：CI 流水线和 CD 流水线**通过 Image 实体解耦**，不直接互相依赖。CI 的 `build` stage 产出 Image（写入平台 Image 实体）；CD 的 `deploy` stage 从 Image 仓库选一个 Image（`imageSource=selected/latestReady`）发布。Image 成为流水线间的标准契约。

**4 实体关系**：

```
PipelineTemplate ──(创建)──▶ Pipeline ──(触发)──▶ PipelineRun ──(含)──▶ StageRun[]
 (平台预置+租户自定义)        (应用绑定实例)        (一次运行)           (单阶段记录)
```

## 数据模型

### PipelineTemplate（模板）

平台级预置（`tenantID=""`）+ 租户级用户自定义。模板提供初始 stage 列表，用户从模板创建 Pipeline 后 stage 可增删改（模板+自定义混合的落点）。

```go
type PipelineTemplate struct {
    ID          string
    TenantID    string     // ""=平台预置，否则租户自定义
    Name        string     // ci / cd / microservice
    Kind        string     // "ci" | "cd" | "custom"
    Description string
    Stages      []StageDef // 有序阶段定义
    Builtin     bool       // 平台预置不可删
}
```

### StageDef（阶段定义，模板与 Pipeline 共用）

```go
type StageDef struct {
    Name   string            // 显示名（"构建" / "部署到测试"）
    Type   string            // build|deploy|test|approve|promote|baseline
    Params map[string]any    // 类型相关参数（见下「Stage 类型」）
}
```

### Pipeline（应用绑定的流水线，主线实体）

```go
type Pipeline struct {
    ID         string
    TenantID   string
    AppID      string
    Name       string         // product-ci / chatbot-cd
    Kind       string         // "ci" | "cd" | "custom"
    TemplateID string         // 来源模板（可空=空白创建）
    Stages     []StageDef     // 实例化阶段列表（基于模板可改）
    Trigger    PipelineTrigger
    Disabled   bool
    CreatedAt  time.Time
}

type PipelineTrigger struct {
    Type     string  // "manual" | "webhook" | "cron"
    // webhook:
    Branch   string  // 匹配分支（glob，如 "feature/*"）
    Events   []string // ["push"]
    Token    string  // webhook URL token（创建时生成）
    // cron:
    Schedule string  // "*/5 * * * *"（cron 表达式，租户内）
}
```

### PipelineRun（一次运行，异步状态机载体）

```go
type PipelineRun struct {
    ID          string
    TenantID    string
    AppID       string
    PipelineID  string
    Branch      string         // 触发时的源分支
    Commit      string         // 触发时的 commit sha
    Trigger     string         // manual|webhook|cron
    TriggerRef  string         // 触发人 userID 或 webhook 来源
    Status      string         // running|paused|succeeded|failed|aborted
    CurrentStage int           // 当前推进到的 stage index
    StageRuns   []StageRun
    Version     string         // baseline stage 写入的版本号
    CreatedAt   time.Time
    FinishedAt  time.Time
}
```

### StageRun（单阶段执行记录，输出链载体）

```go
type StageRun struct {
    Index      int
    Type       string
    Name       string
    Status     string         // pending|running|succeeded|failed|skipped|waiting
    Input      map[string]any // 执行时解析的输入（如 deploy 的 imageId/envId）
    Output     map[string]any // 产出（build=imageId, deploy=releaseId/workloadDomain）
    StartedAt  time.Time
    FinishedAt time.Time
    Error      string
}
```

**Output 字段是 stage 输出链的核心**：后续 stage 从前序 StageRun.Output 取输入，不重新选。

### Release 加 Version 字段

`internal/devops.Release` 加 `Version string`（baseline stage 写入）。来源优先级：git tag > 手动指定 > 自增 build 号。

migration `0011_release_version`：`ALTER TABLE releases ADD COLUMN IF NOT EXISTS version TEXT NOT NULL DEFAULT '';`

## Stage 类型与执行逻辑

| Type | 输入 | 输出 | 复用 |
|---|---|---|---|
| `build` | branch, commit, dockerfile?, buildArgs? | `imageId` | BuildRun ✅已有 |
| `deploy` | envId, imageSource, imageId?, strategy | `releaseId`, `workloadDomain` | Release ✅已有 |
| `test` | mode=smoke\|manual, path?, message? | `result` | 新增（轻量） |
| `approve` | message | — | 新增 |
| `promote` | （从 prior deploy 取 releaseId） | `releaseId` | PromoteRelease ✅已有 |
| `baseline` | mainBranch, versionStrategy, mergeMode | `version`, `mergeCommitSha` | 新增（Gitea merge） |

### Stage Params 详细 schema

**build**：
```json
{"dockerfile":"Dockerfile", "buildArgs":{"SERVICE":"product"}, "branchOverride":""}
```
（`branchOverride` 空=用 PipelineRun.Branch）

**deploy**：
```json
{
  "envId":"env-acme-test",
  "imageSource":"priorBuild",   // priorBuild|selected|latestReady
  "imageId":"img-xxx",          // imageSource=selected 时必填
  "strategy":"rolling"
}
```

**test**：
```json
{"mode":"smoke", "path":"/livez"}        // 探活路径可配（默认 /livez）
{"mode":"manual", "message":"请业务验证"}  // 人工确认
```

**approve**：
```json
{"message":"等待上线审批"}
```

**baseline**：
```json
{"mainBranch":"main", "versionStrategy":"auto-increment", "mergeMode":"squash"}
```
（`versionStrategy`: auto-increment | tag | manual；`mergeMode`: ff | squash）

## 执行引擎（状态机推进）

新增 `internal/devops/pipeline/engine.go`，依赖倒置注入各 Repository + Gitea client + workload readiness reader。Engine 不碰 HTTP，由 handler 调用。

### 推进循环

```
advance(run):
  while run.CurrentStage < len(stages):
    stage = stages[run.CurrentStage]
    sr = run.StageRuns[run.CurrentStage]
    switch stage.Type:
      build:
        br := createBuildRun(run.AppID, run.Branch, run.Commit, stage.Params.buildArgs)
        poll until br.Status ∈ {success, failed}
        if failed: sr.Error=br.Log; fail(run); return
        sr.Output.imageId = br.ImageID
      deploy:
        imageId := resolveImage(stage, run)   // 见「stage 输出链解析」
        envId := stage.Params.envId
        rel := createRelease(run.AppID, envId, imageId, stage.Params.strategy)  // 复用 CreateRelease 编排
        poll until workload ready (复用 workload readiness reader)
        sr.Output.releaseId = rel.ID
        sr.Output.workloadDomain = workloadDomainOf(rel.WorkloadID)
      test (smoke):
        url := "http://" + resolvePriorDeployDomain(run) + stage.Params.path
        poll GET url until 200 or timeout
        if timeout: fail(run); return
      test (manual):
        run.Status = paused; sr.Status = waiting; persist; return
      approve:
        run.Status = paused; sr.Status = waiting; persist; return
      promote:
        srcReleaseId := resolvePriorDeployReleaseId(run)
        newRel := promoteRelease(srcReleaseId)   // 复用 PromoteRelease（算 NextPromoteTarget）
        sr.Output.releaseId = newRel.ID
      baseline:
        merge run.Branch → stage.Params.mainBranch (Gitea API)
        version := computeVersion(run, stage.Params.versionStrategy)
        write Release.Version=version for all releases in this run
        run.Version = version
    sr.Status = succeeded; run.CurrentStage++; persist
  run.Status = succeeded
```

### Stage 输出链解析（`resolveImage` / `resolvePriorDeploy*`）

```
resolveImage(stage, run):
  switch stage.Params.imageSource:
    "priorBuild":   return 最近的前序 build StageRun.Output.imageId（向前扫描）
    "selected":     return stage.Params.imageId
    "latestReady":  return ImageRepository.LatestReady(run.AppID)

resolvePriorDeployReleaseId(run):
  return 最近的前序 deploy/promote StageRun.Output.releaseId

resolvePriorDeployDomain(run):
  return 最近的前序 deploy StageRun.Output.workloadDomain
```

**这是「一键跑完、不重新选镜像」的核心**：CI 流水线内 deploy 用 `priorBuild`（自产自销）；CD 流水线 deploy 用 `selected`/`latestReady`（消费 CI 产物）。

### 失败 / 暂停 / 取消

- **失败**：任何 stage failed → `run.Status=failed` 中止，**不自动回滚**已部署环境（回滚是显式独立操作）。原 error 写 `StageRun.Error` + 记审计。
- **暂停**：`test(manual)` / `approve` → `run.Status=paused`，等 `POST /api/pipelineruns/{id}/stages/{idx}/approve` 恢复（设 `sr.Status=succeeded` → 重新 advance）。
- **取消**：`POST /api/pipelineruns/{id}/abort` → `run.Status=aborted` + cancel 进行中的 BuildRun（context cancel）。

### 并发与持久化

- 同一 Pipeline 同时只允许一个 `running`/`paused` 的 PipelineRun（CI 高频但单实例串行；并发构建留后续）。
- 推进循环 goroutine 持久化每次 stage 状态变化（PG/memory store），进程重启后可恢复 `running` 态（参考 devops `SweepInterrupted` 模式）。
- 持久化设计：engine 不持有运行态锁，每次 advance 读最新 run + 写回（PG 行锁 / memory mutex）。

## 触发器

### manual
`POST /api/applications/{id}/pipelines/{pid}/run` body `{branch, commit?}` → 校验无活跃 run → 建 PipelineRun → goroutine advance。

### webhook
`POST /api/devops/webhook/{token}`（Gitea push event，免应用鉴权用 token）→ 按 token 查 Pipeline → 匹配 `trigger.branch` glob 与 `trigger.events` → 建 PipelineRun。token 创建 Pipeline 时生成，存 Pipeline.Trigger.Token。

### cron
`internal/devops/pipeline/scheduler.go` 启动 goroutine + time.Ticker（每分钟检查），按 `trigger.schedule` 定时建 PipelineRun（租户内单实例串行）。多副本部署需 leader election，MVP 单副本。

## baseline stage 语义

**版本固化层**：
- `Release.Version` 来源优先级：git tag（commit 上有 tag）> `versionStrategy=manual` 时取 PipelineRun.Version（用户触发时填）> `auto-increment`（`<branch>-<seq>` 或 `<shortsha>-<buildno>`）。
- 给本 PipelineRun 关联的所有 Release 写 Version。

**主干合并层**：
- Gitea API merge `run.Branch` → `stage.Params.mainBranch`（`mergeMode`: ff 优先 fallback squash）。
- 复用 `internal/devops/gitea/client.go`（已有 CreateRepo/GetTree/ListCommits，补 merge 方法）。

**与多泳道的关系**：本期"基线环境"= default lane，baseline stage 不涉及泳道。多泳道基线环境归后续。

## 前端 UI

### 应用详情新增「流水线」tab（主线，DevOps 分组首位）

按 `Kind` 分组展示：

```
┌─ 应用 paas-shop / 流水线 ──────────────────────────────┐
│ ▸ 开发流水线 (CI)                                         │
│   [product-ci]  push→build→dev→test→merge   最近 ✓       │
│ ▸ 发布流水线 (CD)                                         │
│   [product-cd]  approve→prod→baseline  [▶ 发布]          │
│                 选版本: img-xxx (v1.2.3) ▼               │
│ ＋ 新建流水线  模板: [开发流水线][发布流水线][空白]       │
└──────────────────────────────────────────────────────────┘
```

### 三块功能

1. **流水线列表**（按 Kind 分组）：每条卡显示 trigger、stage 缩略、最近运行状态；CI 卡显 webhook 地址（复制）；CD 卡显「发布」按钮 + 版本选择器（`imageSource=selected/latestReady` 时）。
2. **流水线设计器**（点卡 → 编辑）：stage 有序列表 + 增删改 + 每 stage 参数面板（deploy 选环境+imageSource、test 选 mode+path、approve/baseline 填配置）+ trigger 配置 + 从模板初始化。复用 console-user `useDangerConfirm`（生产 deploy stage 标 prod）。
3. **运行视图**（点「运行」或最近运行 #N）：stage 进度条 5s 轮询 + 每 stage 状态/Output（build 的 imageId、deploy 的 releaseId、test 探活结果）+ paused 时显 approve 按钮 + 失败 stage 展开错误 + 取消按钮。

### DevOps 中心增强

现有「流水线」矩阵 tab（app×env 发布视图）保留；新增「最近运行」section（跨应用所有 PipelineRun，看谁在跑/卡住/失败）。**应用详情是主线，DevOps 中心是跨应用观察**。

### 模板选择

新建流水线弹窗选模板（开发流水线 / 发布流水线 / 空白），或「微服务」快捷按钮一次建 ci+cd 两条；从模板创建后 stage 可改。

## 预置模板（平台级 seed）

| 模板 | Kind | Stages |
|---|---|---|
| 开发流水线 | ci | `build → deploy(dev, priorBuild) → test(smoke) → baseline(merge main)` |
| 发布流水线 | cd | `approve → deploy(staging, latestReady) → test(smoke) → approve → deploy(prod, priorBuild) → baseline(版本)` |

**组合创建「微服务」**：前端「新建流水线」提供「微服务」快捷按钮，一次生成 ci + cd 两条 Pipeline（ci 模板 + cd 模板），ci 的 build stage 预填 `buildArgs.SERVICE`（参数化构建）。它不是单模板（Kind 单值），是双模板快捷创建。

dev 默认 seed ci/cd 两模板（`PAAS_DISABLE_DEMO_SEED != true`），生产空模板由 admin 配。

## paas-shop dogfooding

### setup-paas-shop.sh 改造

不再直接 docker build/push + 直建 workload，改走完整平台流程：

```
1. 内置 Gitea 建仓（paas-bot 凭证）+ git push examples/paas-shop/ 源码
2. 平台 CodeRepo 绑定（source=internal）
3. 每个微服务创建 CI Pipeline（web-service 模板，build stage 带 buildArgs.SERVICE）
   product / recommend / chatbot / bff / frontend / traffic-gen / mcp-server
4. 创建 prod CD Pipeline（latestReady）
5. 触发 CI PipelineRun → build→deploy(dev)→test(smoke)→baseline(merge)
6. （可选）触发 CD PipelineRun 手动发布到 prod
```

### build stage 加 buildArgs（dogfooding 倒逼）

paas-shop 的 `Dockerfile.backend` 参数化（`ARG SERVICE`，覆盖 product/recommend/chatbot/bff）。build stage params 增 `buildArgs map[string]string`，透传给 BuildRun → builder.K8sJob 脚本（`--build-arg KEY=VALUE`）。

### 前置条件

`PAAS_DEVOPS_BUILDER=k8s`（真实构建，非 Mock）。集群 builder Job DooD 模式已验证可用（见 CLAUDE.md「DevOps CI/CD k8s 模式」）。

### 代码上 Gitea

setup 脚本用 `paas-bot` 凭证经 Gitea API 建 internal 仓库 + `git push` examples/paas-shop/ 内容。后续变更可经 Gitea Web UI / git push 触发 webhook → CI Pipeline。

## 遗留数据清理

### 集群数据

- 删 `app-cs`（含 3 release + buildruns + images + workloads，经 admin 应用级联删 `appCascadeDeleter`）。
- 删 `app-1785719131840599575`（孤儿）。
- paas-shop 当前 7 个"绕过流程"的裸 workload 清理（改由 Pipeline 重建）。

### 仓库旧脚本删除

- `examples/scripts/create-resources.sh`（指向废弃 app-rec/app-cs 演示）。
- `examples/scripts/create-workloads.sh`（旧 paas-examples:v1 镜像）。
- `examples/scripts/verify.sh`（旧 wl-rec-svc/wl-cs-api 验证）。
- `examples/README.md` 更新为流水线部署流程。

## 权限与横切

### 权限

- 新增 `pipeline:read` / `pipeline:write` 权限（admin/dev 读写，viewer 只读），并入 `identity.BuiltinRoles`。
- PipelineRun 的 deploy/promote stage 到 prod 环境 → 复用 `prod:write` 横切（EnvTypeResolver；deploy stage 解析 envId 后查环境类型，prod 则要求 Pipeline 触发者持 `prod:write`）。
- baseline stage merge 到主干 → 经 Gitea paas-bot 凭证（平台级），记审计。

### 审计

- Pipeline 创建/删除/运行/审批/取消 → 经 `identityAuditAdapter` 记审计（参照 P1.4 identity 审计模式）。
- baseline merge 主干 → 高敏感（改主干代码），必记审计 + handler 返回 merge commit sha。

### 多租户隔离

- Pipeline/PipelineRun Repository 全方法强制 tenant 过滤（与现有 devops 实体一致）。
- webhook token → Pipeline → tenant 反查，跨租户 token 不泄漏。

### 错误脱敏

- Gitea merge 失败 / BuildRun 失败的原始错误 → `httputil.WriteServiceError` 特征分流脱敏（不泄漏 Gitea 内部 URL / token）。
- baseline merge 冲突 → 返友好"合并冲突，请手动解决"，不泄漏 git internals。

## REST 端点清单

### Pipeline（应用下）
- `GET    /api/applications/{id}/pipelines` — 列表（按 Kind 分组前端处理）
- `POST   /api/applications/{id}/pipelines` — 创建（可选 `templateId` 从模板）
- `GET    /api/applications/{id}/pipelines/{pid}` — 详情（含 stages）
- `PUT    /api/applications/{id}/pipelines/{pid}` — 更新 stages/trigger
- `DELETE /api/applications/{id}/pipelines/{pid}` — 删除
- `POST   /api/applications/{id}/pipelines/{pid}/run` — 手动触发（body: `{branch, commit?, version?}`）

### PipelineRun
- `GET    /api/pipelineruns?appId=&pipelineId=&status=` — 列表
- `GET    /api/pipelineruns/{rid}` — 详情（含 stageRuns）
- `POST   /api/pipelineruns/{rid}/stages/{idx}/approve` — 恢复 paused stage
- `POST   /api/pipelineruns/{rid}/abort` — 取消

### 模板
- `GET    /api/pipeline-templates` — 平台预置 + 租户自定义

### Webhook
- `POST   /api/devops/webhook/{token}` — Gitea push event 入口（无鉴权，token 校验）

### Admin（跨租户总览）
- `GET    /api/admin/pipelineruns` — 跨租户运行总览（`adminGuard` super_admin 只读）

所有写操作登记 OpenAPI Operation（Perm 映射）。`/api/devops/webhook/{token}` 免鉴权端点（token 即凭证），不进 OpenAPI 公开契约（或标 internal）。

## 数据流（端到端示例：paas-shop chatbot CI+CD）

```
开发改 chatbot 代码 → git push feature/chatbot-x 分支到 Gitea
  → webhook 触发 chatbot-ci Pipeline（trigger=webhook[feature/*]）
  → PipelineRun.advance:
     build:   BuildRun(feature/chatbot-x, buildArgs.SERVICE=chatbot) → Image img-xxx
     deploy:  imageSource=priorBuild → CreateRelease(env-dev, img-xxx) → Workload ready
     test:    smoke GET http://chatbot.svc/livez → 200
     baseline: merge feature/chatbot-x → main + Version=chatbot-<seq>
  → PipelineRun succeeded

产品决定发布到 prod:
  → 应用流水线 tab → chatbot-cd → 「发布」选 img-xxx (latestReady)
  → PipelineRun.advance:
     approve:   paused → 人点 approve
     deploy:    imageSource=selected → CreateRelease(env-prod, img-xxx) → Workload ready
     baseline:  Version=chatbot-<seq> 写 Release
  → PipelineRun succeeded
```

## 文件结构（新增/修改）

```
internal/devops/
  pipeline/
    model.go          # PipelineTemplate/Pipeline/PipelineRun/StageRun/StageDef + Kind/Trigger 常量
    repository.go     # Repository 接口（Pipeline/PipelineRun/Template CRUD）
    engine.go         # 执行引擎（advance/resolveImage/resolvePriorDeploy*）
    scheduler.go      # cron 触发器（time.Ticker）
    store_memory.go   # memory 实现
    store_pg.go       # PG 实现
    handler.go        # REST handler（pipeline/pipelinerun/template/webhook）
    templates.go      # 平台预置模板（ci/cd）
  gitea/client.go     # 补 merge 方法（已有 client）
  builder/k8s.go      # buildArgs 透传（--build-arg）
  model.go            # Release 加 Version
  repository.go       # ReleaseRepository 加 Version 读写
  memory/store.go     # Release Version 读写
  pg/store.go         # Release Version 读写
  handler.go          # 挂 /api/applications/{id}/pipelines 等路由

internal/storage/pg/migrations/
  0011_release_version.up.sql   # releases 加 version 列
  0012_pipeline.up.sql          # pipeline_templates/pipelines/pipeline_runs/stage_runs 表

cmd/core/
  persistence.go      # buildAllStores 注入 pipeline store + engine 依赖（BuildRun/Release/Promote repo + gitea client + workload readiness）
  main.go             # 路由注册 / 启动 cron scheduler

api/core/v1alpha1/    # （无 CRD 改动，Pipeline 是平台内实体不落 K8s）

frontend/console-user/src/
  views/app-tabs/
    AppPipelines.vue        # 流水线 tab（按 Kind 分组列表）
    PipelineDesigner.vue    # 流水线设计器
    PipelineRunView.vue     # 运行视图（stage 进度 + approve）
  api.ts                    # pipeline/pipelinerun/template API + 类型
  views/DevOps.vue          # 加「最近运行」section

examples/scripts/
  setup-paas-shop.sh        # 改走 Gitea+Pipeline 流程
  （删 create-resources.sh / create-workloads.sh / verify.sh）
examples/README.md          # 更新流水线部署流程
```

## 测试

### 后端单测
- `pipeline` engine：advance 各 stage 类型（build/deploy/test-smoke/test-manual/approve/promote/baseline）、stage 输出链解析（priorBuild/selected/latestReady）、失败中止、暂停恢复、取消。
- `pipeline` engine：CI 流水线（deploy priorBuild）+ CD 流水线（deploy latestReady）解耦。
- `pipeline` scheduler：cron 触发 + 单实例串行。
- `gitea` merge：成功 / 冲突 / fast-forward / squash。
- `builder` k8s：buildArgs 透传。
- memory/pg store：Pipeline/PipelineRun CRUD + 租户隔离。
- 集成测试（`//go:build integration`）：PG schema 0011/0012。

### 横切
- PipelineRun deploy 到 prod → `prod:write` 拦截 developer。
- baseline merge 记审计。
- webhook token 跨租户不泄漏。
- 失败错误脱敏。

### 前端
- `pnpm exec vue-tsc --noEmit` + `pnpm build` 三套通过。

### k8s e2e（`./scripts/deploy-k8s.sh`）
- 平台预置模板 seed（ci/cd/微服务）。
- paas-shop 经 Gitea+Pipeline 部署：CI 触发 → build（真实 K8s Job）→ deploy(dev) → test(smoke) → baseline(merge main + Version) 全绿。
- CD 手动发布到 prod。
- DevOps 中心「最近运行」展示。
- 清理：app-cs / 孤儿 app 删除生效。
