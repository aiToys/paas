# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目定位

一站式 PaaS 平台 —— 面向基础设施的统一平台，覆盖服务治理、中间件管理、MaaS、DevOps。

**当前阶段**：开源就绪起步期。本期范围 = **Platform Core 底座 + MaaS 推理平台**，其余三个子系统（治理/中间件/DevOps）后续作为插件接入。完整设计见 `docs/superpowers/specs/2026-07-26-maas-platform-foundation-design.md`。

## 关键架构约束（已定，勿轻易推翻）

- **主语言 Go**，云原生控制面。
- **混合部署**：控制面跑在 K8s 上，数据面既能纳管 K8s 原生资源，也能接管外部实例。
- **商业多租户**；**SaaS + 私有化双模交付**（控制面必须可打包为离线交付件）。
- **三层 + 插件架构**：Platform Core（最小不可分内核）+ 插件化子系统。子系统是**插件而非独立微服务**。
- **数据面与控制面解耦**：控制面只下发 CRD 期望状态，控制面挂了数据面继续跑。
- **Core 不依赖任何外部服务治理/中间件**（避免元设施鸡生蛋）：元数据 PostgreSQL，事件 NATS（Core 自带）。
- **全开源，Apache 2.0**；CI 禁止引入 GPL/AGPL 依赖。

## 技术栈

| 领域 | 选型 |
|------|------|
| 控制面 | Go + controller-runtime + kubebuilder |
| 元数据存储 | PostgreSQL |
| 事件总线 | NATS（嵌入式） |
| 推理引擎 | vLLM（纳管，不自研） |
| GPU 调度 | K8s device-plugin + 自研编排（本期仅显存核算 + 反亲和） |
| 可观测 | OpenTelemetry + Prometheus + Loki + Tempo |
| 交付 | Helm + OCI + `airsync` 离线工具 |
| 前端 | Vue 3 + Element Plus + Vite + TypeScript + Pinia |
| 后台前端基座 | Fork `vue-admin/vue-admin`（MIT） |
| API 契约 | OpenAPI + codegen（前后端类型一致） |

## 仓库结构（monorepo）

```
cmd/core/                       # Platform Core 启动入口
pkg/plugin/                     # 插件契约（对外可见）
internal/core/                  # Core 内部：plugin/identity/health
frontend/                       # pnpm workspace（三套前端）
├── console-admin/              # 后台管理（基于 vue-admin 基座，MIT）
├── console-user/               # 用户控制台（模型市场/部署/Playground/API Key/用量）
└── landing/                    # 官网展示页
docs/superpowers/{specs,plans}/ # 设计与实施计划
CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md LICENSE(.gitignore 后续子模块: deploy/ airsync)
```

## 常用命令

**后端（根目录）：**

```bash
make build          # 编译 bin/core
make test           # go test ./... -race
make lint           # golangci-lint run ./...
./bin/core          # 运行，暴露 :8080（/livez /v1/models /v1/chat/completions）
go test ./internal/core/gateway/ -run TestChatCompletions -v   # 单个测试
PAAS_API_KEY=sk-xxx ./bin/core   # 自定义 API Key（追加为 t-acme admin）
```

端到端验证（core 启动后）。API Key 绑定 (租户, 角色)，三预设演示 Key：
`sk-acme-admin`（Acme 管理员，默认）/ `sk-globex-admin`（Globex 管理员）/ `sk-acme-dev`（Acme 开发者）。

```bash
# 列模型（OpenAI 兼容，含 owned_by=供应商；平台级共享）
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/v1/models
# 模型市场富信息（含通道列表）
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/models
# 流式推理（mock 通道，需 model:infer 权限）
curl -N -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"model":"qwen2.5-7b","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://localhost:8080/v1/chat/completions
# 多租户隔离：Acme 只见 Acme 应用；跨租户访问 404 不泄漏
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/applications
curl -H "Authorization: Bearer sk-globex-admin" http://localhost:8080/api/applications
curl -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer sk-acme-admin" \
  http://localhost:8080/api/applications/app-etl   # 404（app-etl 属 globex）
```

**前端（`frontend/` 目录）：**

```bash
pnpm install                  # 安装三套全部依赖
pnpm dev:admin | dev:user | dev:landing   # 分别启动（端口 5173/5174/5175）
pnpm build                    # 构建三套
```

## 垂直切片（已落地）

### MaaS 端到端闭环 + 多通道模型市场

三层抽象 `Model → Channel → Provider`，模型市场真实化已落地：

```
console-user 模型市场(/api/models) / Playground(/v1/chat/completions)
  → Gateway(API Key 鉴权 + OpenAI 兼容 SSE + Token 计量 + 按通道路由)
  → MaaS 插件(catalog seed 加载) → Channel(mock/echo Provider) → 流式返回
```

