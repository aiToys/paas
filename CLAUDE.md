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
| 推理引擎 | 第三方供应商对接（OpenAI/DeepSeek/通义千问，OpenAI 兼容协议聚合；不自建 vLLM） |
| GPU 调度 | K8s device-plugin + 自研编排（本期仅显存核算 + 反亲和） |
| 可观测 | OpenTelemetry + Prometheus + Loki + Tempo |
| 交付 | Helm + OCI + `airsync` 离线工具 |
| 前端 | Vue 3 + Element Plus + Vite + TypeScript + Pinia |
| 后台前端基座 | Fork `vue-admin/vue-admin`（MIT） |
| API 契约 | OpenAPI 3.0 route registry（单一真源，手写 reflector 零依赖）+ openapi-typescript 前端生成 |

## 仓库结构（monorepo）

```
cmd/core/                       # Platform Core 启动入口
cmd/airsync/                    # 离线交付工具 CLI（bundle/install/verify/doctor）
api/core/v1alpha1/              # Workload CRD 类型（K8s 数据面期望状态）
pkg/plugin/                     # 插件契约（对外可见）
internal/core/                  # Core 内部：plugin/identity/health
internal/controller/            # K8s WorkloadReconciler + K8sApplier（CRD→Deployment/Job/CronJob）
internal/airsync/               # 离线交付核心逻辑（manifest/sha256/bundle/install）
frontend/                       # pnpm workspace（三套前端）
├── console-admin/              # 后台管理（基于 vue-admin 基座，MIT）
├── console-user/               # 用户控制台（模型市场/部署/Playground/API Key/用量）
└── landing/                    # 官网展示页
deploy/charts/paas/             # Helm chart（core + postgres + ingress，公网/离线共用）
config/crds/                    # controller-gen 生成的 Workload CRD YAML
docs/superpowers/{specs,plans}/ # 设计与实施计划
CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md LICENSE
```

### 离线交付（airsync）

私有化双模交付（SaaS + 私有化）。公网/私有两路径共用同一 Helm chart（`deploy/charts/paas`），仅 `image.registry` 不同：

- **公网（在线）**：`helm install paas deploy/charts/paas`（或推 tag 后 `helm install paas oci://ghcr.io/aitoys/charts/paas`）。
- **私有（离线）**：`airsync bundle --version <v>`（打包镜像 + chart + manifest 为 `paas-bundle-<v>.tar.gz`）→ 物理介质传到客户 → `airsync install --bundle <file> --target-registry <reg>`（解包 → verify 完整性 → docker load/retag/push → helm install）。
- `airsync verify --bundle <file>` 校验 sha256 完整性（防传输损坏/篡改）；`airsync doctor` 检查 docker/helm/kubectl。

`make airsync` 编译；`make manifests` 生成 CRD（controller-gen）。

## 常用命令

**后端（根目录）：**

```bash
make build          # 编译 bin/core
make test           # go test ./... -race（内存后端，零依赖）
make test-pg        # PostgreSQL 集成测试（自动拉起 compose 的 postgres，需 docker）
make lint           # golangci-lint run ./...
./bin/core          # 运行，暴露 :8080（/livez /v1/models /v1/chat/completions）
go test ./internal/core/gateway/ -run TestChatCompletions -v   # 单个测试
PAAS_API_KEY=sk-xxx ./bin/core   # 自定义 API Key（追加为 t-acme admin）
PAAS_DB_URL=postgres://paas:pwd@host:5432/db?sslmode=disable ./bin/core   # 启用 PG 持久化（全 10 模块，observability 除外）
git tag v0.1.0 && git push origin v0.1.0   # 发版：推 tag v* 触发 CI 多平台镜像发布（ghcr.io/aitoys/paas-core:0.1.0/0.1/0，amd64+arm64）
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

### MaaS 端到端闭环 + 多通道模型市场 + 第三方供应商对接

三层抽象 `Model → Channel → Provider`；聚合第三方供应商（OpenAI/DeepSeek/通义千问），不自建推理集群/不部署 vLLM（轻资产路线）：

```
console-user 模型市场(/api/models) / Playground(/v1/chat/completions)
  → Gateway(API Key 鉴权 + OpenAI 兼容 SSE + Token 计量 + 请求级 failover)
  → MaaS 插件(catalog seed 真实供应商通道 + mock 演示)
  → Channel(openai-compatible Provider 转发 / mock·echo 进程内) → 流式返回
