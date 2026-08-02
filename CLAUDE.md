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

### 集群部署（Helm + 前端嵌入单镜像）

一套 Helm chart（`deploy/charts/paas`）一把梭部署到 K8s：core 单镜像同源 serve 前端 + API（无 CORS），参考 aiem 模式（`hub.wang.dd:5000` 私有 registry + `hermes` ingress）。

- **前端嵌入 core**：三套 SPA（console-user/console-admin/landing）构建产物 `//go:embed` 进 core 二进制（`internal/web/`），core 同域 serve。同域路由（ServeMux 最长前缀匹配）：`/api/*` `/v1/*` `/openapi.json` `/docs` `/livez` → API（精确匹配优先）；`/console/*` → console-user；`/admin/*` → console-admin；`/*` → landing（兜底）。前端 base path 子路径化：console-user `base:'/console/'`、console-admin `VITE_BASE='/admin/'`、landing 默认 `/`。
- **Dockerfile 多阶段**：node:22-alpine 构建三套前端（console-admin 需 Node ≥ 22.13）→ golang:1.26-alpine Go 交叉编译（`ARG GOARCH=amd64`，builder 跑本地架构交叉编译到 amd64 避 QEMU）→ distroless runtime。国内源默认（`ARG NPM_REGISTRY=https://registry.npmmirror.com`、`ARG GOPROXY=https://goproxy.cn,direct`），海外 `--build-arg` 覆盖。无 `# syntax=` 指令（避免 buildkit 拉远程 frontend）。
- **Helm chart 组件**：`templates/crds.yaml`（Workload+DataService CRD，`crds.install` 门控）+ `templates/rbac.yaml`（ServiceAccount + ClusterRole[core.aitoys CRD + apps/batch workload + pods/services/events] + ClusterRoleBinding）+ core-deployment（`serviceAccountName` + `PAAS_K8S_NAMESPACE` + `PAAS_METRICS_ADDR=0`）+ ingress（`hermes` class，host 配）+ 内置 postgres（`db.enabled` 开关）。
- **in-cluster 数据面**：core 容器内 `startManager` 用 `ctrl.GetConfig()` 自动检测（PAAS_KUBECONFIG 显式 或 in-cluster SA token + KUBERNETES_SERVICE_HOST），集群内部署无需 PAAS_KUBECONFIG 文件，SA token 自动挂载 + RBAC 授权。**关键修复**：原以 `PAAS_KUBECONFIG` 非空作启 manager 的唯一门控，导致集群内部署（SA token 可用但 env 空）数据面不生效——改用 `ctrl.GetConfig()` 容错（无 config 才降级）。此 bug 单元测试测不出（无 in-cluster 环境），真实集群部署才暴露。
- **部署脚本**：`scripts/deploy-k8s.sh`（docker build → push → helm upgrade）。docker push 在某些环境（如 colima VM 到 registry 路由不通）超时时，自动 fallback `crane push --insecure`（不经 dockerd，直 HTTP 推）。集群覆盖文件 `deploy/charts/paas/values-paas-k8s.yaml`（registry=hub.wang.dd:5000/paas、ingress=hermes/paas.k8s.dd、本集群无 StorageClass 时 `db.enabled=false` 内存模式）。

```bash
./scripts/deploy-k8s.sh                          # 构建前端 embed 镜像 + push + helm install（paas.k8s.dd）
# 部署后访问：http://paas.k8s.dd/（landing）/console/（用户控制台）/admin/（后台）+ /v1/models（API）
# 验证数据面：curl POST /api/applications/app-cs/workloads → kubectl get deploy 自动出现
```

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
- **airouter 真实推理入口（统一真实化）**：catalog 追加 12 个 airouter 网关模型（通义/DeepSeek/智谱/月之暗面/豆包/万相，覆盖文本/推理/视觉/图片/向量，`internal/maas/airouter_catalog.go`，复用 `OpenAICompatibleProvider` Bearer），`baseAirouter=https://airouter.ddmc-inc.com/api/v1` + `credAirouter=sec-platform-airouter`。airouter 内部已聚合百炼/千帆/豆包多供应商容灾，平台配一个 api_key 即全模型真实可用（白嫖其容灾链路，无需各自申请百炼/千帆/豆包 Key）。api_key **不入库**：经 env `PAAS_AIROUTER_API_KEY`（helm values `maas.airouterApiKey`，部署 `--set`）注入，启动 seed 到平台级 Secret。security pg seed 改为「表非空也 ensure 平台级凭证」（`ensurePlatformSecrets`，`ON CONFLICT DO NOTHING` 不覆盖用户已填值），重部署到已有 PG 自动补齐 airouter 凭证。

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

### P1.1 应用去假 + 租户开通（生产可用起步）

承接 P0 登录会话，解决「看起来假」的核心：应用 seed 演示数据 + 租户无法开通 + 模型硬编码（模型管理归 P1.2）。P1.1 聚焦应用去假 + 租户开通。设计见 `docs/superpowers/specs/2026-08-02-p1-real-platform.md`。

