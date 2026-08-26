# 泳道实体化 + 资源规格建模 + 生产发布策略 设计

- 日期：2026-08-26
- 状态：待用户审阅
- 决策记录：D1=A（weight 字段留位不实现切流）/ D2=A（应用级默认 + deploy stage 覆盖）/ D3=A（人工分批金丝雀）/ D4=三刀切片

## 1. 问题与目标

### 1.1 三块缺口（一个缺失的两面）

平台的「部署形态」从未被建模，具体三缺口：

1. **泳道无实体**：lane=分支名隐式懒创建，散落在环境矩阵/trace/GC 三处无主人。真实场景对不上：大项目独立上线要数周随项目存续的泳道、周期火车要常驻联调泳道、临时 feature 要天级泳道——三种生命周期挂不上任何现有实体（批次是合并/发布单元，与泳道生命周期不对齐）。
2. **资源规格缺失**：Workload CRD 无 resources 字段，Pod 以 QoS BestEffort 裸跑——节点压力最先被驱逐、服务间无上限互挤、调度器无依据。
3. **生产发布只有 rolling**：无金丝雀分批观察、无蓝绿并行验证。出问题靠回滚指针事后兜底，无发布中止损能力。

### 1.2 目标形态（用户心智一句话）

> **建泳道（起名/标常驻）→ 变更部署进泳道联调（未变更服务降级基线）→ 生产发布按批放量人工确认 → 泳道用完关闭即销毁。**

### 1.3 非目标（边界）

- 不做项目/需求管理（Jira 领域；泳道实体只留 `externalLink` 一个外部引用字段）。
- 不做指标自动分析金丝雀（错误率阈值自动放量/回滚）——本期人工确认推进，规则引擎留后续。
- 不做生产流量按权重切泳道（D1=A）：`weight` 字段留位，ingress 权重下发留后续。
- 不做 HPA/PDB/PriorityClass（标准基线表已列为债务，独立切片）。
- 裸分支隐式泳道路径完全保留（不建实体也有零成本联调，TTL 回收兜底）。

## 2. 架构

```
┌─ 控制面 ────────────────────────────────────────────────┐
│ Lane 实体（新）：name/env/mode(standard|permanent)/     │
│   status/weight(留位)/externalLink + 归属变更/资源视图  │
│    ↑ lane 解析优先级：deploy 显式指定 > 实体匹配 > 分支名│
│ Application.ResourceTemplate（新）：cpu/mem 请求与上限   │
│    ↑ deploy stage params.resources 覆盖 > 应用默认      │
│ Release+Canary（扩）：canary stage 人工分批放量          │
└────────────────────────────────────────────────────────┘
       │ 投影                          │ 查询
       ▼                               ▼
┌─ K8s 数据面 ─────────────┐   ┌─ 可观测（已有）──────┐
│ Workload CRD + resources │   │ 指标卡 + trace      │
│ → Deployment resources/  │   │ （canary 观察窗口） │
│   replicas/probe（已有） │   └─────────────────────┘
└──────────────────────────┘
```

三块咬合点：**泳道实体是资源规格与发布策略的挂载锚**——实体化后泳道有 owner（用户显式管理）、有生命周期（关闭即销毁）、有配置面（联调自动降规格）；金丝雀的「并行版本 + 观察指标 + 决策推进」复用泳道已有机制（并行 Deployment + 指标查询 + 审批门禁同款 UI 模式）。

## 3. 详细设计

### 3.1 泳道实体（Lane）

**领域模型** `internal/lane/`（新模块，克隆 governance 模块形制：model + repository + memory + pg + handler）：

