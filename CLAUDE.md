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
| 可观测 | OpenTelemetry + Prometheus + Loki + Jaeger |
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
examples/                       # 平台示例（**独立 module** github.com/aitoys/paas-examples，非 Platform Core）
CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md LICENSE
```

> **示例与平台隔离**：`examples/` 是独立 Go module（`github.com/aitoys/paas-examples`），与主仓 Go 依赖完全解耦。示例（paas-shop/mcp-server/traffic-gen）是**平台的用户/消费者**，演示如何被平台纳管，不属于 Platform Core——业务领域逻辑绝不进平台 `cmd/`（判断标准同下「开发约定」）。示例不引用任何 paas 内部包（第三方依赖如 pgx/nats/otel 仅用于示例自身演示真实业务形态）。

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

Repository 接口是后端切换点——除 observability（惰性 mock，接真实 Prometheus/Loki/Jaeger 时再迁）外，**全 10 模块已迁 PostgreSQL**，按 `PAAS_DB_URL` 在「全内存」与「全 PG」两路径间切换（为空则纯内存、与现状一致、零依赖）：

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
- **数据面 SDK 纳管（zeus，/dp/ + paas-registry 插件，P1）**：服务治理 Instance 真源从「governance 手动注册 mock」切到 **K8s Endpoints**（readiness probe 驱动）。`internal/dataplane` 暴露 `/dp/` HTTP API（`GET /dp/services`、`GET /dp/instances?service=<name>` 从 K8s Endpoints 读 ready 实例 + signature、`POST/DELETE /dp/register` 声明服务元信息、`PUT /dp/heartbeat` 兼容保留），鉴权复用 `gateway.BearerAuth`（dp token = API Key，绑 tenant，`Authorization: Bearer`）。`sdk/paas-registry/`（**独立 module** `github.com/aitoys/paas/sdk/paas-registry`，不进主 go.mod 避免主仓引 zeus）实现 zeus `Registrar+Discovery+Watcher`，`init()` 注册 `paas://` scheme（URL `paas://paas-core.paas.svc/dp?token=<dp-token>`），GetService 2s 轮询 + signature 对比触发重发现（仿 zeus `examples/20-full-demo/gwdisc`）。controller 给 service 类型 Pod 注入 `PAAS_DP_ENDPOINT`/`PAAS_DP_TOKEN`/`PAAS_TENANT_ID` env（`PAAS_DP_TOKEN` helm values `dataplane.token`，cmd/core manager.go 读 env 注入 reconciler）。RBAC 补 `endpoints get/list/watch`。非集群部署（无 clientset）`/dp/instances` 降级返空（不 panic，与 observability real 同构）。**zeus 已公开发布**：`sdk/paas-registry` go.mod `require github.com/go-zeus/zeus v0.2.0`（公开 module，goproxy.cn 可解析，含 SDK 消费的 `ServiceEntry.Snapshot()` API），无本地 replace，SDK 可外部构建。熔断真实 stats 采集、配置 watch 长连接、应用级 metrics/traces 埋点、多租户 ns 隔离、per-workload token（现平台级 sk-acme-admin 演示）留后续。
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
- **留后续**：I4 parseDigest 信任 Pod 日志、I5/I6 各 client CheckRedirect 纵深、I13/I14 refresh rotation + logout 撤销（jti 黑名单，架构级）、I16 限流多副本 Redis、Tempo span 详情、Vendor 改 BaseURL/凭证后存量通道自动同步。

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

### 深度检测第 17 轮（构建日志 SSE + 模板 CRUD + 治理契约两路审计，2026-08-09）

两路 agent 深审近期改动（构建日志 SSE 资源泄漏 / 模板 CRUD + governance Route.Host + configcenter Namespace.ServiceID 契约），修 3 Important + 记录 4 Minor：

- **typed-nil 装箱陷阱（Important）**：`NewK8sBuildLogStreamer` 原返 `*K8sBuildLogStreamer`，cs nil 时 `(*T)(nil)` 装箱为非 nil 接口 → handler `h.logStreamer != nil` 误判 → 降级分支不触发，非集群部署前端等 30s 才收到「Pod 未就绪」而非「非集群部署」。改返 `BuildLogStreamer` 接口，cs nil 时返真 nil 接口（与第 4 轮 admin dataservice restarter typed-nil 同类陷阱，Go 接口装箱语义）。
- **pipeline Retry deploy 必失败（Important）**：`execDeploy`/`execBuild` 用 `sr.Input = map{...}` 整覆盖 Input，丢初始解析参数（envId/buildArgs/branchOverride）。deploy 在 PollWorkloadReady 失败后 run failed，Retry 不重置 Input → 重新执行 execDeploy 时 envId 为空 → fail-fast「deploy stage 缺 envId 参数」。改**合并而非覆盖**：保留初始 Input 供 retry，追加运行时产物（imageId 供 priorBuild 链/前端、buildRunId 供前端拉日志），releaseId 走 Output。补 `TestEngineRetryDeployAfterPollFail` 回归（fakeReleaser 加 pollErr 字段）。
- **configcenter CreateNamespace 悬挂引用（Important）**：POST namespace 不校验 ServiceID 归属，可创建指向不存在/已删/跨租户 serviceID 的脏数据。加 `ServiceLookup` 接口（依赖倒置，避免 configcenter→governance import），handler CreateNamespace 时校验非空 ServiceID 存在+属本租户，cmd/core `ccServiceLookup` 桥接 `governance.GetService`（按 ctx tenant 过滤，跨租户/不存在返 false 不泄漏）。补 `TestCreateNamespaceRejectsDanglingServiceID`。
- **Minor（第 18 轮已修）**：① pipeline store_pg「租户自定义需 ctx 租户匹配」注释措辞误导 super_admin 跨租户语义 -> 改明确「super_admin 亦不跨租户管租户自定义（资源运维在租户内）」；② governance UpdateRoute PUT 全字段绑定 -> 加方法头注释文档化混合语义（必填字段非空才覆盖防误清，可清空字段直接覆盖）；③ SSE 读循环无显式 ctx.Done 分支 -> 加 `ctx.Err()!=nil` 区分「客户端断连静默退出」与「流正常结束发 end」；④ PipelineRunView readyState 判定冗余 -> 移除，error 统一走 close+降级提示。
- **确认无问题项**：builtin 模板双重保护（memory 显式拒 + PG WHERE builtin=false 兜底）✓；POST/PUT 强制 Builtin=false 防伪造 ✓；4 admin 端点 super_admin 守卫全挂载 ✓；Route.Host/Namespace.ServiceID SQL 列序严格对齐 ✓；migration 幂等（ADD COLUMN IF NOT EXISTS）✓；多租户隔离全路径 tenant_id 过滤 ✓；OpenAPI 全登记 ✓；{data:T} 契约统一 ✓；SSE 越权（本租户校验）/ctx 传播/defer close/EventSource 生命周期/SSE 缓冲（X-Accel-Buffering:no + Flusher）全正确。


### 深度检测第 18 轮（builtin 模板版本升级 + 第 17 轮 Minor 收口，2026-08-09）

承接第 17 轮留后续，补齐 dogfooding 暴露的 builtin 模板升级机制（生产升级路径实质缺口）+ 收口 4 个 Minor：

- **builtin 模板版本升级机制（Important）**：第 17 轮 dogfooding 暴露「`BuiltinTemplates()` 改了但 PG seed 幂等不覆盖已有记录」，此前每次改 builtin 要手写 migration UPDATE 补救（如 0020）。修复：
  - `PipelineTemplate.Version int` 字段（破坏性改动 +1）；`BuiltinTemplates()` tpl-ci/tpl-cd 标 `Version:1`。
  - `TemplateRepository.ReplaceBuiltinTemplate(ctx, t)` 接口方法（平台级 seed 专用，UPDATE WHERE builtin=true 绕过 builtin 拒改保护，仅覆盖 stages/name/description/version，不动 kind/tenant_id）；memory + pg 双实现。
  - `SeedTemplates` 版本比对：CreateTemplate 返 ErrTemplateExists 时 GetTemplate 比对，代码 Version > DB Version 则 ReplaceBuiltinTemplate 覆盖；同 Version 不动。
  - `GetTemplate` 放宽：无租户 ctx（平台级 seed）仅可访问平台预置（`tenant.TenantFrom` 取 tid，tid="" 时 SQL `tenant_id IS NULL` 只匹配平台预置，租户自定义 NotFound 不泄漏）；memory + pg 同步。此前 tenantOrErr 硬拦截致 seed 升级路径 GetTemplate 永失败 -> 版本升级永不触发（被 continue 吃掉）。
  - migration 0025：`ADD COLUMN version INT NOT NULL DEFAULT 0` + `UPDATE WHERE builtin=true AND version=0 SET version=1`（存量 builtin 回填为 1，与代码 Version=1 对齐，启动不误覆盖）。
  - 测试：`TestSeedTemplatesUpgradesBuiltinOnVersionBump`（旧 v0 -> 代码 v1 覆盖 stages/name/description/version）+ `TestSeedTemplatesSameVersionNoOverwrite`（同 v1 不动）。下次破坏性改 builtin 只需 `Version: 2`，启动自动覆盖。
- **第 17 轮 Minor 收口（4 项）**：① pipeline store_pg `UpdateTemplate` 注释改明确「super_admin 亦不跨租户管租户自定义（资源运维在租户内，GetTemplate 已过滤跨租户 NotFound 不泄漏）」；② governance `UpdateRoute` 加方法头注释文档化混合语义（必填字段 Path/ServiceID/Methods 非空才覆盖防误清，可清空字段 StripPath/Enabled/Host 直接覆盖允许清空）；③ devops SSE 读循环加 `ctx.Err()!=nil` 区分「客户端断连静默退出不发 end」与「流正常结束发 end」（避免对已断连连接 Write）；④ `PipelineRunView.vue` 移除 `readyState===CLOSED` 冗余判定，EventSource error 统一走 close+降级提示。
- **abort 后 stage_runs 残留 running 修复（数据一致）**：`Engine.Abort` 原只标 run=aborted + cancel，不清理残留 StageRunning 的 stage_runs -> run=aborted 但某 stage=running 数据不一致。加 `StageAborted` 常量 + Abort 时遍历 stage_runs 把 StageRunning 标 StageAborted + FinishedAt（已终态 stage 不动）+ 前端 `stageStatusType`/`stageIcon` 加 aborted case（info/⏹）。补 `TestAbortClearsRunningStage` 回归。
- **构建验证**：`go test ./...` 全绿（含 2 新测试）+ 三套 `pnpm build` 通过。


### 流水线 webhook 触发器（Git push 自动触发，2026-08-10）

承接「流水线功能完整」（总体目标），补齐触发器最大缺口：此前仅 manual（UI 点触发），生产需 Git push 自动触发构建。设计参考 Gitea/GitHub webhooks（push event -> 触发 CI）。

- **PipelineTrigger 落地**：`Token` 字段 `json:"token,omitempty"`（持久化到 trigger JSONB 列，存量列复用无需 migration）；触发类型常量 `TriggerManual/Webhook`（cron 留后续，type 校验拒）；`WebhookPath(pid)` helper。
- **token 生成 + 安全**：createPipeline/updatePipeline 调 `normalizeTrigger`（webhook type 无 token 时 `crypto/rand` 生成 32 字节 hex）；update 时前端不传 token -> 保留原 token（避免每次保存重置致 webhook URL 失效）；token 用 `subtle.ConstantTimeCompare` 常量时间比较（防时序枚举）。get 返回明文 token（同租户可见，designer 展示 webhook URL 配 Gitea）；list 清空 token（`maskTrigger`，列表概览不需要）。
- **webhook 端点**（`POST /api/webhooks/pipeline/{pid}?token=<Token>`，**无 auth 中间件**，token 鉴权）：接收 Gitea/GitHub push event（`ref`/`after`），解析 branch（`refs/heads/` 前缀）+ commit sha；非分支引用（tag/PR）静默返 200（避免 webhook 源重试）；分支 glob 匹配（`path.Match`，`Trigger.Branch` 空=全部）；`GetPipelineAny`（Repository 新增方法，跨租户按 ID 查，webhook 无 tenant ctx）+ 派生 pipeline 租户 ctx（后续 GetTemplate/CreateRun 要求 tenant）；触发 run（`TriggerWebhook`）。
- **触发逻辑复用（DRY）**：抽 `triggerRunInternal(ctx, appID, pid, resolved, branch, commit, version, trigger)` —— manual/webhook 共用核心（validateDeployEnvs + 单实例串行 + CreateRun + engine.Start）；manual 路径额外 perm + prod:write 校验（调 triggerRunInternal 前），webhook 跳过 prod:write（CD pipeline 靠 approve 门禁兜底）。
- **审计**：webhook 无用户身份，`recordAuditCtx`（ctx 版本，webhook 专用）actor="webhook" + pipeline 租户作 tid（adapter 对空 tenant 兜底归 platform）。
- **路由 + cmd/core**：`mux.Handle("/api/webhooks/pipeline/", pipelineHandler)`（无 auth 包装）；不登记 OpenAPI 公开契约（webhook 是接收端点，URL 在 pipeline 响应返回，非用户 curl 调）。
- **前端**（`PipelineDesigner.vue`）：加「触发方式」section（manual/webhook radio + 分支 glob 输入）；webhook type 时展示完整 webhook URL（baseURL+token，可复制）+ Gitea 配置指引（Content-Type: application/json）；save 时 trigger 写回。创建默认 manual，designer 改 webhook（首次切自动生成 token）。
- **测试**：`TestPipelineWebhookTrigger`（创建生成 token + get 返回 token + list 清空 + 正确 token 触发 run + 错误 token 401 + 不匹配分支静默 + 非 webhook pipeline 400）。
- **留后续**：Gitea webhook 自动注册（创建 pipeline 时调 Gitea API 注册 webhook，免用户手动配 URL）、cron 定时触发（scheduler）、webhook secret 轮转、push event 富解析（commits message 触发 skip ci）。