```

- `pkg/provider/`：`Provider`（`Name()+Chat()`）/ `Model` / `Channel`（含 `UpstreamModel`/`CredentialRef` 第三方通道字段）/ `GatewayRegistrar` / `CredentialResolver`（凭证解析接口，依赖倒置）/ 5 个错误 sentinel（`ErrCredentialMissing`/`ErrCredentialInvalid`/`ErrUpstreamRateLimit`/`ErrUpstreamUnavailable`/`ErrUpstreamConfig`，驱动降级分类）。
- `internal/maas/`：MaaS 插件 + `OpenAICompatibleProvider`（一个适配器覆盖三家 OpenAI 兼容协议，纯 `net/http` + SSE 解析）+ catalog seed（gpt-4o/gpt-4o-mini/qwen-plus 双通道容灾/deepseek-chat/deepseek-reasoner + echo-demo/qwen2.5-7b mock 演示）+ `MockProvider`/`EchoProvider`。
- `internal/core/gateway/`：API Gateway（`ResolveChannels` 候选列表 / 请求级 failover：degraded 类错误切备通道，offline 类标 offline / `MarkChannelStatus` / API Key 中间件 / OpenAI 流式 handler / Meter）。
- 凭证：平台级 Secret（`security.Scope=platform`，全租户共享，仅 tenant-admin 可写）经 `Resolve` 取明文（仅内存），`main.go` 桥接 security store → `CredentialResolver` 注入 MaaS 插件。
- 路由策略：通道按 `Priority` 升序，跳过 `offline`；请求时 degraded 类错误（限流/5xx/超时）自动 failover，offline 类（凭证/配置）切下一通道，全部失败 503。
- `pkg/plugin.CoreDeps` 的 `Gateway()` + `SecretResolver()` 注入点（依赖倒置，破除 maas→security import）。
- 切片**不依赖 K8s/GPU**；未配凭证时真实通道返回 `ErrCredentialMissing` → 503，echo-demo 开箱可演示。

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

### 持久化层（PostgreSQL，全模块已迁）

Repository 接口是后端切换点——除 observability（惰性 mock，接真实 Prometheus/Loki/Tempo 时再迁）外，**全 10 模块已迁 PostgreSQL**，按 `PAAS_DB_URL` 在「全内存」与「全 PG」两路径间切换（为空则纯内存、与现状一致、零依赖）：

```
PAAS_DB_URL 非空 → internal/storage/pg.Open + RunMigrations（embed SQL，启动自动 up）
  → cmd/core/persistence.go buildAllStores 构造全 11 PG store + seedPGAllIfEmpty
  → Stores 聚合注入 handler（devops PG 注入 workload PG store，Release 编排经接口透明）
PAAS_DB_URL 为空 → 各 memory.NewStore() 聚合注入（devops memory 与 wlHandler 共享同一 wlRepo）
```

- `internal/storage/pg/`：`pg.go`（pgxpool 连接池 + ping）、`migrate.go`（golang-migrate + `//go:embed all:migrations`）、`helpers.go`（共享 `ErrAlreadyExists`/`IsUniqueViolation`/`FormatExists`/`TenantOrErr`/`RowScanner`，各 pg 子包引用，消除重复）、`migrations/0001..0011_*.up.down.sql`（11 模块 schema）。pgxpool→`stdlib.OpenDBFromPool`→migrate `postgres.WithInstance` 桥接。
- 各 `internal/<mod>/pg/store.go`：实现对应 Repository，显式 `WHERE tenant_id=$1` 多租户隔离（与内存 1:1，RLS 留后续）；多值字段全 JSONB（dataservice.Spec / governance Instance.Meta+Route.Methods / configcenter Publish.Snapshot / billing Limits+Counts+Items）；identity Roles 行存 `*_roles` 子表读时聚合；application_bindings 带 `ord` 保序，Resources 读时 Recount 派生。
- **横切正确性**：billing `CheckAndInc` 用 `FOR UPDATE` 行锁 + `limitForLockedTx`（复用 tx 连接）保证配额检查-递增原子（与内存 sync.Mutex 语义等价，`-race` 并发验证）；configcenter `Publish` 事务内 `MAX(version)+1` 单调 + 旧 active 翻 rolled-back；security 平台级 Secret `tenant_id NULL` + 两个 partial unique index（`WHERE scope='platform'`/`WHERE scope='tenant'`），Resolve 仅平台级返明文；GenerateBill `ON CONFLICT ... WHERE status='unpaid'`（已支付账单不可变）。
- **devops 跨 store 透明**：Release 编排（CreateRelease/RollbackRelease）调注入的 `workload.Repository` 接口（List 找/建基线 Workload + UpdateImage + 记 PreviousImageID），不读写 workloads 表，对 workload 存储后端完全透明；事务仅覆盖 releases 表。
- 多租户隔离键：所有业务表带 `tenant_id`，查询层显式过滤；`Create` 以 ctx 租户为准忽略请求体（防越权写）。跨租户访问 not found 不泄漏。
- seed 幂等：`seedPGAllIfEmpty` 各模块表空才灌（简单模块 Count+Create，复杂模块 devops/configcenter/billing/security 各自 `SeedIfEmpty` 直接 SQL 绕过编排/状态机，保留 paid 历史账单/BuildSuccess 已完成/平台级 Secret NULL tenant_id）；内存路径保持 `NewStore` 内联 seed。各模块 memory 导出 `Seed<X>()` 供 PG 复用（DRY，PG/内存同一真源）。
- 集成测试 `//go:build integration` 门控（`PAAS_TEST_PG_URL` 驱动，每测 resetSchema DROP 全表重建）；`make test-pg` **加 `-p 1` 串行**（各包 resetSchema 共享同一 database，并行互清），默认 `make test` 不依赖 PG。
- **docker-compose** 内置 `postgres:16-alpine` + 持久卷 `paas-pg`，core `depends_on` 健康检查后启动；`docker compose up` 一键持久化。
- **后续**：observability 迁 PG（接真实后端时）；RLS 行级安全、ClickHouse 时序计量库、连接池精细调优、跨包测试独立 schema 留后续。

### API 契约（OpenAPI 3.0 单一真源，已落地）

route registry 作路由 + 元数据单一真源，同时驱动 mux 注册与 spec 生成，消除"路由声明两处"漂移：

```
cmd/core serveHTTP
  └─ reg := apiroute.New(mux, Info{...})
     ├─ reg.Register(method, path, handler, ...)   # mux（Go 1.22 method-scoped）+ spec
     ├─ reg.Operation(method, path, ...)           # spec only（composite 子操作，mux 粗粒度不变）
     └─ mux.Handle("/openapi.json", apiroute.ServeSpec(reg))
```