```go
type Lane struct {
    ID       string
    TenantID string
    EnvID    string          // 泳道属环境（联调泳道只在测试环境）
    Name     string          // 租户×环境内唯一，dns1035 合法（作 K8s 资源名后缀）
    Mode     string          // standard（默认，TTL 可回收）| permanent（常驻，GC 永不回收）
    Status   string          // active（默认）| closed
    Weight   int             // 入口流量权重（0-100，留位，本期不实现切流，恒 0）
    ExternalLink string      // 外部关联（如 Jira issue key），仅展示
    Description  string
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

**REST**（复用 governance:read/write 权限 + EnvTypeResolver prod 护栏——泳道只在测试环境，生产建泳道 403）：
- `GET/POST /api/lanes?envId=`（列表/创建；permanent 建议仅 tenant-admin）
- `GET/PUT/DELETE /api/lanes/{id}`（详情含聚合视图/更新/关闭）
- `DELETE` 语义 = 关闭：`Status=closed` + **同步回收该泳道全部 workloads**（复用 laneGC 删除逻辑：逐租户 ctx、配额 -1、审计 `lane_close`）+ K8s 资源随 CR 级联清。已 closed 的 lane 保留记录（审计追溯），不物理删行。
- OpenAPI 全登记，`{data:T}` 契约。

**lane 解析优先级**（deploy stage lane 参数解析，改 `pipeline` 包 ResolveStages 后的 deploy 执行侧）：
1. 显式指定：deploy stage params `lane=xxx`，且 xxx 匹配本租户×环境 active Lane 实体 → 用之
2. 显式指定但无实体：**自动补建实体**（Mode=standard，懒创建实体化——用户显式 deploy 到不存在的泳道名，实体自动出现，不报错）
3. 未指定：`{{run.branch}}` 分支名（现状路径不变，同样懒建实体）

> 兼容：存量隐式泳道（lane=分支名已部署的 workloads）首次被 lane 列表查询触及时自动补建实体（List 时对 workloads 中无实体对应的 lane 惰性 backfill，Mode=standard）。

**GC 联动**：laneGC Sweep 跳过条件追加 `Mode=permanent 或实体 Status=closed 已回收`；standard 泳道 TTL 逻辑不变（实体化后 TTL 仍是「忘了关」的兜底）。laneGC 删除时同步把实体标 closed（若无实体则跳过——纯 K8s 遗留）。

**聚合视图**（`GET /api/lanes/{id}` 返回 Detail）：
- `workloads`：该泳道各服务部署（服务名/镜像/副本/就绪）——复用 workload Repository List(laneID)
- `changes`：归属变更（`Change.Branch == Lane.Name` 反查，前端聚合亦可）
- `recentRuns`：最近 run（branch==lane 名）
- `traceEntry`：trace 查询 URL（带 lane 过滤，前端拼）

### 3.2 资源规格建模

**两级来源**：deploy stage params `resources` > 应用默认 `Application.ResourceTemplate` > 都无（现状裸跑，仅联调泳道可接受）。

**Application 扩展**：
```go
type ResourceTemplate struct {
    CPURequest string `json:"cpuRequest,omitempty"`  // "500m"
    CPULimit   string `json:"cpuLimit,omitempty"`    // "2"
    MemRequest string `json:"memRequest,omitempty"`  // "256Mi"
    MemLimit   string `json:"memLimit,omitempty"`    // "1Gi"
}
// Application 加 ResourceTemplate ResourceTemplate `json:"resourceTemplate,omitempty"`
```

**Workload CRD 扩展**（`api/core/v1alpha1/workload_types.go` + `make manifests` 重生成）：
```go
type ResourceSpec struct {
    CPURequest string `json:"cpuRequest,omitempty"`
    CPULimit   string `json:"cpuLimit,omitempty"`
    MemRequest string `json:"memRequest,omitempty"`
    MemLimit   string `json:"memLimit,omitempty"`
}
// WorkloadSpec 加 Resources ResourceSpec `json:"resources,omitempty"`
```

**链路透传**：
1. 内部 `workload.Workload` 模型加 `Resources`（PG migration：workloads 表加 JSONB 列）
2. deploy stage：`stage.Params["resources"]`（map，同 buildArgs 模式）非空时注入；否则 `ParamResolver` 新增 `AppResourceTemplate(appID)` 取应用默认注入
3. reconciler `podSpec`：Resources 非空字段 → `corev1.ResourceRequirements{Requests/Limits}`（k8s 资源名 `cpu`/`memory`，Quantity 解析失败回写 `phase=failed` fail-fast）
4. **泳道规格降级**：联调泳道（Lane Mode=standard，env=test）默认强制 `replicas≤1`（超 1 截断 + 日志）；limits 不强制降（保联调真实性），但 permanent 泳道不截断
5. K8sApplier.Apply 投影 CRD spec 补 Resources 字段（防再次「投影漏字段」类 bug，fake client 测断言）

**校验**：Quantity 格式校验（`k8s.io/apimachinery/pkg/api/resource.ParseQuantity`）在 workload.Validate——非法格式 400 而非等 reconciler 失败。

**标准基线对齐**：生产环境部署（envType=prod）时 Resources 全空 → 拒绝（400「生产工作负载必须配置资源规格」）——落实「生产 Pod 禁止 BestEffort」基线。存量基线 workloads 豁免（仅新写校验）。

### 3.3 生产发布策略

**模型**：Release 不加策略字段（YAGNI——策略是过程不是属性），改为**金丝雀 stage**：

```yaml
# tpl-cd 演进（Version 2，经 builtin 版本升级机制自动覆盖）
stages:
  - type: approve         # 人工门禁（已有）
  - type: deploy
    params: {envId: "{{app.env.prod}}", lane: default, imageSource: priorBuild}
  - type: canary          # 新 stage 类型：分批放量
    params:
      batches: [1, 10, 100]      # 副本百分比序列
      observationMins: 10        # 每批观察窗口（提示用，不强制阻塞计时）
  - type: release        # 打版本（已有）
  - type: baseline       # 合并主干（已有）
