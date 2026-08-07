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

一套 Helm chart（`deploy/charts/paas`）一把梭部署到 K8s：core 单镜像同源 serve 前端 + API（无 CORS），**集群内自建 registry**（`deploy/k8s/registry.yaml`，NodePort 30050）+ `hermes` ingress。所有镜像依赖统一来自集群内 registry（去除外部 hub.wang.dd 依赖），镜像清单 + 同步 + 排查见 `docs/deploy/dependencies.md`。

- **集群内自建 registry**（去除 hub.wang.dd 依赖）：`deploy/k8s/registry.yaml`（registry:2 + local-path PVC 20Gi + Service NodePort 30050）。所有镜像地址统一 `<nodeIP>:30050`——kubelet/CRI 在节点拉镜像**无法解析 `svc.cluster.local`**，故 Pod 镜像也用 IP:NodePort（builder daemon push / core Pod registry client / Mac 导入同址）。`scripts/deploy-k8s.sh` 自动检测 worker nodeIP + `envsubst` 注入 `values-paas-k8s.yaml` 的 `${NODE_IP}` 占位。`scripts/sync-images.sh` 列表驱动导入核心镜像（postgres/docker:git/gitea/引擎等，daocloud 中转）。**core hostNetwork 退役**（早期为访问外部 hub.wang.dd 的 hack，自建 registry 后 core Pod 经 Pod 网络→NodePort 可达，`values-paas-k8s.yaml` 设 `core.hostNetwork=false`）。
- **前端嵌入 core**：三套 SPA（console-user/console-admin/landing）构建产物 `//go:embed` 进 core 二进制（`internal/web/`），core 同域 serve。同域路由（ServeMux 最长前缀匹配）：`/api/*` `/v1/*` `/openapi.json` `/docs` `/livez` → API（精确匹配优先）；`/console/*` → console-user；`/admin/*` → console-admin；`/*` → landing（兜底）。前端 base path 子路径化：console-user `base:'/console/'`、console-admin `VITE_BASE='/admin/'`、landing 默认 `/`。
- **Dockerfile 多阶段**：node:22-alpine 构建三套前端（console-admin 需 Node ≥ 22.13）→ golang:1.26-alpine Go 交叉编译（`ARG GOARCH=amd64`，builder 跑本地架构交叉编译到 amd64 避 QEMU）→ distroless runtime。国内源默认（`ARG NPM_REGISTRY=https://registry.npmmirror.com`、`ARG GOPROXY=https://goproxy.cn,direct`），海外 `--build-arg` 覆盖。无 `# syntax=` 指令（避免 buildkit 拉远程 frontend）。
- **Helm chart 组件**：`templates/crds.yaml`（Workload+DataService CRD，`crds.install` 门控）+ `templates/rbac.yaml`（ServiceAccount + ClusterRole[core.aitoys CRD + apps/batch workload + pods/services/events] + ClusterRoleBinding）+ core-deployment（`serviceAccountName` + `PAAS_K8S_NAMESPACE` + `PAAS_METRICS_ADDR=0`）+ ingress（`hermes` class，host 配）+ 内置 postgres（`db.enabled` 开关）。
- **in-cluster 数据面**：core 容器内 `startManager` 用 `ctrl.GetConfig()` 自动检测（PAAS_KUBECONFIG 显式 或 in-cluster SA token + KUBERNETES_SERVICE_HOST），集群内部署无需 PAAS_KUBECONFIG 文件，SA token 自动挂载 + RBAC 授权。**关键修复**：原以 `PAAS_KUBECONFIG` 非空作启 manager 的唯一门控，导致集群内部署（SA token 可用但 env 空）数据面不生效——改用 `ctrl.GetConfig()` 容错（无 config 才降级）。此 bug 单元测试测不出（无 in-cluster 环境），真实集群部署才暴露。
- **部署脚本**：`scripts/deploy-k8s.sh`（检测 nodeIP → docker build → push 集群内 registry → envsubst values → helm upgrade）。docker push 超时（colima VM 路由问题）自动 fallback `crane push --insecure`。集群覆盖文件 `deploy/charts/paas/values-paas-k8s.yaml`（`${NODE_IP}:30050` 占位、ingress=hermes/paas.k8s.dd、`core.hostNetwork=false`）。首次部署需先 `kubectl apply -f deploy/k8s/registry.yaml` + `./scripts/sync-images.sh` 导入镜像（见 `docs/deploy/dependencies.md`）。

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
# 流式推理（airouter 真实通道，需 model:infer 权限 + 配 sec-platform-airouter 凭证）
curl -N -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"你好"}],"stream":true}' \
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
  → MaaS 插件(catalog seed 仅 airouter 真实通道)
  → Channel(openai-compatible Provider 转发 / mock·echo 进程内) → 流式返回
```

- `pkg/provider/`：`Provider`（`Name()+Chat()`）/ `Model` / `Channel`（含 `UpstreamModel`/`CredentialRef` 第三方通道字段）/ `GatewayRegistrar` / `CredentialResolver`（凭证解析接口，依赖倒置）/ 5 个错误 sentinel（`ErrCredentialMissing`/`ErrCredentialInvalid`/`ErrUpstreamRateLimit`/`ErrUpstreamUnavailable`/`ErrUpstreamConfig`，驱动降级分类）。
- `internal/maas/`：MaaS 插件 + `OpenAICompatibleProvider`（一个适配器覆盖 OpenAI 兼容协议，纯 `net/http` + SSE 解析）+ catalog seed（**仅 airouter 网关真实模型 12 个**，配 `sec-platform-airouter` 即全模型真实可推理）+ `MockProvider`/`EchoProvider`（BuildProvider fallback 用，不在默认 catalog）。**catalog 已收敛清理**：早期混入的直连供应商占位（gpt-4o/qwen-plus/deepseek-* 等，需各自独立凭证未配即不可用）+ mock/echo 演示（echo-demo/qwen2.5-7b/bge-m3，非真实推理）已全部移除，ID 列入 `DeprecatedSeedModelIDs`，启动 seed 自动清理遗留 PG 记录（CASCADE 清通道），保证模型市场「全部真实可用」。
- `internal/core/gateway/`：API Gateway（`ResolveChannels` 候选列表 / 请求级 failover：degraded 类错误切备通道，offline 类标 offline / `MarkChannelStatus` / API Key 中间件 / OpenAI 流式 handler / Meter）。
- 凭证：平台级 Secret（`security.Scope=platform`，全租户共享，仅 tenant-admin 可写）经 `Resolve` 取明文（仅内存），`main.go` 桥接 security store → `CredentialResolver` 注入 MaaS 插件。
- 路由策略：通道按 `Priority` 升序，跳过 `offline`；请求时 degraded 类错误（限流/5xx/超时）自动 failover，offline 类（凭证/配置）切下一通道，全部失败 503。
- `pkg/plugin.CoreDeps` 的 `Gateway()` + `SecretResolver()` 注入点（依赖倒置，破除 maas→security import）。
- 切片**不依赖 K8s/GPU**；未配 airouter 凭证时真实通道返回 `ErrCredentialMissing` → 503；配 `PAAS_AIROUTER_API_KEY`（helm `maas.airouterApiKey`）后全模型真实可推理。
- **airouter 真实推理入口（统一真实化，默认 catalog 唯一来源）**：catalog 即 12 个 airouter 网关模型（通义/DeepSeek/智谱/月之暗面/豆包/万相，覆盖文本/推理/视觉/图片/向量，`internal/maas/airouter_catalog.go`，复用 `OpenAICompatibleProvider` Bearer），`baseAirouter=https://airouter.ddmc-inc.com/api/v1` + `credAirouter=sec-platform-airouter`。airouter 内部已聚合百炼/千帆/豆包多供应商容灾，平台配一个 api_key 即全模型真实可用（白嫖其容灾链路，无需各自申请百炼/千帆/豆包 Key）。api_key **不入库**：经 env `PAAS_AIROUTER_API_KEY`（helm values `maas.airouterApiKey`，部署 `--set`）注入，启动 seed 到平台级 Secret。security pg seed 改为「表非空也 ensure 平台级凭证」（`ensurePlatformSecrets`，`ON CONFLICT DO NOTHING` 不覆盖用户已填值），重部署到已有 PG 自动补齐 airouter 凭证。**catalog 收敛**：早期直连供应商占位 + mock/echo 演示已移除（`DeprecatedSeedModelIDs`，启动自动清遗留 PG 记录），airouter 成为默认 catalog 唯一内容，模型市场全部真实可用。

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
- **响应契约全平台统一**（深度检测 P1）：所有平台 CRUD 成功响应一律 `{data:T}`（经 `httputil.WriteData`/`WriteDataCreated`），错误一律 `{error:msg}`（`WriteError`），500 脱敏（`WriteInternalError`）。**仅以下协议端点保留裸 JSON**（`httputil.WriteJSON`，非 `{data:T}`）：OpenAI 兼容 `/v1/*`（`{"object":"list","data":[...]}`）、K8s 探针 `/livez`（`{"status":"ok"}`）、数据面 SDK `/dp/*`（zeus 发现协议 shape）、配置中心发现 `/api/configcenter/.../published`（`{published,version,snapshot,publishId}` 客户端直取）。前端两路消费均兼容：console-user `fetchJSON` 智能解包 `{data:T}` 否则原样；console-admin http interceptor 检测 `'data' in payload` 自动解包。消除各 handler 50+ 处裸 `json.NewEncoder` + auth 自定义 `writeAuthData/writeErr` 重复（DRY 收敛到 httputil 单一真源）。单资源响应聚合用专用类型（如 `governance.ServiceDetail{Service,Instances}`）+ spec `WithResp` 对齐。
- **后续**：vendored 本地 Scalar JS（离线）、自动 mock。