### 可观测真实化 + 治理实例接入数据面 + 应用监控全面聚合（2026-08-10）

承接「服务治理与可观测功能完善」，解决用户端治理/可观测无数据 + 应用监控不全面。三处根因修复 + 体验聚合：

- **internal/metrics 装配（此前漏接）**：`internal/metrics` 包已建但从未接入 cmd/core（Meter.Inf 为 nil、/metrics 端点缺失、无中间件）。修复：`metrics.NewRegistry()` + `meter := &gateway.Meter{Inf: metricsReg.Inference()}` 注入；`mux.Handle("/metrics", metricsReg.Handler())`；中间件链 `metrics.HTTPMiddleware(reg, paths)(recovery(csrf(mux)))`（metrics 在 recovery 外层才能记录 panic-500 状态）；`otelhttp` 过滤 `/livez /openapi.json /docs /metrics` 免噪音。paas-core Prometheus scrape 目标改 :8080/metrics（`PAAS_METRICS_ADDR=0` 时 8081 关闭），relabel label `__meta_kubernetes_pod_label_app_kubernetes_io_name`（非 `app`，容器 label 点/斜杠→下划线）。
- **应用级指标查询根因（cAdvisor 不带 pod 自定义 label）**：cAdvisor 节点级抓取不带 pod label，旧 `{paas_aitoys_app=...}` 过滤匹配不到。弃 KSM join（镜像国内源拉不到）。改 **WorkloadLister pod 正则**方案：注入 `observability.AppWorkloadLister`（cmd/core `workloadLister` 桥接 `workload.Repository.List`），应用级查询解析 app→工作负载 ID 列表，PromQL `sum(rate(container_cpu_usage_seconds_total{namespace="paas-<tenant>",pod=~"wl-<id>-.*|...",container!="POD"}[5m]))`。命名约定：Deployment 名=工作负载 ID，Pod=`<id>-<rsHash>-<podHash>`。dataservice 走 `pod="<ds-id>-0"` container=main。memory mock path 保持惰性。
- **应用级日志查询根因（promtail 不提取自定义 label）**：同因，Loki 查询改 `{namespace="paas-<tenant>",pod=~"wl-<id>-.*"}` + 内容正则 best-effort level 过滤（Pod stdout 无结构化 level label）。未注入 lister / app 无工作负载降级返空（不 panic）。
- **治理实例接入数据面真源**：治理服务详情 `instances:[]`（手动注册表空），而 `/dp/instances` 有真实 K8s Endpoint。约定 **governance Service.Name == K8s Service 名 == 工作负载名**（paas-shop-bff 等 seed 已对齐）。`governance.InstanceDiscoverer` 接口（`DiscoverInstances(ctx, ns, serviceName, lane)`）+ `WithInstanceDiscoverer` 选项；serveServiceItem 优先返 discoverer 的 ready 实例（readiness probe 通过），无/未注入回退手动注册表（向后兼容）。cmd/core `govInstanceDiscoverer` 桥接 `dataplane.EndpointsReader`（Addr=IP:Port，Status=healthy，携带 pod 名入 Meta）。lane 透传（L2 跨泳道降级发现复用）。`tenant.Namespace(tid)` 多租户隔离（reader 内再按 Service tenant label 校验）。
- **应用监控全面聚合（AppObservability 内部 tab）**：原单页改 el-tabs 两类，聚合应用全部相关监控：
  - **应用实例 tab**：CPU/内存/RPS/延迟 4 指标卡（sparkline）+ 副本就绪状态条（`workloads` 聚合 ready/want）+ 最近日志 + 最近链路（span 展开）。
  - **依赖资源 tab**：应用绑定的数据服务（db/cache/mq/storage/vector/search，过滤 models/knowledgebase）经 `/api/dataservices` 解析名→ID，逐个拉 `targetType=dataservice` CPU/内存 sparkline + 状态 tag。
  - **批判性取舍（不造伪指标）**：不设独立「业务监控」tab——平台无法获知应用业务 KPI（订单/收入需应用自定义埋点），强行造伪指标不诚实；推理 token/请求消耗已归「用量」tab 避免重复；流量健康（RPS/延迟/副本就绪）即业务相关运行视图，归「应用实例」。ApplicationDetail 透传 `:bindings` 给 AppObservability 免重复 fetch app。
- **横切**：多租户隔离贯穿（metrics/logs/governance 全按 `paas-<tenant>` ns + tenant label）；无 K8s clientset 全路径降级返空不 panic（与 observability real 同构）；10s 轮询 silent + onUnmounted clearInterval。
- **e2e 验证**：paas-shop CPU=0.0009 核/内存 110MiB 真实时序；副本就绪 1/1 真实；shop-db CPU=0.0004/内存 36MiB 真实；治理服务详情返真实 Endpoint 实例（192.168.196.189:8080）。
- **留后续**：应用级业务指标埋点（core 加 `paas_rps` 等 Prometheus 埋点供 RPS/延迟真实化，现 rps/latency 仅 memory 模式有数据）、promtail 自定义 label 提取（DS pod label `app=<appID>` 供精确应用级日志）、治理 Service 与 Workload 显式绑定（现靠名同构约定）、K8s Service 名校验防用户建治理服务填不存在的名。



### DevOps 发布体验改造（指挥台 + 发布流水线 + 一键发布，2026-08-06）

DevOps 中心从「只读监控大屏」升级为「CI/CD 指挥台」，补齐发布流水线逐级提升，发布点击数从 6+ 降到 2：

- **Environment 发布流水线阶序**：`Environment.PromoteOrder int`（test=10/prod=20 默认，0=不参与）；`DefaultPromoteOrder(type)`；`Repository.NextPromoteTarget(ctx, envID)` 返同租户内 order 严格大于当前的最小阶序环境（同 order 取 id 最小，确定性），最高阶返 `ErrNoPromoteTarget`。`EnvPromoter` 依赖倒置接口供 devops 注入。migration `0009_env_promote_order`（ADD COLUMN + 存量按 type 回填，幂等）。environment handler Create 在未指定 order 时按 type 填默认（开箱即用）。
- **Release 晋升链**：`Release.PromotedFrom` 字段（非空=由 promote 产生，追溯晋升链）；`Repository.PromoteRelease(ctx, srcID, targetEnvID)` 复用 CreateRelease 编排（找/建基线 Workload + 回滚指针）+ 标 PromotedFrom；migration `0010_release_promoted_from`。**不引入 Pipeline 实体**（YAGNI，promote = 新环境产生新 Release + 来源标记）。
- **promote 端点**：`POST /api/releases/{id}/promote`（serveReleaseAction 加 promote 分支；handler 算 target env + 目标 prod 走 `allowProd` prod:write 校验 + 调 store.PromoteRelease）。OpenAPI 登记。
- **DevOps 中心指挥台化**（`console-user/DevOps.vue` 重写）：① 新增**流水线 tab（默认）**——按 app 分组渲染 env 阶序横向矩阵（`[test:v1.2✓] → [staging:v1.1✓] → [prod:v1.0] 提升→`），每格最新 succeeded release + 「提升」按钮（末列无）；② 构建行加「重新构建」（用原 repoId 调 POST）；③ 发布行加「提升」+ 应用列可点跳详情；④ releases 轮询（10s，原仅 buildruns 5s）；⑤ 回滚/提升按**目标 env.type 显式 isProd**（覆盖顶栏 scope，防不一致削弱防护）。
- **一键发布 + 多环境勾选**：① `AppBuilds` 构建成功行加「发布」按钮（复用镜像 tab `emit('pick')` 机制，父切发布 tab 预选镜像，构建→发布断链修复）；② `AppReleases` 创建弹窗环境改 `multiple` 多选，`create()` 循环 POST + Promise.allSettled 聚合（部分成功列出失败 env），含 prod 勾选时 `confirmDangerous(isProd:true)`。
- **统一契约**：回滚按钮统一 `status==='succeeded' && previousImageId`；AppReleases 回滚改显式 isProd；envStore Env 接口加 promoteOrder。
- **e2e 验证**：环境 promoteOrder 回填（test=10/prod=20）+ promote 端点路由就绪 + OpenAPI 登记 + 核心 200。
- **留后续**：promote 跨级跳迁（test->prod 直达，现逐级）、蓝绿/金丝雀真实编排（耦合泳道归服务治理）、registry catalog 镜像直接发布（仓库名无 appID 反查）。（Pipeline 独立实体 + 审批门禁已落地，见下「DevOps 流水线引擎」）


### DevOps 流水线引擎（模板+绑定：应用零操作 + 占位符自动解析，2026-08-08）

承接「发布体验改造」留后续项「Pipeline 独立实体 + 审批门禁」，补齐可自定义的一键式「变更->构建->发布->部署->测试->写基线」流水线。**模板+绑定模型**（参考 Argo WorkflowTemplate+Workflow / Tekton Pipeline+PipelineRun / GitLab include / GitHub reusable workflow）：平台预置通用模板，应用建即自动绑定默认流水线（CI/CD 各一），应用零操作即可用；占位符参数化让同一条模板服务所有应用，避免每个应用重复创建雷同流水线。计划见 `docs/superpowers/plans/2026-08-08-pipeline-designer.md` + `docs/superpowers/plans/2026-08-08-pipeline-template-binding-refactor.md`。

