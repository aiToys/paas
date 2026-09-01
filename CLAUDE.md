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
| 推理引擎 | 第三方供应商聚合（OpenAI 兼容协议；不自建 vLLM） |
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
internal/core/                  # Core 内部：plugin/identity/health/gateway
internal/controller/            # K8s WorkloadReconciler + K8sApplier
internal/airsync/               # 离线交付核心逻辑
frontend/                       # pnpm workspace（三套前端）
├── console-admin/              # 后台管理（基于 vue-admin 基座，MIT）
├── console-user/               # 用户控制台
└── landing/                    # 官网展示页
deploy/charts/paas/             # Helm chart
config/crds/                    # controller-gen 生成的 CRD YAML
docs/superpowers/{specs,plans}/ # 设计与实施计划（各功能详细设计都在这）
examples/                       # 平台示例（独立 module，非 Platform Core）
sdk/paas-registry/              # 数据面 SDK（独立 module）
CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md LICENSE
```

> **示例与平台隔离**：`examples/` 与 `sdk/paas-registry/` 均为独立 Go module，与主仓 Go 依赖完全解耦，业务领域逻辑绝不进平台 `cmd/`。

## 部署体系（要点）

- **离线交付（airsync）**：公网 `helm install paas deploy/charts/paas`；私有 `airsync bundle` 打包 → 物理介质 → `airsync install`（verify sha256 → load/retag/push → helm install）。`make airsync` 编译；`make manifests` 生成 CRD。
- **K8s 集群部署**：core 单镜像同源 serve 前端 + API（无 CORS）；集群内自建 registry（NodePort 30050，Pod 镜像用 `<nodeIP>:30050`，kubelet 解析不了 svc DNS）；`scripts/deploy-k8s.sh` 检测 worker nodeIP + envsubst 注入 values。前端三套 SPA `//go:embed` 进 core 二进制（`internal/web/`），路由：`/console/*` → console-user、`/admin/*` → console-admin、`/*` → landing、`/api|/v1/*` → API。
- **Dockerfile**：多阶段（node:22-alpine 前端 → golang 交叉编译 → distroless）；国内源默认，`--build-arg` 覆盖。**必须 `DOCKER_BUILDKIT=1` + builder `FROM --platform=$BUILDPLATFORM`**（否则 arm64 Mac QEMU 下 Go SIGSEGV）。
- **helm 注意**：chart templates/ 内 CRD 不随 upgrade 应用，需显式 `kubectl apply -f config/crds/`；镜像 tag 不变不触发 rollout，需 `kubectl rollout restart`；手动 helm 前必须 `export NODE_IP`（否则 envsubst 写坏 registry 地址）。
- **in-cluster**：`ctrl.GetConfig()` 自动检测（PAAS_KUBECONFIG 或 SA token）；manager cache 按 `PAAS_K8S_NAMESPACE` 限定 namespace。
- 镜像依赖清单见 `docs/deploy/dependencies.md`；国内拉镜像走 daocloud 中转（`docker.m.daocloud.io` → crane copy → 本地 registry）。

## 常用命令

**后端（根目录）：**

```bash
make build          # 编译 bin/core
make test           # go test ./... -race（内存后端，零依赖）
make test-pg        # PostgreSQL 集成测试（-p 1 串行，各包共享同一 database）
make lint           # golangci-lint run ./...
./bin/core          # 运行，暴露 :8080
PAAS_DB_URL=postgres://... ./bin/core   # 启用 PG 持久化
git tag v0.1.0 && git push origin v0.1.0   # 发版：触发 CI 多平台镜像发布
```

端到端验证：API Key 绑定 (租户, 角色)，三预设演示 Key：`sk-acme-admin`（Acme 管理员，默认）/ `sk-globex-admin` / `sk-acme-dev`（开发者）。

```bash
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/v1/models
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/models
curl -N -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://localhost:8080/v1/chat/completions
# 多租户隔离：跨租户访问统一 404 不泄漏
```

**前端（`frontend/` 目录）：**

```bash
pnpm install                  # 安装三套全部依赖
pnpm dev:admin | dev:user | dev:landing   # 端口 5173/5174/5175
pnpm build                    # 构建三套
```

## 核心横切机制（改代码必须遵守）

### 多租户身份与隔离

