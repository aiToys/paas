# 应用工作台（Application Workbench）设计

> 日期：2026-08-06
> 范围：console-user 应用详情升级为「应用工作台」，收敛散落在顶级菜单的关联能力。
> 方案：B（应用工作台）。关联能力：服务治理实例 + 可观测 + 安全密钥引用 + 用量与计费。

## 背景

应用是 console-user 的主线抽象（`Application` + `Bindings` + 工作负载 + DevOps 链路）。但开发者以应用操作为主时，关联能力散落在顶级菜单：

- 服务治理：`/platform/governance`（`governance.Service.AppID` 已有归属关系，但应用内看不到）
- 可观测：`/platform/observability`（已有 `?appId=` 过滤，但只能跳走）
- 用量计费：`/settings/billing`（租户级，无应用维度）
- 安全密钥：`/platform/security`（租户级资产；应用真正用的是 `appconfig(type=secret)`）

开发者被迫在顶级菜单间跳转，应用「主线」感弱。

## 目标

应用详情升级为**应用工作台**：开发者进入应用即得全貌，关联能力按应用过滤后嵌入应用内，减少跳转。概览页去 seed 假数据，改为真实运行态仪表盘。

## 非目标（留后续，明确降级）

- **应用级活动流（audit timeline）**：`audit_logs` 无 `app_id` 列，无法按应用过滤。给 audit 加列 + 回填侵入式大，本期不做。
- **全局资源页归属应用反查（方案 C）**：本期聚焦应用内收敛，全局反查留后续。
- **概览 sparkline 真实化**：接真实 Prom 后去掉静态降级（依赖 observability 应用级业务指标 gap，core 暂无 `paas_rps` 等埋点）。

## 后端 reality（已调研）

| 能力 | 现状 | 改动 |
|---|---|---|
| 服务治理按 app | `Store.ListServices(ctx, envID, appID)` + `GET /api/services?appId=` **已支持**；实例经 serviceID 列出 | 零 |
| 可观测按 app | `observability/real` logs/traces/metrics **已按 appID 过滤**；`?appId=` 端点已有 | 零 |
| 安全密钥 | `security.Secret` 租户级不归属应用；应用用 `appconfig(type=secret)` | 前端重组（配置 tab 内分组） |
| 用量按 app | `ResourceUsage.ByApp map[string]map[string]int` **已落地**：memory/pg `IncUsage` 在 appID 非空时填充，gateway `meter.Record` 经应用级 Key 归因，pg `by_app` JSONB 列。`GET /api/billing/usage` 返回的 `UsageView.Usage.ByApp` 已暴露。token/gpu 精确归因可用 | 零（前端取 `usage.byApp[appID]`） |
| 概览 metrics | seed 假数据（rps/replicas） | 改聚合真实 workloads/bindings |

**结论：本期零后端改动**（所有数据维度已就绪，含 billing 应用维度归因），纯前端聚合 + 重组。

## 设计

### 1. tab IA 重组（视觉分组，10 tab 防膨胀）

```
── 运行态 ──  概览(工作台) │ 部署(工作负载) │ 服务治理 │ 可观测
── 资源 ──    资源绑定 │ 配置(含凭证分组)
── DevOps ──  代码仓库 │ 构建 │ 镜像 │ 发布
```

视觉分组：tab 条上加「运行态 / 资源 / DevOps」分组标签（纯视觉分区，不折叠，一屏可见不滚动）。新增 2 tab（服务治理 / 可观测），配置 tab 内部重组出凭证分组，概览改造为工作台。

### 2. 概览页 = 真实工作台（去 seed 假数据）

```
┌─ 运行态（聚合 workloads）─┬─ 最新发布 ─┬─ 最新构建 ─┐
│ 副本 ready/total  服务 N  │ v1.2 prod │ success  │
│ CPU▁▂▄ RPS▁▃▅ sparkline   │ 2h ago    │ 5h ago   │
└────────────────────────────┴───────────┴──────────┘
┌─ 资源依赖拓扑（增强：含治理服务节点）──────────────────┐
│  [App] ── 模型×2 · DB×1 · 缓存×1 · 服务×3（治理）      │
└────────────────────────────────────────────────────────┘
```

- 副本数/服务数/资源数：从已加载 workloads + bindings + 治理服务真实聚合。
- sparkline：复用 `/api/observability/metrics?appId=`（无 Prom 时静态降级，不伪装）。
- 最新发布/构建卡：复用 releases/buildruns 列表首条。
- 删除 `app.rps`/`app.replicas` 等 seed 假字段的使用（App 接口保留但概览不再展示假值）。