- **4 实体 + 模板/绑定分离**：`PipelineTemplate`（平台预置 builtin + 租户自定义，含 `Stages []StageDef` + `Params []ParamDef` 声明占位符）+ `Pipeline`（Application 1:N，**绑定**而非复制--只存 `TemplateID` + `ParamOverrides`，不存 Stages）+ `PipelineRun`（一次运行，异步状态机载体）+ `StageRun`（单阶段执行记录，输出链载体，`Input` 存解析后的 stage.Params）。`internal/devops/pipeline/` 包。
- **7 阶段类型**（2026-08-09 deploy/release 分离）：`build`（构建镜像）/ `deploy`（部署到 env×lane，产部署记录可回滚，**不打版本**）/ `test`（smoke 自动 + manual 人工确认）/ `approve`（人工审批门禁）/ `release`（**打版本号里程碑**：git tag + Image.Version + 给本 run 部署记录回填 version，**不部署**）/ `promote`（重定义=把本 run 已部署镜像 Deploy 到下一阶序环境基线，复用 deploy 逻辑）/ `baseline`（瘦身=只合并主干，打版本归 release）。**deploy 与 release 正交**：deploy 是运行态动作，release 是版本管理动作。模板 `tpl-ci`（build→deploy test 泳道→test，无 release）/ `tpl-cd`（approve→deploy prod 基线→release→baseline）开箱即用。
- **占位符参数化**（应用零操作核心）：模板 stage params 用 `{{app.env.test}}`/`{{app.env.prod}}`/`{{app.repo}}`/`{{run.branch}}`（分支独立泳道，2026-08-09 加）占位，触发 run 时 `ResolveStages(ctx, tplStages, overrides, resolver, appID, branch)`（包级函数）经 `ParamResolver` 接口（`EnvByType(appID, envType)` 取应用该类型环境 ID / `InternalRepoID(appID)` 取应用内置仓库 ID）按当前应用 + 触发分支解析为真实值写入 `StageRun.Input`。同一条模板服务所有应用，应用无需手配环境/仓库。`ParamDef` 声明占位符类型供前端表单。
- **engine 用 StageRuns 推进**：`Advance` 读 `run.StageRuns[run.CurrentStage].Input` 作 stage.Params（不再加载 Pipeline 实体），run 创建时已解析固化，运行期不依赖模板变更（模板改了不影响在跑的 run）。
- **buildArgs 透传**（多服务构建）：build stage 的 `buildArgs` map（如 `{"SERVICE":"product"}`）经 `stage.Params["buildArgs"]` -> `buildBridge.CreateBuildRun` -> `devops.BuildRun.BuildArgs` -> `builder.Params.BuildArgs` -> K8s 脚本 `BUILD_ARG_FLAGS` env（`formatBuildArgs` 拼成 `--build-arg K=V`，校验 key `^[A-Za-z_][A-Za-z0-9_]*$` / value `^[A-Za-z0-9_.:/-]+$` 仅安全字符防 shell 注入，不安全字符跳过）/ Real 模式 os/exec args 数组（无需校验，参数数组无 shell 注入）。应用级覆盖经 `Pipeline.ParamOverrides["0.buildArgs"]`（key 格式 `<stageIdx>.buildArgs`，`getStringMap` 处理 JSON 反序列化的 map[string]any）。PG `build_runs.build_args` JSONB 列持久化（migration 0021，`marshalStrMap`/`unmarshalStrMap` nil 安全）。
- **默认 binding 自动建**（OnAppCreate hook）：`application.Handler` 加 `OnAppCreate` 回调（best-effort，log 不阻断），cmd/core 装配 `defaultPipelineBinder`（建 tpl-ci/tpl-cd 两条 binding，`ErrPipelineExists` 忽略幂等）。新建 app 即得 CI/CD 两条流水线，应用零操作。已有 app 用 `scripts/seed-default-pipelines.sh` 补建。
- **CI/CD 解耦**：deploy stage 的 `imageSource`（`priorBuild` 本流水线前序 build 产出 / `selected` 指定 imageId / `latestReady` app 最新 ready Image）--CD 消费 CI 产物，不强制重构建。
- **异步状态机**：`RunRunning/Paused/Succeeded/Failed/Aborted` + `StagePending/Running/Success/Failed/Waiting/Skipped`；engine goroutine 推进，approve/test-manual 暂停等审批。
- **stage 输出链**：build.Output.imageId -> deploy(priorBuild).Output.releaseId/workloadDomain -> release.OutVersion（版本号）-> promote/baseline（mergeSha）。Output map 透传，前端时间线展示。release stage 从前序 deploy 收集 OutReleaseID 批量 `SetVersion` 回填版本 + 调 `Publish`（gitea.CreateTag + Image.Version）。
- **单实例串行**：`ux_pipeline_runs_active` 部分唯一索引（WHERE status IN running/paused）+ `HasActiveRun` + `ErrActiveRunExists` 409，防同 pipeline 并发运行。
- **依赖注入桥接**（cmd/core `pipeline_adapters.go`）：`ParamResolver`/`RepoResolver`/`EnvTypeResolver`/`PromoteTargetTypeResolver`/`GiteaMerger`/`BuildRunner`/`Releaser`/`AuditRecorder` 接口，桥接 application/devops/environment/security 既有一等公民，pipeline 包零业务依赖倒置。
- **prod:write 横切**（关键安全）：deploy stage 目标 prod 或 promote stage 链路 target prod 均需 `PermProdWrite`（`allowProdFlow` 静态预演 promote 链，防 developer 经 [deploy test, promote] 绕过）。deploy envId 必填 fail-fast（400，避免注定失败的 run 占串行槽位）。
- **REST + OpenAPI**：`/api/applications/{id}/pipelines`（CRUD + run，composite 分发）+ `/api/pipeline-templates` + `/api/pipelineruns`（list/get/approve/abort），11 operation 登记。
- **前端设计器**（`PipelineDesigner.vue` 重写为参数覆盖器）：显示模板 stages 只读 + `paramOverrides` 覆盖表单（deploy.envId select / build.branchOverride / approve.message 等）+ 生产环境标红 + save 前 `validateDeployEnvs` 预校验。应用只调覆盖参数，不重写 stage 序列（模板归平台治理）。
- **前端运行视图**（`PipelineRunView.vue`，2026-08-09 加日志展开）：stage 时间线（状态着色 + 输出链展示 imageId/releaseId/domain/version/mergeSha + error + **lane tag**）+ stage 卡片可点击展开**日志区**（build 展开调 `GET /api/buildruns/{id}` 拉全量 BuildRun.Log 缓存展示，其它 stage 用 `StageRun.Log`，monospace + 自动滚到底）+ 5s 轮询（终态自停）+ approve（paused+approve/test-manual 时显「批准继续」）+ abort。
- **CD 触发增强**（`AppPipelines.vue`）：CI 直接默认 branch 触发；CD 弹窗收集 version（baseline 写入）+ branch；含 prod deploy 走 `confirmDangerous(isProd:true)` 二次确认。
- **DevOps 运行记录 tab**（`DevOps.vue`）：跨应用最近 PipelineRun 表格（应用/状态/当前阶段/分支/版本/时间）+ 10s 轮询，点应用跳详情。
- **e2e 验证（2026-08-08 模板+绑定重构）**：新建 app 自动建 2 条 binding（OnAppCreate ✓）；占位符 `{{app.env.test}}`->env-acme-test、`{{app.env.prod}}`->env-acme-prod-bj 自动解析 ✓；approve 暂停/恢复 ✓；engine 用 run.StageRuns 推进 ✓；developer 触发含 prod deploy 的 pipeline 403 forbidden prod:write ✓。
- **deploy/release 分离 + 泳道 + 实时日志（2026-08-09 L1 落地，spec `docs/superpowers/specs/2026-08-09-pipeline-deploy-release-lane-design.md`）**：dogfooding 暴露三个产品模型问题（deploy 把部署/发布焊死 / 测试环境无正确抽象 / 运行看不到日志），L1 重设计：
  - **deploy/release 解耦**：`deploy` 产**部署记录**（可回滚，不打版本）；`release` 打**版本号里程碑**（git tag + Image.Version + 批量回填部署记录 version，不部署）。两者正交。测试流水线只 deploy 不 release；生产 deploy(prod) 后 release 打版本。
  - **泳道（lane）作 deploy 一等参数**：`deploy(env, lane)`，默认 `default` 基线。测试联调用 `deploy(env=test, lane={{run.branch}})`——每分支独立泳道互不干扰。**L1 数据模型落地**（Workload 找/建含 lane 维度：`(tenant, app, env, lane)`；`Release` 加 `LaneID`/`SourceRunID`；`workload.List(ctx, envID, appID, laneID, wtype)` 空串不过滤）。**L2 流量联调独立切片**（数据面 `/dp/instances?service=x&lane=feature-x` 优先返泳道实例，缺失降级 default + `x-paas-lane` header 染色），L1 已锁接口（Workload/Release 带 LaneID），L2 直接消费不改流水线。
  - **实时日志**：`StageRun.Log` 字段（engine 各 stage `logf(sr, fmt, args...)` append 关键事件：构建提交/部署 env×lane/Workload 就绪/探活/打 tag/合并）；build 全量日志走独立 `GET /api/buildruns/{id}`（不重复存 StageRun.Log 避免膨胀）。前端 stage 卡片可展开日志区。PG migration 0022（releases lane_id/source_run_id + images version + stage_runs log）。
  - **Releaser 接口扩展**（`pipeline_adapters.go`）：`Deploy(ctx, appID, envID, lane, imageID, sourceRunID) (Release, domain, err)` 找/建基线 Workload + UpdateImage + 产部署记录；`Publish(ctx, appID, imageID, version, commit) (tagSha, err)` 打 tag + Image.Version；`SetVersion(releaseIDs [], version)` 批量回填；`MarkSourceRun(id, runID)` 部署记录追溯。PromoteRelease 隐式走基线 lane（CreateRelease 空 lane 兜底 LaneDefault）。
  - **dogfooding 验证（2026-08-09 paas-shop CI 端到端）**：build（k8s 真实构建产 digest）→ deploy（PollWorkloadReady 读实时 Ready，Workload 就绪 + domain）闭环成功，logf 日志完整透传 env/lane/Workload/domain，lane=main（`{{run.branch}}` 解析）+ 无 release stage（测试不打版本）全过。**dogfooding 暴露并修复 4 个既有数据面 bug**（均 L1 首次新建 lane Workload + 从头建 Deployment 才暴露，之前 default lane Workload 已存在未触发）：①`builder.Result` 不回传 Registry（Build 值传递修改副本，store 用 RegistryOrDefault(p) 拿默认 registry.paas.local）→ Image.Registry 与实际 push 不一致，commit 541c0f8 加 Result.Registry 回传；②Image.Registry 缺 repo 段（reconciler 拼 `<registry>:<tag>` 缺 `/appID` 致 InvalidImageName）→ store 拼 `res.Registry + "/" + p.AppID`（与 seed `registry.paas.local/app-cs` + ImageRef 对齐）；③PollWorkloadReady 读 PG Repository Ready（反向同步留后续）永远 0 → 注入 workload.StatusReader 用 FillStatus 读 K8s 实时 Deployment readyReplicas（commit d5d435c）；④FillStatus 值语义 bug（`FillStatus([]Workload{wl})` 传含 wl 副本的 slice，原地改 slice 元素不影响外层 wl）→ `wls:=[]Workload{wl}; FillStatus(wls); wl=wls[0]` 读回（commit eb74b30）。test 探活失败归 paas-shop 应用配置（Workload port=0 无 Service + tpl-ci test path=/livez vs 应用 /healthz 不匹配），非 L1 代码缺陷。
  - **builtin 模板版本升级机制（第 18 轮已实现）**：`PipelineTemplate.Version` 字段 + `ReplaceBuiltinTemplate` 接口方法（平台级 seed 专用，绕过 builtin 拒改保护）+ `SeedTemplates` 版本比对（代码 Version > DB Version 时覆盖 stages/name/description/version）+ `GetTemplate` 放宽无租户 ctx 仅访问平台预置（seed 升级路径）+ migration 0025（version 列 + 存量 builtin 回填 1）。下次破坏性改 builtin 只需 `Version: 2`，启动自动覆盖，无需手写 migration UPDATE（0020 那种补救方式退役）。
- **留后续**：内存路径 `PollWorkloadReady` 必超时（dev trade-off，仅 K8s 可端到端 CD 闭环）；webhook/cron 触发器（现 manual）；租户自定义模板编辑器（现从 builtin 复制）；promote 跨级跳迁；流水线运行历史分页；Vendor 改配置后存量 binding 自动同步；**L2 泳道联调**（数据面染色降级 + 跨泳道服务发现，独立 spec）；build 日志流式追加（现首次展开拉取缓存，不随构建进度刷新）；release stage 独立「版本发布记录」实体（含 changelog/artifacts，现用 Image.Version + PipelineRun.version + git tag 表达）。（abort 后 stage_runs 残留 running 已修，见第 18 轮。）

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

### 配置中心（治理四件套：运行时动态配置，应用维度主路径）

版本化动态配置，**与 appconfig（工作负载级、静态、重启注入）正交**：跨实例共享、版本/发布/回滚、客户端按版本发现。独立于物理环境（namespace 逻辑隔离），不接 `EnvTypeResolver`。**应用维度改造（2026-08-29，spec `docs/superpowers/specs/2026-08-29-configcenter-app-centric-design.md`）**：Namespace 双 scope——`app`（应用派生，`EnsureByApp(ctx, appID)` 懒建，name=`app-<appID>`，用户零 namespace 心智）/`shared`（跨应用共享，治理方手工建，现行为保留；存量迁移为 shared）。应用维度端点权限 `application:read/write` + AppGuard `write` 动作；shared 端点维持 `governance:read/write`：