- `pkg/provider/`：`Provider`（`Name()+Chat()`）/ `Model` / `Channel` / `GatewayRegistrar`(`RegisterModel`) 平台级公共契约（独立包避免 import 循环）。
- `internal/core/gateway/`：API Gateway（`map[model]*Model` 路由表 / `Resolve` 按通道优先级取首个健康 / `MarkChannelStatus` 调用失败被动降级 / API Key 中间件 / OpenAI 流式 handler / Meter / `/api/models` 富信息 + `/v1/models` OpenAI 兼容）。
- `internal/maas/`：MaaS 插件（Init 阶段加载 `catalog()` 注册模型目录）+ `MockProvider`（按预设文本流式）+ `EchoProvider`（回显）。
- `pkg/plugin.CoreDeps` 的 `Gateway()` 注入点（依赖倒置）。
- 路由策略：通道按 `Priority` 升序，跳过 `offline`；全部 `offline` 报错；调用失败标记 `degraded`。
- 切片**不依赖 K8s/GPU**（进程内 mock）；真实 vLLM 纳管 + K8s 编排为下一切片。

### 应用为主线（统一控制台）

应用是主线抽象，各类资源以绑定形式归属应用：

- `internal/core/application/`：`Application` 领域（`Bindings` 真源 + `Resources` 派生计数）/ Repository / 内存实现（seed）。
- REST API：`GET/POST /api/applications`、`GET /api/applications/{id}`、`POST /api/applications/{id}/bindings`、`DELETE /api/applications/{id}/bindings/{type}/{name}`。
- console-user：应用列表 + 详情（资源绑定分组 + 绑定/解绑端到端）+ 模型市场 + Playground（模型下拉 + `?model=` 预选）。

### 多租户身份骨架（RBAC + 租户隔离端到端）

API Key 是 `(租户, 用户, 角色)` 三元组的凭证，鉴权与计量的统一锚点：

```
Authorization: Bearer <key>
  → identity.LookupAPIKey → (tenantID, userID, roles)
  → tenant.WithTenant(ctx) + WithRoles(ctx)        # ctx 传播
  → Repository 强制按 tenant 过滤（缺失即拒）       # 数据隔离
  → Require(perm) / handler.Authorize              # 粗粒度 RBAC
```

- `internal/core/identity/`：`Tenant/User/Role/Permission/APIKey` + `BuiltinRoles()`（tenant-admin 通行 / developer / viewer）；Repository + 内存实现。
- `pkg/tenant`：`WithTenant/TenantFrom`，租户上下文走 ctx（PG 时代可下沉行级安全，调用方无感）。
- `internal/core/application/`：`Application.TenantID`；Repository 全方法从 ctx 取租户强制过滤，跨租户访问统一 not found（不泄漏存在性），`Create` 以 ctx 为准忽略请求体。
- `internal/core/gateway/`：`APIKeyAuth(idb)` 解析 Key 注入身份；`Require(perm)` 粗粒度中间件；`RequestAllowed` 供需方法级判定的 handler 复用（应用 API 按方法区分 `application:read/write`、`binding:write`）。
- `cmd/core/seed.go`：两租户（Acme/Globex）+ 三演示 Key。
- **模型目录平台级共享**（`/api/models` `/v1/models` 不按租户过滤）；租户私有的是应用及其绑定。

### 工作负载（应用运行形态）

工作负载归属应用，分三类（Service/Job/CronJob），本期进程内 mock，真实 K8s 编排为下一切片：

```
internal/workload/  领域（Type/Status 常量 + Validate）+ Repository（租户隔离）+ 内存 seed
  -> handler: /api/applications/{id}/workloads（应用下）+ /api/workloads?type=（跨应用）
  -> 权限 workload:read/write（方法级 Authorize 注入）
```

- `internal/workload/`：`Workload`（期望 Replicas vs 就绪 Ready 分离，为 controller-runtime 铺路）/ Repository / 内存实现（seed 5 条跨两租户）/ handler。
- 路由：`GET/POST /api/applications/{id}/workloads`、`GET /api/workloads?type=`、`PUT /api/workloads/{id}`（扩缩容/状态）、`DELETE /api/workloads/{id}`。
- cmd/core 用 composite handler 按 `/workloads` 后缀分发，避免与 application 的 `/api/applications/` 前缀冲突。
- console-user：`Workloads.vue`（按类型 tab 接真实 API + 扩缩容/删除）+ 应用详情「部署」tab 渲染该应用工作负载。
- 切片**不依赖 K8s**（进程内 mock）；真实 vLLM 纳管 + K8s 编排为下一切片。

### 环境（物理隔离单元）

环境是独立一等公民（非应用子节点），应用 × 环境多对多，交叉点 = 部署实例（Workload/Binding 带 EnvID）：

```
internal/environment/  领域(type prod|test + cluster) + Repository(租户隔离) + 内存 seed
  -> handler: /api/environments（CRUD）
  -> Workload/Binding 带 EnvID + LaneID(default=基线，预留不实现路由)
```