### P1.1 应用去假 + 租户开通（生产可用起步）

承接 P0 登录会话，解决「看起来假」的核心：应用 seed 演示数据 + 租户无法开通 + 模型硬编码（模型管理归 P1.2）。P1.1 聚焦应用去假 + 租户开通。设计见 `docs/superpowers/specs/2026-08-02-p1-real-platform.md`。

- **应用 seed demo 门控**（零假数据）：`PAAS_DISABLE_DEMO_SEED=true`（chart `seed.demo=false`）时内存 `application/memory.NewStore()` + PG `seedApplications()` 两路径均跳过 `SeedApps()` 灌入，生产应用列表为空由用户自建；dev 默认灌示例应用。与演示凭证门控同源（`seedIdentity`），避免两套门控。`SeedApps()` 函数保留作 dev seed 真源。
- **租户开通**（admin 后台）：identity 后端 CRUD 已齐（`/api/admin/tenants|users|api-keys`，`adminGuard` super_admin），补前端：
  - **菜单**：`auth/menus.go` `staticMenus()` system 下加「租户管理」（`/system/tenant`），前端 `lib/router/dynamic.ts` `import.meta.glob('@/modules/**/*.vue')` 自动识别组件。
  - **system/tenant 模块**（console-admin）：`api.ts`（对接 `/api/admin/tenants`，假分页适配 useCrud + `fetchAllTenants` 供用户表单选租户）+ `List.vue`（SearchTable + 删除走 `ElMessageBox.prompt` 输入租户 ID 确认）+ `TenantFormDrawer.vue`（id+name，core 无租户更新端点仅新建）。
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

### P1.2+ 供应商管理（Vendor 实体：选供应商自动带入 BaseURL/凭证）

承接 P1.2，解决「每个 openai-compatible 通道都要手填 BaseURL + 选凭证」的痛点。把公共部分（BaseURL + 凭证 + Type）抽成可复用 `Vendor` 预设，创建通道选供应商即带入，免去手填。

- **领域**：`pkg/provider.Vendor{ID/Name/Type/BaseURL/CredentialRef/Description}`（平台级，全租户共享）；`Channel` 加 `VendorID` 关联（非空时 handler 从 Vendor 解析 BaseURL/CredentialRef 回填到通道字段，BuildProvider 不动——仍用 Channel 的 Endpoint/CredentialRef 构造 Provider，向后兼容旧通道）。
- **Repository**：`maas.Repository` 加 Vendor CRUD 六方法（ListVendors/GetVendor/CreateVendor/UpdateVendor/DeleteVendor/VendorsCount）+ sentinel（`ErrVendorNotFound/Exists`）；memoryStore + pg.Store 双实现（克隆 Model CRUD 模式）。
- **PG 持久化**：migration `0001`（新部署）含 `maas_vendors` 表 + `maas_channels.vendor_id` 列；`0003_maas_vendor`（已部署 PG 增量，`ADD COLUMN IF NOT EXISTS` + `CREATE TABLE IF NOT EXISTS` + UPDATE 回填存量 airouter 通道 vendor_id，幂等）。`chCols` 加 vendor_id（3 处 Scan 同步，列错位 panic 是最易踩坑）。
- **REST**（`/api/admin/providers`，composite 按路径分发，复用 maasHandler + adminGuard super_admin）：`GET/POST /api/admin/providers` + `GET/PUT/DELETE /{id}`。Vendor 不是路由实体（不进 gateway），CRUD 后无需 reloadModel。OpenAPI Operation 登记 5 操作。
- **通道创建回填**（handler `serveChannels` POST/`serveChannel` PUT）：`VendorID` 非空时 `GetVendor` 解析 BaseURL/CredentialRef 覆盖通道字段（「选供应商自动带入」真正落点）；vendor 不存在 -> 404。
- **airouter 预置**：`AirouterVendor()`（airouter_catalog.go 导出）seed 时 ensure（`persistence.seedMaasCatalog` 在 ModelsCount 判空前 CreateVendor，不被「表非空跳过」门控跳过；ON CONFLICT 幂等）。12 个 airouter 通道 VendorID 均指向 airouter，admin 修改其 BaseURL/凭证后新通道带入（存量通道字段不自动同步留后续）。
- **console-admin 供应商管理**（super_admin）：菜单「供应商管理」（`/provider`，Connection 图标，dynamic.ts 自动识别）。`modules/provider/`：`api.ts`（re-export model/api 的 Vendor CRUD + 假分页）+ `List.vue`（SearchTable+useCrud，BaseURL/凭证列，删除输入 ID 确认）+ `ProviderFormDrawer.vue`（id/name/type/baseUrl/credentialRef 选平台 Secret/description）。`model/views/ChannelFormDrawer.vue` 改造：openai-compatible 时显 vendorId select（替代手填 endpoint/credentialRef）+ upstreamModel，endpoint/vendor/credentialRef 隐藏（后端回填）。
- **留后续**：Vendor 改 BaseURL/凭证后存量通道自动同步（反向级联 reloadModel）、凭证测试（建供应商时 ping 上游）、通道健康检查真实探活。

### P1.3 API Key 自助管理（去假 + 租户自助端点）

承接 P1.2，去除 console-user `ApiKeys.vue` 纯假数据（硬编码 k1/k2/k3，按钮无后端对接）。补租户自助端点 + 前端真实化。复用 identity handler 既有 APIKey 方法（已是租户隔离），无需新增领域逻辑。

- **租户自助端点**（`/api/api-keys`，`auth` 守卫，任意已认证用户管理本租户密钥）：`GET`（列本租户，掩码）/ `POST`（创建，返明文一次）/ `DELETE /{id}`。与 `/api/admin/api-keys`（super_admin 跨租户）并存——同一 handler 方法，靠 `platformAdmin` 分支区分作用域。
- **授权模型（零提权）**：`identity.Handler` 注入 `CallerUserID`/`CallerRoles`（main.go 桥接 `gateway.UserIDFrom`/`RolesFrom`，依赖倒置避免 identity→gateway import）。非超管创建密钥时：① 强制 `tenantId=callerTenant`（防跨租户）；② 强制 `userId=callerUserID`（绑定调用者，自助场景前端无需传）；③ `roles` 经 `capRoles` 求交——只能赋予自身持有角色，求交为空（含未指定）→ 镜像调用者自身角色（developer 无法创建 tenant-admin/super_admin 密钥）。校验顺序：非超管先从 ctx 补全归属，再做必填校验（避免自助空 body 400）。
- **lastID 路由兼容**：`DeleteAPIKey` 改用 `lastID(r)`（取路径末段，不依赖固定前缀），兼容 `/api/admin/api-keys/{id}` 与 `/api/api-keys/{id}` 两挂载点。
- **console-user `ApiKeys.vue` 真实化**：`fetchJSON` 对接 `/api/api-keys`（cookie 会话鉴权）；列表掩码 + roles tag + 归属用户；创建弹确认 → 空 body POST → 明文仅显示一次（el-dialog + 复制按钮 + 「仅显示一次」警示）；吊销走普通确认（API Key 租户级资产，不按物理环境隔离，不走 prod gated）；空状态引导。
- **console-admin API 密钥后台页**（super_admin 跨租户）：`modules/system/apikey/`（`api.ts` + `List.vue` + `ApiKeyFormDrawer.vue`），菜单 `/system/apikey`（Key 图标）。SearchTable + 租户过滤 + keyword；创建表单显式选租户/用户/角色（super_admin 指定，与自助的 ctx 补全互补）；删除走 `ElMessageBox.confirm`；明文仅展示一次。与租户/用户管理构成 identity 管理三件套（tenants/users/api-keys + roles 只读）。
- **MVP 取舍**：自助列表返本租户全部密钥（租户=信任边界，掩码不泄漏明文，租户内可见性可接受，按用户 scope 留后续）；创建不提供角色选择（镜像自身，最简最安全，角色细分留后续）。
- **e2e 验证**：developer 登录 → 列表仅本租户（t-globex 不泄漏）→ 空 body 创建返明文+roles=[developer] → 请求 tenant-admin/super_admin 被封顶为 [developer]（零提权）→ 删本租户 204 / 跨租户 404 不泄漏。
- **留后续**：密钥 name/label 字段（现以 id 展示）、按用户 scope 列表（自助只见自己的）、密钥过期/轮转、创建时角色细分选择、使用次数/最后使用时间统计、API Key 审计日志。