- **应用维度 REST**（`internal/configcenter/app_handler.go`，挂 application composite `dynamic-configs` 分发）：`GET/POST /api/applications/{id}/dynamic-configs`（列表自动懒建/upsert）+ `DELETE .../items/{itemId}`（归属校验防跨 ns 越权）+ `POST .../publish`（记审计 `configcenter_publish`）+ `GET .../publishes|published`（只读走 FindAppNamespace 不懒建）。删应用级联清 app ns（appCascadeDeleter `cc` 字段）。
- **按应用名发现**（数据面主入口）：`GET /api/configcenter/apps/{appName}/published`（`AppLookup` 依赖倒置桥接 application.List 按名匹配，租户过滤）；响应裸 JSON `{published,version,snapshot}`（不含 publishId）；未知应用/无 ns/无 active 三路统一 `{"published":false}` 不泄漏。
- **前端**：应用详情「配置」tab 加「动态配置」子区（`app-tabs/AppDynamicConfigs.vue`：draft KV + 发布/回滚 useDangerConfirm + 当前生效 + 版本历史）；ConfigCenter 页双视图（默认「按应用」/「共享配置」保留 ns CRUD，`?serviceId=` 兼容）。
- **dogfooding**：paas-shop chatbot 接入（examples `chatbot/dynconfig.go`）——启动拉取 + 60s 轮询按名发现，version 比对原子替换（RWMutex），消费 `welcome_message`/`recommend_topk`，失败静默降级默认值不 panic。e2e 已验证：upsert→publish→按名发现→跨租户不泄漏→chatbot 热更新。
- 留后续：EnsureByApp 不校验 app 存在性（孤儿 ns，仿 ServiceLookup 可补）、AppIDByName O(N) 优化、shared 应用侧引用（Nacos common.yml 模式）、长连接 watch、灰度下发、key/value 长度上限、json 类型不校验、发布历史上限、前端快照 KV 组件抽取。
- **10 轮深度审计修复（2026-08-30）**：修 PG 并发双 active（migration 0037 partial unique index `uq_cc_publishes_ns_active` + 存量脏数据保留最大 version 清理 + RollbackPublish 读 target 加 `FOR UPDATE`）；审计全覆盖（ns 维度 Handler 注入 `AuditFunc`，6 写操作 `configcenter_*` 审计 + app 维度补 item upsert/delete）；死端点收口（删 `GET /api/configcenter/publishes` 注册+登记，补 ns 级 publishes / items delete 两条漏登 Operation）；领域 sentinel 化（`ErrNamespaceNameTaken` 等，409 判定删 strings.Contains 中文匹配）；rollback/itemDelete 不懒建（FindAppNamespace 不存在 404）；精简（memory Seed 三死函数 + pg Count 三方法 + 锁外预检删除 + `itemBelongsToNS`/`writePublishedJSON` helper 收敛）；前端修 `cur` 未解包 `{data:T}`（详情页写操作全挂）、加载失败不再静默、radio 切换清 `?serviceId=` 残留、确认强度统一不随顶栏 env 漂移。

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
- **接真实后端（env 开关）**：`internal/observability/real`（MetricsStore/LogsStore/TracesStore 纯 net/http 适配 Prometheus/Loki/Jaeger HTTP API）+ `internal/observability/compose`（聚合 Repository）+ 细粒度 reader 接口（`MetricsReader`/`LogsReader`/`TracesReader`/`RuleStore`）。`cmd/core.buildObservabilityStore` 按三 env 开关切换：`PAAS_PROM_URL`/`PAAS_LOKI_URL`/`PAAS_JAEGER_URL` 非空接真实后端，空则该支柱保持 memory 惰性 mock（三支柱独立可混用）；alert rules 始终 memory，`ListAlerts` 基于 metrics reader 即时评估（real 模式取真实 Prometheus 当前值）。后端不可达降级返空 + 日志（不 5xx/panic）。未配 URL 行为与现状完全一致。metric/label 命名约定（`paas_cpu_usage` 等）查询端落地。
- **应用侧 OTel 埋点（P2-3，采集端闭环）**：`internal/observability/tracing` 初始化全局 TracerProvider（OTLP/HTTP exporter，`PAAS_OTEL_ENDPOINT` env 开关，空=noop 行为不变），mux 经 `otelhttp` 中间件包装自动建 span（探针/契约/文档端点过滤避免噪音），W3C traceparent 透传播。控制面自身链路可被 `/api/observability/traces`（接 Jaeger）观测，端到端可观测闭环（采集→存储→展示）。引入 go.opentelemetry.io/otel + sdk + otlptracehttp + contrib/otelhttp（Apache 2.0）。
- **留后续**：Collector 采集管道编排、长期存储降采样、Jaeger badger 本地存储 + PVC（持久化 trace）。（告警通知通道/状态机已落地——后台评估引擎 + webhook 出站 + 通知并入，00ce2ff）
- **trace 后端用 Jaeger all-in-one（替 Tempo，2026-08-11）**：Tempo single-binary 把 ingester+querier+compactor 塞一个进程，512Mi OOM、2Gi 才稳（OOMKilled 循环重启丢 trace）；Jaeger all-in-one 是天生单体（Go），in-memory 存储 ~256Mi 长期稳定，CNCF 毕业。**core 推送端零改动**（OTLP/HTTP 4318，Jaeger 原生接收），查询端 `real/traces.go` 适配 Jaeger v1 JSON（`/api/traces?service=X&start=&end=&limit=` 一次返完整 span 树含 processes，无需 Tempo 那样二次 enrich）。错误判定走 Jaeger `error=true` tag + http 5xx；`gen_ai.*` 属性全透传。
- **K8s 真实后端部署（observability namespace，三支柱真实化）**：dev 集群装真实后端栈，core 经 env 接通后即从惰性 mock 切真实。部署清单 `deploy/observability/`（社区 helm chart + values + ingress）：
  - **组件**：`prometheus-community/prometheus`（Prometheus + node-exporter，关 alertmanager/kube-state-metrics）+ `grafana/loki-stack`（Loki + Promtail）+ `deploy/observability/jaeger.yaml`（Jaeger all-in-one 原生 Deployment，in-memory 存储，非 helm chart——轻量单体无需 chart）+ `grafana/grafana`（预配 Prom/Loki/Jaeger 三数据源）。
  - **K8s 1.23 兼容**：`kube-prometheus-stack` 要求 ≥1.25，本集群 1.23.1 用 `prometheus-community/prometheus`（普通 Deployment，无 K8s 版本硬约束）。
  - **镜像源（国内必走）**：节点（kb1/kb2/kb3 内网）拉 docker.io/quay.io 经常超时 → 全部经 daocloud 中转 `docker.m.daocloud.io` 复制到本地 `hub.wang.dd:5000/observability/`：`crane copy docker.m.daocloud.io/<repo>/<image>:<tag> hub.wang.dd:5000/observability/<name>:<tag> --insecure`（registry 间复制不经 docker daemon），再 `kubectl set image` 改工作负载镜像。注意路径：quay.io/prometheus/prometheus ≠ docker.io/prom/prometheus，daocloud 用后者路径。prometheus v3.13.2 等新版本国内源可能未同步，可让节点直拉 quay（慢但能成，prometheus-server 实测最终拉到）。Jaeger：`crane copy docker.m.daocloud.io/jaegertracing/all-in-one:1.62 hub.wang.dd:5000/observability/jaeger:1.62 --insecure`。
  - **PVC**：集群有 `local-path` StorageClass，Prometheus/Loki/Grafana 各启 PVC（10G/10G/5G）持久化；Jaeger in-memory 无 PVC（重启丢 trace，dev 可接受；持久化用 badger + PVC 留后续）。
  - **scrape**：chart 自带 `kubernetes-nodes-cadvisor`（直接抓 kubelet:10250/metrics/cadvisor）即可得数据服务 `container_*` 指标（real/metrics.go TargetDataservice 依赖）；extraScrapeConfigs 加 paas-core /metrics（label `app.kubernetes.io/name=paas-core`，relabel pod_ip:8081）。
  - **关键坑：Promtail 漏挂 docker data-root**。集群 docker `data-root=/data/docker`（非默认 /var/lib/docker），kubelet 的 `/var/log/pods/<pod>/<c>/0.log` 是软链 → `/data/docker/containers/<id>/<id>-json.log`。Promtail 默认只挂 `/var/log` + `/var/lib/docker/containers`，follow 软链时 stat 目标失败（`failed to tail file, stat failed ... no such file`）→ 日志采不到、`/ready` 500 卡 NotReady。修复：Promtail 加 hostPath 挂 `/data/docker` → 容器 `/data/docker`（readOnly），软链目标可解析。patch：`kubectl patch ds loki-promtail --type=json -p='[{"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"data-docker","hostPath":{"path":"/data/docker"}}},{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"data-docker","mountPath":"/data/docker","readOnly":true}}]'`。
  - **kb1 master 节点**：未配 `hub.wang.dd:5000` insecure registry，该节点 Promtail/ DaemonSet 拉镜像失败（ImagePullBackOff）；业务 Pod 全在 worker（kb2/kb3），kb1 不采集可接受（或给 kb1 配 insecure registry）。
  - **Jaeger OTLP**：jaeger svc 暴露 16686(UI/Query API) + 4317(OTLP gRPC) + 4318(OTLP HTTP)，core `PAAS_OTEL_ENDPOINT=jaeger.observability.svc.cluster.local:4318`（otlptracehttp + WithInsecure，不含 scheme）+ `PAAS_JAEGER_URL=http://jaeger.observability.svc.cluster.local:16686`（查询）。控制面 otelhttp 自动建 span 推 Jaeger，`/api/traces` 真实可查。
  - **ingress 暴露（*.k8s.dd 通配域名 + hermes class）**：`deploy/observability/ingress.yaml` 暴露 `grafana.k8s.dd`（统一面板，admin/paas-admin）+ `prom.k8s.dd`（Prometheus UI）+ `jaeger.k8s.dd`（Jaeger UI 排查 trace）；Loki 不暴露（仅 ClusterIP，供 Grafana 与 core 内部访问，减少攻击面）。core 可观测页仍走 core API（core 经 ClusterIP 访问三支柱）。
  - **helm upgrade 注意**：Promtail 的 /data/docker 挂载与 readinessProbe 当前为 patch（loki-stack values 未持久化），`helm upgrade loki` 会覆盖；升级前把这些写进 `deploy/observability/loki-stack-values.yaml` 的 `promtail.extraVolumes/extraVolumeMounts/readinessProbe`。
  - **应用级 logs 查询 gap（留后续）**：real/logs.go 用 LogQL `{app="<appID>"}` 查询，但 Pod label 是 `app.kubernetes.io/name`（非 `app=<PaaS appID>`），应用级日志查询当前查不到。Pod 级真实（按 pod/namespace）已通。完整应用级需 controller 给 Pod 打 `app=<appID>` label + real/logs 用对应 label。
  - **应用级业务指标 gap（留后续）**：core 无业务 `paas_cpu_usage`/`paas_rps` 等埋点（仅 controller-runtime 进程级 /metrics），real 模式查应用指标返空（降级）；数据服务指标走 cAdvisor `container_*` 真实可用。完整应用级需 core 加 Prometheus 埋点（http_request_duration/handler 维度）。
- **GenAI span 补全（trace 树展开，2026-08-11）**：此前 trace 树在 `agent.run` 处断（底层 LLM 调用 + 工具调用黑盒，无 token 用量）。补全后 trace 树：`HTTP server span -> agent.run(gen_ai.agent.id) -> [provider.chat(gen_ai.operation.name=chat, gen_ai.usage.{input,output}_tokens) × N 轮] -> [tool.call(gen_ai.tool.name)]`。三处改动：① `pkg/provider.Chunk` 加 `Usage` 字段（InputTokens/OutputTokens）；② `openai_compatible.go` 加 `chatTracer` span + `stream_options.include_usage=true`（流末解析 usage chunk 回填 span `gen_ai.usage.*` + 发 `Chunk{Usage}`）；③ `agent/runtime.go` 工具调用加 `tool.call` span（gen_ai.tool.name）。OTel GenAI 语义约定（gen_ai.*）手写 attribute（semconv Go 库 GenAI 还在 experimental）。
- **Grafana 应用可观测 dashboard（2026-08-11）**：`deploy/observability/grafana-dashboards.yaml` 两个 dashboard ConfigMap（sidecar 自动加载，label `grafana_dashboard=1`）：① **paas-app-overview**（RED：RPS/P95/错误率/CPU + RPS 趋势/状态码分布/CPU/内存 按_pod，namespace 变量适配多租户）；② **paas-control-plane**（core 自身 RPS/P95/CPU/内存 + 路由 RPS + P50/P95/P99 延迟分布）。`grafana-values.yaml` 启用 `sidecar.dashboards`（image `docker.m.daocloud.io/kiwigrid/k8s-sidecar`）+ 关 `initChownData`（修 image 内 png/csv/pdf chown Permission denied 致 9 天 CrashLoopBackOff）+ grafana image 内网化。新增面板只需 apply ConfigMap，无需 helm upgrade。

### 安全（密钥/证书 + 审计日志，平台能力横切）

租户级密钥/证书资产（KMS 抽象）+ 审计日志。与 appconfig（应用×环境级、工作负载启动注入）区分：本模块是**租户级平台资产**（DB 密码/TLS 证书/第三方 token），集中管理供应用引用，不绑定具体应用：

```
internal/security/  领域(Secret secret|certificate + AuditLog) + Repository(SecretStore + AuditStore) + 内存 seed
  -> handler: /api/security/secrets[/{id}]、/api/security/audit-logs
  -> 权限 security:read/write（写操作自动记审计）
```

- `internal/security/`：`Secret{Name(租户内唯一)/Type(secret|certificate)/Value}` + `AuditLog{Actor/Action/ResourceType/ResourceID/Detail/At}`；`SecretMask="••••••"` + `Masked()`。
- **Secret 安全**：`List/Get/Create` 返回均掩码（与 appconfig 一致，不泄漏长度/内容）。**静态加密已落地**（2026-08-29，见下「敏感数据静态加密」横切章）。轮转/过期/KMS 留后续。
- **审计自动记录**：handler 层在 Create/Delete Secret 成功后自动 `RecordAudit`，`Actor` 由 `UserIDFrom`（复用 `gateway.UserIDFrom`）从身份 ctx 取。审计只增不删（合规）。
- Repository 单 Store（`ListSecrets/GetSecret/CreateSecret/DeleteSecret` + `ListAuditLogs/RecordAudit`），全方法租户强制过滤；审计按时间倒序，支持 resourceType/action 过滤。
- REST：`GET/POST /secrets`、`DELETE /secrets/{id}`（记审计）、`GET /audit-logs?resourceType=&action=`。
- 权限：`security:read/write`（admin/dev 读写，viewer 只读）；不接 prod:write（租户级资产，不按物理环境隔离）。
- console-user「平台能力 → 安全」`/platform/security`：密钥/证书表（掩码）+ 审计日志表（actor/动作/资源/详情，动作过滤）+ 创建弹窗（删除走 `useDangerConfirm` 输入名称确认）。
- 切片**不做**：IAM 细粒度策略（已有 RBAC）、网络防火墙（依赖数据面）、密钥轮转/过期、证书签发（ACME）——均留后续。

### 敏感数据静态加密（横切，2026-08-29）

留后续复审裁决的最后一笔 A 类基线缺口（基线表·秘钥管理「明文存储」）。三处敏感数据 at-rest 加密，DB 泄漏（备份落盘/注入面/误配只读账号）不再等于凭证全泄。spec `docs/superpowers/specs/2026-08-29-secret-encryption-design.md`：

- **`internal/crypto` 单一真源**：AES-256-GCM（Go 标准库零依赖），密文格式 `enc:v1:<base64(nonce+ct+tag)>`（版本位留升级路）；**`Decrypt` 无前缀原样返回**——存量明文数据零迁移兼容，升级部署后旧数据照常可读，新写入逐步密文化。nil Cipher = 明文兼容模式（Encrypt/Decrypt 透传）。
- **master key 治理**（与 PAAS_JWT_SECRET 同款）：env `PAAS_SECRET_MASTER_KEY`（64 位 hex，`openssl rand -hex 32`）；`PAAS_PROD=true` 未设/非法**拒绝启动**；dev 未设明文模式 + 启动 WARNING。helm `security.secretMasterKey`（values → Secret → secretKeyRef，不进 Deployment 明文 env）。
- **两种切入方式（按模块威胁模型分治）**：
  - **security/appconfig：装配层装饰器**（`security.NewEncryptedRepo` / `appconfig.NewEncryptedRepo`，buildAllStores 内包装）：写路径加密 / 明文消费点（Resolve、ListPlain 供 reconciler 注入）解密；List/Get 掩码直通。appconfig **掩码回写保护**：Upsert 收到 `••••••`（前端编辑不回填值）时用库中原值按存储形态回写（不重加密防 nonce 抖动），顺带修了「掩码覆盖真实凭证」既有 bug。
  - **dataservice：PG 持久层加密**（`dspg.WithCipher`，装饰器方案被审查否决）：managed 模式凭证由 store 内部 `FillConnection` 生成（handler 清空 Connection 传入），store 外装饰器无值可加密、后包则密文被 FillConnection 重建进 uri。加密下沉 store 持久层（FillConnection 之后 marshal 之前 + Scan 后解密），按 `dataservice.MaskKeys` 字段级（password/secretKey/token/api_key/master_key/uri），host/port/user/database 明文保留（排障可读）。applier/reconciler/BindingInjector 天然拿明文（CRD/K8s Secret 本就需明文）。memory 路径不加密（进程内存非 at-rest 泄漏面）。`engines` 表 connection（external-shared 第三方凭证）同款接缝覆盖。
  - **security PG seed**（直连 SQL 绕过装饰器）：`secpg.WithCipher` 在 seedAll/ensurePlatformSecrets INSERT 前加密，fail-fast（宁可不 seed 不明文落库）。