### 3. 服务治理 tab（纯前端聚合，`app-tabs/AppGovernance.vue`）

- 复用 `GET /api/services?appId=<appID>&envId=<scope>` 列该应用注册的服务。
- 每服务展开行：实例表（`GET /api/services/{id}` 返回 ServiceDetail.Instances，healthy/unhealthy 着色）。
- 同页底部两个 section：该应用服务的路由 + 熔断状态 tag（`GET /api/routes` / `GET /api/breakers` 按 serviceID 归属过滤）。
- 空态：「该应用尚未注册服务」+ 引导去 `/platform/governance`。

### 4. 可观测 tab（纯前端聚合，`app-tabs/AppObservability.vue`）

- 复用 `/api/observability/metrics?appId=` + `/logs?appId=` + `/traces?appId=`。
- 4 指标卡（CPU/内存/RPS/延迟，10s 轮询，复用 Observability.vue 逻辑）。
- 最近日志表（level 着色 + 关键字过滤）+ 最近 trace 列表（展开 span）。
- 顶部「在监控大屏中打开」链接 → `/platform/observability?app=<appID>`（保留深度排查出口）。

### 5. 配置 tab 重组（凭证分组，`AppConfigs.vue` 改造）

- 现有 env/secret 混合表 → 拆成两组：「环境变量」（type=env）+ 「凭证 / 密钥」（type=secret，掩码）。
- 凭证组即「应用引用的密钥」的诚实落地（appconfig secret 是工作负载启动注入的真实凭证）。
- 不引入 `security.Secret`（那是租户级平台资产，不归属应用，强行关联会误导）。
- 加「凭证安全」提示：解绑数据服务会清除注入的连接凭证（与现有 unbind 文案一致）。

### 6. 用量 tab（精确归因 + 资源占用，`app-tabs/AppUsage.vue`）

- **token / gpu 精确归因**：`GET /api/billing/usage` 返回 `usage.byApp[appID]` = 该应用 token/gpu 等维度的真实归因用量（gateway 经应用级 Key 落库）。
- **资源占用**（应用自身计数，非 billing 维度）：
  - 工作负载数 / 副本数（聚合 workloads）
  - 各 Kind 绑定资源数（bindings 按 type 计数）
- **预估月成本** = 各维度用量 × `billing.PriceTable` 单价求和（标注「预估，非精确计费引擎」，因 PriceTable 是 mock 单价）。
- 底部链接 → `/settings/billing`（租户级真实配额与账单）。

## 改动清单

### 前端（`frontend/console-user/src/`）

| 文件 | 改动 |
|---|---|
| `views/ApplicationDetail.vue` | tab 视觉分组（运行态/资源/DevOps）；概览改造为真实工作台（去 seed 假数据，加运行态卡 + 最新发布/构建卡 + sparkline + 拓扑增强）；新增 2 tab 挂载 |
| `views/app-tabs/AppGovernance.vue` | **新建**：按 appId 列服务 + 实例 + 路由 + 熔断 |
| `views/app-tabs/AppObservability.vue` | **新建**：4 指标卡 + 日志 + trace（预选 app） |
| `views/app-tabs/AppUsage.vue` | **新建**：资源占用 + PriceTable 预估成本 |
| `views/app-tabs/AppConfigs.vue` | 重组：环境变量 / 凭证密钥两组 |
| `stores/env.ts` | 无改动（scope 已就绪） |
| `api.ts` | 无改动（fetchJSON 已就绪） |

### 后端

- **零改动**。全部能力已支持 appId 过滤。

## 验证

- 前端 `vue-tsc --noEmit` + `pnpm build` 三套通过。
- k8s e2e（`./scripts/deploy-k8s.sh`）：
  - 应用详情 10 tab 视觉分组渲染
  - 概览：副本/服务/资源数真实聚合（无 seed 假值）
  - 服务治理 tab：按 appId 过滤列出 app-cs 的服务 + 实例
  - 可观测 tab：按 appId 过滤的指标/日志/trace
  - 用量 tab：预估成本展示 + 链接租户账单
  - 配置 tab：env / 凭证两组分离

## 留后续

- `audit_logs` 加 `app_id` + 应用级活动 timeline。
- 全局资源页归属应用反查（方案 C：治理/可观测/密钥列表加「归属应用」列 + 一键回应用详情）。
- 概览 sparkline 接真实 Prom 后去掉静态降级（依赖 core 加 `paas_rps` 等业务埋点）。
- 计费引擎真实化（PriceTable mock → 阶梯/套餐），用量 tab 成本随之精确。
