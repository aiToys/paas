# 泳道 L3：零改动联调 + 自动回收 + 泳道可视化

## 背景

L1 把 lane 落到数据模型（Workload 按 `(tenant, app, env, lane)` 找/建，deploy stage 带 lane 参数）；L2 补齐跨泳道服务发现 + SDK 流量染色（`x-paas-lane` header 透传，feature 优先、缺失降级 default）。

L2 的染色要求应用**主动配合**：挂 `LaneMiddleware` 或手动 `WithLane`——应用有感知，未接入的应用不享受泳道联调。且 feature 泳道由 CI 自动创建、无人回收，时间一长测试环境堆积僵尸泳道。开发者也无法一眼看清「一次联调里哪些服务走了 feature、哪些降级了基线」。

L3 收口三件事：
1. **零改动染色**：应用被部署到 feature 泳道即自动参与联调，无需改代码。
2. **泳道自动回收**：闲置 TTL 清理，僵尸泳道不再堆积。
3. **泳道可视化**：部署态矩阵 + 运行态 trace 着色。

## 决策记录

| 议题 | 决策 | 理由 |
|------|------|------|
| sidecar 染色深度 | **SDK env 默认染色**（非 Istio mesh / 非 sidecar） | env 默认值覆盖「零改动联调」90% 场景；单集群轻资产定位，mesh 全家桶过重（YAGNI） |
| 泳道回收 | **闲置 TTL**（非 baseline 触发） | TTL 判定信号（UpdatedAt + 活跃 run）已有且可靠；baseline 触发依赖 change 关联链，直推分支/集成分支不挂 change 会断链 |
| 拓扑可视化 | **泳道矩阵 + trace 着色**（不做流量动画） | 部署态/运行态互补且数据现成；无全量调用采集，动画是伪数据（违背「不造伪数据」原则） |

## 1. SDK env 默认染色（应用零改动核心）

**现状**：SDK `GetService(ctx, name)` 的 lane 来自调用方 ctx（应用须挂 `LaneMiddleware` 或手动 `WithLane`）；reconciler 已给 Pod 注 `PAAS_LANE_ID` env，但 SDK 不消费它。

**改动**（`sdk/paas-registry/lane.go` + `registry.go`）：

- 新增 `SelfLane()`：进程启动时读一次 `PAAS_LANE_ID` env（缺省/`default` 返 `""`），包级缓存。
- lane 解析优先级：**显式 ctx lane（`LaneFromCtx`）> SelfLane() env > 无**。
  - 应用挂了 middleware：用请求级染色（入口指定的 lane，如网关注入的 `feature-x`）。
  - 没挂或请求无 lane header：回落到自身部署泳道——「我部署在 feature-x，我调下游也走 feature-x」，这正是联调语义。
- 出站 `ApplyLaneHeader` 同步加 SelfLane 回落（ctx 无 lane 且 env 有 → 注入）。
- **诚实边界**：非 SDK 应用（裸 http client 直连 Service DNS）不自动染色，SDK 文档明确说明；这类应用跨泳道联调仍需入口侧显式指定（部署隔离仍生效，只是不降级发现）。

## 2. 入口自动染色

Playground/测试触发器无需改动（L2 已裁决：入口染色归 SDK/应用侧）；推理网关不是服务网格入口（L2 已裁决不动）。本节实际改动≈0——SDK 侧 SelfLane 已覆盖「零改动」诉求。

## 3. 泳道闲置 TTL 回收

**新组件** `internal/workload/laneGC.go`（core 内周期任务，非 K8s controller）：

- **周期**：每 30min 扫一次（env `PAAS_LANE_GC_INTERVAL`，0=禁用）。
- **回收条件**（全部满足）：
  1. lane ≠ default；
  2. `Workload.UpdatedAt` 距今 > TTL（默认 72h，env `PAAS_LANE_TTL`）；
  3. 无活跃关联——该 (app, lane) 无 running/paused 的 pipeline run（经依赖倒置接口 `ActiveRunChecker` 注入，避免 workload→pipeline import）。
- **回收动作**：调 Repository `Delete`（经 `Applier` 装饰器投影删 CRD → K8s GC 级联清 Deployment/Service/Pod），记审计（`lane_gc`，actor="lane-gc"）+ 日志。
- **集成**：`cmd/core` 装配，随 HTTP server 生命周期启停（`baseCtx` 派生，进程退出即停）。
- **安全护栏**：
  - prod 环境的 lane Workload 不回收（只回收测试环境泳道；灰度泳道回收留后续单独策略）。
  - 单轮删除上限 20 个（防 TTL 误配置过短引发雪崩）。
- **容错**：ActiveRunChecker 查询失败跳过该 lane（fail-open 下一轮再试）；删除失败记日志下一轮重试。

## 4. 泳道可视化（前端两视图）

- **A 泳道矩阵（部署态）**：`EnvironmentDetail.vue` 应用部署矩阵升级——按 (应用 × 泳道) 展示实例数 + 就绪态，feature 泳道列高亮，未部署该泳道的服务列显「↩ 基线」角标（表示流量将降级 default）。数据：既有 `/api/workloads` 按 lane 分组前端聚合。
- **B 链路泳道着色（运行态）**：trace 瀑布图（`useSpanTree` 已有）给含 `paas.lane` 属性的 span 加泳道色带 + 图例；应用可观测 tab 的「最近链路」同样着色。数据：既有 Jaeger trace（`paas.lane` 已写入）。
- 不做流量动画（无全量调用采集，不造伪数据）。

## 测试

- **SDK 单测**：SelfLane 缓存 + 优先级回落（ctx 显式 > env > 无，三种组合）+ ApplyLaneHeader env 回落。
- **laneGC 单测**：fake clock 驱动 TTL 边界 / 活跃 run 阻止回收 / prod 跳过 / 单轮上限 / 删除投影。
- **e2e（k8s）**：部署 paas-shop feature 泳道服务（不挂 middleware 版本）→ 观察其调下游自动带 feature lane → trace 中下游 span 出现 `paas.lane`；TTL 调成 1min 验证自动回收 + 审计落库。

## 改动面汇总

| 层 | 文件/包 | 量级 |
|----|--------|------|
| SDK | `sdk/paas-registry/{lane,registry}.go` | 小 |
| 后端 | `internal/workload/laneGC.go` + `cmd/core` 装配 + 审计 | 中 |
| 前端 | `EnvironmentDetail.vue` 矩阵 + trace 着色（两处复用 useSpanTree） | 中 |
| 测试 | SDK 优先级 + laneGC 单测 + e2e | 中 |

## 不做项（YAGNI）

- Istio/zeus sidecar 全 mesh 自动染色（远期，等真有多协议/多语言刚需）。
- baseline 触发回收（merge 即回收）——可作 TTL 之上的后续增强（released 时给关联 lane 打「待回收」标记）。
- 实时流量拓扑动画（无全量调用采集）。
- 跨集群泳道。
- 灰度泳道（生产）回收策略。