- `internal/apiroute/`：`openapi.go`（OpenAPI 3.0 文档结构）、`reflect.go`（手写 Go→JSON Schema reflector：struct/slice/map/指针/time.Time/嵌套/命名类型 `$ref` 去重，零外部依赖）、`registry.go`（Registry own mux + Register/Operation + Options + ServeSpec）。
- 响应包裹 `{data:T}`/`{error:string}` 建模一次（inline，不注册命名 component）；perm → BearerAPIKey scope 映射（文档直显每端点所需权限）。
- **Composite 路由**：mux 粗粒度 subtree 保留（`mux.Handle("/api/applications/", composite)`），每个逻辑子操作用 `Operation` 登记（spec-only），spec 完整且不破坏内部派发。
- **`GET /openapi.json`**：公开契约（无鉴权），含 55 路径 / 75 操作 / 36 schema。
- **`GET /docs`**：Scalar 交互文档（公开无鉴权），拉 `/openapi.json` 渲染，支持 try-it-out（填 API Key 试请求）；CDN 加载 Scalar（Apache 2.0），离线场景 noscript 降级提示。
- 前端：`openapi-typescript`（devDep）+ `pnpm gen:api`（需先 `make run`）从 `/openapi.json` 生成 `src/api/types.gen.ts`；`fetchJSON<T>` 泛型 helper 消费生成类型。新代码用生成类型，存量 49 个手写 interface 渐进迁移（YAGNI，不一次性重写）。
- **写操作请求体全覆盖**：所有接收 JSON body 的 POST/PUT 均登记 `WithReqBody`（含 devops/configcenter 漏登记的 4 个 POST Operation）；无 body 的写操作（rollback/pay/heartbeat/publish/generate）按 REST 语义不强加 requestBody。
- **后续**：vendored 本地 Scalar JS（离线）、自动 mock。

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
- 切片**不依赖 K8s**（进程内 mock）。
- **K8s 数据面纳管（env 开关）**：`api/core/v1alpha1` Workload CRD（期望状态 spec/status）+ `internal/controller` WorkloadReconciler（watch CRD → CreateOrUpdate Deployment(service)/Job/CronJob + GPU `nvidia.com/gpu` request + `podAntiAffinity` 反亲和 + 回写 CRD status.ready）+ `workload.Applier`/`ApplyRepo` 装饰器（包装 Repository，写操作投影 CRD；devops Release 编排透明继承）+ `cmd/core` manager（`PAAS_KUBECONFIG` 非空启 controller-runtime + WorkloadReconciler，空则保持 PG/memory 现状）。控制面/数据面解耦（Deployment 归 K8s，manager 挂了不删）。fake client 测试（创建/幂等/GPU 反亲和/CronJob）。引入 controller-runtime v0.24.1 + k8s.io v0.36.0。`make manifests` 生成 CRD + deepcopy（controller-gen）。CRD 落地 namespace 由 `PAAS_K8S_NAMESPACE` 控制。
- **留后续**：envtest 集成测试（本地 etcd/apiserver binary）、GPU 自定义 gpu-memory extended resource 查询、PG Ready 实时反向同步、多租户 namespace 隔离、真实 vLLM 纳管。

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

后续切片受益：应用配置/数据服务资源的写操作自动受 `prod:write` 保护（注入 EnvTypeResolver 即可），生产操作自动有视觉警示和确认（调用 useDangerConfirm），切片只关注业务逻辑。

### DevOps CI/CD（代码->构建->镜像->发布->回滚）

补齐「代码到上线」主链路，构建产物作为不可变一等公民：

```
代码仓库(Git 绑定) -> BuildRun(mock CI runner 异步流转) -> Image(digest 不可变真源)
  -> Release(编排基线 Workload + 更新 ImageRef + 回滚指针) -> 回滚
```

- `internal/devops/`：四实体 `CodeRepo`/`BuildRun`/`Image`/`Release` + 四仓储接口（方法名带实体前缀如 `ListRepos`/`CreateRelease`，单 Store 实现避免重名冲突）+ 内存实现（seed 跨两租户）。
- CI runner（`BuildRun` 创建后 goroutine 异步流转 `pending->running->success/failed`）：构建流水线经 `builder.Pipeline` 接口，`PAAS_DEVOPS_REAL=true` 注入 `Real`（git clone tempdir → docker build → docker push → 解析 RepoDigest），空则 `Mock`（`sha256(commit+app+build)` 派生，零依赖与历史一致）。Store 的 runBuild 调 `Pipeline.Build` 拿 digest/tag/log，状态机仍由 Store 管。Real 凭证从 env 读（`PAAS_REGISTRY`/`PAAS_GIT_TOKEN`/`PAAS_REGISTRY_USER`/`PAAS_REGISTRY_PASS`），docker login 用 `--password-stdin`（不经 argv 防泄漏），clone 到 `os.MkdirTemp` 隔离 + 参数经 os/exec 防注入。
- `Workload` 加 `ImageRef`（不可变 digest，生产锁定）+ `Repository.UpdateImage`（Release 编排调用）。
- Release 编排：取镜像 -> 找/建目标环境基线 Workload（type=service）-> 更新 `ImageRef` -> 记录 `PreviousImageID`（回滚指针）；无基线自动创建。
- 回滚：Workload 回退镜像 + 原 Release 标记 `rolled-back` + 新建 `IsRollback` 发布单。
- REST：`/api/applications/{id}/{repositories|buildruns|images|releases}` + `/api/{buildruns|images}/{id}` + `/api/releases/{id}/rollback`。
- 权限：`repository/build/image/release` 读写 + `prod:write`（发布/回滚到生产）；并入 BuiltinRoles（admin/dev 读写，viewer 只读）。
- **横切继承（已验证）**：发布/回滚到 prod 受 `prod:write`（EnvTypeResolver），前端回滚走 `useDangerConfirm`（生产输入名称），生产视觉强隔离自动生效--DevOps 切片只关注业务逻辑，隔离由平台层兜底。
- console-user 应用详情四 tab：代码仓库 / 构建（状态轮询+日志展开）/ 镜像 / 发布（选镜像+环境+策略+回滚）。
- **跨应用 DevOps 中心**（`/devops`）：3 tab 总览（构建 / 镜像 / 发布），复用 `/api/buildruns`、`/api/images`、`/api/releases` 跨应用列表端点（appID="" = 租户内全部，store 已支持）；构建状态 5s 轮询；发布 tab 含回滚（走 useDangerConfirm）。消除侧栏 DevOps「即将」。
- 切片**真实构建已接（env 开关）**：`PAAS_DEVOPS_REAL=true` 接真实 git/docker/registry（`internal/devops/builder`），空则 Mock（现状不变）。Release 部署经 workload K8s 数据面落地（Deployment）。策略接口开放（rolling/blue-green/canary），实现 YAGNI（只 rolling），蓝绿/金丝雀归后续（灰度耦合泳道归服务治理）。

