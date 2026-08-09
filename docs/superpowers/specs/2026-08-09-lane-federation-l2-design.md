# 泳道联调 L2：跨泳道服务发现 + 流量染色（生产标准）

## 背景

L1（`2026-08-09-pipeline-deploy-release-lane-design.md`）把 lane 落到数据模型：Workload 找/建含 `(tenant, app, env, lane)`，Release 带 LaneID/SourceRunID，deploy stage 带 lane 参数，测试流水线 `deploy(env=test, lane={{run.branch}})` 让每分支独立泳道。

但 L1 只「部署到泳道」，**泳道之间不会联调**——feature 泳道的变更服务调用 default 基线的未变更服务时，服务发现仍只返 default 实例，feature 泳道的变更无法与基线真正组合跑通一条完整链路。这是测试环境「变更 + 基线联调」的本质，也是 PaaS 区别于单服务平台的核心能力。

L2 补齐这块：**跨泳道服务发现 + 流量染色**。feature 泳道服务调用其它服务时，优先发现 feature 泳道实例（若有），缺失则降级 default 基线实例——一次请求里「变更服务在 feature、未变更服务降级 default」，无需全量部署即可联调。

## 业界对标

- **Istio VirtualService + DestinationRule**：流量按 header 路由到 subset（泳道）。功能完整但门槛极高（开发者手写 VirtualService/gateway），且绑定 Istio。
- **Snowflake/多租户 SaaS 的 test scope**：应用层 test context，非基础设施。
- **单服务平台（Vercel/Render/Railway）**：不涉及多服务联调。

L2 的定位：**平台内建「按 header 染色的跨泳道服务发现」**，开发者零配置（SDK 从请求 header 取 lane 自动发现），不绑定特定 mesh（数据面基于 K8s Endpoints + paas-registry SDK，与 L1 一脉相承）。

## 核心模型

### 跨泳道降级发现（L2 核心）

服务发现带 lane 维度，**优先泳道，缺失降级基线**：

```
/dp/instances?service=user-svc&lane=feature-x
  → 有 feature-x 泳道实例：返 feature-x 实例（变更服务的 feature 版本）
  → 无 feature-x 实例：降级返 default 基线实例（未变更服务走基线）
```

一次调用链 `gateway → user-svc(feature-x) → order-svc(default) → product-svc(feature-x)`：
- user-svc 部署在 feature-x 泳道 → 发现 feature-x 实例
- order-svc 未变更（无 feature-x 泳道）→ 降级 default 实例
- product-svc 部署在 feature-x 泳道 → 发现 feature-x 实例

链路里变更服务走 feature、未变更服务降级 default，组合跑通完整链路。

### 流量染色（x-paas-lane header）

请求入口（网关 / 测试触发器）带 `x-paas-lane: feature-x` header。服务间调用时，paas-registry SDK 从请求 ctx/header 取 lane 透传到 GetService(name, lane)，发现对应泳道实例。

- **入口染色**：Playground/测试触发器/gateway 按目标 lane 注入 header。
- **透传**：SDK 从 incoming header 取 lane 放入 outgoing ctx（HTTP client/sidecar 注入）。
- **L2 最简方案**：SDK 透传（应用用 paas-registry SDK 即得）。
- **L3 演进**：sidecar 自动染色（应用零改动，全 mesh）。

## 数据面变化

### Workload/Service 打 lane label

reconciler 建 Deployment/Service 时打 `paas.aitoys/lane=<lane>` label（当前有 `paas.aitoys/workload`/`tenant`/`app`，缺 lane）。Endpoints 经 Service label 派生 lane。

- default 基线：`paas.aitoys/lane=default`
- feature 泳道：`paas.aitoys/lane=feature-x`

### /dp/instances 加 lane 参数 + 降级

`internal/dataplane/endpoints.go` `Instances(ctx, namespace, serviceName)` 扩展：

```go
// Instances 返回 service 的 ready 实例。lane 非空时优先返该泳道实例，缺失降级 default 基线。
Instances(ctx, namespace, serviceName, lane string) ([]Instance, error)
```

实现：
1. lane 空 → 返 default 实例（向后兼容）
2. lane=default → 返 default 实例
3. lane=feature-x → 先查 Endpoints label `paas.aitoys/lane=feature-x`（经 EndpointSlice label selector 或同名 Service 派生）；有则返；无则降级 default。

K8s Endpoints 不直接带 lane label（label 在 Service 上）。降级发现策略：
- **方案 A**（L2 采用）：Service 名含 lane（`<app>-svc-<lane>` vs default 的 `<app>-svc`，L1 CreateRelease 已这么命名）。`/dp/instances?service=<app>&lane=feature-x` → 先查 `<app>-svc-<lane>` 的 Endpoints（feature 泳道），无/空则查 `<app>-svc`（default）。**复用 L1 的命名约定，零额外 label，最简**。
- 方案 B（留后续）：EndpointSlice + lane label selector（更灵活，但 EndpointSlice 迁移 + label 派生复杂）。