- API Key / 会话 = `(租户, 用户, 角色)` 三元组；`identity.LookupAPIKey` → `tenant.WithTenant(ctx)` + `WithRoles(ctx)` → Repository 强制按 tenant 过滤（缺失即拒），跨租户访问统一 **404 不泄漏存在性**；`Create` 以 ctx 租户为准忽略请求体。
- RBAC：`identity.BuiltinRoles()`——tenant-admin 通行 / developer / viewer；`gateway.Require(perm)` 粗粒度中间件。
- 生产写防护：`prod:write` 权限 + `EnvTypeResolver` 依赖倒置（各模块注入，写操作查目标环境类型，prod 需 admin）；fail-closed（EnvID 空/解析失败按生产处理）。
- 应用级权限（App Member，2026-08-24）：租户级 RBAC × 应用成员角色（owner/maintainer/developer/viewer，动作 manage/release/write）；`Application.Restricted` 开关，开启后 `AppGuard` enforcement，非成员 fail-closed；非受限应用成员管理/开 restrict 仅租户管理员（防自封提权）；webhook 不接 Guard（机器凭证，approve 门禁兜底）。

### 登录会话与安全

- console-user 走密码登录 + httpOnly cookie（paas_access 15min / paas_refresh 7d，SameSite=Lax）；`BearerAuth` 三通道：cookie > JWT > API Key。
- 生产强制：`PAAS_PROD=true` 时 JWT secret（≥32 字节）与 `PAAS_COOKIE_SECURE=true` 必配否则拒启。
- 登录限流（per-IP/per-username，带惰性 GC）+ 防账号枚举（统一 401 + dummy bcrypt 防时序侧信道）+ 登录审计 + CSRF Origin/Referer 同源校验 + 安全 headers 中间件 + panic recover 中间件。
- clientIP 取 X-Real-IP → XFF 最右段 → RemoteAddr（不信任 XFF 首段）。

### 响应契约与错误处理

- 所有平台 CRUD 成功响应 `{data:T}`（`httputil.WriteData/WriteDataCreated`），错误 `{error:msg}`；500 脱敏（`WriteInternalError`）；底层技术错误（pgx/SQLSTATE/dial tcp 等）经 `WriteServiceError` 分流到 500 脱敏。**禁止裸 `WriteError(w, 500, err.Error())`**。
- 例外（裸 JSON）：OpenAI 兼容 `/v1/*`、`/livez`、数据面 `/dp/*`、配置中心发现 `/api/configcenter/.../published`。
- 所有接收 JSON body 的写端点登记 OpenAPI `WithReqBody`；新端点必须 Operation 登记。

### 敏感数据

- Secret/凭证全端点掩码（`Masked()`/`MaskConnection`），明文仅内部 Resolve/注入用；编辑不回填真值。
- 静态加密（2026-08-29）：`internal/crypto` AES-256-GCM，格式 `enc:v1:<base64>`；**Decrypt 无前缀原样返回**（存量明文零迁移）。master key env `PAAS_SECRET_MASTER_KEY`（生产必配否则拒启）。切入方式：security/appconfig 用装配层装饰器；dataservice 用 PG 持久层加密（字段级按 MaskKeys，host/port/user 明文保留）。**装配类改动必须 e2e 断言落库形态（SELECT 看 enc:v1 前缀）**。
- 日志脱敏：构建日志 Git token（`MaskToken`）、启动日志 API Key（前 6 字符）、panic 栈经 MaskToken。
- 出站 HTTP client 统一用 `httputil.NewClient(timeout)`（不跟随重定向，防凭证外发）。
- 平台级 Secret（`scope=platform`）删除/写仅 super_admin（`gateway.IsPlatformAdmin`，非 tenant-admin）。

### 审计

- 写操作全记审计（只增不删）：security handler 自动记；identity（tenant/user/apikey CRUD）、maas/engine、configcenter、pipeline（change_/batch_/canary_）、devops PR（review/merge）、app member/restrict 均已接入。actor 从 ctx 取，空租户归 "platform"。

### 配额

- `billing.CheckAndInc(ctx, resource, delta)` 原子「检查+递增」（PG `FOR UPDATE` 行锁）；application/workload Create 拦截 429，repo 失败回滚 -1，Delete 回收配额（admin 级联删同款）。