- **应用 seed demo 门控**（零假数据）：`PAAS_DISABLE_DEMO_SEED=true`（chart `seed.demo=false`）时内存 `application/memory.NewStore()` + PG `seedApplications()` 两路径均跳过 `SeedApps()` 灌入，生产应用列表为空由用户自建；dev 默认灌示例应用。与演示凭证门控同源（`seedIdentity`），避免两套门控。`SeedApps()` 函数保留作 dev seed 真源。
- **租户开通**（admin 后台）：identity 后端 CRUD 已齐（`/api/tenants|users|api-keys`，`adminGuard` super_admin），补前端：
  - **菜单**：`auth/menus.go` `staticMenus()` system 下加「租户管理」（`/system/tenant`），前端 `lib/router/dynamic.ts` `import.meta.glob('@/modules/**/*.vue')` 自动识别组件。
  - **system/tenant 模块**（console-admin）：`api.ts`（对接 `/api/tenants`，假分页适配 useCrud + `fetchAllTenants` 供用户表单选租户）+ `List.vue`（SearchTable + 删除走 `ElMessageBox.prompt` 输入租户 ID 确认）+ `TenantFormDrawer.vue`（id+name，core 无租户更新端点仅新建）。
  - **system/user 选租户**：`api.ts` 去硬编码 `tenantId:'t-acme'`，`UserCreateRequest` 加 `tenantId`，`UserFormDrawer.vue` 加「所属租户」下拉（edit/view 禁用，租户不可改）；普通 tenant-admin 后端强制本租户兜底（`CreateUser` 非 super_admin 强制 `in.TenantID = callerTenant`）。
- **DeleteTenant 非空保护**（防孤儿 + 防误删）：`identity.ErrTenantNotEmpty` sentinel；memory/pg `DeleteTenant` 前置检查有用户返 409（引导先清用户），handler 映射 `409 租户下仍有用户`。memory 保留级联清 API Key（防御孤儿），移除静默级联清用户（前置检查保证无用户）。跨 store 业务数据（应用/工作负载/数据服务）级联清留后（删租户低频高危）。
- **console-user 应用创建**（去假后必需）：`Applications.vue` 「新建应用」从 `notImplemented` 占位改接真实 `POST /api/applications`（name+env+desc，ID 后端兜底生成 + `ApplyDefaults` 补展示）；el-dialog 创建弹窗 + 空状态引导（`filteredApps` 空时 🚀 + 「新建应用」CTA，非空白页）。
- **开通流程**：admin 登录 console-admin -> 租户管理建租户 -> 用户管理建该租户管理员（选租户）-> 用户登录 console-user 自建应用/生成 API Key。
- **留后续**：跨 store 级联删租户业务数据；租户禁用/冻结（软删）；租户自助公开注册。（模型管理见 P1.2）

### P1.2 模型管理（Model/Channel/Credential CRUD + DB 驱动 catalog）

承接 P1.1，解决模型硬编码（`catalog()` 纯函数返固定列表，无法运行时增改）。模型目录改 DB 驱动：admin 后台 CRUD 模型/通道，写后增量刷新 gateway 路由表。设计见 `docs/superpowers/specs/2026-08-02-p1-real-platform.md`（P1.2 概要），计划 `docs/superpowers/plans/2026-08-02-p1.2-model-management.md`。