- **e2e 教训**：Task 4 装配曾漏传 `dspg.WithCipher`（实现者+审查者双漏检，dataservice 落库仍明文）——**装配类改动必须 e2e 断言落库形态**（SELECT 看 enc:v1 前缀），diff 审查看不出「该传未传」。
- **撤 key 降级语义（运维须知）**：加密部署后撤掉 master key 重启 → cipher=nil 对 `enc:v1:` 前缀值原样透传，airouter 等凭证以密文字面量发上游（推理 401）——加密→明文降级无自动告警，换 key/撤 key 需同步评估存量数据。






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

### 泳道联调 L2（跨泳道服务发现 + 流量染色，2026-08-09）

承接 L1（流水线 deploy/release 分离 + lane 一等参数，数据模型层），解决「L1 只部署到泳道、泳道之间不会联调」痛点。L2 补齐**跨泳道服务发现 + 流量染色**：feature 泳道变更服务调用其它服务时，优先发现 feature 泳道实例（若有），缺失降级 default 基线——一次请求「变更服务走 feature、未变更服务降级 default」，无需全量部署即联调。设计见 `docs/superpowers/specs/2026-08-09-lane-federation-l2-design.md`，计划 `docs/superpowers/plans/2026-08-09-lane-federation-l2.md`。

- **跨泳道降级发现（L2 核心，方案 A）**：`/dp/instances?service=<app>&lane=feature-x` 复用 L1 Service 命名派生 lane（`<app>-svc-<lane>` vs default `<app>-svc`，零额外 label）。`internal/dataplane/endpoints.go` `Instances(ctx, ns, service, lane)`：lane 空/default 返基线；lane=feature-x 先查 `<service>-<lane>` Endpoints，isNotFound 或无 ready addresses 降级查 `<service>`。**跨租户防泄漏**：两个候选 Service 名均经同名 Service 的 `paas.aitoys/tenant` label 校验归属本租户，跨租户/不存在统一返空不泄漏（`fetchInstances` + `TestK8sReaderLaneFallback` 8 测试覆盖）。
- **流量染色透传链（SDK）**：`sdk/paas-registry/lane.go`（独立 module，不进主 go.mod）—— `LaneHeader="x-paas-lane"` 常量 + `WithLane/LaneFromCtx` ctx 携带 + `LaneMiddleware(http.Handler)` 入站 header→ctx + `ApplyLaneHeader(ctx, req)` ctx→出站 header。`registry.go` GetService 从 ctx 取 lane 加 URL `&lane=` query。应用引入 SDK middleware 即得透传能力（应用 HTTP server 挂 LaneMiddleware，调下游前 ApplyLaneHeader）。10 测试覆盖。
- **governance Instance.LaneID 启用**：`model.go` 注释从「预留」改「启用」（字段/读写/pg lane_id 持久化/兜底 LaneDefault L1 已就绪）；`GET /api/services/{id}?lane=feature-x` 按 lane 过滤实例（空=全部，向后兼容；`TestHandlerInstanceLaneFilter`）。
- **Workload 打 lane label**：`pkg/labels` 加 `KeyLane="paas.aitoys/lane"`（注释明确不参与跨租户校验）；reconciler `labelsFor` 加 lane（Spec.LaneID 空→default，feature 泳道带各自 lane），selector/Pod 模板/Service 同款 label 自然区分泳道（`TestReconcileLabelsLane`）。
- **前端 lane 分组展示**：`EnvironmentDetail.vue` 应用部署矩阵加「泳道」列（feature 泳道 warning tag + 基线显「基线」）+ `ApplicationDetail.vue` 部署 tab 工作负载行显「泳道 xxx」tag（feature 才显）。Workload JSON laneId L1 已就绪，前端补展示。
- **入口染色归 SDK（决策）**：PaaS gateway 是推理网关（→airouter），非服务网格入口；lane 入口染色归 SDK `LaneMiddleware` + 用户应用自挂，平台不替应用做入口。硬造推理网关染色违反 YAGNI（airouter 不关心 lane）。符合「服务网格入口染色是 sidecar/SDK 职责」行业规范。Playground lane 选择同理由跳过。
- **e2e 验证**：`go test ./...` 全绿 + 三套 `pnpm build` 过 + k8s 部署，`/api/workloads` 返回 `laneId:"default"`（L1 字段生效）+ governance lane filter 200 + livez 200。
- **留后续（L3）**：全链路 mesh（Istio/zeus sidecar 自动染色，应用零改动）、EndpointSlice + lane label selector（方案 B）、泳道自动回收（baseline 触发/闲置 TTL）、拓扑可视化（feature 泳道 + default 基线流量动画）、LaneMiddleware lane 合法性白名单校验、跨集群泳道。

### 泳道实体化 + 资源规格建模（2026-08-28）

L3 收口（零改动染色/TTL 回收/拓扑可视化已先行落地），本切片补齐用户指出的两大完备性缺口：**泳道从隐式分支名升为一等实体** + **工作负载资源规格端到端**。设计 `docs/superpowers/specs/2026-08-26-lane-entity-and-deploy-policy-design.md`，计划 `docs/superpowers/plans/2026-08-26-lane-entity-and-resources.md`（7 任务 SDD）。四决策：D1 weight 留位不实现切流 / D2 应用级默认 + stage 覆盖 / D3 金丝雀降级为并行验证式归下切片 / D4 三刀切片。

**Lane 一等实体**（`internal/lane/` + migration 0035）：
- `Lane{ID/TenantID/EnvID/Name(DNS-1035)/Mode/Status/Weight(留位恒0)/ExternalLink/Description}`；`Mode=standard`（闲置 TTL 可回收）/ `permanent`（火车常驻，GC 永不回收）；`Status=active|closed`。三种生命周期：显式实体 / EnsureByName 懒建 / 裸分支兜底（GC 只回收有实体的）。
- REST `/api/lanes`（governance:read/write + 生产 prod:write）：CRUD + DELETE=标记 closed + **同步回收该泳道全部工作负载**（`ReclaimLane` 依赖倒置桥接 workload.LaneGC，防关闭与 GC 两处删除逻辑漂移）；关闭前置校验「有进行中 run（branch==name 非终态）409」。**终审 C1：生产泳道 DELETE/PUT 均 require prod:write**——级联回收生产资源比「生产建」更危险，必须对称护栏。
- LaneGC 联动：Sweep 回收完泳道全组 MarkClosed（分组 key 含 tenant 防跨租户互扣；MarkClosed ctx 必须派生租户——store 无租户 ctx 拒查会静默失效）。permanent 跳过。
- lane 解析优先级：deploy stage 显式 lane > EnsureByName 懒建实体 > 分支名（pipeline 不 import lane，`LaneEnsurer` 依赖倒置）。

**资源规格端到端**（标准基线「生产禁 BestEffort」落地）：
- `workload.ResourceSpec` 四字段（cpuRequest/memRequest/cpuLimit/memLimit，K8s Quantity）→ CRD `spec.resources` → reconciler `podSpec applyResources` → Pod requests/limits（e2e 验证 15s 内落地 200m/256Mi）。
- 三级来源：deploy stage params `resources` > `Application.ResourceTemplate`（`PUT /api/applications/{id}/resource-template`）> 空。**生产禁 BestEffort 双入口**：workload handler 直建 400 + pipeline execDeploy fail-fast（EnvType 明确解析 prod 才拦，nil 跳过——防破坏存量测试）。
- 联调泳道副本降级（engine 单一真源）：非 default lane 且非 prod，未显式 replicas 置 1（不沿用 svcDef，防逃逸）。
- 前端：应用详情配置 tab「资源规格默认值」表单 + PipelineDesigner deploy stage `resources`/`replicas` 覆盖。

**部署侧修复（重要经验）**：① helm upgrade 对 chart templates/ 内已存在 CRD **不应用变更**——deploy-k8s.sh 显式 `kubectl apply -f config/crds/`；② 手动 helm 绕脚本必须先 `export NODE_IP`，否则 envsubst 写坏 `image.registry=":30050/paas"` 且 `--reuse-values` 持续继承坏值；③ STS 删 pod 不删 PVC（数据可恢复）。

**留后续**：Weight 切流实现（D1 真实现→按比例金丝雀/蓝绿瞬时切换）、HPA/PDB/PriorityClass。

### 金丝雀验证 stage（并行验证式，2026-08-28）

spec 三刀之第二刀落地（`docs/superpowers/plans/2026-08-28-canary-stage.md`，7 任务 SDD）。**诚实边界**：这是并行验证式金丝雀（业界称 preview/buffered deploy），非按比例切流——真切流依赖生产流量权重（D1 留后续），UI 文案叫「金丝雀验证」不叫「灰度放量」。

- **canary stage 类型**（pipeline 第 8 种 stage）：`execCanary` 部署新镜像到 `canary-<dns1035(runID)>` 并行泳道（1 副本，基线不动）→ `StageWaiting` 暂停（复用 approve 机制）。镜像经 `imageSource=priorBuild` 消费前序 deploy 的 Output.imageId（deploy stage 已回写 imageId 到 Output——canary 验证的就是刚部署的那个镜像）。
- **决策端点** `POST /api/pipelineruns/{id}/stages/{idx}/canary` body `{"action":"promote"|"terminate"}`：**promote**=基线全量滚动（Deploy lane=default，空 resources 不覆盖基线规格）+ 删 canary + stage success 续推；**terminate**=仅删 canary，stage failed（基线零风险退出，无回滚需求）。守卫链：pipeline:write + appGuard release + **promote 且目标 prod 需 prod:write**（terminate 免——零风险退出路径不拦 developer，拦了留孤儿泳道更糟）。审计 `canary_promote`/`canary_terminate`。
- **并发安全（CAS 认领）**：CanaryResume 两段锁——锁内校验 + stage 置 Running + decision 记 Input 持久化「认领」决策（并发相反决策第二方在副作用前被拒，杜绝「终止成功但基线已被放量」）；锁外执行副作用；终态落库前三检。认领后失败 fail 闭包回 Waiting（可再次决策）+ 日志落库。**Resume 拒绝 canary stage**（approve 端点无法绕过决策语义——防跳过放量 + 泄漏 canary workload）。Abort 补 canary waiting 清理。
- **Releaser 扩展**：`DeployCanary`（adapter 内 lane=DNS1035 对齐 engine 侧口径、replicas=1、`PAAS_DOMAIN_SUFFIX` 非空时 SetDomain 独立验证 Ingress；**port=0 时 domain 返空**——多服务应用未显式 service/port 时 reconciler 不建 Service，FQDN 不可达，诚实返空防前端死链，用户可经 paramOverrides 显式 service+port 获得独立入口）+ `DeleteWorkload`（派生租户 ctx Delete + 配额 -1）。workload.Repository 加 `SetDomain`（memory+pg）。
- **tpl-cd v2**（builtin 版本升级机制首次实战：Version 1→2 启动自动覆盖存量）：approve → deploy(prod) → **canary** → release → baseline。
- **通知区分**：`RunStatusItem.StageType` + paused 分支 canary 文案「金丝雀验证中，等待确认」（与审批等待区分）。
- **前端观察面板**（PipelineRunView）：canary waiting 时指标卡（CPU/内存/RPS/延迟 10s 轮询 workload 维度）+ 验证地址直链 + 可观测跳转 + 「确认放量/终止」按钮（放量二次确认）；stage 类型下拉加 canary。
- **e2e（k8s 已验证）**：tpl-cd v2 自动升级 → approve → deploy prod 基线 → canary 并行泳道 Running（基线不动）→ 通知「金丝雀验证中」→ promote → 基线滚动（release 记录 lane=default 紧随 canary）→ run succeeded + canary workload 回收 + 审计落库；第二轮 terminate → 基线 digest 前后不变 ✓。
- **e2e 暴露并修复**：① I2 生产 BestEffort fail-fast 未写 sr.Error 且 `return false`（stage 原因丢失掩盖护栏触发，k8s 首次真实拦截时暴露）；② observability queryCache singleflight `close(done)` 先于结果赋值（-race 暴露，等待者读 val/err 竞态）；③ canary port=0 死链（见上）。
- **部署事故（2026-08-28）**：NODE_IP 误检 master 接口地址（121 vs worker 122）→ 全集群镜像 121 引用 14h（kb2 kubelet daemon 无 121 insecure 配置）→ core/postgres/9 数据服务 STS + core 的 `PAAS_REGISTRY`/`PAAS_IMAGE_REGISTRY` env 全部切回 122 恢复。教训：deploy-k8s.sh 的 NODE_IP 检测需过滤 master（已用 `!/master/` 但 master 接口有多地址时仍误检——集群 IP 变更时全量检查所有 STS/Deploy 镜像引用）。

**留后续**：batches 参数消费（D1 切流实现后按比例分批放量）、蓝绿模板变体（canary params fullReplicas=true）、金丝雀观察期告警联动（观察面板嵌告警规则快查）。

### 流水线完善：模板 CRUD + 构建实时日志（2026-08-09）

承接 L1+L2（流水线 deploy/release 解耦 + 泳道联调），补两个用户反馈的产品 gap：①构建中看不到实时日志（仅终态全量）；②模板无法在 UI 配置（只能平台预置 builtin）。设计见 `docs/superpowers/specs/2026-08-09-pipeline-template-crud-and-stream-logs-design.md`，计划 `docs/superpowers/plans/2026-08-09-pipeline-template-crud-and-stream-logs.md`。