### 应用配置（工作负载级 env/Secret）

应用在某环境的静态键值配置，归属应用 + 环境；与运行时动态的「配置中心」（服务治理）严格区分：

```
internal/appconfig/  领域(ConfigItem env|secret + Masked 掩码) + Repository(List/Upsert/Delete) + 内存 seed
  -> handler: /api/applications/{id}/configs[/{cfgId}]?envId=
  -> 权限 config:read/write + prod:write（EnvTypeResolver，生产改需 admin）
```

- `internal/appconfig/`：`ConfigItem{TenantID/AppID/EnvID/Key/Value/Type}`；`TypeEnv`/`TypeSecret`；`SecretMask="••••••"` 固定掩码（不泄漏长度/内容）；`Masked()` secret 返回掩码副本。
- **Secret 安全**：后端明文存储，`Repository.List`/`Upsert` 返回均掩码；前端编辑 secret 不回填值（掩码非真值），需重新输入。
- `Upsert` 语义：同 `(tenant, app, env, key)` 视为同一项更新，否则新增。
- REST：`GET /api/applications/{id}/configs?envId=`（列表，掩码）、`POST`（upsert，生产需 `prod:write`）、`DELETE /api/applications/{id}/configs/{cfgId}`（生产需 `prod:write`，先查 EnvID 校验）。
- cmd/core composite 加 `case "configs"` 分发；注入 `envStore` 作 `EnvTypeResolver`。
- **横切继承（已验证）**：生产改/删配置受 `prod:write`（developer 生产只读），前端删除走 `useDangerConfirm`（生产输入 Key 确认），生产视觉强隔离自动生效——切片只关注业务逻辑。
- console-user 应用详情「配置」tab：依赖顶栏 scope（当前环境过滤），无环境时提示去顶栏选择；env/Secret 表格 + 新增/编辑（secret 密码框）+ 删除。
- 切片**不真注入工作负载**（进程内 mock）；接口为未来接 K8s ConfigMap/Secret 铺路（改后重启注入）。

### 服务治理（注册中心 + API 网关路由 + 熔断器 = 平台能力横切）

服务注册与发现 + API 网关路由 + 熔断器。治理四件套（注册发现 / 配置中心 / API 网关 / 熔断）已落地 4/4。属「平台能力（横切）」维度，租户私有，独立菜单（非应用子页）：

```
internal/governance/  领域(Service http|grpc + Instance healthy|unhealthy + LaneID 预留) + Repository(服务+实例+路由+熔断带前缀方法) + 内存 seed
  -> handler: /api/services[/...]、/api/instances/{id}/heartbeat、/api/routes、/api/breakers
  -> 权限 governance:read/write + prod:write（注册/注销到生产需 admin）
```