- **Model/Channel Repository**（平台级，无 tenant）：`internal/maas/store.go` Repository 接口（Model CRUD + Channel 子资源 CRUD，带 ctx 传播请求取消）+ memory 实现（读返深拷贝隔离）+ `BuildProvider(ch, resolver)` 工厂（type→Provider：echo/mock 进程内、openai-compatible 用 Endpoint/UpstreamModel/CredentialRef+resolver）。`Channel` 加 `Vendor` 字段（展示用）。sentinel 错误（`ErrModelNotFound/Exists`、`ErrChannelNotFound/Exists`）映射 HTTP 状态。
- **PG 持久化**：migration 追加 `maas_models`（capabilities JSONB）+ `maas_channels`（model_id FK CASCADE）到 `0001_init.up.sql`（项目未上线，合并 schema）；`internal/maas/pg/store.go` 实现 Repository（`ListModels` 两次查询 models+channels 内存聚合避免 N+1；channel 写操作前置 `modelExists` 返友好 `ErrModelNotFound`；`ModelsCount` 供 seed 判空）。integration 测试 `//go:build integration`。
- **gateway 增量刷新**：`Gateway.UnregisterModel(id)`（接口 `GatewayRegistrar` 加该方法）；handler CRUD 后 `reloadModel`（GetModel→BuildProvider+SetImpl 重建通道→RegisterModel 同 ID 覆盖）/ `UnregisterModel`，不全量重载。
- **catalog seed 门控**：`SeedCatalog(ctx, repo, resolver)` 幂等灌入（exists 跳过）；`PAAS_DISABLE_DEMO_SEED != true`（dev）时 cmd/core 两路径（内存/PG）灌 catalog 作 demo，生产空目录由 admin 手动配。`MaaSPlugin.Init` 从 Repository 加载模型 + BuildProvider 重建 impl + RegisterModel（repo nil 时 fallback catalog 兼容旧用法/测试）。`MaaSPlugin` 改 `NewMaaSPlugin(repo)` 构造注入，cmd/core 装配时 store 已 seed。
- **REST API**（`internal/maas/handler.go`，composite 按路径分发）：`/api/admin/models`（GET 列表/POST 创建）+ `/{id}`（GET/PUT/DELETE，删除级联清通道+UnregisterModel）+ `/{id}/channels`（GET/POST）+ `/{id}/channels/{cid}`（PUT/DELETE）。每写操作后 reloadModel 刷新 gateway。`adminGuard` super_admin 兜底（平台级，handler 内不判权限）。OpenAPI Operation 登记 9 操作（Perm `super_admin`）。
- **console-admin 模型管理**（super_admin）：菜单「模型管理」（`/model`，Cpu 图标）+ 隐藏详情路由 `/model/:id`（`ShowMenu:false`，列表「通道」按钮跳入，dynamic.ts import.meta.glob 自动识别）。`modules/model/`：`api.ts`（Model/Channel 类型 + CRUD + CHANNEL_TYPES/STATUS 常量 + `fetchPlatformSecrets` 凭证下拉）+ `List.vue`（SearchTable+useCrud 假分页，模型列表/创建/删除输入 ID 确认/跳通道）+ `ModelFormDrawer.vue`（id/name/vendor/contextWindow/capabilities 逗号分隔/input-outputPrice）+ `Detail.vue`（通道表格+状态 tag+CRUD）+ `ChannelFormDrawer.vue`（type select；openai-compatible 时显 endpoint/vendor/upstreamModel/credentialRef 选平台级 Secret/priority/status，用 FormDrawer `dependencies` 声明式显隐）。
- **响应解包契约**：console-admin http interceptor 按是否有 `data` 字段解包——list 端点 `{data:[...]}`→数组，item 端点对象直接返回（model/channel 无顶层 data 字段）。maas handler 与之匹配。
- **留后续**：模型启用/禁用（软删）、通道健康检查（真实探活替代手动 status）、凭证测试（建通道时 ping 上游）、模型分组/标签、用量按模型维度计量。

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
- **K8s 数据面纳管（env 开关，端到端已验证）**：`api/core/v1alpha1` Workload CRD（期望状态 spec/status）+ `internal/controller` WorkloadReconciler（watch CRD → CreateOrUpdate Deployment(service)/Job/CronJob + GPU `nvidia.com/gpu` request + `podAntiAffinity` 反亲和 + 回写 CRD status.ready）+ `workload.Applier`/`ApplyRepo` 装饰器（包装 Repository，写操作投影 CRD；devops Release 编排透明继承）+ `cmd/core` manager（`PAAS_KUBECONFIG` 非空启 controller-runtime + WorkloadReconciler，空则保持 PG/memory 现状）。控制面/数据面解耦（Deployment 归 K8s，manager 挂了不删）。fake client 测试（创建/幂等/GPU 反亲和/CronJob）。引入 controller-runtime v0.24.1 + k8s.io v0.36.0。`make manifests` 生成 CRD + deepcopy（controller-gen）。CRD 落地 namespace 由 `PAAS_K8S_NAMESPACE` 控制。**已真实集群端到端验证**：API 创建 Workload → CRD 投影 → Deployment 落地 → Pod Running → status 回写（ready/running）； DataService → StatefulSet 落地 + status.phase 回写。**关键修复**：`groupversion_info.go` 必须用 controller-runtime `scheme.Builder`（非裸 `runtime.NewSchemeBuilder`），否则真实集群 list/watch 报 ListOptions parameter codec 错（fake client 测不出）。manager metrics 默认 :8080 与 core HTTP 冲突，改 `PAAS_METRICS_ADDR`（默认 :8081，0=禁用）+ 注入 zap logger。
- **留后续**：envtest 集成测试（本地 etcd/apiserver binary）、GPU 自定义 gpu-memory extended resource 查询、PG Ready 实时反向同步、多租户 namespace 隔离、真实 vLLM 纳管。
- **P0 工作负载建 K8s Service（多微服务 DNS 互调前提，数据面发现前置）**：CRD `WorkloadSpec` 加 `Port`/`ContainerPort`（PG migration 0015）；`applyService` 在 type=service 且 Port>0 时 `CreateOrUpdate` `corev1.Service`（名同 Workload，selector 匹配 `paas.aitoys/workload`，端口 Port→ContainerPort，OwnerRef 设 CR 级联清）+ `SetupWithManager` `Owns(&corev1.Service{})`；`podSpec` 在 ContainerPort>0 时加 containerPort + **TCP readiness probe**（对应用零侵入，listen 即 ready），驱动 K8s Endpoints ready 集合（= 数据面 `/dp/instances` 真源）。Port=0 不建 Service（向后兼容）；Job/CronJob 不建。fake client 测（Service 创建/selector/port/OwnerRef + readiness + Port=0 跳过 + job 不建）。
- **数据面 SDK 纳管（zeus，/dp/ + paas-registry 插件，P1）**：服务治理 Instance 真源从「governance 手动注册 mock」切到 **K8s Endpoints**（readiness probe 驱动）。`internal/dataplane` 暴露 `/dp/` HTTP API（`GET /dp/services`、`GET /dp/instances?service=<name>` 从 K8s Endpoints 读 ready 实例 + signature、`POST/DELETE /dp/register` 声明服务元信息、`PUT /dp/heartbeat` 兼容保留），鉴权复用 `gateway.BearerAuth`（dp token = API Key，绑 tenant，`Authorization: Bearer`）。`sdk/paas-registry/`（**独立 module** `github.com/aitoys/paas/sdk/paas-registry`，不进主 go.mod 避免主仓引 zeus）实现 zeus `Registrar+Discovery+Watcher`，`init()` 注册 `paas://` scheme（URL `paas://paas-core.paas.svc/dp?token=<dp-token>`），GetService 2s 轮询 + signature 对比触发重发现（仿 zeus `examples/20-full-demo/gwdisc`）。controller 给 service 类型 Pod 注入 `PAAS_DP_ENDPOINT`/`PAAS_DP_TOKEN`/`PAAS_TENANT_ID` env（`PAAS_DP_TOKEN` helm values `dataplane.token`，cmd/core manager.go 读 env 注入 reconciler）。RBAC 补 `endpoints get/list/watch`。非集群部署（无 clientset）`/dp/instances` 降级返空（不 panic，与 observability real 同构）。**zeus 本地 private module**：sdk go.mod `replace github.com/go-zeus/zeus => /Users/wangtao/.../go-zeus/zeus`，**开源发布前需 zeus 发 public module 或 vendor 进仓**。熔断真实 stats 采集、配置 watch 长连接、应用级 metrics/traces 埋点、多租户 ns 隔离、per-workload token（现平台级 sk-acme-admin 演示）留后续。
- **多租户隔离加固（深度检测修复）**：① observability real 后端（Prom/Loki/Tempo）查询加 `paas_aitoys_tenant=<tid>` label（logs LogQL / metrics PromQL / traces Tempo tags），tid 从 ctx 取--防跨租户枚举 appID 读他人日志/指标/trace；② dataplane `/dp/` reader 按 `paas.aitoys/tenant` label 过滤 Endpoints/Service（先 Get Service 校验归属再读 Endpoints，跨租户/无 ctx fail-closed 返空不泄漏）；③ billing memory `ListBills`/`GetBill` 深拷贝 Items 切片（防 race，与 cloneIntMap 同款）；④ devops `pipeline` 字段读写加锁（memory 用 s.mu、pg 加专属 pipeMu sync.RWMutex），防 SetPipeline/runBuild race。
- **进程优雅退出（深度检测修复）**：① HTTP `srv.Shutdown` 30s grace（in-flight 请求/SSE 流式不被强断，run() 的 <-ctx.Done 后调）；② devops store 加 `baseCtx`（cmd/core run() 注入），构建 goroutine 派生之，进程退出 cancel 构建（K8sJob 子 ctx 随之 cancel），避免 SIGTERM 后 build_runs 卡 running；③ K8sJob Build 失败/超时/无 digest 时 `deleteJob` 清理（defer + 命名返回，PropagationPolicy=Background 级联清 Pod），不等 TTL=86400 兜底，减少 GPU/调度占用窗口。

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

