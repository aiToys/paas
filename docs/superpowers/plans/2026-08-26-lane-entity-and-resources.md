# 泳道实体化 + 资源规格实施计划（切片一）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 Lane 一等实体（三种生命周期场景一套模型）+ 工作负载资源规格建模（生产禁止 BestEffort），二者咬合为本计划。

**Architecture:** 新 `internal/lane/` 模块（克隆 governance 形制）；deploy lane 解析加实体优先级 + 懒建实体；Workload/Application/CRD 三层加 resources 字段并经 reconciler 透传到 Pod；laneGC 联动 permanent 跳过与关闭同步。金丝雀 stage 属切片二，不在本计划。

**Tech Stack:** Go + controller-runtime + pgx + Vue 3 + Element Plus

**Spec:** `docs/superpowers/specs/2026-08-26-lane-entity-and-deploy-policy-design.md`（权威，冲突以 spec 为准）

## Global Constraints

- 注释中文，与代码库一致；错误消息中文业务语 + 英文技术错误经 `WriteServiceError` 脱敏分流
- 全部 Repository 方法强制 `TenantOrErr(ctx)` 租户过滤，跨租户 not found 不泄漏
- 生产写护栏：泳道 Create/Close 经 EnvTypeResolver，prod 环境 403（fail-closed：查询失败也拒）
- OpenAPI Operation 全登记 + `{data:T}` 契约（`WriteData/WriteDataCreated`）
- 审计：泳道创建/关闭记审计（`lane_create`/`lane_close`，经 identityAuditAdapter）
- Quantity 校验用 `k8s.io/apimachinery/pkg/api/resource.ParseQuantity`（已在依赖树内）
- PG migration 编号顺延（当前最大 0034，新用 0035）；memory/pg 双实现同接口同语义
- 裸分支隐式泳道路径完全不变（无实体的 lane 名照常部署，实体现是增强非前置）
- 测试：单元全绿 `go test ./...`；前端 `pnpm build` 三套通过

---

### Task 1: Lane 领域模型 + Repository（memory + pg）

**Files:**
- Create: `internal/lane/model.go`
- Create: `internal/lane/repository.go`
- Create: `internal/lane/memory/store.go`
- Create: `internal/lane/pg/store.go`
- Create: `internal/storage/pg/migrations/0035_lane.up.sql` / `.down.sql`
- Test: `internal/lane/model_test.go`、`internal/lane/pg/store_test.go`（integration build tag）

**Interfaces:**
- Consumes: `pkg/tenant.TenantFrom`、`internal/storage/pg` helpers（`ErrAlreadyExists`/`IsUniqueViolation`/`TenantOrErr`）
- Produces:
  ```go
  type Lane struct {
      ID, TenantID, EnvID, Name, Mode, Status, Weight int, ExternalLink, Description string; CreatedAt, UpdatedAt time.Time
  }
  type Repository interface {
      List(ctx, envID string) ([]Lane, error)               // 租户内，envID 空不过滤
      Get(ctx, id string) (Lane, error)                     // ErrLaneNotFound
      GetByName(ctx, envID, name string) (Lane, error)      // 懒建/解析用；ErrLaneNotFound
      Create(ctx, in Lane) (Lane, error)                    // ErrLaneExists；租户以 ctx 为准忽略请求体
      Update(ctx, id string, in Lane) (Lane, error)         // mode/description/externalLink；name/env 不可改
      Close(ctx, id string) (Lane, error)                   // Status=closed（幂等）；不改 UpdatedAt 语义
      EnsureByName(ctx, envID, name string) (Lane, error)   // 存在返回，不存在懒建 Mode=standard（ON CONFLICT DO NOTHING 幂等，并发竞态兜底）
  }
  var ErrLaneNotFound, ErrLaneExists, ErrLaneNameInvalid = errors.New(...)
  ```

- [ ] **Step 1: 写失败测试** — model_test：Name 校验（dns1035：`^[a-z]([-a-z0-9]*[a-z0-9])?$` ≤63，复用/提取 workload 包 `dns1035` 到 `pkg/names` 共享包）、Mode 枚举（standard/permanent）、Weight 0-100
- [ ] **Step 2: 跑测试确认失败** — `go test ./internal/lane/`（包不存在编译失败即预期）
- [ ] **Step 3: 实现 model + memory store** — 内存 map + RWMutex + 深拷贝；`EnsureByName` 存在即返不覆盖（permanent 不被懒建降级）
- [ ] **Step 4: migration 0035** — `lanes` 表：id/tenant_id/env_id/name/mode/status/weight/external_link/description/created_at/updated_at，UNIQUE(tenant_id, env_id, name)，RLS 策略与其他租户表同款
- [ ] **Step 5: pg store** — 全参数化；`EnsureByName` 用 `INSERT ... ON CONFLICT (tenant_id, env_id, name) DO NOTHING RETURNING id` 失败转 SELECT
- [ ] **Step 6: 跑全部测试通过**（pg 部分 integration tag，`PAAS_TEST_PG_URL` 驱动）
- [ ] **Step 7: Commit** — `feat(lane): Lane 实体——model/repository/memory/pg`