- `internal/governance/`：`Service{TenantID/Name(租户内唯一)/AppID/EnvID/Protocol/Port}` + `Instance{ServiceID/Addr/Status/LaneID/Meta}`；`ProtocolHTTP|GRPC`、`StatusHealthy|Unhealthy`、`LaneDefault="default"`。
- Repository 单 Store 实现服务+实例+路由+熔断接口（方法带前缀 `ListServices/CreateService/...`、`ListInstances/RegisterInstance/DeregisterInstance/Heartbeat/InstanceServiceID`、`ListRoutes/...`、`ListBreakers/...`）；全方法租户强制过滤，跨租户 not found（不泄漏）；`DeleteService` 级联清实例。
- mock：实例注册即 `healthy`，无真实健康检查；`Heartbeat` 仅更新 `UpdatedAt`（消费方据此判活，本期不过期剔除）。数据面 SDK / Sidecar / K8s endpoints 接入（参考 zeus）留后续。
- REST：`GET/POST /api/services?envId=&appId=`、`GET/DELETE /api/services/{id}`、`POST /api/services/{id}/instances`、`DELETE /api/services/{id}/instances/{iid}`、`PUT /api/instances/{iid}/heartbeat`。
- 权限：`governance:read/write`（admin/dev 读写，viewer 只读）+ `prod:write`（生产注册/注销服务/实例需 admin）。实例注销先校验实例归属该服务（防越权路径），再校验服务环境类型。
- **横切继承（已验证）**：生产注册/注销受 `prod:write`（EnvTypeResolver），前端注销走 `useDangerConfirm`（生产输入名称确认），生产视觉强隔离自动生效——切片只关注业务逻辑。
- console-user 侧栏「平台能力 → 服务治理」→ `/platform/governance`（服务列表，按顶栏 scope 过滤）+ `/platform/governance/services/:id`（实例发现 + 注册/注销/心跳）+ 服务列表页底部「API 网关路由」「熔断器」两个 section（CRUD + 启停）。
- **API 网关路由（治理四件套之 API 网关）**：`Route{Name(租户内唯一),Path,ServiceID,Methods[],StripPath,Enabled}` + Method 常量（GET/POST/PUT/DELETE/ANY）；逻辑配置不绑定物理环境，复用 `governance:read/write`，不接 prod:write。REST：`GET/POST /api/routes`、`PUT/DELETE /api/routes/{id}`。
- **熔断器（治理四件套之熔断）**：`CircuitBreaker{Name(租户内唯一),ServiceID,Strategy(error_rate|slow_call),Threshold(0-100),MinRequests,WindowSecs,Enabled}` + 即时评估 `EvaluateBreaker(b, now)`（FNV-1a+时间桶确定性生成窗口统计，三态 closed/open/half-open，无 goroutine，与 metrics/logs/traces 惰性模式同构）。State 非持久化——handler 返回前即时填充；样本不足（Requests<MinRequests）不熔断。逻辑配置复用 `governance:read/write`，不接 prod:write。REST：`GET/POST /api/breakers?serviceId=`、`PUT/DELETE /api/breakers/{id}`。真实流量采集（Sidecar/SDK 上报滑动窗口）留后续。
- 切片**不接真实数据面**（进程内 mock）；泳道路由（Instance.LaneID 预留）归后续治理子切片。

### 配置中心（治理四件套：运行时动态配置）

版本化动态配置，**与 appconfig（工作负载级、静态、重启注入）正交**：跨实例共享、版本/发布/回滚、客户端按版本发现。独立于物理环境（namespace 逻辑隔离），不接 `EnvTypeResolver`，复用 `governance:read/write` 权限：

```
internal/configcenter/  领域(Namespace + ConfigItem draft + Publish 不可变快照) + Repository(三仓储带前缀方法) + 内存 seed
  -> handler: /api/configcenter/namespaces/*、/publishes/{id}/rollback
  -> 权限 governance:read/write（发布/回滚高危走 useDangerConfirm）
```

- `internal/configcenter/`：`Namespace`（租户内唯一名）+ `ConfigItem`（namespace 下键值，draft 可编辑）+ `Publish`（namespace 配置的不可变版本快照，`Version` 单调递增，`Snapshot` map，`Status=active|rolled-back`）。
- 核心流程：**编辑 draft 不影响已发布**；**发布**=快照当前全部 item 生成新 active（旧 active 转 rolled-back）；**发现**=返回当前 active 快照+version；**回滚**=激活历史 rolled-back 为 active。
- Repository 单 Store 实现三接口（`ListNamespaces/CreateNamespace/...`、`ListItems/UpsertItem/DeleteItem`、`ListPublishes/CreatePublish/RollbackPublish/ActivePublish`）；全方法租户强制过滤；`DeleteNamespace` 级联清 item+publish。
- REST：namespace CRUD + `GET/POST /items`、`POST /publish`、`GET /publishes`、`GET /published`（客户端发现）、`POST /publishes/{pid}/rollback`。
- mock：客户端主动拉 version 比对（不做长连接监听）；灰度/审计/diff/数据面热更新留后续。
- console-user「平台能力 → 配置中心」`/platform/config-center`（命名空间列表）+ `/:nsId`（详情：当前生效配置发现视图 + draft 配置项 + 发布历史 + 发布/回滚）。
- **产品区分已落地**：appconfig（应用详情→配置 tab，静态重启注入）vs configcenter（平台能力→配置中心，动态版本热更新）是两个层面，UI 与实现完全独立。

### 可观测（指标监控 + 日志聚合 + 链路追踪 + 告警，平台能力横切）

可观测三支柱（Metrics/Logs/Traces）+ 告警，已全部落地（进程内惰性 mock）：

```
internal/observability/  领域(MetricSeries 惰性时序 + AlertRule + Alert 即时评估 + LogEntry 惰性补点 + Trace/Span 惰性生成) + Repository + 内存 seed
  -> handler: /api/observability/metrics、/alert-rules、/alerts
  -> 权限 observability:read/write（不接 prod:write，独立于物理环境）
```