### P1.4 后台管理重构（菜单分组 + 跨租户资源总览 + 推理流式增强）

解决 console-admin 三大痛点：菜单散乱（个人中心/模型/供应商都是顶级）、管理员无法跨租户查看用户数据、推理模型流式思考过程丢失。设计见 `docs/superpowers/specs/2026-08-02-p1-real-platform.md`。

- **菜单架构重构**（`auth/menus.go` staticMenus）：按平台运维职责分组——工作台 / 身份与权限（租户/用户/角色/密钥）/ 推理服务（模型/供应商）/ 资源总览（**三级嵌套**：资源总览下按业务域分 4 组——应用运行态（应用/工作负载/数据服务/环境）/ DevOps 链路（构建/镜像/发布）/ 平台能力（配置中心/服务治理/告警规则/密钥）/ 计费审计（配额/账单/审计日志））。MenuItem.vue 递归渲染支持任意层级，dynamic.ts 只注册叶子节点（中间分组节点无 component 被跳过）。个人中心移出侧栏（`ShowMenu:false`），路由保留供右上角用户下拉入口。子菜单 path 保持稳定（不随分组前缀化），前端跳转引用零牵连。图标全局唯一（修正早期 Key×2/Odometer×2/Cpu×3 撞车）。mock/handlers/menu.ts 与后端 staticMenus() 结构对齐（dev 与生产零漂移）。
- **跨租户资源总览**（核心：管理员管理用户数据）：各业务 Repository 加 `ListAll(ctx)`（跨租户，不过滤 tenant，返回对象带 `TenantID`，pg 提取共享查询 helper 复用 DRY）；REST `/api/admin/applications|workloads|dataservices`（`adminGuard` super_admin，**只读**——跨租户写越权风险高，资源运维仍在 console-user 租户内）。console-admin `modules/resources/` 模块 3 页（SearchTable + useCrud 假分页，租户列 + keyword + tenantId 过滤）。dataservice admin 总览同样 `MaskConnection`（与 list/detail 同源，明文仅内部绑定注入用）。
- **全模块 admin 总览扩展**（每个模块管理功能完善 + 闭环，2026-08-04）：解决「每个模块都有后台管理、管理员能管理其他用户数据」核心诉求。environment/devops/configcenter/governance/observability/security/billing 7 模块补 `ListAll`（Repository 接口 + memory + pg）+ 11 端点（`/api/admin/environments|buildruns|images|releases|namespaces|services|alert-rules|secrets|audit-logs|quotas|bills`，`adminGuard` super_admin 只读）+ 11 前端总览页（`resources/views/{Environments,BuildRuns,Images,Releases,Namespaces,Services,AlertRules,Secrets,AuditLogs,Quotas,Bills}.vue`）+ 菜单 11 子项。security `ListAllSecrets` 返回 `Masked()`；billing `Limits`/`Items` 深拷贝防 race；observability 用 alert-rules（metrics/logs/traces 惰性时序不适总览）；devops 三实体各一端点。console-admin 后台现覆盖全部模块（身份 4 + 推理 2 + 资源总览 14 = 20 管理页），每个模块管理功能闭环。
- **推理流式增强**（Playground 真正 SSE 效果）：推理模型（GLM/QwQ/Doubao/DeepSeek-R1 等）流式返回 `reasoning_content`（思考过程），原代码只解析 `content` 致思考阶段被丢弃→前端长时间空白。`provider.Chunk` 加 `Reasoning` 字段 → `OpenAICompatibleProvider` 解析 `delta.reasoning_content` → gateway `serveStream` 透传为 `deltaMessage.ReasoningContent` + 加 `X-Accel-Buffering: no`（防 nginx/ingress 缓冲，确保逐 chunk 到达）→ console-user Playground 渲染**可折叠「思考过程」**（流式中显「思考中…」脉动徽标）+ 正文实时流式。链路完整、断连有退出路径无 goroutine 泄漏。
- **SSE 经 hermes ingress 缓冲修复（三层根因）**：SSE 流式经 hermes ingress 被缓冲成一次性大块，三层根因逐一修复——① **zeus accesslog `statusRecorder` 未实现 `http.Flusher`**：嵌入 `http.ResponseWriter` 但 Go 接口嵌入不转发额外接口，代理/ReverseProxy 的 `w.(http.Flusher)` 断言失败 → flush 空操作 → 全量缓冲。修复：加 `Flush()` 委托底层（`if f, ok := r.ResponseWriter.(http.Flusher); ok { f.Flush() }`）。② **hermes metrics `statusRecorder` 同样问题**：同模式修复。③ **hermes SSE 分流依赖请求 Accept 头**：`isSSERequest` 检 `Accept: text/event-stream`，前端 `fetch()` 默认 `Accept: */*` → 落 `handleHTTP`。修复：Playground fetch 加 `Accept: text/event-stream`（SSE 标准声明）。修复后 `httputil.ReverseProxy` 的 `flushInterval`（对 `text/event-stream` 返回 -1 立即 flush）穿透中间件链正常工作：**无 Accept 头也 71 chunk 逐块到达**（~30-50ms/块，与直连 service 一致）。验证：修复前 ingress 1 块 vs 修复后 71 块。zeus/hermes 镜像已重建部署。
- **深度检测第 1 轮**：19 条 `/api/admin/*` 端点全挂 adminGuard（无漏挂）；ListAll 仅 admin 路径调用，普通 List 仍强制 tenant 过滤；PG 全参数化无注入；memory 锁+深拷贝正确。修复 2 项 Important：① admin dataservices 端点 Connection 掩码（补 `MaskConnection`）；② model.go 过时注释纠正。

### P1.5 应用工作台（tab 分组 + 概览真实化 + 关联能力收敛进应用，2026-08-06）

解决「开发者以应用操作为主，但关联能力散落顶级菜单」痛点。应用详情升级为应用工作台，开发者进入应用即得全貌，减少跳转。设计见 `docs/superpowers/specs/2026-08-06-application-workbench-design.md`。**纯前端聚合，零后端改动**（所有数据维度已就绪）。

- **tab 视觉分组**（`ApplicationDetail.vue`）：10 tab 按「运行态（概览/部署/服务治理/可观测）· 资源（资源绑定/配置/用量）· DevOps（代码仓库/构建/镜像/发布）」三组视觉分区（不折叠，一屏可见防膨胀）。
- **概览真实化**：去 seed 假数据（`app.rps`/`app.replicas`），改聚合真实运行态——副本就绪比（workloads 聚合）+ 绑定资源数 + RPS/CPU 指标卡含 sparkline（复用 `/api/observability/metrics?targetType=app`）+ 最新发布/构建侧卡 + 资源依赖拓扑（含治理服务节点）。
- **服务治理 tab**（`app-tabs/AppGovernance.vue` 新建，只读）：复用 `GET /api/services?appId=` 按应用过滤 + `GET /api/services/{id}`（`{service,instances}` 双重兜底解包）展开懒加载实例；路由/熔断按 serviceID ∈ 该应用服务集合过滤。注册/注销归 `/platform/governance`。
- **可观测 tab**（`app-tabs/AppObservability.vue` 新建）：复用 `/api/observability/{metrics,logs,traces}?appId=`，4 指标卡（CPU/内存/RPS/延迟 + sparkline）+ 最近日志 + 最近 trace（展开 span），10s 轮询（silent 不闪烁，onUnmounted clearInterval）。顶部「在监控大屏中打开」保留深度排查出口。
- **用量与成本 tab**（`app-tabs/AppUsage.vue` 新建）：`GET /api/billing/usage` 返 `usage.byApp[appID]`（gateway 经应用级 API Key 归因落库的 token/gpu **精确归因**，非近似）+ 资源占用（workloads/绑定计数）+ PriceTable 预估月成本（标注「预估」，因 PriceTable 是 mock 单价）。
- **配置 tab 重组凭证分组**（`AppConfigs.vue` 改造）：env/secret 混合表拆成「环境变量（明文）」+「凭证/密钥（掩码）」两组——凭证组即「应用引用的密钥」的诚实落地（appconfig secret 是工作负载启动注入的真实敏感凭证；`security.Secret` 是租户级平台资产不归属应用，不强行关联）。
- **关键澄清（billing 应用维度已就绪）**：调研发现 `ResourceUsage.ByApp map[string]map[string]int` 早已落地（memory/pg `IncUsage` 在 appID 非空时填充 + gateway `meter.Record` 经应用级 Key 归因 + pg `by_app` JSONB 列），`GET /api/billing/usage` 已暴露 `usage.byApp`。用量 tab 直接消费精确归因，无需后端改动。
- **e2e 验证**：三套前端 build + `make test` 全绿 + k8s 部署，核心端点全 200（applications/services?appId=/observability?appId=/billing/usage 含 byApp/configs）。
- **留后续**：`audit_logs` 加 app_id + 应用级活动 timeline；全局资源页归属应用反查（方案 C）；概览 sparkline 接真实 Prom 后去静态降级（依赖 core 加 `paas_rps` 业务埋点）；PriceTable mock → 真实计费引擎（成本随之精确）。