### 并发与资源（踩坑沉淀）

- Go mutex 不可重入（第 9 轮自死锁教训）；PG 补偿事务用 `context.WithoutCancel`（防 SIGTERM 后用已 cancel ctx 落库，如 runBuild）。
- typed-nil 装箱陷阱：构造函数失败时返**接口类型 nil** 而非 `(*T)(nil)`（装箱后 != nil，守护失效）——已有 dataservice restarter、build log streamer 两例。
- `defer rows.Close()`；HTTP Body Close；goroutine 用 `select <-ctx.Done()` 退出；前端轮询 `onUnmounted` 清理 + in-flight 防重入。
- 深拷贝防 race：返回给外部的对象/切片一律 clone；读多写少考虑 RWMutex。
- SSE：`X-Accel-Buffering: no` + Flusher 委托（中间件 statusRecorder 必须实现 Flusher）+ 前端 fetch 带 `Accept: text/event-stream`。

### PG 持久化

- Repository 接口是后端切换点；除 observability 惰性 mock 外全模块已迁 PG（`PAAS_DB_URL` 切换，空=内存）。
- `internal/storage/pg/`：pgxpool + golang-migrate（embed SQL）；各模块 `internal/<mod>/pg/store.go` 显式 `WHERE tenant_id=$1`；多值字段 JSONB；迁移文件幂等（`ADD COLUMN IF NOT EXISTS`）+ 0001 合并 schema 双写。
- seed 幂等：各模块 memory 导出 `Seed<X>()` 供 PG 复用（同一真源）；demo seed 门控 `PAAS_DISABLE_DEMO_SEED=true`（生产必设，关闭演示凭证/示例应用）。
- 关键表有 RLS 策略 + partial unique index（如 pipeline active run、cc publish active、platform/tenant secret scope）。
- builtin 模板升级走 `Version` 字段比对 + `ReplaceBuiltinTemplate`（改 builtin 只需 Version+1，启动自动覆盖，**禁止手写 migration UPDATE 补救**）。

## 模块地图（完成度 ~96%，详细设计一律看 docs/superpowers/specs/ 对应文档）

### MaaS 推理（Model → Channel → Provider 三层）

- `pkg/provider/`：Provider/Model/Channel 契约 + 5 个错误 sentinel（ErrCredentialMissing/Invalid/ErrUpstreamRateLimit/Unavailable/Config 驱动降级分类）+ Vendor 预设（BaseURL+凭证，选供应商自动带入通道字段）。
- `internal/maas/`：插件 + OpenAICompatibleProvider（纯 net/http + SSE）+ catalog seed（**仅 airouter 网关真实模型 12 个**，`DeprecatedSeedModelIDs` 清理遗留）；模型/通道/供应商 admin CRUD（`/api/admin/models|providers`，super_admin）+ DB 驱动 catalog + 写后增量刷新 gateway（reloadModel）。
- `internal/core/gateway/`：API Key 鉴权 / OpenAI 兼容 SSE（含 reasoning_content 透传 + stream usage 计量）/ 请求级 failover（degraded 切通道，offline 标 offline）/ Meter token 计量。
- 凭证：平台级 Secret（`sec-platform-airouter`）经 env `PAAS_AIROUTER_API_KEY` 注入，api_key 不入库；未配凭证 503 降级不 panic。

### 应用主线 + 工作负载 + 环境

- `internal/core/application/`：Application（Bindings 真源 + Resources 派生）+ OnAppCreate hook（自动绑定默认 CI/CD 流水线）+ CascadeDeleter（级联 workload/appconfig + 配额回收 + 清 app 级配置 ns）。
- `internal/workload/`：Workload（Service/Job/CronJob，多服务 Service 字段）/ Instance（Pod 级）/ PodLogs（越权校验）/ ResourceSpec（cpu/mem requests+limits，**生产禁 BestEffort 双入口**）/ K8s 数据面（CRD 投影 + WorkloadReconciler，fake client 测不出 apiserver 校验类 bug，需真实集群验证）+ LaneGC（泳道回收级联 cleaners）。
- `internal/environment/`：环境一等公民（type prod|test）+ PromoteOrder 发布阶序 + `NextPromoteTarget`。
- 命名约定：Deployment 名 = 工作负载 ID；Pod = `<id>-<rsHash>-<podHash>`；治理 Service.Name == K8s Service 名 == 工作负载名（数据面发现靠此对齐）；lane label `paas.aitoys/lane`（值经 sanitize，K8s label/Service 名不许 `/`）。