**Phase B 模板 CRUD（admin 后台）**：
- **Repository 补全**：`TemplateRepository` 加 `UpdateTemplate/DeleteTemplate`（L1 已有 Builtin 字段/TenantID/migration 0018 builtin 列/seed Builtin=true，本期仅补 CRUD 缺口）。
- **builtin 保护**：`ErrTemplateBuiltin` sentinel；builtin 模板（tpl-ci/tpl-cd）拒改删（防误改致新应用 OnAppCreate 默认 binding 漂移）。改 builtin 走代码发版 + `Version` 版本号升级覆盖（见下「builtin 模板版本升级机制」，第 18 轮）。
- **admin 端点**：`/api/admin/pipeline-templates` CRUD（super_admin，`WithPlatformAdmin` 注入 `gateway.IsPlatformAdmin`）；POST 强制 `Builtin=false`（防伪造）；ErrTemplateBuiltin->409。OpenAPI 4 操作。
- **console-admin 管理页**：`modules/pipeline-template/`（api.ts + List.vue + TemplateFormDrawer.vue），菜单「流水线模板」（推理服务分组）。**表单化 stage 编辑器**（每行 type select + name + params key-value 动态增删，与 PipelineDesigner 覆盖表单同款风格）；builtin 模板只读 + 锁提示。

**Phase A 构建实时日志（SSE follow）**：
- **BuildLogStreamer 接口**（`internal/devops/builder/logs_streamer.go`，依赖倒置）：`StreamBuildLogs(ctx, buildID, tenantID) (io.ReadCloser, error)` follow 流。`K8sBuildLogStreamer`（clientset label `job-name=<BuildJobName>` 找 Pod + `GetLogs(Follow:true, TailLines:1000)`）。`ErrNoBuildPod`（Pod 未 ready）。clientset nil 返 nil（集群外降级）。导出 `BuildJobName` helper（K8sJob.Build 与 StreamBuildLogs 共用，保证 Pod label 一致）。
- **SSE 端点** `GET /api/buildruns/{id}/logs/stream`（`build:read` + 本租户校验防泄漏源码/凭证）：终态返 `BuildRun.Log` 全量 + `event:end`；运行中 follow Pod logs 逐块 flush（`text/event-stream` + `X-Accel-Buffering:no`，复用 gateway serveStream 同款）；Pod 未 ready loop 等 30s 发 `: heartbeat` 保连接；streamer=nil 降级 503 event。
- **前端 EventSource 消费**（`PipelineRunView.vue`）：build stage 展开 + 运行中 -> `new EventSource('/api/buildruns/{bid}/logs/stream')` -> onmessage 逐行 append + 自动滚底；`event:end` 关流 + 拉终态全量（覆盖实时片段保证完整）；error 降级提示保留片段；折叠/切走/卸载 close 防泄漏（合并 watch runId 清缓存）。终态走 `getBuildRun` 全量。
- **越权**：拉 Pod 日志前 `GetBuildRun` 校验 `TenantID == ctx tenant`，跨租户枚举 buildID 读他人构建日志 404 不泄漏（与 workload PodLogs 同语义）。
- **非 k8s 模式降级**：mock/process 模式无 Pod 日志，端点返 503 event（前端降级提示）；process 模式构建中实时日志要 builder 边构建边写 buffer（大改，留后续）。
- **留后续**：租户自定义模板（可视化编辑器 + 租户隔离）、process/mock 模式构建中实时日志、日志全文搜索/过滤、follow 流多副本/ingress 缓冲深度验证。（builtin 模板版本号 + 升级覆盖机制已实现，见流水线 deploy/release 章节「builtin 模板版本升级机制」。）

### 流水线运行详情独立页 + DevOps 中心闭环（2026-08-09）

承接流水线完善，解决用户反馈的产品设计缺陷（操作不闭环、入口错乱、菜单分组错）：

- **运行详情独立页（GitHub Actions 式）**：新增路由 `/devops/runs/:runId` → `PipelineRunPage.vue` 全屏渲染 `PipelineRunView`（stage 时间线 + 节点日志 + build SSE 实时流），页面壳负责返回导航 + 应用归属（fetchAuth 拉应用名）。`PipelineRunView` 单一真源复用（DRY，应用详情抽屉 + 独立页同源）。此前运行详情只能在应用详情抽屉看，无独立路由，DevOps 中心运行记录点不进去。
- **DevOps 中心闭环**：默认 tab 从「流水线（旧 promote 矩阵）」改为「运行记录」（新 pipeline 引擎）；运行记录行加「查看详情」→ `/devops/runs/:id`（此前只跳应用列表，断链）；旧 promote 矩阵 tab 改名「发布提升」（区分「流水线运行」与「环境逐级晋升」两概念，避免混淆）。
- **触发即跳转**：`AppPipelines.vue` 触发流水线成功后 `router.push('/devops/runs/' + runId)` 看实时进度（不再留在原地）。
- **菜单分组修正**（console-admin）：流水线模板从「推理服务」移到「资源总览 → DevOps 链路」（与构建/镜像/发布并列），流水线是 DevOps 概念非推理服务；图标 Files（撞应用）→ Stopwatch（全局唯一）；mock menu.ts 同步补 pipeline-template（保持 dev 与生产零漂移）。


### 服务治理完善：网关对外域名 + 服务配置中心关联（2026-08-09）

承接流水线完善，解决两个服务治理 gap：网关 Route 无法按域名路由 + 配置中心与服务脱节。设计见 `docs/superpowers/specs/2026-08-09-governance-route-host-and-service-config-design.md`。

- **需求1 governance Route 加 Host（对外域名）**：行业对标 Kong/APISIX Route（hosts+paths+service_id），域名是路由匹配维度非独立实体（YAGNI，不新增「对外域名」实体）。`Route.Host string json:"host,omitempty"`（空=不限 Host，多 Host 逗号分隔）；匹配语义 Host 非空则要求请求 Host 头匹配（数据面 SDK/zeus 消费，本期控制面只存配置）。memory `UpdateRoute` 直接覆盖语义（允许清空）；pg `routeCols`+scanRoute+CreateRoute+UpdateRoute 全链路；migration 0023 + 0001 schema 合并。前端 `ServiceRegistry.vue` Route 表单加 Host 输入 + 列表加「对外域名」列（空显「不限」）。
- **需求2 configcenter Namespace 关联 Service + 双向显示**：`Namespace.ServiceID string`（可选关联，空=不关联）；`ListNamespaces(ctx, serviceID string)` 加过滤参数（与 governance ListRoutes(serviceID) 一致风格）；migration 0024 + 0001 schema 合并。**前端聚合避免跨模块后端耦合**（governance 不 import configcenter）：① `ConfigCenter.vue` Namespace 表单加「关联服务」select（调 /api/services 拉租户内 Service）+ 列表加关联服务列；② `ServiceDetail.vue` 加「关联配置」section（调 /api/configcenter/namespaces?serviceId=<id> + 各 ns published，显示 namespace 卡片 + active 配置项，点 ns 跳配置中心）。
- **横切**：多租户隔离不变（Route.Host 不影响 tenant 过滤；Namespace.ServiceID 租户内引用）；Route/Namespace 均逻辑配置不接 prod:write（与现有一致）；migration 幂等 `ADD COLUMN IF NOT EXISTS`。
- **测试**：governance pg `TestRouteHostRoundTrip`（Create/Update/清空 往返）+ configcenter pg `TestNamespaceServiceIDRoundTrip`（ServiceID 持久化 + ListNamespaces 过滤）。OpenAPI `WithReqBody(结构体)` 自动含新字段（reflector 自动生成 schema）。
- **留后续**：Route Host 通配符匹配（`*.example.com`）、Host 数组化、实际 ingress/hermes 配置下发（数据面消费）、configcenter 配置变更通知 governance Service。

### Code Review（PR 评审闭环，2026-08-25）

基于内置 Gitea PR API 落地轻量评审闭环：PR 列表 → diff 查看 → 整体评审（approve/request-changes/comment）→ merge。设计见 `docs/superpowers/specs/2026-08-25-code-review-design.md`，计划 `docs/superpowers/plans/2026-08-25-code-review.md`。

- **PR 真源 Gitea，平台不落库**（无新实体/migration）：gitea.Client 扩展 6 方法（`ListPRs/GetPR/GetPRDiff/ReviewPR/MergePR` + `ErrPRNotFound` sentinel）；`GetPRDiff` 走 `.diff` raw 端点（text/plain 绕 doJSON），client 层 `LimitReader(2MB+1)` 防无界读 OOM；`doMerge` 补 422→ErrMergeConflict（Gitea 不可合并实际返 422，change 集成链路语义改进无回归）。
- **REST**（devops handler `serveRepoPulls`，composite `/repositories/{rid}/pulls[/{number}[/{reviews|merge}]]`）：列表（state 白名单 open/closed/all）/详情+diff（2MB 截断 + truncated 标志）/评审三态/merge。权限：读=`repository:read`；评审=`repository:write`+AppGuard `write`（developer+）；**merge=`repository:write`+AppGuard `release`（maintainer+）**。**归属校验**：`repo.AppID != appID` 返 404（防「非受限 appId+受限 repoID」移花接木绕 AppGuard——审计 R2 Critical 修复）。评审/merge 记审计（`pull_request_review`/`pull_request_merge`，devops handler 新增 `AuditRecorder` 依赖倒置 + `WithAudit` 注入 identityAuditAdapter）。
- **跨应用聚合** `GET /api/pulls`（mux 精确注册 + OpenAPI）：遍历租户 internal 仓库 `ListPRs(open)`，单仓失败跳过日志降级 + 30s 聚合 deadline（防慢 Gitea 串行长挂）。
- **前端**：① 应用详情代码仓库 tab internal 仓库「评审」入口 → `Pulls.vue`（state tab + 请求序号防竞态）；② `PullDetail.vue`（`/devops/pulls/:repoId/:number?appId=`）——meta + **自研轻量 diff 解析**（`utils/diff.ts` parseDiff：`diff --git` 分文件、+绿/−红行、\ 与 GIT binary patch 归 meta，不引外部 diff 库）+ 评审三按钮 + merge（confirmDangerous 输入 PR#N 危险确认）；③ DevOps 中心新「评审」tab + 值班台第四列「🔀 等评审」（10s 轮询并入既有 usePolling）。
- **10 轮深度检查**（R1-R10 并行 agent：安全/多租户/并发/契约/前端/回归/权限矩阵/测试覆盖/修复复核/端到端走查）：修复 3 Critical（appId 移花接木绕 AppGuard、`/api/pulls` mux 未注册致功能静默失效、Pulls.vue appId 误取 query 致列表必空）+ 3 Important（GetPRDiff 无界 ReadAll OOM、聚合串行长挂、列表 tab 竞态）+ 8 Minor（405 显式/state 白名单/审计失败日志/聚合跳过日志/diff meta 行/Pulls 错误提示/PullDetail 缺 appId 防御/`/api/pulls` 非 GET 405）。权限矩阵 21/21 与 spec 一致；回归零破坏（doMerge 422 对 change 集成是语义改进）。
- **e2e（k8s 已验证）**：Gitea 造真实 PR（分支+提交+建 PR）→ 平台列表/详情 diff/评审 APPROVE 204/聚合/非法 state 400/错误 appId 404/merge 204（PR state=closed merged=true）/审计双记录落库（admin audit-logs 可查 pull_request_*）。
- **留后续**：行级评论、PR 创建端点（现经 Gitea API/本地 push）、CI 状态挂 PR、评审门禁接变更管理（Change 须 PR 通过才可入批）、diff 渲染大文件虚拟滚动。

### 变更管理（Change/IntegrationBatch，火车发车模型，2026-08-15）

解决「多个变更合在一起测试、测试成功后同时上线」诉求。设计见 `docs/superpowers/specs/2026-08-15-change-management-design.md`。三实体：`Change`（feat/hotfix 分支粒度，open→integrated→tested→released/abandoned）+ `IntegrationBatch`（临时集成分支 `integration/YYYYMMDD-seq`，collecting→conflict/testing→tested→releasing→released/failed）+ 既有 `Release`（整批上线记录）。**流水线引擎零改动**：批次触发 CI 时 branch=集成分支名，deploy 泳道经 `{{run.branch}}` 占位符天然隔离（集成分支名即 lane）。

- `internal/devops/change/`：model + Repository（memory/pg，migration 0027，JSONB change_ids 有序，唯一索引 tenant+repo+branch / tenant+branch）+ `Service` 编排（依赖倒置 `GiteaBrancher`/`RunTrigger`/`RunReader`/`RepoLookup`，cmd/core `change_adapters.go` 桥接 gitea.Client + pipeline 三仓储）。
- **编排动作**：`CreateChangeWithBranch`（平台代建分支 main 派生 / 校验已有分支）；`Integrate`（删重建集成分支→按 ChangeIDs 顺序 merge→FindPipeline(ci)→TriggerAppRun(branch=集成分支)→testing；冲突→批次 conflict+`BatchConflictError`）；`Approve`（tested→releasing，handler 校验 prod:write）；`Release`（releasing 态逐个 merge 到 main→CD run branch=main）；`SyncBatchStatus` **惰性终态推进**（GET 批次详情时轮询 run 终态：testing succeeded→tested / failed→批内变更回 open 出批；releasing succeeded→released+ReleaseIDs 回填，无后台 goroutine）。
- REST（`/api/applications/{id}/changes|batches[...]`，pipeline:read/write 权限 + approve 额外 prod:write）：变更 CRUD/放弃 + 批次 CRUD/放弃 + 入批/出批 + integrate/approve/release，OpenAPI 13 操作。审计 change_/batch_ 前缀全动作覆盖。
- Gitea client 扩展：`CreateBranch/GetBranch/DeleteBranch` + `doBranch`（404→ErrBranchNotFound、**删除不存在分支 Gitea 返 500 "object does not exist" 归一 NotFound**——幂等重建依赖）；`Merge` 创建 PR 409（同源 PR 已存在）→ `ErrPRExists` + `findOpenPR` 复用（集成重试幂等）。
- **lane 清洗（e2e 实测两连修）**：集成分支名含 `/`（如 `integration/20260815-1`）——① `labelsFor` 的 lane label 值经 `sanitizeLane`（K8s label 不许 `/`）；② `BaselineWorkloadName` 经 `dns1035`（Service 名 DNS-1035 不许 `/`，dataplane 泳道发现 `dns1035Name` 同款对齐，两侧一致才命中 Endpoints）。
- 前端：`api/change.ts` + `AppChanges.vue`（应用详情「变更」tab：变更列表/创建弹窗展示 clone 命令 + 批次列表/创建 + 批次抽屉 el-steps 状态机 + 变更 chips 移出 + integrate/approve(confirmDangerous isProd)/release + 关联 run 跳转 + testing/releasing 10s 轮询）+ DevOps 运行记录 branch `integration/` 前缀显「集成」tag。
- **e2e 全链路已验证（paas-shop）**：建分支→入批→integrate（merge+CI 构建真实 digest）→testing→deploy 泳道（清洗后 Service 名合法）→探活通过→tested→approve→release（merge main+CD run）→released+ReleaseIDs 回填+change released。
- **留后续**：跨应用批次、变更级审批、冲突预检（merge 前干跑）、PR 实体/评审流、外部仓库变更、自动批次（定时发车）、release 阶段已合并分支 409 细分、批内变更 UI 拖拽排序。