### P1.6 console-user IA 按操作频率重组（侧栏分层 + 头部紧凑化，2026-08-06）

解决「开发者以应用操作为主，但低频项占了侧栏大多数空间、应用主线感被淹没；应用详情头部大卡片吃首屏」痛点。仅 console-user（console-admin P1.4 已重构，频率模型不同不动）。设计见 `docs/superpowers/specs/2026-08-06-console-user-ia-by-frequency.md`，纯前端零后端。

- **侧栏三段分层**（`App.vue`）：主操作区（应用 brand 色强化 + DevOps + Playground + AI 服务）置顶 → 资源与能力区（资源中心/工作负载/平台能力，默认折叠，各自 `localStorage` 记忆展开态）→ 环境。资源组从「永远铺开 13 子项」改为「可点击折叠 + chevron + 记忆」，回收 ~70% 侧栏空间。「应用」brand 左竖条常显 + 字重 600 强化主线锚点。section label（主操作/资源与能力）复用 `.nav-section-label`。
- **资源组折叠记忆**（新增 `composables/useNavState.ts`）：模块级单例 ref<Set<NavGroup>>，localStorage key `paas:nav-open:<group>`，默认全折叠，try/catch 容错。`isOpen`/`toggle` 接口。
- **应用详情头部紧凑化**（`ApplicationDetail.vue`）：删 header 大卡片（大图标+名称行+描述行+`[监控][部署][删除应用]` 三按钮），改一行面包屑紧凑身份条（返回箭头 + 应用 / 应用名 + 环境 chip[prodenv 红] + 健康 + 右端 `⋯` `el-dropdown`）。删冗余「监控」「部署」按钮（能力交由「可观测」「部署」tab，监控大屏出口在可观测 tab 内）。描述移到概览 tab 顶部。删除应用降权进 `⋯` 下拉（`confirmDangerous` 逻辑不变）。回收首屏 ~70px。`⋯` 用纯文本（Icon `collapse` 形态是 chevrons-left 不适合）。
- **顺带修复基线回归**：645a5c2 的 ApplicationDetail.vue vue-tsc 失败（模板引用应用工作台 Task 5 误删的 goObservability/goDeploy，pnpm build 的 vue-tsc 步骤失败被 tail 掩盖）；P1.6 删按钮后引用消除，vue-tsc 恢复绿。
- **留后续**：命令面板（Cmd+K）跳转/创建资源（激进 IA）；「编辑应用/导出配置」后端端点 + ⋯ 下拉填充；el-dropdown 多项时迁 `@command` 模式。

### 工作负载（应用运行形态）

工作负载归属应用，分三类（Service/Job/CronJob），本期进程内 mock，真实 K8s 编排为下一切片：

```
internal/workload/  领域（Type/Status 常量 + Validate）+ Repository（租户隔离）+ 内存 seed
  -> handler: /api/applications/{id}/workloads（应用下）+ /api/workloads?type=（跨应用）
  -> 权限 workload:read/write（方法级 Authorize 注入）
```

- `internal/workload/`：`Workload`（期望 Replicas vs 就绪 Ready 分离，为 controller-runtime 铺路）/ Repository / 内存实现（seed 5 条跨两租户）/ handler。
- 路由：`GET/POST /api/applications/{id}/workloads`、`GET /api/workloads?type=`、`GET /api/workloads/{id}`（详情含运行实例）、`GET /api/workloads/{id}/logs?pod=&tail=&previous=`（实例 Pod 日志）、`PUT /api/workloads/{id}`（扩缩容/状态）、`DELETE /api/workloads/{id}`。
- cmd/core 用 composite handler 按 `/workloads` 后缀分发，避免与 application 的 `/api/applications/` 前缀冲突。
- **详情 + 运行实例（Pod 级）+ 日志**（Job/CronJob 看运行详情）：`workload.Instance`（Pod 级：name/status/ready 重启/节点/IP/启动时间/message）+ `workload.Detail{Workload,Instances}`；`StatusReader` 接口加 `Instances(ctx,id)`（K8s 按 label `paas.aitoys/tenant+workload` 查 Pod 映射）+ `PodLogs(ctx,id,pod,tail,previous)`（K8s Pods.Logs，**越权校验**：先 Get Pod 确认 label 同时含本租户+本 workload 否则拒绝，跨租户/跨 workload 统一 not found 不泄漏）；handler `GET /api/workloads/{id}` 返回 Detail（聚合期望态+实例），`GET /api/workloads/{id}/logs` 透传日志流（text/plain，`previous=true` 取上次终止容器日志，Job 崩溃排查关键）。无 clientset 降级返空/友好错误（详情页仍展示期望态）。console-user `Workloads.vue` 详情抽屉（实例表格 + 每行「日志」按钮 + 日志对话框含「查看上次终止日志」开关）。fake client 测（实例映射/就绪重启/降级/越权拒绝）。
- console-user：`Workloads.vue`（按类型 tab 接真实 API + 扩缩容/删除 + 详情/日志）+ 应用详情「部署」tab 渲染该应用工作负载。
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
- **平台级管理越权防护**：`adminGuard`（保护 `/api/admin/tenants|users|api-keys|dashboard/*`）从 `tenant:admin` 收紧为 `super_admin`（`gateway.IsPlatformAdmin`），防 tenant-admin 越权枚举全部租户与用户。console-admin 的 IsAdmin 用户 token 携带 super_admin 标记（auth.issueTokens）通行。
- **HTTP panic recover**：`recoveryMiddleware` 包 mux 最内层（otelhttp 外层），捕获 handler panic 防单请求挂掉进程（in-flight/SSE 流被强断），panic 栈入服务端日志，客户端只收 500 internal error。
- **错误响应脱敏**：`httputil.WriteInternalError(w, err)` 统一 500 返 "internal error"（不泄漏 SQL 语句/表名/连接串），原始错误入日志；替换散落 40+ 处 `WriteError(w, 500, err.Error())`。`/v1/chat/completions` 503 按 sentinel 分类返脱敏 cause（credential issue/rate limited/upstream unavailable），不泄漏上游 URL/IP。
- **构建日志 Git token 脱敏**：`builder.MaskToken/MaskErr` 对 `https://<token>@host` -> `https://***@` 脱敏，store `runBuild` 拿 res 后统一应用，防 git clone 失败 stderr 经 `BuildRun.Log` -> `GET /api/buildruns/{id}` 泄漏给 build:read。
- **启动日志 API Key 脱敏**：只打前 6 字符 + 长度，防生产 Key 明文进容器日志聚合。
- **登录防账号枚举**：先验密码（不存在/密码错统一 401），密码正确后再查状态（禁用 403），攻击者需已知密码才能发现账号禁用。用户不存在路径补 dummy bcrypt 比对（见第 3 轮），防时序侧信道。
- **数据服务 Create/Update 凭证掩码**：与 List/Detail 一致返 `MaskConnection`，明文仅内部绑定注入用（防日志/proxy/MITM 捕获）。
- **K8s 数据面 Port 投影修复**：`K8sApplier.Apply` 投影 CRD spec 补 `Port/ContainerPort`（原遗漏致 reconciler 不建 Service + 不加 readiness probe -> Pod 间 DNS 不可达 + Endpoints 永不 ready -> `/dp/instances` 返空，数据面失效；fake client 测不出）。
- **K8s manager cache namespace 限定**：`Cache.DefaultNamespaces` 按 `PAAS_K8S_NAMESPACE` 限定（ns 非空时），防 watch 全集群 CRD + 收敛数据面到本 ns（多租户隔离加固）。
- **Job TTL 清理**：WorkloadReconciler `applyJob` 设 `TTLSecondsAfterFinished=86400`（与 devops builder 一致），防完成 Job/Pod 永久残留拖慢 list/watch。
- **Job/CronJob restartPolicy 修复**：`podSpec()` 未设 `RestartPolicy`，Job/CronJob 的 Pod template 默认 `Always` 被 apiserver 拒绝（"Required value: valid values OnFailure/Never"）-> Job 永远创建失败 + reconciler 疯狂报错。`applyJob`/`applyCronJob` 显式设 `RestartPolicyNever`（与 `BackoffLimit=0` 一致，失败即终止不重试）；fake client 测不出（无 apiserver 校验），真实集群才暴露。回归测试断言 restartPolicy=Never。
- **applyService 顺序调整**：先建 Service 再回写 status（一次 Status().Update），避免 running/deploying 抖动 + apiserver 写放大。
- **跨 store 编排补偿事务**：devops PG `CreateRelease`（INSERT release 失败回滚 workload 镜像）+ `RollbackRelease`（tx 失败补偿恢复 workload），防丢失回滚指针。
- **并发深拷贝防御**：Gateway `Models()` 深拷贝防 `Channel.Status` 撕裂读；billing `GenerateBill` 返回深拷贝；configcenter `clonePublish`（Snapshot map）+ 锁内复校验 namespace；governance `Route.Methods` 深拷贝；billing PG `IncUsage/CheckAndInc` 负值夹紧防配额绕过；application PG `BindResource` advisory lock 防 ord 重复。