### console-user 生产级登录会话（httpOnly cookie）

console-user 弃用 localStorage 明文 API Key 裸奔模式，改走密码登录 + httpOnly cookie 会话（L1+L2 生产级）。API Key 体系保留供程序化调用（`/dp/`、SDK、curl），浏览器登录态走 cookie。

- **httpOnly cookie 三通道**：`gateway.BearerAuth` 升级为 ① cookie access ② `Authorization: Bearer <JWT>` ③ `Authorization: Bearer <APIKey>` 三通道（cookie 优先）。login/refresh 成功 `Set-Cookie: paas_access`（Path=/，15min）+ `paas_refresh`（Path=/api/auth，7d，收窄暴露面），HttpOnly+SameSite=Lax，Secure 由 `PAAS_COOKIE_SECURE` 控制（HTTP false / TLS true）。core 同域 serve 前端，cookie 同源无 CORS。
- **JWT secret 生产强制**：`PAAS_JWT_SECRET` 空 + `PAAS_PROD=true` 拒启；dev 未设随机生成（本地零配置）。`PAAS_PROD` 正向标识生产。
- **登录限流**：`internal/core/auth/loginLimiter` per-IP + per-username 内存令牌桶，失败 5 次/5min 锁 15min；`clientIP` 取 X-Forwarded-For 首段。单实例够用，多副本上 Redis 延后。
- **登录审计**：`auth.AuditRecorder` 接口（依赖倒置，基本类型参数避免 auth->security 循环）+ `cmd/core.authAuditAdapter` 桥接 `security.AuditStore`。login/login_failed/logout 记审计；adapter 注入 ctx tenant（login_failed 租户未知归 "platform"，因 audit_logs.tenant_id NOT NULL）。
- **强密码策略**：`auth.ValidatePassword`（≥8 + 字母 + 数字）+ `identity.PasswordValidatorFn` 依赖倒置注入；admin CreateUser/UpdateUser 强制，seed demo 账号豁免（demo 门控）。
- **安全 headers 中间件**：CSP / HSTS（仅 HTTPS）/ X-Frame-Options:DENY / X-Content-Type-Options:nosniff / Referrer-Policy，挂 mux 最外层。
- **前端**：`api.ts` `credentials:'include'` 不碰 token，401 自动 refresh+重试一次，refresh 失败触发 `paas:session-expired`；`stores/session.ts` 缓存 profile；`Login.vue` + `router.beforeEach` 守卫（ping `/api/auth/users/me` 判登录态）+ 顶栏演示账号快切（预设账号一键登录，生产关 demo 后失效）。
- **配置**（helm `auth` 段）：`jwtSecret`（生产必填，`openssl rand -hex 32`）/ `cookieSecure`（HTTP false）/ `prod`（生产 true）。生产部署 `auth.prod=true` + `auth.jwtSecret=<随机>` + `seed.demo=false`。
- **留后续（L3）**：refresh token rotation、MFA、OIDC/SSO、主动 session 撤销黑名单、密码重置、多副本 Redis 限流共享、ingress TLS（本期用户确认先 HTTP，cookieSecure=false，配证书后切 true + HSTS 即闭环）。