### 泳道（Lane）

- `internal/lane/`：Lane 一等实体（standard=可 TTL 回收 / permanent；Weight 留位恒 0）；`/api/lanes`（DELETE=closed + 级联回收全部工作负载 + 前置校验进行中 run 409；生产泳道 DELETE/PUT 需 prod:write）。
- L2 跨泳道发现：`/dp/instances?service=x&lane=feature-x` 先查 `<service>-<lane>` 降级 `<service>`，均经 tenant label 校验防泄漏；SDK `LaneMiddleware`/`ApplyLaneHeader` 染色透传（入口染色归 SDK，平台 gateway 不做）。

### DevOps CI/CD

- `internal/devops/`：CodeRepo（internal=Gitea / external 校验防 RCE）/ BuildRun（builder 三模式：`k8s` DooD Job 默认集群 / `process` 本地 / `mock`）/ Image（digest 不可变）/ Release（编排基线 Workload + PreviousImageID 回滚指针 + PromotedFrom 晋升链 + LaneID）。
- 内置 Gitea（`internal/devops/gitea/`）：无头 + paas-bot basic auth；PR API（列表/diff 2MB 截断/评审/merge，`ErrPRExists` 幂等）；分支 API（404/500 归一 NotFound）。
- registry v2 适配（`internal/devops/registry/`）。
- **pipeline 引擎**（`internal/devops/pipeline/`，模板+绑定模型）：PipelineTemplate（builtin tpl-ci/tpl-cd，Version 升级覆盖）+ Pipeline（绑定 TemplateID + ParamOverrides）+ PipelineRun/StageRun；占位符 `{{app.env.prod}}`/`{{app.repo}}`/`{{run.branch}}` 经 ResolveStages 解析固化进 run（模板改了不影响在跑的 run）。
- 8 种 stage：build（buildArgs 透传防注入）/ **deploy**（产部署记录可回滚，不打版本；泳道一等参数；联调泳道非 prod 未显式 replicas 置 1）/ test（smoke+manual）/ approve / **release**（版本里程碑：git tag + Image.Version，不部署）/ **promote**（Deploy 到下一阶序环境基线）/ baseline（只合并主干）/ **canary**（并行验证式金丝雀：`canary-<runID>` 泳道 1 副本 → 等待人工 promote（基线全量滚动）/terminate（零风险退出）；CAS 认领两段锁防并发相反决策；Resume 拒绝 canary stage）。
- 单实例串行（partial unique index + HasActiveRun 409）；prod:write 静态预演（`allowProdFlow` 防 developer 经 stage 组合绕过）；webhook 触发器（token 常量时间比较，branch glob，跳过 prod:write 靠 approve 门禁）。
- **变更管理火车发车**（`internal/devops/change/`）：Change（分支粒度）+ IntegrationBatch（临时集成分支，冲突/测试/发车状态机，惰性终态推进无后台 goroutine）+ 既有 Release；批次触发 CI branch=集成分支名（即泳道）。
- 通知聚合 `GET /api/notifications`（实时拼装无持久化）+ 值班台/详情页路由 `/devops/*`。

### 数据服务 + 备份

- `internal/dataservice/`：6 Kind（db/cache/mq/storage/vector/search）同构领域 + KindMetas 表单元数据真源 + Connection（凭证生成持久化 / FQDN / MaskConnection）/ Engine 三模式（managed/external-shared/external-dedicated，轻量引擎 qdrant/meilisearch）；reconciler 建 Secret+Service+StatefulSet（OwnerRef 级联）+ exporter sidecar（指标按 `paas_aitoys_dataservice` label 过滤）；PG 持久层加密；占位 Kind 返 failed 不拉起（避免 port=0 死循环）。
- 绑定注入：`application.BindingInjector` 桥接，按 Kind 写 appconfig 连接条目（DATABASE_URL/REDIS_URL/...），解绑重注入最后剩余绑定。
- `internal/backup/`：备份 CRUD（mock 完成）+ prod:write fail-closed。