### 深度检测第 3 轮（安全 + 前端双路审计，2026-08-04）

安全维度 + 前端质量两路 agent 审计，修核心 Critical/Important：

- **平台级 Secret 越权（Critical）**：`security.WithIsAdmin` 从 `gateway.IsAdmin`（校验 `tenant:admin`，每租户 admin 都有）改 `gateway.IsPlatformAdmin`（super_admin）。原实现致任意 tenant-admin 可删 `sec-platform-airouter` → 全平台推理 503，或伪造平台级 Secret 诱导跨租户绑定。
- **Provider SSRF（Critical）**：`OpenAICompatibleProvider` 默认 httpClient 加 `CheckRedirect: http.ErrUseLastResponse`（不跟随重定向），防 baseURL 被配为攻击者主机返 302→metadata 时平台 airouter Key（Authorization 头）被外发。
- **CodeRepo 注入/RCE（Critical）**：`CodeRepo.Validate` external 仓库加 URL/branch 校验——scheme 仅 http/https（拒 `file:///ssh:///ext::` 防 git 历史 RCE 如 CVE-2018-17456）、拒云元数据 host（169.254.169.254/metadata/loopback，防诱导 builder 把 `PAAS_GIT_TOKEN` 发往云元数据接口）、GitURL 不得自带 user info（凭证由 injectToken 从 env 注入）、Branch 正则 `^[A-Za-z0-9._/-]+$` + 拒 `-` 前缀（防 git argv flag 注入 `--upload-pack=sh -c...`）。
- **Playground SSE 流式渲染（Critical）**：根因是 Vue 响应式引用 bug——`messages.value.push(原始对象)` 后改原对象属性不触发 trigger，token 流式到达但视图不刷新（一次性出现）。改取 `messages.value[len-1]`（reactive proxy）再修改。非后端/ingress/fetchAuth 问题（均已验证正确）。
- **BuildRun panic 日志脱敏（Important）**：memory/pg store 的 `recover()` 分支 `构建异常: %v` 未走 `MaskToken`，panic 栈含 `cloneURL=https://<PAAS_GIT_TOKEN>@...`，统一补 `MaskToken`（与正常错误路径一致），防 build:read 权限者读平台 Git 凭证。
- **Workload 绕过 prod:write（Important）**：`Workload.Validate` 强制 EnvID 必填（与 dataservice/governance 一致）；`allowProd` 区分「未注入 envResolver」（跳过，测试场景）与「EnvID 空」（fail-closed 保守按生产要 prod:write）。原 `envID==""` 直接放行，developer 可提交无环境归属负载绕过生产写防护。
- **生产 cookieSecure 强制（Important）**：`PAAS_PROD=true` 且未开 `PAAS_COOKIE_SECURE=true` 时拒启，防生产误用 HTTP 致 access(15min)+refresh(7d) cookie 被嗅探。
- **JWT secret 弱串防护（Important）**：生产模式 secret `<32` 字节拒启，防运维误配 `"paas"` 等弱串被 hashcat 暴破伪造 token。
- **登录时序侧信道（Important）**：用户不存在路径补一次 dummy bcrypt 比对（`dummyHash` 启动生成 cost=10），使响应时间与「密码错」路径一致，防时序枚举用户名。
- **限流 XFF 伪造（Important）**：`clientIP` 从「信任 XFF 首段」（客户端可控，每请求换随机 XFF 即绕过 per-IP 限流）改为优先 `X-Real-IP`（ingress 覆盖）→ XFF 最右段（ingress 追加）→ RemoteAddr。
- **审计已验证正确**：13 个 pg store 全参数化无 SQL 注入；airouter api_key 经 env 注入不入库；Secret/Connection 全端点掩码；JWT `hmac.Equal` 常量时间比较；`/api/admin/*` 13 端点全挂 adminGuard；自助 API Key `capRoles` 求交零提权；未发现 v-html XSS。
- **留后续（下一轮）**：I8/I9 错误响应脱敏（20+ 处 `WriteError(err.Error())` 泄漏 pgx 错误细节）、I10 CSRF Origin 校验、I4 parseDigest 信任 Pod 日志、I5/I6 各 client CheckRedirect 纵深、I13/I14 refresh rotation + logout 撤销（jti 黑名单）、I16 限流多副本 Redis、前端审计项（console-admin 401 自动刷新拦截器 / lib 死代码清理 / mock 双重门控 / vue-admin 品牌文案替换）。
### 深度检测第 4-8 轮（横切加固 + 前端死代码清理，2026-08-04）

承接第 3 轮留后续项，逐轮修复 + 开源打磨：

- **第 4-5 轮（CSRF + mock 门控 + 401 拦截器 + 品牌）**：① CSRF SameSite=Lax 主防线 + Origin/Referer 同源校验纵深（`csrfMiddleware`，非 safe 方法校验 host==r.Host，跨站 403；origin 空放行兼容同源 GET 跳转）。② console-admin mock 双重门控（`enableMock = DEV && VITE_ENABLE_MOCK==='true'`，生产默认关）。③ console-admin 401 自动刷新拦截器（`RefreshHandler` interface + `setRefreshHandler` + `refreshAccessToken`，401 且非 _retry 且非 /refresh 时刷新重试一次，失败走登录）。④ vue-admin 品牌->PaaS（Footer/Sidebar/Index）。
- **第 6 轮（I8/I9 错误响应脱敏）**：`httputil.WriteServiceError(w, status, err)` 特征分流--底层英文技术错误（pgx/SQLSTATE/dial tcp/`*.svc.cluster.local`/duplicate key 等 `internalErrorMarkers`）-> 500 脱敏（`WriteInternalError`），业务中文消息 -> 按传入 status 返回。全局替换散落 `WriteError(w, 500, err.Error())`，防 pgx 错误细节泄漏 build:read 权限者。
- **第 7 轮（数据一致性 + I1 PG 侧）**：① I2 devops PG CreateRelease/RollbackRelease 补偿事务用 `context.WithoutCancel(ctx)` + log 失败（不阻断主流程）。② I3 application Delete 成功后 QuotaCheck(-1) 回滚配额。③ I4 identity DeleteTenant 单事务 + `SELECT ... FOR UPDATE` 行锁防并发。④ I5 devops `SweepInterrupted(ctx)` 启动恢复（pending/running build_runs -> failed，防进程退出后构建卡 running）。⑤ I1 PG 侧 CreateRelease `pg_advisory_xact_lock(hashtext(app|env))` 串行化同 (app,env) 发布。
- **第 8 轮（I1 memory 侧对齐 + 前端死代码清理）**：① I1 memory 侧 CreateRelease 把 step2（List->UpdateImage）+ step3（存 release）包进 `s.mu` 临界区（与 PG advisory lock 语义对齐；跨 store 嵌套 devops.mu -> workload.mu 单向无死锁；`-race` + 全量 devops 测试验证通过）。② **前端基座演示死代码清理**（开源打磨）：删 6 个 vue-admin 遗留演示模块（`modules/system/{dept,dict,permission,menu,log,notice}`）+ 关联组件（`DeptSelector`/`DictTag`）+ composable（`useDict`）+ mock handler（dept/dict/permission/menu-manage/log/notice）+ 死常量（DICT/DEPT/PERMISSION/MENU/NOTICE/LOG 共 10 个）+ `MODULE_STATUS_MAP`（仅 permission List 用）；`NoticeInfo` 类型迁移到 `app/stores/notice`（保留 notice UI 壳 Header 铃铛 + dashboard 横幅，数据已短路为空）；`mock/handlers/menu.ts` 重写为基座最小演示（home + 访问控制 user/role）。i18n 零 dept/dict 文案（无需动）。vue-tsc + vite build 双通过。②b **i18n 死 key 命名空间清理**：删 `dept/permission/menu/crud/dict/log` 6 个死 key 命名空间（zh-CN + en-US 共 12 块，对应模块已删全无引用，Serena regex 跨文件批量删，vue-tsc 验证通过）；`notice` 命名空间保留（NoticeCenter 壳在用）。③ **后端资源泄漏 + 前端代码质量双路审计完成，无严重新发现**：后端关键点（`observability/real/http.go` defer Body.Close、`maas/{openai_compatible,mock,echo}` goroutine `select <-ctx.Done()` 断连退出 + defer close/body close、devops runBuild baseCtx 进程退出 cancel）全部正确处理，印证第 3 轮验证；前端 5 个轮询文件（App/Observability/DevOps/DataServiceDetail/AppBuilds）`setInterval` 均 `onUnmounted clearInterval` 清理，无 v-html/innerHTML、无 localStorage/sessionStorage 残留（已迁 cookie）、无 pinia persist、无 document.cookie 直接操作。
- **留后续**：I4 parseDigest 信任 Pod 日志、I5/I6 各 client CheckRedirect 纵深、I13/I14 refresh rotation + logout 撤销（jti 黑名单，架构级）、I16 限流多副本 Redis、告警通知通道、Tempo span 详情、Vendor 改 BaseURL/凭证后存量通道自动同步。