### DevOps UX 业界优秀标准改造（值班台 + 收件箱 + 通知 + 对账，2026-08-16）

解决「各模块不统一不完整、用户需在多列表间横跳」痛点，设计对标 GitLab MR 收件箱制 / Argo 持续对账。核心原则：**让用户只盯一个地方（值班台/通知），且只在需要时被打扰**。spec `docs/superpowers/specs/2026-08-16-devops-ux-excellence-design.md`。

- **后端两轻量端点**：① `GET /api/changes|batches?appId=&status=` 跨应用列表（change.Handler.ServeGlobal，tenant 内跨应用与 /api/buildruns 同款语义，perm pipeline:read；**注意 main.go 装配须 `http.HandlerFunc(ServeGlobal)`——误接 ServeHTTP 会被解析为 appID="api" 返空，已有路由层回归测试**）；② `GET /api/notifications` 通知聚合（`internal/devops/change/notifications.go` 实时拼装无持久化——批次 conflict/testing/releasing/tested(待审批) + run failed/paused/running，severity error>warning>info 排序；run 侧租户过滤由 bridge ListRuns ctx 保证（勿用批次归属白名单——会吞掉无批次应用的 run 通知）；`RunLister` 依赖倒置接口，`runTriggerBridge.ListRunStatuses` 桥接）。
- **单据一等公民**：五详情独立路由 `/devops/{changes|batches|builds|releases}/:id`（runs 已有）——ChangeDetail（**收件箱五段**：我的代码（分支+clone+commits）/集成批次/测试验证 el-steps/发布状态/时间线）、BatchDetail（el-steps 状态机 + 内联下一步操作 integrate/approve/release + 批内变更表 + 发布记录链接）、BuildDetail（全量日志 monospace + 产出镜像链接）、ReleaseDetail（信息 + 回滚 + **运行态对账卡**：按 (appId,envId) 找基线 workload 展示副本/实际镜像，digest 不一致警示已被覆盖）。详情间链路串联（change↔batch↔run↔release↔workload 双向可走）。
- **DevOps 中心 = 值班台 + 档案室**：默认 tab 值班台（通知驱动三列：🔴失败待处理/⏸等审批/🏃进行中，点击直达详情，失败数 badge 上 tab；「一切正常 🎉」空态）+ 七 tab 档案室（值班台/运行/变更/批次/构建/镜像/发布），全单据行可进详情页。变更/批次此前在 DevOps 中心完全缺失（用户反馈核心缺口）。
- **变更收件箱**：AppChanges 列表加「下一步」内联操作列（open 未入批→入批集成按钮 / conflict→解决冲突 / tested→待审批 / released→✓已上线），标题可点跳收件箱详情页。
- **通知中心（L1）**：`NotificationBell.vue` 顶栏铃铛（message 图标）+ 未读红点（localStorage `paas:notif-read` 记已读 ID，通知 ID 稳定 `target:targetID:status`）+ severity 着色列表 + 点击跳对应详情 + 30s 轮询静默。
- **明确不做（YAGNI）**：DAG 画布、完整评审流、通知持久化/订阅/Webhook 出站（L3）、漂移检测告警。
- **横切**：新端点 OpenAPI 登记 + `{data:T}` + camelCase；notifications 权限 pipeline:read（登录态）；轮询全部 onUnmounted clearInterval。


### 应用级权限（App Member + 受限模式，2026-08-24）

解决「权限只到用户（租户角色）级，无法表达应用/服务级粒度——如测试人员无发布权限」。设计对齐 GitHub Repo 成员模式，**渐进启用不破坏存量**：

- **双层权限模型**：租户级 RBAC（identity.BuiltinRoles，管「能干什么类型的事」）× 应用级成员角色（管「能在哪个应用干什么事」）。应用内四角色：`app-owner`（全权+成员管理）/ `app-maintainer`（可发布部署）/ `app-developer`（可开发构建，**不可发布**——测试人员典型角色）/ `app-viewer`（只读）。动作三粒度：`manage`（owner）/ `release`（maintainer+）/ `write`（developer+），授权矩阵 `MemberAllowed`。
- **受限开关**：`Application.Restricted`（默认 false=租户级 RBAC 即可写，现状不变）；`PUT /api/applications/{id}/restrict` 开启后 enforcement 生效。开启需 application:write + 应用级 manage（防非 owner 先开锁自封）。
- **AppGuard（enforcement 核心，依赖倒置）**：`application.AppGuard{Apps, Members, IsAdmin, UserIDFn}`——`Allow(r, appID, action)`：非受限/未注入放行（向后兼容）；租户管理员通行；其余查 `MemberRole` 判矩阵，**非成员 fail-closed**。cmd/core 装配桥接 `gateway.IsAdmin` + `gateway.UserIDFrom`，注入 devops/pipeline/workload/application 四 handler。
- **enforcement 切点**：发布/回滚/promote/流水线审批 → `release`；流水线触发（含 deploy/release/promote/approve stage 的 CD → `release`，纯 CI → `write`）；构建触发/工作负载写（create/scale/schedule/delete）/应用绑定 → `write`；应用删除/restrict/成员管理 → `manage`。读不拦（租户=信任边界，与 API Key 同款取舍）。
- **数据层**：`app_members` 表（UNIQUE(app_id,user_id)，FK CASCADE）+ applications.restricted 列（migration 0032 + 0001 合并）；memory/pg 双实现 `MemberRepository`（ListMembers/ListAllMembers/GetMember/AddMember(ON CONFLICT 覆盖语义)/RemoveMember/RemoveAppMembers/MemberRole(无记录返空串)）；pg AddMember 前置应用归属校验（防跨租户挂成员）+ LEFT JOIN users 带展示名；级联删应用清成员（appCascadeDeleter + PG CASCADE 双保险）。
- **REST**：`/api/applications/{id}/members`（GET/POST/DELETE，OpenAPI 4 操作）+ restrict（PUT）。member POST 校验角色合法 + UserLookup 校验用户存在（防悬挂引用）。**租户自助用户列表** `GET /api/users`（任意已认证，强制本租户，不回传 password_hash——成员选择器数据源）。admin 观测面 `GET /api/admin/app-members`（super_admin 跨租户只读）。
- **前端**：console-user 应用详情新「成员与权限」tab（`app-tabs/AppMembers.vue`：受限开关 el-switch + 二次确认、成员表格角色 inline select 改角色、添加弹窗用户下拉（/api/users filterable）+ 角色选择、空态警示「开启前先加 owner 否则锁死」）；console-admin 资源总览新「应用成员」页（`AppMembers.vue` 跨租户只读观测 + appId→应用名索引，Stamp 图标）。
- **e2e 验证（k8s）**：developer 加为 app-developer 成员 + 开受限 → 构建 400（放行到业务校验）+ 发布 403「无该应用的应用级权限（release）」；admin 通行到业务校验；移除成员 → 构建 403（fail-closed）；关闭受限 → 恢复 400（现状）。/api/users 200、admin app-members 总览可见。
- **10 轮深度检查修复（2026-08-24，R1-R10）**：① 绕过缺口——appconfig/change/service 三 handler 补 Guard（configs 写=write、批次 integrate=write/approve·release=release、服务写=write），pipeline retry 按 run.StageRuns 构成判定动作（与触发同源，防失败 CD 重试绕过）；② **提权链封堵**——非受限应用成员管理/开启 restrict 仅租户管理员（防 developer 自封 owner→开 restrict→锁死他人；关闭/受限态仍 owner），+3 回归测试；③ `/api/users` 无 tenant ctx 拒绝（原 ListUsers("") 返全租户用户）；④ app_members 补 RLS 策略（0032+0001，与其他租户表同款纵深）；⑤ 成员增删 + restrict 开关补审计（app_member_add/remove + app_restrict_on/off，经 identityAuditAdapter 落 security）；⑥ webhook 触发不接 Guard（机器凭证语义，与跳过 prod:write 同源，approve 门禁兜底——记录接受）。k8s e2e 复验：提权链 403×2 + 授权链 201 + 审计落库可见。
- **留后续**：应用级权限前端按钮级显隐（现后端 403 兜底）、服务（Service 实体）级权限粒度、API Key 携带应用成员角色（程序化调用对齐）、restrict 开启时自动将创建者设为 owner、Guard 结果短 TTL 缓存（当前每写请求 +2 点查，索引查询可接受）。

### Agent Skill 支持 + AI 编排 UX + admin 闭环（2026-08-24）

Agent 模块功能补齐：新增 Skill 能力指令包 + 工具/知识库富展示（去裸 ID）+ admin 后台 AI 编排跨租户总览（此前完全缺失的闭环）。

- **Skill 实体（P3.x）**：`internal/ai/skill/`（model + repository + memory + pg + handler，克隆 tool 模块）。Skill = 可复用指令能力包（name/description/instructions/enabled），与 Prompt 互补——Prompt 是整体 system prompt 模板，Skill 是可叠加的能力指令，一个 Agent 可绑多个组合（对标 Claude Skills / GPTs Instructions）。权限复用 agent:read/write。
- **Agent.Skills 字段**：`Skills []string` JSONB（migration `0031_ai_skill`：ai_skills 表 + ai_agents.skills 列；0014 合并 schema）。memory clone + pg scan 同步深拷。
- **runtime 注入**：`Runtime.WithSkills(skill.Repository)` 依赖倒置；buildSystem 在 system prompt/PromptRef 之后逐个注入启用的 skill（`# 能力：<name>\n<instructions>`），禁用/缺失静默跳过（与 tool 降级语义一致）。回归测试 `TestBuildSystemInjectsSkills`。
- **REST**：`/api/skills` CRUD（5 操作 OpenAPI 登记）。
- **console-user UX**：① `Skills.vue` 新页（含「被 N 个 Agent 引用」+ 删除前引用检查）+ 菜单/路由（`/ai/skills`，闪电方块图标）；② `Agents.vue` 富化——列表 Skill/工具/知识库列显示具名 tag（id→name 索引），表单三选择器 option 带「名称+类型标签+描述」（禁用工具不可选），PromptRef 裸文本改激活版下拉，空资源引导 hint；③ Tools.vue/KnowledgeBases.vue 加「被引用」列（tooltip 列出引用 Agent）。
- **admin AI 编排闭环**（回答「管理后台没 agent 管理怎么闭环」）：此前 AI 编排 5 实体零 admin 入口。补：① 五仓储（agent/tool/kb/prompt/skill）加 `ListAll`（跨租户带 TenantID，LIMIT 1000，pg prompt 仅 active 版本）；② `internal/ai/admin` 聚合 handler → `/api/admin/ai/{agents|tools|knowledgebases|prompts|skills}`（adminGuard super_admin 只读，5 操作 OpenAPI 登记）；③ console-admin `resources/views/AiOverview.vue` 单页 5 tab（租户列 + 引用计数前端聚合 + id→name 索引展示）+ 菜单「AI 编排」（ChatDotRound，menus.go + mock menu.ts 对齐）。
- **e2e 验证**：skill CRUD + agent skills roundtrip + admin 5 端点（super_admin 会话 200/普通 admin Key 403）+ 菜单「AI 编排」+ 真实推理注入验证（绑定「写周报」skill 的 agent 输出精确遵循 skill 指令的「每周五下午/三节/markdown」约束）。
- **留后续**：skill 分组/标签、admin AI 总览写操作（L2 干预：启停/删除，需配额与审计）、工具/KB 删除前的引用强校验（现前端引导后端放行）、skill 版本化、Playground 内嵌 skill 试调。

### AI 编排广场（Marketplace，2026-08-24）

对标 Dify Explore / Coze 商店 / Claude Skills 生态，补齐跨租户能力复用层。设计见 `docs/superpowers/specs/2026-08-24-ai-marketplace-design.md`。