### 服务治理（四件套已落地）

- `internal/governance/`：Service/Instance（真源=K8s Endpoints 经 `InstanceDiscoverer`，手动注册表兜底）/ Route（Host 对外域名，ApplyRepo→K8sRouteApplier 聚合 Ingress 下发）/ CircuitBreaker（即时评估确定性生成，真实流量采集留后续）。
- `internal/configcenter/`：Namespace 双 scope（app 派生懒建 `EnsureByAppEnv` / shared）+ 版本化 Publish 不可变快照（partial unique index 防双 active）+ **LaneOverride**（lane key 级覆盖，merge 链 app×env → lane，version+overrideHash 双指纹）+ 按应用名发现（未知/无 ns/无 active 统一 `{"published":false}` 不泄漏）+ 泳道回收级联 `LaneOverrideCleaner`。
- 与 appconfig 严格区分：appconfig=应用×环境静态配置重启注入；configcenter=运行时动态版本热更新。

### 可观测（三支柱 + 告警 + OTel）

- `internal/observability/`：memory 惰性 mock + `real/`（Prom/Loki/Jaeger 适配，env 开关 `PAAS_PROM_URL|PAAS_LOKI_URL|PAAS_JAEGER_URL` 三支柱独立混用）+ `compose/` 聚合。多租户：PromQL/LogQL/Tempo 均带 tenant/ns 过滤；应用级查询走 WorkloadLister pod 正则（`wl-<id>-.*`）。
- trace 后端 = Jaeger all-in-one（非 Tempo，OOM）；`internal/observability/tracing`：env 门控 OTLP init（`PAAS_OTEL_ENDPOINT`，空=noop）+ otelhttp mux 中间件 + **ErrorTraceMiddleware**（error span 写 exception.type/message，panic 写 exception.stacktrace）；GenAI 语义约定（gen_ai.*）手写 attribute。
- trace-log 关联：`ListLogs` 支持 `?traceId=`（内存过滤，trace_id 非 Loki label）；前端 trace 列表含 traceID 深链 + 错误摘要 + 关联日志面板。
- `internal/metrics`：Prometheus registry + HTTPMiddleware（在 recovery 外层才能记 panic-500）+ `/metrics`。
- trace 按 service.name 查（非工作负载 ID）；span 属性透传 + 瀑布树形可视化（`useSpanTree` composable）。

### 安全 + 计费

- `internal/security/`：Secret（secret|certificate，静态加密）/ AuditLog（只增不删，LIMIT 1000 防御）；平台级 Secret 越权防护。
- `internal/billing/`：Quota（-1=无限）/ Usage（byApp 精确归因）/ Bill（unpaid→paid 不可变）；PriceTable mock 单价；配额强制拦截见横切。

### 智能体（AI 编排）

- `internal/ai/`：agent/tool/knowledgebase/prompt/skill 五实体（Skill=可叠加能力指令包）+ runtime（多轮记忆 conversationStore / eval 历史落库 / workflow 编排引擎 llm/condition/approve/end 节点 DB 状态驱动恢复）+ Marketplace（`marketplace_items` 平台级快照不可变 + fork 安装，SanitizeConfig 剔除凭证，EntityForker 依赖倒置）；admin 总览 `/api/admin/ai/*`（ListAll 只读）。
- REST `/api/skills|agent-evals|marketplace` 等；console-user「智能体」菜单 + `Explore.vue` 广场。

### 身份 + 前端

- `internal/core/identity/`：Tenant/User/Role/APIKey CRUD（`/api/admin/*` super_admin）+ 租户自助 `/api/api-keys`（capRoles 求交零提权）+ `/api/users`（本租户）+ DeleteTenant 非空 409；写操作全审计。
- console-admin：20 管理页（身份/推理/资源总览 14 三级菜单）+ AI 编排总览 + 流水线模板管理（builtin 只读锁）；admin 跨租户写规范：独立端点 + adminGuard + tenant.WithTenant 派生 + 绕过 prod:write 但全审计 + 代建消耗目标租户配额。
- console-user：应用工作台（10 tab 三组）+ 三层 IA（资源中心=数据服务 / 工作负载 / 平台能力）+ 顶栏环境 scope（生产 gated 15min 自动回退 + 视觉强隔离）+ `useDangerConfirm`（生产输入名称确认，isProd 显式入参防顶栏漂移）+ ESLint（新代码禁 `as any`，错误文案走 `apiError/respError`）。
- 主模块 vs 应用详情原则：主模块=全局视图 + 维度是过滤器不是门槛 + 告警漏斗式下钻。