### 深度检测第 9 轮（资源泄漏 + 性能 + 死锁，2026-08-04）

第 7 轮留后续审计 agent 发现项逐个修复（grep 误报剔除 + 真实项修）：

- **devops memory CreateRelease 自死锁（Critical）**：第 7 轮 I1 memory 侧把 step2-3 包进 `s.mu` 临界区，但 step2 内调 `findImageIDByDigest`（取回滚指针）该函数自己又 `s.mu.Lock()` → **Go mutex 不可重入，自死锁**。`TestReleasePreviousAndRollback` 10min 超时暴露（`-race` 检测不出死锁，只检测数据竞争）。修复：`findImageIDByDigest` 去掉自取锁改「调用方已持锁」语义（注释明确），CreateRelease 临界区内直调。
- **登录限流内存泄漏（Important）**：`loginLimiter.fails` map 永不清理过期条目（lockedUntil 过 + 窗口外的条目永久残留），长跑内存单调增长。修复：惰性 GC `sweep`（写操作时若距上次清理 > `loginWindow` 即扫除完全过期条目），持锁内调用无额外开销。
- **identity PG ListUsers/ListAPIKeys N+1（Important）**：列表先查全部用户/Key，再逐条 `userRoles`/`apiKeyRoles` 查角色（N 次查询）。admin 跨租户列表可能很大。修复：加 `usersRolesBatch`/`apiKeysRolesBatch`（`WHERE user_id = ANY($1)` 一次查回 map），列表路径用批量聚合（单/Get 路径保留单查）。
- **审计日志无分页 LIMIT（Important）**：`ListAuditLogs`/`ListAllAuditLogs` 无 LIMIT，审计增长后全量返回撑爆/超时。修复：SQL 末尾 `LIMIT 1000`（防御性上界，不改接口签名，前端可后续加分页）。
- **误报剔除**：grep `for {` 命中三处「无限循环」——gateway `serveStream`（select ctx.Done+channel close 退出的事件循环）、k8s builder 轮询、airsync install，均为带退出条件的正常循环，非 bug，不改。
- **出站 HTTP client CheckRedirect 纵深（I5/I6 收口）**：第 3 轮只修了 `OpenAICompatibleProvider`（airouter Key），其余 4 个平台 client（gitea basic auth / registry / observability Prom·Loki·Tempo）仍裸 `&http.Client{Timeout}` 跟随重定向——gitea 携带 paas-bot 密码，端点被劫持/误配返 302→攻击者主机会泄漏凭证。抽 `httputil.NewClient(timeout)`（内置 `CheckRedirect=ErrUseLastResponse` 不跟随），5 处统一改用（DRY，单一真源）。
- **前端死代码复核（确认已干净）**：lib/ 子系统全在用（http/client 是 10+ 模块 api 入口，interceptors→notify/problem/token 链完整；auth 模块经 authService 公共入口内部消费）；resources/views 经 dynamic.ts `import.meta.glob` 约定路由加载（非孤儿）；console-user 零孤儿；全仓无 notImplemented/即将/敬请期待假占位（唯一「即将」是 Icon.vue 装饰火箭图标注释），后端零 TODO/FIXME。基座已达开源标准。
- 全量 `go test ./...` 45 包通过（修死锁后）。

### 深度检测第 10 轮 + 开源打磨 + 体验优化（2026-08-05）

菜单重构 + 模块互链 + 死代码复核 + 深度审计 + k8s 部署：

- **后台菜单三级重构**（见上「菜单架构重构」）：资源总览 14 项平铺改 4 业务子分组（应用运行态/DevOps 链路/平台能力/计费审计），图标全局唯一（修 Key/Odometer/Cpu 撞车），mock 与后端对齐。
- **console-user 模块互链**（消除孤岛）：① 应用详情资源绑定卡可点→资源列表；② 部署 tab 工作负载行可点→对应类型工作负载列表；③ 头部「监控」入口→`/platform/observability?app=:id`（Observability 读 query.app 预选）；④ 构建/镜像/发布 tab 加「跨应用总览→」跳 DevOps；⑤ 工作负载表格 appId 列可点→应用详情；⑥ 数据服务详情新增「绑定此资源的应用」反查面板（前端聚合 applications bindings 过滤）；⑦ 环境详情工作负载总览 stat 卡可点（覆盖 jobs/cronjobs 孤岛）+ 新增「环境内数据服务」section。
- **死代码复核**：仓库已干净（前 9 轮已清，零 TODO/FIXME/孤儿/占位，docs/scripts 均被引用，无散落截图/临时文件）。
- **深度审计修复**：PG `rows.Close()` 显式调用改 `defer rows.Close()`（panic 安全）—— maas/pg（ListModels/ListChannels/ListVendors 3 处）+ configcenter/pg（CreatePublish），与全仓其余 11 store 一致。子代理 fan-out 因模型限额中断，转内联审计覆盖 SQL 注入（干净，全 `$N` 参数化）、adminGuard 覆盖（全 14 `/api/admin/*` 端点挂 super_admin）、错误脱敏（WriteServiceError 完全迁移，零残留 `WriteError(500,err)`）、前端轮询清理（5 文件全 clearInterval）、goroutine 退出（已验证）。无新严重问题。
- **构建验证**：`make test` 全绿 + `pnpm build` 三套前端通过。
- **k8s 部署**（[[k8s-always-latest]]）：`deploy-k8s.sh` 构建前端 embed 镜像 + push registry + helm upgrade + rollout restart，e2e 验证全 200（landing/console/admin/livez/v1/models/api/admin menus 三级结构生效）。

### 深度检测第 11-12 轮（并发 + 契约双路审计，2026-08-06）

承接第 10 轮，并发安全/资源泄漏 + 业务契约/逻辑两路 agent 深度审计，复核前 10 轮修复 + 找新问题：