- **统一 Marketplace 实体（方案 A）**：`marketplace_items` 表（平台级公开，同 maas 模型目录先例；migration 0033）存四类实体的脱敏快照（`entityType: skill|prompt|tool|agent` + snapshot JSONB + 分类 + installs 计数 + 发布者），`UNIQUE(entity_type, name, publisher_tenant)` 重发布覆盖。
- **快照不可变 + fork 安装**：发布即定格（源实体后续修改不影响），更新 = 下架重发（无版本链）；安装 = fork 副本到本租户（同名自动 -2/-3 后缀），之后独立演进。Agent 整包：snapshot 内嵌引用的 skills/prompt/tools（KB 不进——含租户数据），安装时全部 fork 并重写 Agent 引用（skills/tools 按新 ID、PromptRef 按 fork 后 name）。
- **凭证安全**：`SanitizeConfig` 发布时剔除敏感 key（正则 `apikey|api_key|token|password|passwd|secret|authorization|auth`，不区分大小写，后端真源），快照零凭证；安装者自行补填（前端 hint 提示）。
- **REST**（`/api/marketplace`，agent:read/write）：列表（?entityType=&category=&q=，snapshot 列表剥离）+ 详情（含 snapshot）+ 发布（POST，category 必填）+ 下架（DELETE，仅发布者/super_admin）+ install + `/published` 我的发布；admin `GET /api/admin/ai/marketplace`（super_admin 总览）。OpenAPI 7 操作。
- **依赖倒置**：`EntityForker` 接口 + `RegisterAllForkers`（marketplace → agent/skill/prompt/tool 单向，各实体包不反向 import）；cmd/core 装配注入四仓储聚合 `Repos`。
- **实体字段扩展**：四实体加 `Category`/`InstalledFrom`（migration 0033），Skill 另加 `UseCases`/`Examples`（详情富化，对标 Claude Skills SKILL.md 结构）。
- **前端**：`Explore.vue` 广场页（`/ai/explore`，AI 分组置顶菜单「广场」）——类型 tab + 分类 pill + 搜索 + 卡片网格 + 详情抽屉（snapshot 预览/组装结构）+ 安装跳落点；四模块页「发布到广场」（`usePublish.ts` composable 复用：选分类→确认快照语义→发布→分类回写）+「来自广场」来源标记；Skills 详情抽屉（instructions/useCases/examples）；admin AiOverview 加广场 tab。
- **留后续**：评论/评分/点赞、版本链与「源头已更新」提示、官方认证标、KB 广场、安装量防刷、快照导出 YAML、推荐算法。


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

**主模块 vs 应用详情维度原则**（2026-08-17 确立，对标 Datadog/Grafana/Jaeger）：应用详情 = 应用维度聚合（各能力以 tab 收敛进应用）；主模块（平台能力入口）= 综合平台视角——**入口全局，维度是过滤器不是门槛**。三条铁律：① 主模块默认租户全局视图，环境/应用/数据服务等维度是可切换的过滤器；② 漏斗式排障：告警总览（置顶入口）→ 实体健康矩阵 → 单实体下钻；③ 告警天然全局，从告警点击下钻到对应实体。落地样板：`Observability.vue` 多维度重构（维度过滤器条 + 告警点击下钻 + 健康矩阵，spec `docs/superpowers/specs/2026-08-17-observability-multi-dimension-design.md`）。审计确认 DevOps 值班台/ConfigCenter/Security/ServiceRegistry 已符合，无需改。

- API 契约：后端 OpenAPI 自动生成前端 TS 类型（Plan 4 起接入 Gateway）。
- console-admin 的基座源码自带其 `CLAUDE.md` 与 `docs/standards/`（四层架构 lib/app/modules/shared），改它时遵循其自身规范。

## 平台模块全景

见 [平台模块蓝图](./docs/superpowers/specs/2026-07-27-platform-modules-blueprint.md)。当前完成度约 96%（Core 骨架 + 多租户身份 + MaaS + 应用主线 + 工作负载 + 环境 + 生产安全防护 + DevOps CI/CD + 应用配置 + 服务治理注册中心 + 配置中心 + 可观测指标告警 + 安全密钥审计 + 配额计费 + 数据服务资源 + 配额强制拦截 + PostgreSQL 持久化全 10 模块迁移（observability 除外）+ OpenAPI 契约单一真源 + 前端 TS 生成），其余模块按蓝图优先级推进。

## 标准基线（审计外部基准，2026-08-26 设立）

**为什么有这一章**：十几轮深度审计均为「守卫式」（查已实现代码的正确性），结构性盲区是「该实现而未实现」的完备性缺口（如 Workload 无 CPU/内存 requests、生产发布只有 rolling 无金丝雀/蓝绿——均被「YAGNI 留后续」文档化后免疫审计）。本章提供**外部业界基准**，审计时对缺失提问，而非只对已实现提问。**用户每次指出的 gap 沉淀进本章，基线持续进化。**

**审计三原则**：
1. **完备性对表**：重要模块审计必须对照下表提问「业界标准能力我们缺哪些」，不能只查内部正确性。
2. **旅程式审计常态化**：每模块至少一轮「模拟真实用户完整旅程」（冷启动审计发现 F1 死锁、泳道演示暴露 service 名坑，均为此类产出）。
3. **留后续复审**：每次大功能落地后扫一遍各章「留后续」清单，问「这笔欠账现在该还了吗」。

**模块完备性基线**（对标业界，缺项 = 已知债务，非「不符合设计」）：

| 模块 | 业界对标 | 基线能力清单 | 当前缺口 |
|------|---------|------------|---------|
| 工作负载部署 | K8s Deployment | resources requests/limits（QoS 非 BestEffort）、readiness/liveness probe、副本数、优先级 PriorityClass、PDB、HPA | requests/limits 已落地（2026-08-28，生产禁 BestEffort双入口）；HPA/PDB/优先级缺 |
| 生产发布策略 | Argo Rollouts / Flagger | 滚动/金丝雀（按比例分批放量+指标分析+自动回滚）/蓝绿（并行环境+瞬时切流），发布观察窗口 | 并行验证式金丝雀已落地（2026-08-28，人工观察决策）；按比例切流缺（依赖流量权重）；蓝绿可借泳道底座 |
| 泳道/流量隔离 | 阿里全链路灰度 MSE | 泳道一等实体（显式创建/常驻标记/手动关闭）、资源规格模板、入口流量按比例切泳道 | 实体已落地（2026-08-28，standard/permanent/Weight 留位）；入口流量按比例切泳道缺 |
| 可观测 | Grafana/Pyroscope | RED/USE 指标、结构化日志、trace、告警通知通道、SLO 燃烧率 | 告警通知已落地（后台评估引擎+webhook 出站+通知并入，00ce2ff）；SLO 缺（2026-08-23 审计已列） |
| 秘钥管理 | Vault/KMS | 加密存储（envelope encryption）、轮转、过期、动态凭证 | 静态加密已落地（2026-08-29 AES-GCM envelope）；轮转/过期缺 |

**工作负载资源规格最低标准**（新增 Workload 能力时必须满足）：
- 生产环境 Pod 必须有 CPU/内存 requests+limits（不得 BestEffort）；联调泳道可放宽（副本 1 + 无 limits 可接受，但需文档说明取舍）。
- 有入流量的服务必须有 readiness probe（已满足：TCP probe）。

## 开发约定

- 新建模块或引入新技术栈时，同步更新本文件对应章节。
- 注释语言与代码库现有注释保持一致。
- **未经用户明确要求，不要执行 git commit / 分支操作。**
- 所有依赖须与 Apache 2.0 兼容；新增依赖前确认 license。
- 业务领域逻辑绝不进 Platform Core；判断标准："MaaS / 治理 / DevOps 都会用吗？"
- 多租户隔离由 Core 统一治理（DB 访问层强制 tenant 过滤），插件不得绕过。

## 开源就绪 backlog（2026-08-10 审计，待处理）

开源就绪审计（独立 agent）+ 批判性筛选后的待办，按「是否影响当前 dev 集群部署」分级。**核心原则：开源清理不得破坏正在运行的 dev 集群链路**（`hub.wang.dd` / `paas.k8s.dd` / `airouter.ddmc-inc.com` 是 dev 集群真实可达的基础设施，[[k8s-always-latest]] 依赖）。

**已完成**：
- **C1 sdk/paas-registry 本地 replace 彻底解决**（2026-08-10）：zeus 发公开版 `v0.2.0`（`github.com/go-zeus/zeus`，含 SDK 消费的 `ServiceEntry.Snapshot()` API；annotated tag `v0.2.0` 打在 origin/main HEAD `6d55c6c` 并推 origin，GitHub 公开可达）。SDK `go.mod` 移除本地 replace 改 `require github.com/go-zeus/zeus v0.2.0`，go.sum 走 goproxy.cn 公开解析。SDK build/vet/test 全过，**零本地路径泄漏、零 replace 指令**。主仓 core 不依赖 zeus（主 go.mod 0 引用），SDK 作为可选独立 module 现可被外部构建。发版背景：本地 replace 一直掩盖版本漂移——SDK 用 post-v0.1.0 的 `Snapshot()` API，published v0.1.0 无此方法，去掉 replace 即暴露；v0.2.0 补齐该 API（新增导出方法，向后兼容 minor bump）。
- console-admin 文档品牌清理：LICENSE/README/SECURITY/COC/CONTRIBUTING 个人邮箱 `rushui@qq.com` → 项目级渠道（GitHub Security Advisory + aitoys/paas repo）；版权 holder `如水` → `The PaaS Authors`；README 标题改 PaaS Admin Console 定位（标注 derived from vue-admin MIT）；vue-admin 上游 badges/deploy 按钮/demo 链接 → aitoys/paas。纯文档零构建影响。
- airouter 真实域名去品牌化：源码默认改中性（`airouterBaseURL()` 读 `PAAS_AIROUTER_BASE_URL` env，默认空），migration 0003 backfill 改 `credential_ref` 匹配（vendor-neutral）；Go/SQL/TS/Vue 源码全部移除 `ddmc-inc.com`，仅 dev overlay `values-paas-k8s.yaml` 保留 dev 网关（带说明注释）。PG seed 幂等（ON CONFLICT DO NOTHING）保证 dev 集群推理链路不受影响（已验证 glm-5.2 流式正常）。
- examples 内网 IP：`${REGISTRY:-192.168.41.122:30050}` → `${REGISTRY:?必填}`（强制开源用户显式指定）。
- 历史 plan 文档个人邮箱：`提交者：如水 <rushui@qq.com>` → `The PaaS Authors`。
- README 部署章节开源化：新增开源用户首选公网 Helm 路径（`helm install paas deploy/charts/paas`，默认 ghcr.io/aitoys 公开镜像），`deploy-k8s.sh` 标注为本地 dev 集群便捷脚本。
- `values-paas-k8s.yaml` 加文件级 dev-overlay 声明（含 dev 演示凭证，标注「复制后替换」），开源用户基于 `values.yaml` 自定义。

**待处理（按价值×风险排序）**：
1. **deploy/ yaml + scripts 的 `hub.wang.dd:5000` / `paas.k8s.dd`**：dev 集群内网 registry/域名。脚本已支持 `${REGISTRY}`/`${NODE_IP}` envsubst 覆盖，但默认值是内网地址。这些是「自建离线集群便捷脚本」（sync-images/seed-default-pipelines/deploy-k8s），开源用户主路径走 `helm install paas deploy/charts/paas`（README 已引导），dev 脚本保留 dev 默认 + 头注释说明覆盖方式即可，不强拆（拆了破坏 dev 集群）。
2. **`values-paas-k8s.yaml` 含 dev 密码**（`paas-gitea-bot-2026` / `sk-acme-admin` token）：已加文件级 dev-overlay 声明（标注复制后替换）。重命名为 `.example` 会破坏 `deploy-k8s.sh` 引用，权衡后保留文件名 + 头声明。
3. **EnvTypeResolver DRY**（pipeline/handler.go 用 `func` 类型，其余 5 处用 `environment.EnvTypeResolver` 接口别名）：pipeline 包内部 func 类型一致自洽，统一为接口别名需改 handler/engine/adapter 多处调用点，纯重构收益小（Minor，YAGNI 暂留）。

**判断**：CLAUDE.md 保留（项目导航真源，非泄漏他人信息）；vue-admin 上游 MIT 声明保留（fork 归属合规）。

### 智能体模块增强（命名 + 多轮记忆 + 评估历史，2026-08-24）

- **命名改「智能体」**：「AI 编排」→「智能体」（console-user 菜单/console-admin 菜单/后端 staticMenus/mock 对齐）。理由：编排（orchestration）在业界指 workflow 流程编排，本模块实际是 Agent 及其能力资产的构建管理，对齐 Coze「智能体」主概念。
- **Agent 多轮记忆**：`conversationId` 可选参数（handler serveRun + gateway chatReq 透传链 4 层：chatReq→AgentDispatcher.ServeSSEConv→adapter→Runtime.RunConv）；内存 `conversationStore`（per agent×conv 环形 20 条 + 全局 1000 会话惰性清扫防滥用增长）；成功结束后 append 本轮 user/assistant。空 convId = 无状态单轮（现状不变）。Playground 不传（前端自管完整 hist，防双重历史）；试运行弹窗传（单输入框场景）。
- **评估历史（对标 LangSmith Eval 记录）**：`EvalRun` 实体（migration 0034 eval_runs，JSONB results）+ RunRepository（memory 环形 20 次/agent + pg 惰性 DELETE 清理）；service RunAll 落历史（best-effort 失败记日志）；`GET /api/agent-evals/runs?agentId=` + `/{id}`（OpenAPI 2 操作）；Agents.vue 评估弹窗加历史表（通过率 tag + 点击回看逐用例结果）。
- **子模块对标结论**：Prompt 版本 diff/KB 检索命中调试/工具调用统计留后续（低价值密度）；多轮记忆与评估历史是最高价值缺口已补。
- **深度检查（10 轮）**：R1-R6 内联审计修复 7 findings（Explore preview 时序 bug/conversation 无上限/forker TOCTOU 500→409/试运行 conversationId 缺失/死代码/eval 静默失败/gateway 透传）+ R7-R10 确认（Playground 双重历史防避/分发测试补齐/装配回归/前端三套+后端全绿）。k8s 部署 + e2e 复验全通。