### Task 2: Lane REST handler + 装配

**Files:**
- Create: `internal/lane/handler.go`
- Modify: `cmd/core/main.go`（装配 laneStore + handler + mux `/api/lanes`）
- Modify: `cmd/core/persistence.go`（PG 路径构造 laneStore）
- Test: `internal/lane/handler_test.go`

**Interfaces:**
- Consumes: Task 1 Repository；`gateway.Require`（governance:read/write）；EnvTypeResolver（`func(ctx, envID) (string, error)`）；AuditRecorder（`Record(ctx, tenantID, actor, action, resourceType, resourceID, detail)`，与 identity 同款签名）
- Produces: `GET/POST /api/lanes?envId=`、`GET/PUT/DELETE(lane) /api/lanes/{id}`；Detail 聚合结构 `LaneDetail{Lane, Workloads []workload.Workload, RecentRuns []pipeline.RunSummary}`（changes/trace 前端聚合，spec 3.1）

- [ ] **Step 1: 失败测试** — CRUD + 权限（401/403）+ 生产建泳道 403（EnvTypeResolver 返 prod）+ EnvType 查询失败也 403（fail-closed）+ 唯一冲突 409 + 非法名 400 + 跨租户 Get 404
- [ ] **Step 2: 实现 handler** — composite 按路径分发（克隆 governance handler 形制）；Create/Close 记审计；**DELETE 本期语义 = 仅标记 closed**（资源回收在 Task 5 关闭钩子接通前，UI 关闭按钮先隐藏——避免半功能）。Close 前置校验：有进行中 run（branch==name 且非终态）409
- [ ] **Step 3: main.go 装配** — envResolver 桥接 `stores.Environment.EnvType`、auditAdapter 复用 identityAuditAdapter
- [ ] **Step 4: OpenAPI 登记** — 5 操作（WithReqBody(Lane)、Perm governance 读写）
- [ ] **Step 5: 测试全绿 + Commit** — `feat(lane): REST handler + 装配 + OpenAPI`

### Task 3: 资源规格——模型/CRD/migration/reconciler 链路

**Files:**
- Modify: `internal/workload/model.go`（`Resources ResourceSpec` + Validate Quantity 校验 + 生产空 resources 拒绝）
- Modify: `internal/workload/memory/store.go`、`internal/workload/pg/store.go`（JSONB 列读写）+ migration 0035 追加 `workloads.resources JSONB`（同一 migration 文件内追加，Task 1 先建文件此处追加）
- Modify: `api/core/v1alpha1/workload_types.go`（ResourceSpec）+ `make manifests` 重生成 + `config/crds/` 与 chart `templates/crds.yaml` 同步
- Modify: `internal/controller/workload_controller.go`（podSpec resources 映射 + ParseQuantity 失败回写 failed）
- Modify: `internal/controller/k8sapplier.go`（Apply 投影补 Resources）
- Modify: `internal/application/model.go`（ResourceTemplate）+ memory/pg + migration 追加 `applications.resource_template JSONB`
- Test: 上述各 `_test.go` 扩展

**Interfaces:**
- Produces:
  ```go
  type ResourceSpec struct { CPURequest, CPULimit, MemRequest, MemLimit string }  // workload 包
  // CRD 同构（api/core/v1alpha1）
  // reconciler: 非空字段 → corev1.ResourceRequirements{Requests: {cpu, memory}, Limits: {...}}
  ```
- Consumes: `resource.ParseQuantity`；workload.Validate 需 envType 判定生产（注入已有 EnvTypeResolver，workload handler 已有——Validate 签名扩展为 `Validate(envType string)` 或 handler 侧前置校验，**取后者**：handler 在 Validate 后追加生产空 resources 检查，Validate 保持纯函数）