- **并发路（Important）**：`devops/pg/store.go` `runBuild` SIGTERM 路径用已 cancel 的 baseCtx 写 PG（panic/err/markBuildFailed 三处）→ build_run 永久卡 running 只能等下次启动 `SweepInterrupted` 兜底。第 7 轮 `CreateRelease/RollbackRelease` 补偿事务已切 `WithoutCancel`，但 `runBuild` 自身漏修。修复：三处落库统一 `context.WithoutCancel(ctx)`（pipe.Build 仍用原 ctx 响应取消，仅落库脱离请求生命周期）。并发路 Minor 4 项（ResolveChannels 未 Clone/observability+maas 读多写少用 Mutex/airsync 循环内 Close 非 defer/memory CreateRelease 持锁跨仓储）均模式脆弱非现存缺陷，留后续。
- **契约路（Important ×4）**：① devops handler 4 处裸 `WriteJSON` 违反 `{data:T}` 契约（`GET /buildruns/{id}` 详情、`GET /images/{id}` 详情、`POST /releases` 创建、`POST /rollback`）→ 改 `WriteData/WriteDataCreated`；② security `POST /secrets` 创建改 `WriteDataCreated`；③ appconfig `POST /configs` 创建改 `WriteDataCreated`；④ **identity 7 个写操作（Create/DeleteTenant、Create/Update/DeleteUser、Create/DeleteAPIKey）不记审计**——凭证签发/吊销 + 用户/租户增删属高敏感操作却零审计，违反「审计只增不删」合规承诺。修复：identity 定义 `AuditRecorder` 接口（依赖倒置，`Record(ctx,tenantID,actor,action,resourceType,resourceID,detail)`）+ `WithAudit` 注入 + handler 写操作成功路径调 `h.audit(...)`；cmd/core 加 `identityAuditAdapter` 桥接 `security.AuditStore`（与 `authAuditAdapter` 同源模式），actor 取 `UserIDFrom(ctx)`，超管跨租户 ctx 无 tenant 归 "platform"。
- **契约路（Minor ×3）**：devops/security/appconfig 删除 ack `WriteJSON({"deleted":id})` → 统一 `WriteData`（与 governance/observability 对齐）。
- **轻量引擎测试补齐**：补 `connection_test.go`（qdrant/meilisearch port=6333/7700、api_key/master_key 凭证、uri 不含凭证、mask 一律掩 uri 保 host/port）+ `dataservice_controller_test.go`（`TestEngineImageLightEngines` 含 milvus/es 弃用返空、registry 内网化；`TestReconcileQdrant`/`TestReconcileMeilisearch` 镜像+env QDRANT__SERVICE_API_KEY/MEILI_MASTER_KEY+端口+PVC size 映射 StorageGB+VolumeMount 路径；`TestReconcileDataserviceImageOverride` spec.Image 覆盖默认镜像）。修 appconfig handler_test 适配 `{data:T}` 解包。
- **横切机制复核确认无问题**：多租户隔离（11 个 ListAll 仅 admin 路径调、全 PG 参数化）、prod:write 防护（6 模块写全接 EnvTypeResolver、先 Get 取 EnvID 再 allowProd）、配额回滚（application/workload Create 失败 -1、Delete 回收）、错误脱敏（零残留 `WriteError(500,err)`）、OpenAPI 登记（163 Operation 覆盖全部 mux 路由含 lifecycle/engine 新端点）、goroutine 退出/rows.Close/HTTP Body Close/map 锁/mutex 自死锁（第 9 轮 findImageIDByDigest 保持）全部确认。
- **构建验证**：`go test ./...` 全绿 + `pnpm build` 三套前端通过。
- **留后续**：ResolveChannels Clone（handler 锁外读 Status 时）、observability/maas Mutex→RWMutex（读热点）、identity 审计可在 security.AuditLog 加 ResourceType 常量枚举、airsync 循环内 defer fd 回收。

### P1-P4 admin 管理基线（console-admin 资源总览闭环，2026-08-07）

承接 P1.6，解决「console-admin 资源总览无管理功能、像查看面板」痛点。admin = 平台运营中枢（看穿+干预+代建+审计），与 console-user 租户自助正交。设计见 `docs/superpowers/specs/2026-08-07-admin-management-foundation-design.md`。

**三层模型**（基线 spec）：
- **L1 详情可见**：跨租户 GET /{id} 详情（资源 + 运行实例 Pod 级）
- **L2 运维干预**：启停·重启·扩缩·删·日志·注销实例·回滚·set-quota·pay（绕过 prod:write 但审计+UI警示）
- **L3 代建**：仅基础设施类（dataservice/environment），代建消耗目标租户配额

**跨租户写端点规范**（横切继承，全 10 admin handler）：
- 独立 /api/admin/<resource>/{id}/{action} 端点 + adminGuard(super_admin)
- 跨租户读：ListAll filter by id（绕过 Repository.Get 的 ctx tenant 强制）/ GetAny（dataservice）
- 写操作：tenant.WithTenant 派生目标租户 ctx + 绕过 prod:write + 全记审计（admin: 前缀，target_tenant=资源租户，平台级 Secret tenant_id="" 经 identityAuditAdapter 转 "platform"）
- 代建消耗目标租户配额（QuotaCheck +1，失败回滚）；删除回收配额（-1，Delete 成功后）
- 凭证 Connection 一律 MaskConnection 掩码；security Secret 返回 Masked()

**模块覆盖**（10 admin handler，console-admin 20 管理页闭环）：
- P1 数据服务（dataservice L1+L2+L3）+ 环境（environment L3 代建 + L1 详情）—— 样板
- P2 工作负载（workload L1+L2）+ 应用（application L1+L2，CascadeDeleter 级联清 workload+appconfig）
- P3 DevOps（buildruns/images/releases L1 详情 + 回滚）
- P4 治理（governance 服务 L1+L2）/ 可观测（observability 告警规则 L1+L2）/ 计费（billing 配额调整+账单标记）/ 安全（security 密钥 L1+L2）

**横切正确性**（深度检测验证）：appCascadeDeleter 级联删 workload 回收 workload 配额（admin+租户侧统一复用 appCascadeDeleter）；平台级 Secret sentinel "platform"（DeleteSecret TenantOrErr 通过，不误删租户级）；typed-nil 防御（restarter opts 条件注入，集群外返 503 友好降级）；workload scale Replicas *int（nil 保留当前值防意外缩容）。

**留后续**：maas（9 端点）+ engine（3 端点）admin 写操作审计（违反「审计只增不删」，super_admin 信任角色，上线后补，模式已标准化参照 identity P1.4）；devops BuildRun/Image/Release L2 delete + observability AlertRule L2 启停（需先补 Repository Delete/Update 方法）；workload admin serveList 跨租户不回填 K8s 真实状态。

### 深度检测第 13-16 轮（admin 跨租户 5 轮审查，2026-08-07）

P1-P4 admin 跨租户管理代码 5 轮深度审查（每轮独立 agent 视角），修复 16 个 findings（1 Critical + 11 Important + 4 Minor）：

- **第 1 轮（5 路：安全/多租户隔离/并发/契约/业务逻辑）9 findings**：security 删平台级 Secret 失败（Critical，adminTenantCtx sentinel "platform"）；dataservice/workload serveDelete 配额回收顺序错（改先 Delete 再 -1）；appCascadeDeleter 级联删 workload 不回收配额（注入 wlQuota，admin+租户侧统一）；dataservice serveDetail 未检查 HTTP 方法（405）；4 admin ServeHTTP 列表路径 405；environment admin 尾斜杠注册；serveRestart recordAudit 用 rr；billing serveSetQuota 校验租户存在。
- **第 2 轮（前端深审 + 修复验证 + 路由/OpenAPI）3 findings**：Drawer 审计 section 假阴性（fetchAuditLogList size 50->1000）；DataserviceDrawer 连接信息 SENSITIVE_KEYS 客户端兜底掩码（防后端漏加字段白名单泄漏）；4 Drawer（Application/BuildRun/Image/Release）关闭后 detail 清空（@close+onUnmounted）。
- **第 3 轮（横切继承 + 模块闭环）3 findings**：dataservice serveRestart restarter=nil 假成功+漏审计（改返 503）；environment admin 补 L1 详情 GET /{id}（基线 spec P1 要求）；workload scale Replicas 改 *int（nil 保留当前值防意外缩容）。
- **第 4 轮（部署前最终审查）1 Critical**：admin dataservice restarter typed-nil panic（*DSRestarter(nil) 装箱接口 != nil 致守护失效 -> panic，opts 列表模式条件注入修复，集群外 503 降级）；maas/engine 审计留后续。
- **第 5 轮（部署 e2e）**：deploy-k8s.sh + curl admin 端点全 403（adminGuard super_admin 生效）+ 新 L1 详情 environments/{id} 路由注册 + OpenAPI 登记。

验证：go test ./... -race 全绿（含回归测试 security TestAdminDeletePlatformSecret / billing TestAdminSetQuotaRejectsUnknownTenant / dataservice TestAdminItemMethodNotAllowed + environment TestAdminEnvironmentDetail）+ 前端 console-admin build 通过 + k8s e2e 全通过。

### DevOps 发布体验改造（指挥台 + 发布流水线 + 一键发布，2026-08-06）

DevOps 中心从「只读监控大屏」升级为「CI/CD 指挥台」，补齐发布流水线逐级提升，发布点击数从 6+ 降到 2：