### governance Instance.LaneID 启用

`governance.Instance.LaneID` 从「预留」转「启用」：实例注册时带 LaneID（数据面 /dp/ 从 K8s Endpoints 读时，按 Service 名派生 lane）。governance 列表/发现按 lane 过滤。

### sdk/paas-registry GetService 加 lane

```go
// GetService 调 GET /dp/instances?service=<name>&lane=<lane>。lane 从 ctx 取（x-paas-lane header）。
func (r *paasRegistry) GetService(ctx context.Context, name string) (*types.ServiceEntry, error)
```

SDK 从 ctx 取 lane（context value，由应用 HTTP middleware 从 `x-paas-lane` header 注入）。空则不传 lane（default）。

## 流量染色透传链

```
测试触发器/Playground
  └─ 注入 x-paas-lane: feature-x（按目标 lane）
     ↓
gateway（入口）
  └─ 透传 header 到 upstream
     ↓
user-svc（paas-registry SDK）
  └─ middleware 从 x-paas-lane header 取 lane 存 ctx
     ↓ 调 order-svc
  └─ SDK GetService(ctx, "order-svc") → ctx 含 lane=feature-x
     └─ /dp/instances?service=order-svc&lane=feature-x → 降级 default（order-svc 无 feature 泳道）
     └─ outgoing HTTP 注入 x-paas-lane: feature-x（透传）
        ↓
order-svc ... 同款透传
```

关键：**SDK 提供 lane middleware**（从 incoming header 取 lane 存 ctx）+ **outgoing 注入**（HTTP client 从 ctx 取 lane 加 header）。应用引入 SDK middleware 即得透传能力。

## 前端

- **环境详情页**：显示该环境的 Workload 按 lane 分组（default 基线 + 各 feature 泳道），泳道卡显示变更服务 + 健康度。
- **应用详情/部署 tab**：Workload 按 lane 分组显示。
- **Playground/测试触发**：选 lane（default 或 feature-x），请求带 x-paas-lane header。
- 拓扑可视化（feature 泳道 + default 基线流量）留后续。

## 泳道生命周期

- **创建**：CI 流水线 deploy(test, lane={{run.branch}}) 自动建 feature 泳道 Workload。
- **联调**：测试触发器按 lane 注入 header，跨泳道降级发现。
- **回收**：baseline 合并主干后，feature 泳道 Workload 可回收。
  - L2：手动回收（admin/应用详情删除 feature Workload，或提供「合并后清理」按钮）。
  - 自动回收（baseline stage 触发清理、闲置 TTL）留后续。

## 影响范围

### 后端
- `internal/controller/`：reconciler 建 Service/Deployment 时打 lane label（applier podSpec/applyService）。
- `internal/dataplane/endpoints.go`：`Instances` 加 lane 参数 + 降级发现（Service 名派生 lane）。
- `internal/dataplane/handler.go`：`/dp/instances` 加 `lane` query 参数。
- `internal/governance/`：Instance.LaneID 启用（注册/发现按 lane）。
- `sdk/paas-registry/`：GetService 从 ctx 取 lane + 提供 lane middleware（HTTP incoming→ctx→outgoing）。
- `internal/core/gateway/`：入口染色（按目标 lane 注入 x-paas-lane header 到 upstream）。

### 前端
- 环境详情页 + 应用部署 tab：Workload 按 lane 分组。
- Playground/测试触发：lane 选择。

### 测试
- dataplane：lane 降级发现（feature 有/无 → 返 feature/default）。
- sdk：lane middleware 透传（incoming header → ctx → outgoing header）。
- e2e：paas-shop feature 泳道 + default 基线联调（变更服务调未变更服务，跨泳道发现）。

## 不做项（YAGNI）

- **L3 全链路 mesh**（Istio/zeus sidecar 自动染色 + 请求级流量染色）：远期，L2 用 SDK 透传够用。
- **EndpointSlice + lane label selector**（方案 B）：L2 用 Service 名派生（方案 A）最简。
- **泳道自动回收**（baseline 触发/闲置 TTL）：L2 手动回收。
- **拓扑可视化**（流量动画/服务网格图）：L2 列表分组够用。
- **跨集群泳道**：单集群内。

## L1 已为 L2 锁定的接口（直接消费）

1. Workload.LaneID（L1 写入）+ Service 命名 `<app>-svc-<lane>`（L1 CreateRelease）→ L2 数据面按名派生 lane。
2. Release.LaneID/SourceRunID（L1 写入）→ L2 追溯。
3. deploy stage lane 参数（L1）→ L2 feature 泳道 Workload 由 CI 自动建。

## 验证

- 后端单测：dataplane lane 降级 + sdk middleware 透传 + governance lane 过滤。
- `go test ./...` 全绿。
- e2e（k8s）：paas-shop feature-x 泳道（CI deploy）+ 测试触发器带 x-paas-lane:feature-x header → 变更服务发现 feature 实例、未变更服务降级 default 实例。