- `internal/observability/`：`MetricSeries`（按 target×metric 维度，`Current` 随机游走，`Points` 环形截断 `MaxPoints=60`）+ `AlertRule`（`MetricName/TargetType/TargetID(空=全部)/Operator(> >= < <=)/Threshold/Severity`，`Matches`+`Breached` 方法）+ `Alert`（评估命中即时生成，不持久化）。
- **惰性时序（无 goroutine）**：`ListMetrics` 时若 `now-lastPoint > interval(5s)` 按当前时间补点（随机游走，有界 clamp），模拟采集；测试可控、查询时"看起来实时"。
- **告警即时评估**：`ListAlerts` 遍历 enabled 规则，对匹配 series 当前值超阈值者生成 `firing` 告警；不持久化告警历史/状态机（留后续）。
- Repository 单 Store（`ListMetrics/ListAlertRules/CreateAlertRule/DeleteAlertRule/ListAlerts`），全方法租户强制过滤。
- REST：`GET /metrics?targetType=&targetId=&name=`（惰性补点）、`GET/POST /alert-rules`、`DELETE /alert-rules/{id}`、`GET /alerts`（即时评估）、`GET /logs?appId=&level=&q=&limit=`（惰性补点）、`GET /traces?appId=&status=&limit=`（惰性补点）。
- **日志聚合（惰性补点）**：`LogEntry{AppID,Level info|warn|error,Message,TraceID,Timestamp}`；查询时按 `now-lastLog > interval(8s)` 追加 mock 日志（模板池随机 + 应用轮换归属），环形截断 `MaxLogs=200`；按 appID/level/关键字过滤，倒序返回。
- **链路追踪（惰性生成）**：`Trace{AppID,Operation,Status success|error,DurationMs,Spans[]}` + `Span{ParentID,Operation,Service,StartMs,DurationMs,Tags}`；查询时按 `now-lastTrace > interval(20s)` 生成 mock trace（入口操作 + 服务调用链模板池，~20% error），环形截断 `MaxTraces=100`；按 appID/status 过滤。前端 trace 展开行渲染 span 列表 + 时长比例条。
- 权限：`observability:read/write`（admin/dev 读写，viewer 只读）；规则写用 write，不接 prod:write。
- console-user「平台能力 → 可观测」`/platform/observability`：应用下拉 + 4 指标卡（CPU/内存/RPS/延迟，当前值 + CSS sparkline 趋势，10s 轮询）+ 告警规则列表（增删）+ 当前告警（severity 着色）。
- **接真实后端（env 开关）**：`internal/observability/real`（MetricsStore/LogsStore/TracesStore 纯 net/http 适配 Prometheus/Loki/Tempo HTTP API）+ `internal/observability/compose`（聚合 Repository）+ 细粒度 reader 接口（`MetricsReader`/`LogsReader`/`TracesReader`/`RuleStore`）。`cmd/core.buildObservabilityStore` 按三 env 开关切换：`PAAS_PROM_URL`/`PAAS_LOKI_URL`/`PAAS_TEMPO_URL` 非空接真实后端，空则该支柱保持 memory 惰性 mock（三支柱独立可混用）；alert rules 始终 memory，`ListAlerts` 基于 metrics reader 即时评估（real 模式取真实 Prometheus 当前值）。后端不可达降级返空 + 日志（不 5xx/panic）。未配 URL 行为与现状完全一致。metric/label 命名约定（`paas_cpu_usage` 等）查询端落地。
- **应用侧 OTel 埋点（P2-3，采集端闭环）**：`internal/observability/tracing` 初始化全局 TracerProvider（OTLP/HTTP exporter，`PAAS_OTEL_ENDPOINT` env 开关，空=noop 行为不变），mux 经 `otelhttp` 中间件包装自动建 span（探针/契约/文档端点过滤避免噪音），W3C traceparent 透传播。控制面自身链路可被 `/api/observability/traces`（接 Tempo）观测，端到端可观测闭环（采集→存储→展示）。引入 go.opentelemetry.io/otel + sdk + otlptracehttp + contrib/otelhttp（Apache 2.0）。
- **留后续**：告警通知通道（webhook/Slack）/状态机/大盘、Tempo span 详情（OTLP 解析）、Collector 采集管道编排、长期存储降采样。

### 安全（密钥/证书 + 审计日志，平台能力横切）

租户级密钥/证书资产（KMS 抽象）+ 审计日志。与 appconfig（应用×环境级、工作负载启动注入）区分：本模块是**租户级平台资产**（DB 密码/TLS 证书/第三方 token），集中管理供应用引用，不绑定具体应用：

```
internal/security/  领域(Secret secret|certificate + AuditLog) + Repository(SecretStore + AuditStore) + 内存 seed
  -> handler: /api/security/secrets[/{id}]、/api/security/audit-logs
  -> 权限 security:read/write（写操作自动记审计）
```

- `internal/security/`：`Secret{Name(租户内唯一)/Type(secret|certificate)/Value}` + `AuditLog{Actor/Action/ResourceType/ResourceID/Detail/At}`；`SecretMask="••••••"` + `Masked()`。
- **Secret 安全**：后端明文存储，`List/Get/Create` 返回均掩码（与 appconfig 一致，不泄漏长度/内容）。真实加密存储（KMS/Vault）留后续。
- **审计自动记录**：handler 层在 Create/Delete Secret 成功后自动 `RecordAudit`，`Actor` 由 `UserIDFrom`（复用 `gateway.UserIDFrom`）从身份 ctx 取。审计只增不删（合规）。
- Repository 单 Store（`ListSecrets/GetSecret/CreateSecret/DeleteSecret` + `ListAuditLogs/RecordAudit`），全方法租户强制过滤；审计按时间倒序，支持 resourceType/action 过滤。
- REST：`GET/POST /secrets`、`DELETE /secrets/{id}`（记审计）、`GET /audit-logs?resourceType=&action=`。
- 权限：`security:read/write`（admin/dev 读写，viewer 只读）；不接 prod:write（租户级资产，不按物理环境隔离）。
- console-user「平台能力 → 安全」`/platform/security`：密钥/证书表（掩码）+ 审计日志表（actor/动作/资源/详情，动作过滤）+ 创建弹窗（删除走 `useDangerConfirm` 输入名称确认）。
- 切片**不做**：IAM 细粒度策略（已有 RBAC）、网络防火墙（依赖数据面）、密钥轮转/过期、证书签发（ACME）、真实加密存储——均留后续。






### 配额计费（租户级配额 + 用量 + 账单，多租户商业化根基）