### 生产安全防护（横切机制）

生产/测试隔离是**平台级横切关注点**，统一在此解决，后续切片（DevOps/应用配置）自动继承：

- **环境类型感知 RBAC**（最硬防线）：生产写操作需 `prod:write` 权限。`developer` 角色无 `prod:write` -> **生产只读**；`tenant-admin` 有。`identity.PermProdWrite` + `gateway.RequestAllowedProd`。
- **workload 环境类型感知校验**：`workload.EnvTypeResolver` 接口（依赖倒置，由 environment.Repository 实现 `EnvType`），写操作（Create/Update/Delete）查目标环境类型，prod 则校验 `prod:write`。environment handler 的 Create/Delete 同理（body.type / EnvType）。
- **全局环境上下文**（前端）：`stores/env.ts` pinia store，环境从「页面过滤」升为「全局上下文」（顶栏常驻，贯穿所有页面）。
- **生产 gated 模式**：切到生产需二次确认 + **15 分钟超时自动回退**测试环境。
- **生产视觉强隔离**：`app.env-prod` class 驱动整页红边框 + 顶栏红条 + 警示横幅「⚠️ 生产环境」+ 倒计时。
- **统一危险操作确认**：`composables/useDangerConfirm.ts`，生产高危操作（删除）要求**输入名称确认**，测试普通确认。`confirmDangerous` 加 `isProd` 显式入参（调用方按资源所在环境传入，覆盖顶栏 scope，防顶栏与资源环境不一致时防护削弱）。

后续切片受益：应用配置/数据服务资源的写操作自动受 `prod:write` 保护（注入 EnvTypeResolver 即可），生产操作自动有视觉警示和确认（调用 useDangerConfirm），切片只关注业务逻辑。

### 深度检测加固（横切安全 + 稳定性）

多轮深度代码检测修复，平台级横切，后续切片自动继承：