- [ ] **Step 1: 失败测试** — workload Validate Quantity 非法 400；CRD resources 投影（fake client 断言 Deployment container resources 四字段）；ParseQuantity 失败回写 failed；应用 ResourceTemplate roundtrip（memory+pg）
- [ ] **Step 2: 实现三层模型** — workload model/memory/pg + application model/memory/pg + migration 追加两列
- [ ] **Step 3: CRD + reconciler + applier** — `make manifests`；podSpec 映射；Apply 投影；chart crds.yaml 同步
- [ ] **Step 4: 生产空 resources 拒绝** — workload handler Create/Update：envType=prod 且 Resources 全空 → 400「生产工作负载必须配置资源规格（CPU/内存 requests）」；**存量豁免**：仅拦截 Create/Update 新写，存量基线不受影响（Update 空且未改 resources 字段时放行——判断依据：请求体 omitempty 空 + 现有 Workload 也空 = 存量未触碰，放行；一旦带 resources 或改镜像则强制要求）
- [ ] **Step 5: 测试全绿 + Commit** — `feat(workload): 资源规格建模——CRD/PG/reconciler 透传 + 生产禁 BestEffort`

### Task 4: deploy 链路接入——lane 解析优先级 + 资源注入

**Files:**
- Modify: `internal/devops/pipeline/engine.go`（execDeploy：lane 解析 + resources 注入）
- Modify: `internal/devops/pipeline/engine.go` Engine 字段 + `cmd/core/pipeline_adapters.go`（LaneEnsurer + AppResourceLookup 桥接）
- Modify: `internal/devops/release orchestration`（`internal/devops/store` CreateRelease/Deploy 透传 Resources 到 Workload——`UpdateImage` 找/建 Workload 时写入）
- Test: `internal/devops/pipeline/engine_test.go` 扩展

**Interfaces:**
- Consumes: Task 1 `lane.Repository.EnsureByName`；Task 3 `AppResourceTemplate`
- Produces:
  ```go
  // Engine 新依赖（依赖倒置，pipeline 不 import lane/application）
  LaneEnsurer interface { Ensure(ctx, envID, name string) error }        // cmd/core 桥接 lane.EnsureByName，err 时 stage failed
  AppResourceLookup interface { Template(ctx, appID string) (workload.ResourceSpec, error) }
  ```
- lane 解析（execDeploy 内，spec 3.1 优先级）：
  ```
  lane := strOr(params, "lane", LaneDefault)
  if lane != LaneDefault { LaneEnsurer.Ensure(ctx, envID, lane) }  // 懒建实体（standard）
  ```
- resources 注入：
  ```
  res := parseResources(stage.Params["resources"])     // map，同 buildArgs 模式
  if res 全空 { res = AppResourceLookup.Template(appID) }
  透传 Releaser.Deploy(...)（签名加 resources 参数）
  ```
- 联调泳道副本降级（spec 3.2.4）：deploy 到非 default lane 且 env 非 prod 时 replicas>1 截断为 1 + logf 提示

- [ ] **Step 1: 失败测试** — 显式 lane 懒建实体（fake LaneEnsurer 断言）；resources stage params 覆盖应用默认；两者皆空透传空；联调泳道 replicas 截断；LaneEnsurer 出错 stage failed
- [ ] **Step 2: 实现** — engine + adapters + devops Deploy 透传链（Deploy 签名变更同步全部调用点/fake）
- [ ] **Step 3: 测试全绿 + Commit** — `feat(pipeline): deploy lane 实体化解析 + 资源规格注入`

### Task 5: laneGC 联动 + 关闭回收钩子

**Files:**
- Modify: `internal/workload/lanegc.go`（permanent 跳过 + 删除后实体同步 closed）
- Modify: `internal/lane/handler.go`（Close 时同步回收 workloads——复用 laneGC 删除逻辑抽共享函数）
- Modify: `cmd/core/main.go`（LaneGC 注入 LaneStore；装配闭环）
- Test: `internal/workload/lanegc_test.go`、`internal/lane/handler_test.go` 扩展

**Interfaces:**
- Produces:
  ```go
  // workload 包导出共享回收（lanegc 与 lane handler 复用，防两处漂移——spec 风险 7.4）
  func (g *LaneGC) ReclaimLane(ctx context.Context, tenantID, envID, laneName string) (int, error)
  // LaneGC 加字段 LaneStatus interface { Mode(ctx, laneID) (string, error); MarkClosed(ctx, laneID) error }
  ```
- Sweep 变更：回收前查实体 Mode=permanent → 跳过；无实体（纯遗留）照旧；删除成功后 MarkClosed
- lane handler Close：调 `ReclaimLane`（同步，逐 workload：租户 ctx、配额 -1、审计 lane_close detail 含回收数）+ 实体 Status=closed