租户级资源配额 + 用量统计 + 计费账单闭环，属「设置」维度，租户私有；独立于物理环境，不接 `prod:write`（与可观测/安全一致）：

```
internal/billing/  领域(ResourceQuota + ResourceUsage + BillingRecord + 单价表 PriceTable) + Repository(三仓储带前缀方法) + 内存 seed
  -> handler: /api/billing/{quota,usage,records}、records/generate、records/{id}/pay
  -> 权限 billing:read/write（admin/dev 写，viewer 只读）
```

- `internal/billing/`：`ResourceQuota{Limits map,-1=无限}` + `ResourceUsage{Counts map}` + `BillingRecord{Period(YYYY-MM),Items,Total,Status unpaid|paid}` + `BillItem`；`PriceTable` 平台级 mock 单价（导出，前端对齐）；6 资源维度 `applications/workloads/models/gpu/tokens/storage_gb`；`BuildUsageView` 组装配额+用量+逐项超限标记。
- **核心流程**：`GetQuota`（不存在返回默认配额，非错误）/ `SetQuota`（覆盖）；`GetUsage` + `BuildUsageView` 返回超限标记；`GenerateBill(period)` 按当前用量 × 单价逐项算 amount 求和，**同 period 已有 unpaid 则覆盖**（避免账单堆积）；`PayBill` 状态机 `unpaid -> paid`，重复支付拒绝。
- Repository 单 Store（`GetQuota/SetQuota` + `GetUsage/IncUsage` + `ListBills/GenerateBill/GetBill/PayBill`），全方法租户强制过滤，跨租户 not found（不泄漏）。
- REST：`GET/PUT /api/billing/quota`、`GET /api/billing/usage`（UsageView）、`GET /api/billing/records`、`POST /api/billing/records/generate?period=`（空则当前月）、`POST /api/billing/records/{id}/pay`。
- 权限：`billing:read/write`（admin/dev 读写，viewer 只读）；**不接 prod:write**（独立于物理环境）。`ValidatePeriod` 校验 YYYY-MM。
- console-user「设置 → 配额与账单」`/settings/billing`：6 资源配额用量卡（`el-progress` 进度条，超限红色告警 + 超额标签）+ 调整配额弹窗（`el-input-number`，-1=无限）+ 生成本期账单（period 输入）+ 账单列表（展开行明细：资源/数量/单价/金额 + 支付按钮）。
- **配额强制拦截（横切，已落地）**：`billing.CheckAndInc(ctx, resource, delta)` 原子「检查超限 + 递增」（同锁原子；limit>0 且 usage+delta>limit 返 `ErrQuotaExceeded` 不递增；Unlimited/未设不拦截）。`application`/`workload` handler 注入 `QuotaCheck` 字段（cmd/core 桥接 `billing.ResApplications`/`ResWorkloads`），Create 前拦截超限回 429 不创建，repo.Create 失败回滚（delta=-1）。前端 `fetchAuth` 全局拦截 429 → 引导去「配额与账单」。后续资源（数据服务等）Create 复用同一原语即可接入。
- **本期不做（YAGNI）**：真实计量采集（从 workload/应用/token 派生，本期 seed）/ 计费引擎（阶梯/套餐/优惠券/税）/对接支付网关 / 账单导出（PDF/发票）——均留后续。

### 数据服务资源（资源中心：DB/缓存/MQ/存储/向量/搜索）

用一个通用领域 + `Kind` 区分覆盖 6 种同构数据服务（DRY，一套代码），消除资源中心全部「即将」标记。属「资源中心」维度（可绑定 Add-on），租户私有：

```
internal/dataservice/  领域(DataService + 6 Kind 常量 + KindMeta 表单元数据) + Repository + 内存 seed
  -> handler: /api/dataservices[?kind=]、/meta、/{id}
  -> 权限 dataservice:read/write + prod:write（EnvTypeResolver，生产创建/删除需 admin）
```

