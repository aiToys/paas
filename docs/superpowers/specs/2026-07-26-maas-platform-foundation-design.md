# 设计规格：一站式 PaaS 平台（MaaS 切入 + 平台底座）

- **状态**：已批准（待用户审核）
- **日期**：2026-07-26
- **范围**：本 spec 覆盖平台的整体架构骨架与第一个落地子系统（MaaS 推理平台 + Platform Core 底座）。服务治理、中间件管理、DevOps 作为后续迭代，沿用本 spec 定义的插件契约接入。

---

## 1. 项目定位与约束

一站式 PaaS 平台，覆盖服务治理、中间件管理、MaaS、DevOps 四大领域的基础设施统一平台。

**已确定的关键约束：**

| 维度 | 决策 |
|------|------|
| 主语言 | Go（云原生控制面） |
| 部署形态 | 混合模式——控制面跑在 K8s 上，数据面既能纳管 K8s 原生资源，也能接管外部实例 |
| 用户模型 | 商业多租户 |
| 交付形态 | SaaS + 私有化双模（控制面必须可打包成离线交付件） |
| 切入点 | MaaS（推理平台）+ Platform Core 底座，并行落地 |
| 开源策略 | 全开源，Apache 2.0；社区就绪全套 |
| 后台前端基座 | Fork [`vue-admin/vue-admin`](https://github.com/vue-admin/vue-admin)（Vue 3.5 + Element Plus + Pinia + TS） |
| 用户/展示前端 | 自研（同技术栈） |

---

## 2. 整体架构

三层 + 插件结构：

```
接入层   统一 API Gateway（OpenAI-compatible + 平台 REST + 鉴权/多租户路由/限流/计量）
控制面   Platform Core（最小不可分内核） + 插件槽（MaaS｜治理*｜中间件*｜DevOps*）
数据面   Inference Gateway / vLLM Pods / Provider Agent
         控制面与数据面解耦：控制面挂了，已部署模型继续服务
```

**三个关键原则：**

1. **Platform Core 最小不可分**：只含所有子系统都依赖的元能力（租户、鉴权、资源纳管、编排、可观测、插件机制），不含任何业务领域逻辑。
2. **子系统是插件而非独立微服务**：以插件形式注册进 Core，共享 Core 的鉴权/存储/事件总线；避免"元设施鸡生蛋"。
3. **数据面与控制面解耦**：控制面只下发期望状态（CRD），数据面负责实际运行。

---

## 3. Platform Core 组件

| 模块 | 职责 | 存储/依赖 |
|------|------|----------|
| Identity | 租户/用户/角色/RBAC/Token | PostgreSQL |
| Provider Registry | 纳管 K8s 集群、外部 vLLM 实例，抽象为 Provider + Credential | PostgreSQL + Secret |
| Orchestrator | CRD 注册 + controller-runtime 调和循环 | K8s apiserver |
| Event Bus | 跨模块/插件事件发布订阅 | NATS（Core 自带，不引入 Kafka） |
| Plugin Registry | 插件生命周期：注册/启用/禁用/依赖声明 | Core 自管 |
| Metering | 统一计量埋点（Token/GPU 时长/API 调用） | PostgreSQL + 可选 ClickHouse |
| Observability Hooks | 统一日志/指标/链路埋点接口 | OpenTelemetry SDK |

**存储红线**：Core 不依赖任何外部服务治理/中间件——元数据走 PostgreSQL，事件走 Core 自带 NATS，时序计量走独立库。

### 插件契约

每个插件实现固定 Go interface 注册到 Plugin Registry：

```go
type Plugin interface {
    PluginSpec       // 元信息：名称/版本/依赖/所需 CRD/所需 RBAC
    APIProvider      // 声明暴露的 REST/OpenAPI 路由
    CRDProvider      // 声明由 Core 统一注册的 CRD schema
    MeteringProvider // 声明产出的计量事件类型
    Init(ctx, CoreDeps) error  // Core 注入：DB/EventBus/Provider Registry/OTel
    Run(ctx) error
}
```

**关键决策：**
- **依赖倒置**：插件不自行创建 DB 连接，由 Core `Init` 注入，保证多租户隔离与可观测性统一治理。
- **CRD 统一注册**：Core 在插件 `Init` 阶段统一 apply 所有 CRD schema，避免私有化交付混乱。
- **路由插件声明、Gateway 聚合**：插件只声明路由 + 权限，鉴权/限流/租户路由由 Gateway 统一执行。

---

## 4. MaaS 子系统

### 4.1 组件

- **ModelRegistry**：模型元数据/版本/权重来源（HuggingFace/对象存储/本地）。
- **DeployController**：把"部署模型 X N 副本"翻译为 vLLM Deployment + Service + GPU 申请，持续调和。
- **GPUScheduler**：显存核算 + 反亲和 + 副本编排（本期范围）。
- **MeterHook**：Token 计量 → Core.Metering。

### 4.2 数据面

- **Inference Gateway**：唯一对外推理入口。OpenAI-compatible API、多模型路由、负载均衡、限流、Token 计量、协议转换。**商业差异化核心**，多副本无状态，路由表存 Core。
- **vLLM Pods**：纯执行，被纳管，不自研。
- **ModelCache**：权重缓存；私有化场景预打包权重，避免 GPU 节点访问外网。

### 4.3 数据流（部署模型 → 推理服务）

1. 用户 `POST /v1/models/deploy` → Gateway 鉴权 + 租户路由 → 写 `InferenceDeployment` CRD。
2. DeployController watch → GPUScheduler 选节点（显存核算 + 反亲和）→ 创建 vLLM Deployment。
3. vLLM Pod 从 ModelCache 拉权重加载 → 就绪 → DeployController 注册端点到 Gateway 路由表 → EventBus 发布 `model.ready`。
4. 用户 `POST /v1/chat/completions` → Gateway 查路由表 → 负载均衡到副本 → 流式返回 + MeterHook 记 token。

### 4.4 GPU 调度本期范围

只做：显存核算 + 反亲和 + 副本编排。**多模型共享单卡（time-sharing）放到后续迭代**（YAGNI）。

---

## 5. 多租户隔离

- **控制面**：所有元数据表带 `tenant_id`；Core DB 访问层强制注入 tenant 过滤（中间件级，插件无法绕过）；RBAC 在租户域内生效。
- **数据面**：模型部署 Pod/Service 打 tenant 标签；本期采用**命名空间内 label + NetworkPolicy** 隔离（避免 namespace 爆炸）。
- **GPU**：device-plugin 分配 + 租户级配额（ResourceQuota / 自定义配额控制器）。
- **Gateway**：API Key 绑定 tenant；路由表按 tenant 分区；限流/计量按 tenant 独立计数。

---

## 6. 可观测性与可交付性

- **可观测**：全链路 OpenTelemetry（Loki 日志 / Prometheus 指标 / Tempo 链路）；私有化默认打包轻量版（Prometheus + Loki 一体化）。
- **交付件**：Helm Chart + OCI 镜像仓库；离线交付提供 `airsync` 工具（`airsync save` 打包镜像 + 权重为 tar，`airsync load` 导入）。
- **升级**：声明式 CRD + 控制面版本协商；`paas upgrade` 做版本兼容性检查 + 自动回滚（应对私有化版本碎片）。

---

## 7. 测试策略

- **后端**：Go 单元测试 + `envtest`（controller-runtime 真实 apiserver 跑 CRD 调和）+ E2E（Kind 集群跑真实部署）。
- **前端**：Vitest 单测 + Playwright smoke（基座已具备）。
- **CI**：lint + license 合规检查（禁 GPL/AGPL）+ 单测 + envtest + 镜像构建；PR 强制。

---

## 8. 前端架构

三个独立 SPA，共享设计系统（Element Plus + 自建 token，暗黑模式）：

| 应用 | 技术 | 面向 | 核心模块 |
|------|------|------|---------|
| Admin Console | Fork `vue-admin/vue-admin` | 平台运维/管理员 | 租户管理、用户/角色、Provider 纳管、模型治理、系统监控、插件管理 |
| User Console | Vue3 + Element Plus（新 SPA） | 租户开发者 | 模型市场、我的部署、Playground、API Key、用量计费、文档 |
| Landing | Vue3 + 静态（SEO 友好） | 访客 | 产品介绍、能力矩阵、快速开始、文档入口 |

- **API 契约**：后端 OpenAPI 自动生成前端 TS 类型（openapi-typescript），前后端类型一致。
- **部署**：三套静态资源独立构建；Admin/User 走 Gateway 鉴权，Landing 公开。

---

## 9. 开源就绪

仓库结构（monorepo）：

```
paas/
├── cmd/                    # 组件入口（core, gateway, agent）
├── pkg/                    # 可复用库
├── internal/core/          # Platform Core
├── internal/plugins/maas/  # MaaS 插件
├── api/                    # CRD schema + OpenAPI 定义
├── console-admin/          # 后台管理前端（vue-admin 基座）
├── console-user/           # 用户控制台前端
├── landing/                # 官网展示页
├── deploy/                 # Helm chart + airsync 工具
├── docs/                   # 用户文档 + spec + plan
├── CONTRIBUTING.md / SECURITY.md / CODE_OF_CONDUCT.md / LICENSE (Apache-2.0)
```

- **协议**：Apache 2.0；依赖 license 合规检查（CI 拦截 GPL/AGPL）。
- **治理**：CONTRIBUTING / CODE_OF_CONDUCT / SECURITY / 模板 issue/PR / good-first-issue 标签。
- **CI/CD**：GitHub Actions（lint/test/build/release）。

---

## 10. 关键技术选型汇总

| 领域 | 选型 | 理由 |
|------|------|------|
| 语言 | Go | 云原生控制面事实标准 |
| 控制面框架 | controller-runtime + kubebuilder | K8s Operator 标准 |
| 元数据存储 | PostgreSQL | 强一致，运维成熟 |
| 事件总线 | NATS（嵌入式） | 轻量，避免元设施依赖 |
| 推理引擎 | vLLM（纳管） | 开源事实标准，GPU 利用率高 |
| GPU 调度 | K8s device-plugin + 自研编排 | 不重造调度器 |
| 可观测 | OpenTelemetry + Prometheus + Loki + Tempo | 云原生标准栈 |
| 交付 | Helm + OCI + airsync 离线工具 | 双模交付 |
| 前端 | Vue 3 + Element Plus + Vite + TS | 基座已就绪 |
| API 契约 | OpenAPI + codegen | 前后端类型一致 |
| 协议 | Apache 2.0 | 云原生主流 |

---

## 11. 本期范围边界（YAGNI 裁剪）

**做**：Platform Core 七模块 + MaaS 插件（模型仓库/部署编排/Inference Gateway/vLLM 纳管/显存+反亲和调度）+ 三前端 + Helm/airsync 交付 + 开源治理。

**不做**（后续迭代）：
- 服务治理、中间件管理、DevOps 子系统
- 多模型共享单卡（GPU time-sharing）
- ClickHouse 计量聚合（本期 PG 明细够用）
- 企业版闭源模块（架构预留插件位）

---

## 12. 风险与缓解

| 风险 | 缓解 |
|------|------|
| 插件契约设计不当导致后续子系统返工 | 本 spec 已定义明确 interface；首个子系统 MaaS 作为契约验证基准 |
| 私有化版本碎片化难升级 | 声明式 CRD + 版本协商 + 自动回滚，从架构层设计 |
| GPU 调度复杂度爆炸 | 本期严格裁剪为显存+反亲和；time-sharing 延后 |
| 双模交付打包复杂 | airsync 工具集中处理离线镜像/权重 |
| 开源协议污染 | CI license 检查拦截 GPL/AGPL |