- [ ] **Step 1: 失败测试** — permanent 跳过（Sweep 断言不删）；MarkClosed 调用断言；Close 端到端（关闭→workloads 删→配额回退→审计→再 Get 返 closed）；Close 幂等（已 closed 200 不重复回收）；Close 遇 active run 409
- [ ] **Step 2: 实现** — ReclaimLane 抽取（Sweep 内部改调它）+ LaneStatus 注入 + handler Close 接通 + UI 解锁条件就绪
- [ ] **Step 3: 测试全绿 + Commit** — `feat(lane): 关闭即回收 + laneGC permanent 联动`

### Task 6: 前端——泳道管理 + 资源表单 + 设计器扩展

**Files:**
- Modify: `frontend/console-user/src/views/EnvironmentDetail.vue`（泳道矩阵列头可点入泳道详情抽屉；「管理泳道」入口）
- Create: `frontend/console-user/src/views/lane/LaneDetail.vue`（聚合：服务部署表/最近 run/trace 入口/关闭按钮 confirmDangerous/permanent 标记）
- Create: `frontend/console-user/src/api/lane.ts`
- Modify: `frontend/console-user/src/views/ApplicationDetail.vue` 或设置区（ResourceTemplate 四字段表单）
- Modify: `frontend/console-user/src/components/PipelineDesigner.vue`（deploy stage params：lane 选择器 [active 泳道下拉 + 自由输入] + resources 覆盖四字段）
- Modify: `frontend/console-user/src/router/index.ts`（如需泳道详情路由）

- [ ] **Step 1: lane.ts API 对接** — CRUD + Detail 类型（手写 interface，与现有 api/ 模块同风格）
- [ ] **Step 2: 环境详情泳道管理** — 矩阵列头点击 → LaneDetail 抽屉/路由；「管理泳道」弹层：列表（name/mode tag/status/外部链接）+ 创建（name/mode select/description/externalLink）+ 关闭（confirmDangerous 显示将回收 N 个工作负载）
- [ ] **Step 3: 泳道详情** — workloads 表（服务/镜像 tag/副本就绪）+ recentRuns（跳 /devops/runs/:id）+ trace 入口（带 lane 过滤跳可观测页）+ 归属变更（前端聚合 /api/changes?branch=）
- [ ] **Step 4: 资源规格表单** — 应用级 ResourceTemplate 表单（四字段 placeholder 示例 "500m"/"2"/"256Mi"/"1Gi"）+ 保存 PUT
- [ ] **Step 5: 设计器** — deploy stage lane 选择器 + resources 覆盖表单（空=继承应用默认提示）
- [ ] **Step 6: `pnpm build` 三套通过 + Commit** — `feat(console-user): 泳道管理 + 资源规格表单 + 设计器扩展`

### Task 7: e2e 验证 + 部署 k8s

**Files:** 无新文件（验证任务）

- [ ] **Step 1: 全量回归** — `go test ./...` + `pnpm build` 三套
- [ ] **Step 2: 部署** — `./scripts/deploy-k8s.sh`（[[k8s-always-latest]] 常驻授权）
- [ ] **Step 3: e2e** — ①建泳道 coupon（standard）→ 流水线 deploy lane=coupon → workloads 进泳道 + 泳道详情可见 + 矩阵列头点入；②配置应用 ResourceTemplate → 重新 deploy → Pod YAML 有 resources requests/limits（kubectl 验证）；③联调泳道 replicas>1 被截断为 1；④生产空 resources deploy 被 400 拒绝（用 prod env 流水线试）；⑤关闭泳道 → workloads 消失（kubectl get deploy）+ 配额回退 + 审计 lane_close；⑥permanent 泳道不被 GC（日志验证跳过）；⑦裸分支 deploy（不建实体）照常跑通（兼容回归）
- [ ] **Step 4: 修 e2e 发现的问题（如有）+ 最终 Commit**

---

## 自审记录

- **Spec 覆盖**：3.1 实体→Task 1/2/5；3.1 解析优先级→Task 4；3.2 资源三层→Task 3、注入→Task 4、副本降级→Task 4、生产拒绝→Task 3 Step 4；3.4 UI→Task 6；3.5 持久化→各 Task；3.3 canary 属切片二不在本计划 ✓
- **占位符扫描**：无 TBD/“适当处理”；所有签名/错误名给出确切形式 ✓
- **类型一致性**：`EnsureByName`（Task 1 定义，Task 4 经 LaneEnsurer 桥接消费）；`ResourceSpec`（Task 3 定义 workload 包，engine/applier/CRD 同构）；`ReclaimLane`（Task 5 定义，handler 复用）✓
- **风险点**：Task 3 Step 4 存量豁免判定（“空且未触碰放行”）语义略软——计划已写明判定规则；Task 5 共享回收抽取是防漂移关键，review 重点