- `internal/dataservice/`：`DataService{Kind,Name(租户内唯一),Spec map,Status,EnvID,AppID(预留)}`；6 Kind `db/cache/mq/storage/vector/search`；`Status creating|running|stopped`；`KindMeta{Label,Icon,Fields[]SpecField}` + `KindMetas` 平台级权威表单元数据（导出，前端 `/api/dataservices/meta` 拉取对齐）；`SpecField{Key,Label,Type text|select,Options,Default}`。
- **Create 即 running**（KISS，无 goroutine 异步流转，测试可控）；`Validate` 校验 Kind/Name + 必填 spec（Default 为空的 text 字段=必填，如 storage.bucket）；`Update` 仅改 spec/status；租户内 Name 唯一。
- Repository 单 Store（`List(kind)/Get/Create/Update/Delete`），全方法租户强制过滤，跨租户 not found（不泄漏）。
- REST：`GET /api/dataservices?kind=`、`GET /api/dataservices/meta`、`POST`、`GET/PUT/DELETE /api/dataservices/{id}`。
- 权限：`dataservice:read/write`（admin/dev 读写，viewer 只读）+ **`prod:write`**（EnvTypeResolver 依赖倒置，生产 Create/Update/Delete 需 admin，developer 生产只读）。
- **横切继承（已验证）**：生产写受 `prod:write`，前端删除走 `useDangerConfirm`（生产输入名称确认），生产视觉强隔离自动生效——切片只关注业务逻辑。
- console-user `DataServices.vue` **6 路由共用**（`/resources/{db,cache,mq,storage,vector,search}`，route `props.kind` 区分）：按 KindMeta 动态渲染规格列 + 创建弹窗表单（select/text）+ 启停/删除；侧栏资源中心去掉全部 6 个「即将」标记。
- **K8s 数据面纳管（P1-3，env 开关，复刻 Workload 范式）**：`api/core/v1alpha1` DataService CRD（spec: kind/engine/spec map；status: ready/phase/image）+ `internal/controller` DataServiceReconciler（watch CRD → 按 Kind+Engine 选容器镜像 → CreateOrUpdate **StatefulSet**（数据服务有状态，稳定网络标识）+ 回写 status.phase running|creating|failed）+ `DataServiceK8sApplier` 实现 `dataservice.Applier` + `ApplyRepo` 装饰 PG/memory repo。`startManager` 启 Workload+DataService 双 reconciler，`PAAS_KUBECONFIG` 非空生效，空则纯 PG/memory（dev 路径，现状不变）。Kind→镜像占位表（`engineImage`）：db→postgres/mysql、cache→redis/valkey、mq→kafka/rabbitmq/rocketmq、storage→minio、vector→milvus/qdrant、search→elasticsearch/opensearch；未知组合记 failed 不拉起（安全默认）。`make manifests` 生成 DataService CRD + deepcopy（`+kubebuilder:object:generate=true` 强制为含 map 的 DataServiceSpec 生成 DeepCopyInto）。控制面/数据面解耦（StatefulSet 归 K8s，manager 挂了不删）。fake client 测试（创建/幂等/未知 engine/镜像覆盖）。
- 切片**起步单副本 StatefulSet 占位**（KISS）；应用 Add-on 绑定/解绑（复用 application binding）、真实 Operator 高级能力（HA/集群/备份/扩缩容/DB 只读副本/MQ 消费组）、异步创建流转（creating→running 的 PG 反向同步）、PVC 持久卷声明、spec 字段透传为容器 env——均留后续（接口已铺路）。

## 前端架构

三套独立 SPA，共享设计系统（Element Plus + 暗黑模式）：

| 应用 | 定位 | 端口 |
|------|------|------|
| console-admin | 平台运维/管理员（基于 `vue-admin` 基座，已对接 core 密码登录+JWT，RBAC/动态路由就绪） | 5173 |
| console-user | 租户开发者（应用为主线 + 三层信息架构，见下） | 5174 |
| landing | 访客官网展示页（静态、SEO 友好） | 5175 |

console-user 导航采用**三层信息架构**（避免「资源」概念被滥用）：

- **资源中心** = 数据服务（可绑定 Add-on）：模型推理 / 数据库 / 缓存 / 消息队列 / 对象存储 / 向量数据库 / 搜索引擎
- **工作负载** = 应用运行形态：服务（Deployment）/ 任务（Job）/ 定时（CronJob）
- **平台能力** = 横切基础设施：服务治理（含注册发现/配置中心/API网关/熔断）/ 可观测 / 安全
- 另有：应用（主线）/ DevOps / Playground

关键区分：**配置中心属服务治理**（运行时动态、跨实例、版本灰度），与**应用详情的「应用配置」**（env/Secret、工作负载级、静态）是两个层面，勿混。

**环境信息架构（主辅结合）**：环境扮演两个角色，UI 厘清边界，避免三入口混乱--

- **顶栏 scope（操作面）**：唯一环境切换入口（生产 gated + 视觉强隔离），统管**运行态**（工作负载/发布/部署操作）；**逻辑态**（应用列表/模型目录）不按 scope 过滤，但应用卡片显示「scope 环境部署徽标」（scope 具体环境显示副本就绪，全部显示部署环境数）。
- **环境菜单（管理面）**：环境实体 CRUD + 跨环境总览（统计卡：工作负载数/健康度/物理落点），点环境进**环境详情页** `/environments/:id`（工作负载总览按类型 + 应用部署矩阵），**不跳工作负载列表**。「在此环境工作」按钮桥接到操作面（switchEnv + 跳工作负载）。
- 一句话区分：顶栏 scope =「我在哪个环境干活」；环境菜单 =「我管理环境本身」。工作负载页无环境切换控件（去重，环境切换唯一走顶栏）。设计见 `docs/superpowers/specs/2026-07-28-environment-ia-redesign.md`。

- API 契约：后端 OpenAPI 自动生成前端 TS 类型（Plan 4 起接入 Gateway）。
- console-admin 的基座源码自带其 `CLAUDE.md` 与 `docs/standards/`（四层架构 lib/app/modules/shared），改它时遵循其自身规范。

## 平台模块全景

见 [平台模块蓝图](./docs/superpowers/specs/2026-07-27-platform-modules-blueprint.md)。当前完成度约 96%（Core 骨架 + 多租户身份 + MaaS + 应用主线 + 工作负载 + 环境 + 生产安全防护 + DevOps CI/CD + 应用配置 + 服务治理注册中心 + 配置中心 + 可观测指标告警 + 安全密钥审计 + 配额计费 + 数据服务资源 + 配额强制拦截 + PostgreSQL 持久化全 10 模块迁移（observability 除外）+ OpenAPI 契约单一真源 + 前端 TS 生成），其余模块按蓝图优先级推进。

## 开发约定

- 新建模块或引入新技术栈时，同步更新本文件对应章节。
- 注释语言与代码库现有注释保持一致。
- **未经用户明确要求，不要执行 git commit / 分支操作。**
- 所有依赖须与 Apache 2.0 兼容；新增依赖前确认 license。
- 业务领域逻辑绝不进 Platform Core；判断标准："MaaS / 治理 / DevOps 都会用吗？"
- 多租户隔离由 Core 统一治理（DB 访问层强制 tenant 过滤），插件不得绕过。