- **演示凭证门控**（防生产后门）：`PAAS_DISABLE_DEMO_SEED=true`（chart `seed.demo=false`）关闭 `admin/123456` + `sk-acme-admin/sk-globex-admin/sk-acme-dev` 演示凭证 seed；生产部署必须设 false。租户结构 + `PAAS_API_KEY`（运维配置）不受影响。两路径（内存 `seedIdentity` + PG `seedPGAllIfEmpty`）同源门控。
- **平台级管理越权防护**：`adminGuard`（保护 `/api/tenants|users|api-keys|dashboard/*`）从 `tenant:admin` 收紧为 `super_admin`（`gateway.IsPlatformAdmin`），防 tenant-admin 越权枚举全部租户与用户。console-admin 的 IsAdmin 用户 token 携带 super_admin 标记（auth.issueTokens）通行。
- **HTTP panic recover**：`recoveryMiddleware` 包 mux 最内层（otelhttp 外层），捕获 handler panic 防单请求挂掉进程（in-flight/SSE 流被强断），panic 栈入服务端日志，客户端只收 500 internal error。
- **错误响应脱敏**：`httputil.WriteInternalError(w, err)` 统一 500 返 "internal error"（不泄漏 SQL 语句/表名/连接串），原始错误入日志；替换散落 40+ 处 `WriteError(w, 500, err.Error())`。`/v1/chat/completions` 503 按 sentinel 分类返脱敏 cause（credential issue/rate limited/upstream unavailable），不泄漏上游 URL/IP。
- **构建日志 Git token 脱敏**：`builder.MaskToken/MaskErr` 对 `https://<token>@host` -> `https://***@` 脱敏，store `runBuild` 拿 res 后统一应用，防 git clone 失败 stderr 经 `BuildRun.Log` -> `GET /api/buildruns/{id}` 泄漏给 build:read。
- **启动日志 API Key 脱敏**：只打前 6 字符 + 长度，防生产 Key 明文进容器日志聚合。
- **登录防账号枚举**：先验密码（不存在/密码错统一 401），密码正确后再查状态（禁用 403），攻击者需已知密码才能发现账号禁用。
- **数据服务 Create/Update 凭证掩码**：与 List/Detail 一致返 `MaskConnection`，明文仅内部绑定注入用（防日志/proxy/MITM 捕获）。
- **K8s 数据面 Port 投影修复**：`K8sApplier.Apply` 投影 CRD spec 补 `Port/ContainerPort`（原遗漏致 reconciler 不建 Service + 不加 readiness probe -> Pod 间 DNS 不可达 + Endpoints 永不 ready -> `/dp/instances` 返空，数据面失效；fake client 测不出）。
- **K8s manager cache namespace 限定**：`Cache.DefaultNamespaces` 按 `PAAS_K8S_NAMESPACE` 限定（ns 非空时），防 watch 全集群 CRD + 收敛数据面到本 ns（多租户隔离加固）。
- **Job TTL 清理**：WorkloadReconciler `applyJob` 设 `TTLSecondsAfterFinished=86400`（与 devops builder 一致），防完成 Job/Pod 永久残留拖慢 list/watch。
- **applyService 顺序调整**：先建 Service 再回写 status（一次 Status().Update），避免 running/deploying 抖动 + apiserver 写放大。
- **跨 store 编排补偿事务**：devops PG `CreateRelease`（INSERT release 失败回滚 workload 镜像）+ `RollbackRelease`（tx 失败补偿恢复 workload），防丢失回滚指针。
- **并发深拷贝防御**：Gateway `Models()` 深拷贝防 `Channel.Status` 撕裂读；billing `GenerateBill` 返回深拷贝；configcenter `clonePublish`（Snapshot map）+ 锁内复校验 namespace；governance `Route.Methods` 深拷贝；billing PG `IncUsage/CheckAndInc` 负值夹紧防配额绕过；application PG `BindResource` advisory lock 防 ord 重复。

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
- 切片**真实构建三模式（env 开关）**：`PAAS_DEVOPS_BUILDER` 显式三选一构建执行体——`k8s`（默认集群模式）/ `process` / `mock`（未设=mock，现状不变）；`PAAS_DEVOPS_REAL=true` 向后兼容别名 → `process`。
  - **`k8s` 模式（DooD）**：core 创建 `batch/v1 Job` Pod（`docker:git` 镜像 + 挂节点 `hostPath: /var/run/docker.sock`，**非 privileged** 复用节点 daemon），Pod 内跑 git clone→docker build→push，core 轮询 Job 完成取 Pod 日志解析 `PAAS_DIGEST=` 回写 `BuildRun`。`builder.K8sJob` 实现 `Pipeline`（`Clientset kubernetes.Interface` + `Namespace` + 凭证，状态机仍在 Store）。`startManager` 用 `kubernetes.NewForConfig` 构造 clientset 注入；无 K8s clientset 时降级 Mock。RBAC 补 `pods/log` get（`rbac.yaml`）。Job `TTLSecondsAfterFinished=86400`（1 天后自动清）+ `BackoffLimit=0`（失败不重试）。digest 解析正则 `^PAAS_DIGEST=(sha256:[0-9a-f]+)$`，无匹配 → BuildRun failed + 全量 Pod 日志作 Log。脚本纯 env 变量替换（业务逻辑在 Go 侧算好透传，防注入），`docker login --password-stdin` 密码不进 argv。内网 builder 镜像 `hub.wang.dd:5000/library/docker:git`（amd64 预推）。**安全取舍**：构建共享节点 daemon（理论隔离弱），dev 集群 + 平台级凭证可接受；生产多租户硬化（Kaniko/专用构建节点）归后续。**集群 e2e 已端到端验证**（构建 docker-library/hello-world → 真实 registry digest 落 Image，对失败路径亦优雅回写 failed+日志）。**真实集群踩坑修复**：① 脚本 `DOCKER_BUILDKIT=0` 强制 classic builder——DooD 下 buildkit 经 sock 的 context transfer 有 bug（dockerfile 收 2B 损坏 → "failed to read dockerfile"）；② docker build 参数用 `BUILD_ARGS` 变量无引号拼接（非 `${var:+-f "$var"}`），busybox ash 对 `${:+}` 嵌套引号展开会丢 `-f`；③ `dockerfile`/`buildContext` 相对 cwd `/workspace`（如 `amd64/Dockerfile` + `buildContext=amd64`）；④ helm upgrade 不改 deployment spec 时不触发 rollout（image.tag 不变需 `kubectl rollout restart` 强制拉新镜像，pullPolicy: Always 仅在 Pod 重建时生效）。
  - **`process` 模式**：core 进程内 `os/exec` git/docker（`builder.Real`，本地 dev；distroless/K8s 部署不可用——故集群必须用 `k8s`）。
  - **`mock` 模式**：`sha256(commit+app+build)` 派生 digest，零依赖。
  - Release 部署经 workload K8s 数据面落地（Deployment）。策略接口开放（rolling/blue-green/canary），实现 YAGNI（只 rolling），蓝绿/金丝雀归后续（灰度耦合泳道归服务治理）。

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
- **K8s 真实后端部署（observability namespace，三支柱真实化）**：dev 集群装真实后端栈，core 经 env 接通后即从惰性 mock 切真实。部署清单 `deploy/observability/`（社区 helm chart + values + ingress），轻量 single-binary 模式：
  - **组件**：`prometheus-community/prometheus`（Prometheus + node-exporter，关 alertmanager/kube-state-metrics）+ `grafana/loki-stack`（Loki + Promtail）+ `grafana/tempo` + `grafana/grafana`（预配三数据源）。
  - **K8s 1.23 兼容**：`kube-prometheus-stack` 要求 ≥1.25，本集群 1.23.1 用 `prometheus-community/prometheus`（普通 Deployment，无 K8s 版本硬约束）。
  - **镜像源（国内必走）**：节点（kb1/kb2/kb3 内网）拉 docker.io/quay.io 经常超时 → 全部经 daocloud 中转 `docker.m.daocloud.io` 复制到本地 `hub.wang.dd:5000/observability/`：`crane copy docker.m.daocloud.io/<repo>/<image>:<tag> hub.wang.dd:5000/observability/<name>:<tag> --insecure`（registry 间复制不经 docker daemon），再 `kubectl set image` 改工作负载镜像。注意路径：quay.io/prometheus/prometheus ≠ docker.io/prom/prometheus，daocloud 用后者路径。prometheus v3.13.2 等新版本国内源可能未同步，可让节点直拉 quay（慢但能成，prometheus-server 实测最终拉到）。
  - **PVC**：集群有 `local-path` StorageClass，Prometheus/Loki/Tempo/Grafana 各启 PVC（10G/10G/5G/5G）持久化。
  - **scrape**：chart 自带 `kubernetes-nodes-cadvisor`（直接抓 kubelet:10250/metrics/cadvisor）即可得数据服务 `container_*` 指标（real/metrics.go TargetDataservice 依赖）；extraScrapeConfigs 加 paas-core /metrics（label `app.kubernetes.io/name=paas-core`，relabel pod_ip:8081）。
  - **关键坑：Promtail 漏挂 docker data-root**。集群 docker `data-root=/data/docker`（非默认 /var/lib/docker），kubelet 的 `/var/log/pods/<pod>/<c>/0.log` 是软链 → `/data/docker/containers/<id>/<id>-json.log`。Promtail 默认只挂 `/var/log` + `/var/lib/docker/containers`，follow 软链时 stat 目标失败（`failed to tail file, stat failed ... no such file`）→ 日志采不到、`/ready` 500 卡 NotReady。修复：Promtail 加 hostPath 挂 `/data/docker` → 容器 `/data/docker`（readOnly），软链目标可解析。patch：`kubectl patch ds loki-promtail --type=json -p='[{"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"data-docker","hostPath":{"path":"/data/docker"}}},{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"data-docker","mountPath":"/data/docker","readOnly":true}}]'`。
  - **kb1 master 节点**：未配 `hub.wang.dd:5000` insecure registry，该节点 Promtail/ DaemonSet 拉镜像失败（ImagePullBackOff）；业务 Pod 全在 worker（kb2/kb3），kb1 不采集可接受（或给 kb1 配 insecure registry）。
  - **Tempo OTLP**：tempo svc 暴露 4317(grpc)/4318(http)，core `PAAS_OTEL_ENDPOINT=tempo.observability.svc.cluster.local:4318`（otlptracehttp + WithInsecure，不含 scheme）。控制面 otelhttp 自动建 span 推 Tempo，`GET /api/search` 真实可查（如 `GET /api/observability/traces` span）。
  - **ingress 暴露（*.k8s.dd 通配域名 + hermes class）**：`deploy/observability/ingress.yaml` 暴露 `grafana.k8s.dd`（统一面板，admin/paas-admin）+ `prom.k8s.dd`（Prometheus UI）；Loki/Tempo 不暴露（仅 ClusterIP，供 Grafana 与 core 内部访问，减少攻击面）。core 可观测页仍走 core API（core 经 ClusterIP 访问三支柱）。
  - **helm upgrade 注意**：Promtail 的 /data/docker 挂载与 readinessProbe 当前为 patch（loki-stack values 未持久化），`helm upgrade loki` 会覆盖；升级前把这些写进 `deploy/observability/loki-stack-values.yaml` 的 `promtail.extraVolumes/extraVolumeMounts/readinessProbe`。
  - **应用级 logs 查询 gap（留后续）**：real/logs.go 用 LogQL `{app="<appID>"}` 查询，但 Pod label 是 `app.kubernetes.io/name`（非 `app=<PaaS appID>`），应用级日志查询当前查不到。Pod 级真实（按 pod/namespace）已通。完整应用级需 controller 给 Pod 打 `app=<appID>` label + real/logs 用对应 label。
  - **应用级业务指标 gap（留后续）**：core 无业务 `paas_cpu_usage`/`paas_rps` 等埋点（仅 controller-runtime 进程级 /metrics），real 模式查应用指标返空（降级）；数据服务指标走 cAdvisor `container_*` 真实可用。完整应用级需 core 加 Prometheus 埋点（http_request_duration/handler 维度）。

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
- console-user `DataServices.vue` **6 路由共用**（`/resources/{db,cache,mq,storage,vector,search}`，route `props.kind` 区分）：按 KindMeta 动态渲染规格列 + 创建弹窗表单（select/text）+ 启停/删除；点行跳详情页 `/resources/:kind/:id`（`DataServiceDetail.vue`：连接信息卡 + Pod 级监控 + 告警）。
- **真实引擎落地（mysql/postgres/redis/valkey/nats/minio 端到端可连，vector/search/kafka 等占位返空 failed）**：`internal/dataservice/connection.go` 纯函数（`GenerateCredentials` crypto/rand 24 字符 / `EnginePort` 按 engine 区分（mysql 3306·postgres 5432·redis 6379·nats 4222·minio 9000） / `BuildConnection` FQDN `<id>.<ns>.svc.cluster.local`+port+uri / `MaskConnection` password/secretKey/token/uri->`••••••`（uri 含明文密码））+ `DataService.Connection` Create 时 `FillConnection` 填充（凭证持久化重启不变；host 用 `d.ID` 与 K8s Service 名一致，应用 DNS 可解析）+ `NamespaceResolver` 接口注入 `PAAS_K8S_NAMESPACE`（cmd/core 桥接，dev 兜底 `paas`）。CRD spec 加 `connection` 字段（`make manifests` 重生成）。reconciler 建 **Secret**（按 Kind 挑敏感 key，幂等不覆盖）+ headless/ClusterIP **Service** + **StatefulSet**（env `secretKeyRef`：mysql `MYSQL_ROOT_PASSWORD`/postgres `POSTGRES_USER`+`POSTGRES_PASSWORD`+`POSTGRES_DB`/redis `--requirepass $(REDIS_PASSWORD)`/nats `-auth $(NATS_TOKEN)`/minio `MINIO_ROOT_USER`+`MINIO_ROOT_PASSWORD`），OwnerRef 全设 CR（删 CR 级联清），失败态 best-effort 回写 `phase=failed`。`SetupWithManager` Owns(StatefulSet/Service/Secret)。rbac 补 secrets 权限。`engineImage` 真实引擎（mysql/postgres/redis/valkey/nats/minio）返非空，占位（kafka/rabbitmq/rocketmq/vector/search）返空 -> reconciler 走 failed 不拉起（避免 port=0/缺 env 死循环）；`KindMetas` MQ engine nats 默认。fake client 测（Secret/Svc/STS env/幂等不覆盖密码/nats/redis 命令）。**引擎镜像内网化（PAAS_IMAGE_REGISTRY）**：节点拉不到 docker.io 时，`engineImage(kind,engine,registry)` 非空 registry 返 `<registry>/library/<name>:<tag>`（去 repo 前缀，如 `minio/minio`->`library/minio`），reconciler 从 `PAAS_IMAGE_REGISTRY` env 读（chart `devops.registry` 注入，core-deployment 已设）；引擎镜像需先推 `<registry>/library/`（**amd64**，本地 arm64 Mac 拉需 `--platform linux/amd64`，经国内源 `docker.m.daocloud.io` 中转避 docker.io 超时）。改 engineImage 镜像后需删旧 STS 触发 reconciler 重建（CRD 未变不自动重 reconcile）。**STS imagePullPolicy=Always**（containerFor 显式设；tag 不变时强制拉最新 digest，避节点缓存旧架构镜像如 arm64->amd64 切换不生效）。**minio 用 Args 非 Command**（minio 镜像 entrypoint=minio，Command 会覆盖致 `server` 找不到；Args=["server","/data","--console-address",":9001"] 传给 entrypoint）。**集群 e2e 已端到端验证**（mysql/redis/nats/minio 4 引擎 Pod Running + 应用绑定注入 DATABASE_URL + 监控指标返回 cores）。
- **应用绑定自动注入连接信息**：`application.BindingInjector` 接口（依赖倒置，OnBind/OnUnbind）+ `cmd/core.dsBindingInjector` 桥接 dataservice+appconfig。POST bindings type=db/cache/mq/storage/vector/search（具体 kind，`isDataserviceKind` 过滤；非 dataservice kind 无副作用）-> `resolveDS` 按 name 或 ID 解析（前端可填名称或 ID）-> 按 Kind 写 appconfig 连接条目（TypeSecret）：db->`DATABASE_URL`/cache->`REDIS_URL`/mq->`NATS_URL`/storage->`MINIO_*`；工作负载重启 env 注入即真实可连。解绑：查应用剩余同 Kind 绑定，有则重新注入最后一个（key 保留值覆盖），无则删 key（避免同 Kind 多绑定误删仍需连接）。注入失败仅 log 不阻断绑定（绑定是主操作）。
- **监控告警（targetType=dataservice）**：observability `TargetDataservice` 常量 + memory seed 2 条 dataservice series + real/metrics.go 数据服务走 cAdvisor Pod 指标（`container_cpu_usage_seconds_total`/`container_memory_working_set_bytes` 按 `pod=<id>-0` 过滤，CPU 核数/内存 MiB）；未配 Prometheus 走 memory 兜底。前端详情页监控卡 10s 轮询。
- **凭证安全**：list/detail 接口均掩码（`MaskConnection`，含 uri；与 security 一致，read 权限者含 viewer 不可见明文；应用绑定经 repo.Get 拿明文注入不受影响）；密码不进日志（reconciler 只记 dsName/phase）；凭证持久化（Create 一次，Secret 引用，幂等不覆盖）。
- **PG 迁移 0014**：dataservices 加 `connection` JSONB 列（凭证+host/port/uri 持久化）。memory/pg store Create 填 Connection + map 深拷（顺带修 Spec/Connection 引用泄漏）。
- **数据服务备份（backup 模块）**：`internal/backup/` 数据服务备份 CRUD（复用 `dataservice:read/write` 权限）+ `EnvTypeResolver`+`ResourceEnvResolver`（resourceID->envID->EnvType，生产数据服务备份/删除需 `prod:write`，fail-closed）+ `deterministicSize` sha256 派生 mock 大小 + Create 即 `StatusCompleted`（mock，无真实备份任务）。REST：`GET/POST /api/backups`、`DELETE /api/backups/{id}`。`cmd/core` 桥接 `dsEnvLookup`（dataservice.Repository -> ResourceEnvResolver）。
- **留后续**：PVC 持久卷声明（数据服务重启数据丢失，dev/demo 可接受）、HA/集群/扩缩容、引擎原生 exporter（mysql_exporter 等引擎级指标）、同 Kind 多绑定 key 前缀化（`DS_<id>_<KEY>`）、异步创建流转 PG 反向同步、真实备份任务（PV 快照/逻辑备份）--接口已铺路。

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