- **Environment 发布流水线阶序**：`Environment.PromoteOrder int`（test=10/prod=20 默认，0=不参与）；`DefaultPromoteOrder(type)`；`Repository.NextPromoteTarget(ctx, envID)` 返同租户内 order 严格大于当前的最小阶序环境（同 order 取 id 最小，确定性），最高阶返 `ErrNoPromoteTarget`。`EnvPromoter` 依赖倒置接口供 devops 注入。migration `0009_env_promote_order`（ADD COLUMN + 存量按 type 回填，幂等）。environment handler Create 在未指定 order 时按 type 填默认（开箱即用）。
- **Release 晋升链**：`Release.PromotedFrom` 字段（非空=由 promote 产生，追溯晋升链）；`Repository.PromoteRelease(ctx, srcID, targetEnvID)` 复用 CreateRelease 编排（找/建基线 Workload + 回滚指针）+ 标 PromotedFrom；migration `0010_release_promoted_from`。**不引入 Pipeline 实体**（YAGNI，promote = 新环境产生新 Release + 来源标记）。
- **promote 端点**：`POST /api/releases/{id}/promote`（serveReleaseAction 加 promote 分支；handler 算 target env + 目标 prod 走 `allowProd` prod:write 校验 + 调 store.PromoteRelease）。OpenAPI 登记。
- **DevOps 中心指挥台化**（`console-user/DevOps.vue` 重写）：① 新增**流水线 tab（默认）**——按 app 分组渲染 env 阶序横向矩阵（`[test:v1.2✓] → [staging:v1.1✓] → [prod:v1.0] 提升→`），每格最新 succeeded release + 「提升」按钮（末列无）；② 构建行加「重新构建」（用原 repoId 调 POST）；③ 发布行加「提升」+ 应用列可点跳详情；④ releases 轮询（10s，原仅 buildruns 5s）；⑤ 回滚/提升按**目标 env.type 显式 isProd**（覆盖顶栏 scope，防不一致削弱防护）。
- **一键发布 + 多环境勾选**：① `AppBuilds` 构建成功行加「发布」按钮（复用镜像 tab `emit('pick')` 机制，父切发布 tab 预选镜像，构建→发布断链修复）；② `AppReleases` 创建弹窗环境改 `multiple` 多选，`create()` 循环 POST + Promise.allSettled 聚合（部分成功列出失败 env），含 prod 勾选时 `confirmDangerous(isProd:true)`。
- **统一契约**：回滚按钮统一 `status==='succeeded' && previousImageId`；AppReleases 回滚改显式 isProd；envStore Env 接口加 promoteOrder。
- **e2e 验证**：环境 promoteOrder 回填（test=10/prod=20）+ promote 端点路由就绪 + OpenAPI 登记 + 核心 200。
- **留后续**：promote 跨级跳迁（test→prod 直达，现逐级）、蓝绿/金丝雀真实编排（耦合泳道归服务治理）、Pipeline 独立实体 + 审批门禁/自动晋升条件、registry catalog 镜像直接发布（仓库名无 appID 反查）。


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

### 内置 Git 后端 + 镜像库管理 UI（一站式完善）

一站式 PaaS 不让用户跳出平台。补齐「代码托管 + 镜像库管理」两块 UI，后端服务无头、管理 UI 全在 console-user：

- **内置 Git 后端（Gitea 无头）**：`deploy/devops/gitea.yaml`（独立 yaml，`kubectl apply` 部署，paas ns 与 postgres 同形 ClusterIP，SQLite+local-path 10Gi PVC，worker nodeAffinity，不建 ingress）。initContainer 幂等创建 paas-bot admin（password 来自 Secret `paas-gitea`，GITEA__* env 配置：关注册/OAuth/安装页）。镜像内网化 `hub.wang.dd:5000/devtools/gitea:1.22.6`（集群内 DooD Job 中转 daocloud，因 colima 到 registry 路由不通）。
- **Gitea API 客户端**（`internal/devops/gitea/client.go`）：纯 net/http + basic auth（paas-bot:password，避免 token 生成）；CreateRepo/GetRepo/GetTree/ListCommits/GetFileContent/CloneURLWithAuth；sentinel 错误（ErrRepoExists/ErrRepoNotFound/ErrGiteaUnavailable）驱动 handler HTTP 映射。
- **镜像库 v2 适配**（`internal/devops/registry/client.go`）：纯 net/http 调 `hub.wang.dd:5000`（复用裸 registry:2）；Catalog/Tags（HEAD manifests 取 Docker-Content-Digest）；ErrRegistryUnavailable/ErrRepoNotFound 降级。`New()` 容错补 `http://` scheme（`PAAS_REGISTRY` env 无 scheme，同时供 builder docker push 用）。
- **镜像库 UI 网络方案（core hostNetwork）**：dev 集群 Pod 网络与外部物理网隔离，core Pod 无法直访 `hub.wang.dd:5000`（registry client 503）。builder push 不受影响（经挂载 docker.sock 走节点 daemon）。helm chart `core.hostNetwork` 开关（`values-paas-k8s.yaml` 设 true）：core Pod `hostNetwork: true` + `dnsPolicy: ClusterFirstWithHostNet`（保留集群 DNS，gitea/postgres ClusterIP 仍可解析），用节点网络访问 registry。core pod IP 变节点物理网段，ingress（hermes）转发到 Service 不受影响。生产/普通部署保持 false。镜像代码未变时只需 `helm upgrade` 应用 hostNetwork，无需 docker build。
- **CodeRepo 扩展**：加 `Source`（internal/external，默认 external 兼容历史）+ `GiteaOwner`/`GiteaRepo`+ `CloneURL`（`json:"-"` 含 basic auth，builder 内部 Go 调用读，永不序列化防凭证泄漏前端）。internal 仓库：handler 调 Gitea CreateRepo + 回填 GitURL（展示，不含凭证）+ CloneURL（含 paas-bot:pass@）。PG migration 0001 code_repos 加 source/gitea_owner/gitea_repo/clone_url 列；**已部署 PG 实例 0001 不重跑**，新增 `0002_code_repos_source.up.sql`（`ADD COLUMN IF NOT EXISTS`）增量补列（全新部署 0001 已含，IF NOT EXISTS 跳过，无副作用）。
- **构建内网 clone**：builder runBuild 用 CloneURL 优先（internal）否则 GitURL（external）；`injectToken` 加 `@` 检测（含凭证 URL 跳过 token 注入），Gitea http CloneURL 原样透传，git 用 URL 内 basic auth clone（`http://paas-bot:pass@gitea.paas.svc.../repo.git`，不走公网）。天然契合，builder 核心零改动。
- **REST**：仓库浏览 `GET /api/applications/{id}/repositories/{rid}/{tree|commits|file}`（仅 internal，external 返 405）；`file` action 取单文件内容（`?path=Dockerfile` -> `GetFileContent` base64 解码返 `{path,size,content}`，驱动前端点击文件查看代码）；registry 实时视图 `GET /api/registry/repositories`（catalog）+ `GET /api/registry/tags?repository=<name>`（tag+digest，仓库名含 `/` 用 query 避免路径歧义）。复用 image:read 权限。`/api/registry/` 需在 mux 显式注册（`cmd/core` 加 `mux.Handle`，否则落兜底 404）+ OpenAPI Operation 登记。
- **构建 commit/message 真实化**：handler 创建 BuildRun 时，internal 仓库调 `gitea.ListCommits` 拿最新 commit 的**真实 sha + message** 填入 `b.Commit`/`b.Message`（替代 store 的 `mockCommit()`/`"mock: update "+branch`）。否则 builder script `if [ -z "$COMMIT" ]` 因 COMMIT 非空（mock）不 rev-parse HEAD -> commit/tag/message 全 mock（构建记录显示 `a3496eb7 / mock: update main` 与仓库真实 commit 不符，"看起来假"）。external 仓库或 Gitea 不可达回退 store mock（保持原行为）。
- **闭环验证（2026-08-03 全通）**：内置建仓(source=internal)-> 浏览(tree/commits)-> git push Dockerfile -> 构建触发(builder Job clone 内置 Gitea + docker build + push hub.wang.dd:5000 + 解析 PAAS_DIGEST)-> Image 落地(digest 与构建日志一致)-> 镜像库 UI(catalog/tags/digest)。文件点击查看内容（Dockerfile 显示 FROM/COPY/CMD）；构建记录真实 commit/message（`85e3ca86 / add Dockerfile`，非 mock）。
- **console-user**：应用详情「代码仓库」tab 加来源单选（内置 Gitea 创建/绑定外部 gitUrl）+ RepoBrowser 抽屉（el-tree 文件树从扁平 path 构建 + el-timeline 提交历史 + **文件叶子节点 @node-click 查看内容**：调 file 端点加载，展示文件头📄路径+关闭 + 等宽 pre 代码区，可滚动）；DevOps 中心「镜像库」tab 改 registry 实时视图（展开行按需加载 tag+digest，避免 N+1）。
- **留后续**：Webhook（Gitea push 自动触发构建）、registry delete 落地（registry:2 需启 `REGISTRY_STORAGE_DELETE_ENABLED=true`+GC）、租户级 Gitea org 隔离（当前 repo 名租户内唯一够用）、Git diff/MR/分支管理（完整 Git Web UI 替代）。

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