## API 契约

route registry（`internal/apiroute/`）驱动 mux 注册与 spec 生成；`GET /openapi.json` 公开 + `GET /docs` Scalar（公开）。前端 `openapi-typescript` 生成 `types.gen.ts`——**现行策略：改到哪个模块才迁哪个模块的手写 interface**（不一次性重写）。

## 标准基线（审计外部基准，2026-08-26 设立）

**为什么有这一章**：守卫式审计查不出「该实现而未实现」的完备性缺口。审计三原则：
1. **完备性对表**：重要模块审计对照下表问「业界标准能力我们缺哪些」。
2. **旅程式审计常态化**：每模块至少一轮「模拟真实用户完整旅程」（模块接缝断链只有旅程能发现）。
3. **留后续复审**：每次大功能落地后扫「留后续」清单，问「这笔欠账现在该还了吗」。

| 模块 | 业界对标 | 当前缺口 |
|------|---------|---------|
| 工作负载部署 | K8s Deployment | HPA/PDB/优先级缺 |
| 生产发布策略 | Argo Rollouts / Flagger | 按比例切流缺（依赖流量权重）；蓝绿可借泳道底座 |
| 泳道/流量隔离 | 阿里全链路灰度 MSE | 入口流量按比例切泳道缺 |
| 可观测 | Grafana/Pyroscope | SLO 缺 |
| 秘钥管理 | Vault/KMS | 轮转/过期缺 |

**工作负载资源最低标准**：生产 Pod 必须 requests+limits（非 BestEffort）；有入流量的服务必须有 readiness probe（已满足：TCP probe）。

## 遗留债务索引（按需复审）

- **安全**：refresh rotation + logout jti 黑名单、限流多副本 Redis、密钥轮转/过期、各 client CheckRedirect 已收口但新 client 需用 httputil.NewClient。
- **持久化/规模**：observability 迁 PG、RLS 全覆盖、ClickHouse 时序计量、审计分页、流水线运行历史分页、configcenter 长连接 watch/灰度下发。
- **发布/部署**：batches 切流实现（D1）、蓝绿模板变体、金丝雀告警联动、promote 跨级跳迁、PVC 数据服务重启保数据（现 STS 删 pod 不删 PVC 可恢复）、异步创建流转 PG 反向同步、真实备份任务。
- **MaaS**：通道健康检查真实探活、凭证测试 ping、Vendor 改配置后存量通道/绑定自动同步、按用户 scope 列 API Key。
- **流水线**：cron 触发器、Gitea webhook 自动注册、process/mock 模式实时构建日志、租户自定义模板编辑器、行级评论、CI 状态挂 PR。
- **前端**：应用级权限按钮级显隐、命令面板（Cmd+K）、console-admin 手写 interface 不迁（现行策略）。
- **智能体**：admin AI 总览写操作（L2）、工具/KB 删除前引用强校验、skill 版本化、Marketplace 评论/评分/版本链。
- **开源就绪**：dev 脚本内网默认值（hub.wang.dd/paas.k8s.dd）保留 + 头注释说明覆盖方式；`values-paas-k8s.yaml` 含 dev 密码（已加 dev-overlay 声明）。

## 开发约定

- 新建模块或引入新技术栈时，同步更新本文件对应章节（新增内容写**摘要 + spec 链接**，不写过程细节）。
- 注释语言与代码库现有注释保持一致。
- **未经用户明确要求，不要执行 git commit / 分支操作。**
- 所有依赖须与 Apache 2.0 兼容；新增依赖前确认 license。
- 业务领域逻辑绝不进 Platform Core；判断标准："MaaS / 治理 / DevOps 都会用吗？"
- 多租户隔离由 Core 统一治理，插件不得绕过。
- **部署基线**：每批功能提交后主动跑 `./scripts/deploy-k8s.sh` 重新部署 dev 集群（常驻授权，无需再问）；部署后 e2e 验证核心端点。