```

**canary stage 语义（人工确认推进，D3=A）**：
- 引擎执行到 canary：按 batch[0] 比例调整基线 Workload `canaryReplicas`（见下）→ stage 进入 `waiting`（同 approve 机制），前端展示观察面板
- 用户在观察面板看指标卡（目标 workload 的 CPU/RPS/延迟 + 错误率，复用 `/api/observability/metrics?targetType=workload`）+ trace（过滤该 workload）
- 点击「推进下一批」→ 下一比例 → 再 waiting；「完成」→ 100% 放量完成；「终止」→ canary 副本缩 0 + stage failed（run failed，走人工回滚指针）
- 全部批次完成 → stage success

**实现机制——借泳道底座**：金丝雀本质 = 临时并行版本。复用泳道：
- canary stage 起始：以 `Workload{env, lane: "canary-<releaseID>", image: 新镜像, replicas: 按比例}` 创建并行工作负载（与基线同 Service 名不同 lane——**问题：K8s Service 按 lane 命名，canary lane 的 Service 不会有生产入口流量**）
- **所以流量分配不借 Service，借副本比**：金丝雀改为**基线 Workload 双组 Deployment 表达**成本高。简化实现：**canary 阶段直接调基线 Workload Replicas 总量抬升 + 逐批把旧镜像 Pod 换新**是 rolling——不满足「观察窗口」语义。
- **定案：金丝雀 = 泳道并行 + 入口权重**，而入口权重本期不做（D1=A）→ **金丝雀降级实现为「canary 泳道部署 + 人工验证 + 全量切换」三段**：
  1. deploy 到 `canary-<releaseID>` 泳道（新镜像，1 副本）
  2. waiting：人工观察该泳道指标/trace/直连域名验证（canary 泳道 reconciler 建 Ingress，domain=`<app>-canary.<env-domain>`——已有 Domain 机制，零新增）
  3. 人工「确认放量」→ 删 canary 泳道 workload + 基线 Workload UpdateImage（经典全量滚动）→ stage success；「终止」→ 删 canary workload，基线未动（零风险退出）
- **诚实标注**：这是「并行验证式金丝雀」（业界称 preview/buffered deploy），非按比例切流金丝雀——真按比例切流依赖 D1 生产流量权重（留后续）。spec 明确此语义，UI 文案叫「金丝雀验证」不叫「灰度放量」。
- batches 参数此实现下退化为单批（1→100），参数保留为前向兼容（下期实现真切流时消费）。

**蓝绿**：同一机制的编排差异——蓝绿 = canary 泳道用生产同等规格（replicas=基线数 + 完整 resources）+ 验证后「切流」（本期切流=全量滚动替换，瞬时切换留 D1）。不单独建 stage 类型，tpl-cd 模板变体提供（`tpl-cd-bluegreen`，canary params 加 `fullReplicas: true`）。

**abort/回滚**：canary waiting 期 abort run → 引擎清理 canary workload（Abort 已有 stage 清理机制扩展）；基线从未被动过，无需回滚。

**审计**：canary 确认放量/终止记审计（`canary_promote`/`canary_abort`，复用 pipeline AuditRecorder）。

### 3.4 UI

- **泳道列表/详情**（console-user）：环境详情页升级——泳道矩阵每列头可点入「泳道详情」（聚合视图：服务部署表/归属变更/最近 run/trace 入口）；环境页新增「管理泳道」入口（列表 + 创建弹窗 + 关闭按钮 + permanent 标记）。侧栏不加一级入口（泳道属环境维度）。
- **应用配置**：应用详情「设置」或概览加「资源规格默认值」表单（ResourceTemplate 四字段）。
- **流水线设计器**：deploy stage params 面板加 lane 选择器（下拉本环境 active 泳道 + 自由输入）+ resources 覆盖表单；模板库加 canary stage 支持（stage 类型下拉新增 canary）。
- **canary 观察面板**（PipelineRunView 扩展）：canary stage waiting 时展示指标卡（CPU/RPS/延迟）+ trace 链接 + canary 域名直链 + 「确认放量/终止」按钮（生产走 confirmDangerous）。
- **DevOps 中心**：值班台通知聚合 canary waiting（「⏸ 等金丝雀确认」列，复用现有 notifications 拼装）。

### 3.5 持久化

- PG migration 00XX：`lanes` 表（tenant_id/env_id/name 唯一索引）+ `workloads.resources` JSONB 列 + `applications.resource_template` JSONB 列
- memory 实现同步（LaneRepository + Workload/Application 模型字段）
- 种子：无预置泳道（用户自建或懒创建）

## 4. 错误处理

- Lane 名非法（非 dns1035）→ 400（Create 校验，复用 workload 包 `dns1035` helper——提取共享）
- 生产环境建泳道 → 403（EnvTypeResolver 护栏，与工作负载写同款）
- 关闭泳道时有进行中 run（branch==lane 且 run 非终态）→ 409 引导先处理（复用 laneGC 的 LaneActivity 判定）
- Quantity 非法 → 400（Validate 阶段，K8s `resource.ParseQuantity`）
- 生产部署 Resources 空 → 400（新写校验，存量豁免）
- canary 泳道 workload 创建失败 → stage failed + 审计，run 可 retry（retry 语义与既有 deploy retry 一致）

## 5. 测试策略

- 单元：Lane repository CRUD/唯一性/关闭回收级联（memory+pg integration）；lane 解析优先级三分支；ResourceTemplate 注入链（stage params > 应用默认 > 空）；Quantity 校验；生产空 resources 拒绝；reconciler resources→ResourceRequirements 映射 + 非法 fail-fast；canary stage 状态机（waiting/推进/终止/abort 清理）；laneGC permanent 跳过 + 实体同步 closed
- fake client：CRD resources 字段投影（Apply）；canary 泳道 Deployment/Ingress 创建与清理
- e2e（k8s）：建泳道→deploy 显式 lane→矩阵显示→关闭泳道资源销毁；资源规格落到 Pod YAML；tpl-cd v2 canary 全流程（approve→deploy→canary waiting→指标可见→确认放量→基线更新）

## 6. 交付切片（D4）

1. **切片一：泳道实体 + 资源规格**（咬合最紧）：Lane 模块 + 解析优先级 + CRD/migration + reconciler 透传 + UI 泳道管理/资源表单
2. **切片二：金丝雀 stage**：canary 类型 + 泳道并行验证 + 观察面板 + tpl-cd v2
3. **切片三（留后续）**：生产流量权重切流（D1 真实现）→ 真按比例金丝雀 + 蓝绿瞬时切换

## 7. 风险与取舍记录

- **金丝雀语义降级**（诚实边界）：本期是并行验证非比例切流，UI 文案明确「金丝雀验证」；batches 参数前向保留。
- **泳道实体惰性 backfill** 可能与并发创建竞态：唯一索引兜底（ON CONFLICT DO NOTHING）。
- **生产空 resources 拒绝**对存量自动化调用是 breaking：仅校验新写，文档标注；tpl-cd v2 模板内置 resources 默认值规避。
- laneGC 与实体关闭的删除逻辑复用：抽共享函数防两处漂移。