- `internal/environment/`：`Environment`（type: prod|test，cluster 物理落点 prod-bj/prod-sh）/ Repository / 内存实现（seed 跨两租户五环境）/ handler。
- `Workload` 加 `EnvID` + `LaneID`（`LaneID="default"` = 基线单例，其他 = 泳道，本期不创建非 default）；Repository `List(ctx, envID, appID, wtype)` 按 envID 过滤。
- 命名定稿（见蓝图「环境与联调模型」）：物理环境生产/测试；环境内基线（`LaneID=default`，单例）+ 联调泳道（测试，多例）/灰度泳道（生产，多例）。选 `default` 不选空值：显式存在、路由降级无特判、业界一致（zeus/Istio）。
- REST：`GET/POST /api/environments`、`GET /api/environments/{id}`、`DELETE /api/environments/{id}`；`GET /api/workloads?envId=&type=` 按环境+类型过滤。
- console-user：`Environments.vue`（环境列表）+ `Workloads.vue` 环境过滤 pill + 应用详情部署 tab 按环境分组。
- **LaneID 预留不实现路由**：泳道（染色+降级）归后续服务治理切片；Release/EnvTemplate 归 DevOps/GitOps。起步只建环境基座。

### 生产安全防护（横切机制）

生产/测试隔离是**平台级横切关注点**，统一在此解决，后续切片（DevOps/应用配置）自动继承：

- **环境类型感知 RBAC**（最硬防线）：生产写操作需 `prod:write` 权限。`developer` 角色无 `prod:write` -> **生产只读**；`tenant-admin` 有。`identity.PermProdWrite` + `gateway.RequestAllowedProd`。
- **workload 环境类型感知校验**：`workload.EnvTypeResolver` 接口（依赖倒置，由 environment.Repository 实现 `EnvType`），写操作（Create/Update/Delete）查目标环境类型，prod 则校验 `prod:write`。environment handler 的 Create/Delete 同理（body.type / EnvType）。
- **全局环境上下文**（前端）：`stores/env.ts` pinia store，环境从「页面过滤」升为「全局上下文」（顶栏常驻，贯穿所有页面）。
- **生产 gated 模式**：切到生产需二次确认 + **15 分钟超时自动回退**测试环境。
- **生产视觉强隔离**：`app.env-prod` class 驱动整页红边框 + 顶栏红条 + 警示横幅「⚠️ 生产环境」+ 倒计时。
- **统一危险操作确认**：`composables/useDangerConfirm.ts`，生产高危操作（删除）要求**输入名称确认**，测试普通确认。

后续切片受益：DevOps/应用配置的写操作自动受 `prod:write` 保护（注入 EnvTypeResolver 即可），生产操作自动有视觉警示和确认（调用 useDangerConfirm），切片只关注业务逻辑。

## 前端架构

三套独立 SPA，共享设计系统（Element Plus + 暗黑模式）：

| 应用 | 定位 | 端口 |
|------|------|------|
| console-admin | 平台运维/管理员（基于 `vue-admin` 基座，RBAC/JWT/动态路由已就绪） | 5173 |
| console-user | 租户开发者（应用为主线 + 三层信息架构，见下） | 5174 |
| landing | 访客官网展示页（静态、SEO 友好） | 5175 |

console-user 导航采用**三层信息架构**（避免「资源」概念被滥用）：

- **资源中心** = 数据服务（可绑定 Add-on）：模型推理 / 数据库 / 缓存 / 消息队列 / 对象存储 / 向量数据库 / 搜索引擎
- **工作负载** = 应用运行形态：服务（Deployment）/ 任务（Job）/ 定时（CronJob）
- **平台能力** = 横切基础设施：服务治理（含注册发现/配置中心/API网关/熔断）/ 可观测 / 安全
- 另有：应用（主线）/ DevOps / Playground

关键区分：**配置中心属服务治理**（运行时动态、跨实例、版本灰度），与**应用详情的「应用配置」**（env/Secret、工作负载级、静态）是两个层面，勿混。

- API 契约：后端 OpenAPI 自动生成前端 TS 类型（Plan 4 起接入 Gateway）。
- console-admin 的基座源码自带其 `CLAUDE.md` 与 `docs/standards/`（四层架构 lib/app/modules/shared），改它时遵循其自身规范。

## 平台模块全景

见 [平台模块蓝图](./docs/superpowers/specs/2026-07-27-platform-modules-blueprint.md)。当前完成度约 45%（Core 骨架 + 多租户身份 + MaaS + 应用主线 + 工作负载 + 环境 + 生产安全防护），其余模块按蓝图优先级推进。

## 开发约定

- 新建模块或引入新技术栈时，同步更新本文件对应章节。
- 注释语言与代码库现有注释保持一致。
- **未经用户明确要求，不要执行 git commit / 分支操作。**
- 所有依赖须与 Apache 2.0 兼容；新增依赖前确认 license。
- 业务领域逻辑绝不进 Platform Core；判断标准："MaaS / 治理 / DevOps 都会用吗？"
- 多租户隔离由 Core 统一治理（DB 访问层强制 tenant 过滤），插件不得绕过。
