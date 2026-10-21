# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 所有 Agent 必须遵守的研发规范

所有 agent（含子 agent）必须遵守 [贡献指南：临时文件与验证产物](CONTRIBUTING.md#临时文件与验证产物)：**截图、录屏、日志、临时导出、试验脚本及原型统一输出到项目根目录的 `tmp/<任务名>/`，禁止散落根目录或源码目录**；从任何子项目执行工具时也使用根目录 `tmp/`，该目录不入 Git、不进入镜像构建上下文，正式交付资产按指南放入对应目录，交付前检查本任务产物。

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
frontend/                       # pnpm workspace（四套前端）
├── console-admin/              # 后台管理（基于 vue-admin 基座，MIT）
├── console-user/               # 用户控制台（经典版 /console/）
├── console-user-v2/            # 用户控制台 v2（新 IA 试验版 /next/，详见「身份 + 前端」章）
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
- **观测栈离线打包（`bundle --with-observability`，默认关）**：把 3 个上游 chart（`helm pull`）+ `deploy/observability/*` 清单 + **`scripts/deploy-observability.sh` 脚本本身**打进包；install 侧若 manifest 含观测栈条目则自动接着调包内脚本（`OBS_CHART_DIR`/`OBS_MANIFEST_DIR` 指向解包目录，MIRROR 默认 none）。chart 名/版本/仓库 URL 的**真源是脚本**（`--chart-versions` 机器可读输出），Go 侧不重复硬编码（两处各写一份必然漂移）。**包内不含镜像**——目标机需能拉到观测栈各镜像，见 `docs/deploy/dependencies.md`。
- **K8s 集群部署**：core 单镜像同源 serve 前端 + API（无 CORS）；集群内自建 registry（NodePort 30050，Pod 镜像用 `<nodeIP>:30050`，kubelet 解析不了 svc DNS）；`scripts/deploy-k8s.sh` 检测 worker nodeIP + envsubst 注入 values。前端四套 SPA `//go:embed` 进 core 二进制（`internal/web/`），路由：`/console/*` → console-user、`/next/*` → console-user-v2、`/admin/*` → console-admin、`/*` → landing、`/api|/v1/*` → API。
- **Dockerfile**：多阶段（node:22-alpine 前端 → golang 交叉编译 → distroless）；国内源默认，`--build-arg` 覆盖。**必须 `DOCKER_BUILDKIT=1` + builder `FROM --platform=$BUILDPLATFORM`**（否则 arm64 Mac QEMU 下 Go SIGSEGV）。
- **可观测栈部署**：唯一入口 `scripts/deploy-observability.sh`（Prom/Loki/Grafana/Jaeger/Ingress；**勿手工 helm upgrade**）。`deploy/observability/*` 全带 `${...}` 占位符（NS_OBS/STORAGE_CLASS/DOMAIN_SUFFIX/镜像 registry），脚本 envsubst 注入 → 渲染 → 与线上 diff → **先 `kubectl apply` 原始清单再 helm upgrade**（grafana 挂载面板 CM，顺序反了则 FailedMount → `--wait` 超时中断 → CM 永远建不出来）→ 抓取目标断言；`--check` 只预检不写集群。换集群只改环境变量，values 不动。脚本存在的理由 = 该栈曾无部署路径，导致带外 `kubectl set image`/`kubectl apply prometheus.yml` 被 helm 静默打回、失效死键潜伏（`kubeStateMetrics.*`）、冗余抓取 job 与内置 job 重复致指标翻倍。Grafana 预配 10 个面板（file provisioning，4 个 CM 挂 /tmp/dashboards/{app,cp,infra,ds}）：应用总览 / 控制面 / 节点总览 / 数据服务总览 / 集群与工作负载 / **每引擎官方面板 PostgreSQL·Redis·NATS·Qdrant·MinIO**（2026-09-25，对标 9628/11835 等官方 dashboard；聚合面板管跨引擎关键健康量，引擎面板管深度指标）。node-exporter 与 dataservice exporter 端口 9200（9100 被 dev 物理网阻断，见 prometheus-values 注释）。**promtail pipelineStages 必须 cri+docker 双解析**（docker shim 集群 /var/log/pods 软链到 docker json 日志，单 cri 时包装 JSON 原样入 Loki，paas/Grafana 日志全显示原始 JSON；paas 侧 `unwrapDockerJSON` 兜底存量行，2026-10-13）。
- **helm 注意**：chart templates/ 内 CRD 不随 upgrade 应用，需显式 `kubectl apply -f config/crds/`；镜像 tag 不变不触发 rollout，需 `kubectl rollout restart`；手动 helm 前必须 `export NODE_IP`（否则 envsubst 写坏 registry 地址）。**子 chart 镜像变量拼法不一**：grafana/promtail 子 chart 拼 `image.registry + "/" + image.repository`（默认 registry=docker.io），loki 子 chart 用整串 `image.repository`——塞错位置会渲染出 `docker.io/<host>/...`，helm 不报错、Pod ImagePullBackOff。
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

- 写操作全记审计（只增不删）：security handler 自动记；identity（tenant/user/apikey CRUD）、maas/engine、configcenter、pipeline（change_/batch_/canary_）、devops（PR review/merge + build_run/release create/rollback/promote）、app member/restrict、appconfig、backup、messaging、dataservice（租户侧 delete）、governance、lane、service、workflow、billing 均已接入（元审计 2026-09-04 全量比对补齐）。actor 从 ctx 取，空租户归 "platform"。
- 审计接入双维度核查：「代码接了」+「装配层注入」缺一不可（appconfig 曾代码有装配漏）；新模块 handler 加 WithAudit 后必须到 cmd/core/main.go 对应装配段补注入。

### 配额

- `billing.CheckAndInc(ctx, resource, delta)` 原子「检查+递增」（PG `FOR UPDATE` 行锁）；application/workload Create 拦截 429，repo 失败回滚 -1，Delete 回收配额（admin 级联删同款）；devops `CreateRelease` 内建基线 Workload 经 `WithQuotaInc` 同源接入（memory/pg 双实现，2026-09-06 对账审计 C1 修复）；启动自愈 `recalcUsageIfDrift` 对齐 applications/workloads/dataservices 行数计数。

### 并发与资源（踩坑沉淀）

- Go mutex 不可重入（第 9 轮自死锁教训）；PG 补偿事务用 `context.WithoutCancel`（防 SIGTERM 后用已 cancel ctx 落库，如 runBuild）。
- typed-nil 装箱陷阱：构造函数失败时返**接口类型 nil** 而非 `(*T)(nil)`（装箱后 != nil，守护失效）——已有 dataservice restarter、build log streamer、alert engine Restore 三例（第三例 2026-10-18：内存路径 `*obspg.Store(nil)` 装箱成非 nil StateStore，Restore 放行后 LoadStates 解引用 nil db panic 卡死启动且不打印 panic——集群 PG 路径不触发故 0042 起潜伏两月；修法=参数收窄具体类型在函数内装箱前判 nil，`TestBuildObservabilityStoreTypedNilPGStore` 锁）。
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
- `internal/workload/`：Workload（Service/Job/CronJob，多服务 Service 字段）/ Instance（Pod 级）/ PodLogs（越权校验）/ ResourceSpec（cpu/mem requests+limits，**生产禁 BestEffort 双入口**）/ **MetricsPath（指标端点声明，opt-in：非空才注 `prometheus.io/scrape` 注解，空=不采集，见下）** / K8s 数据面（CRD 投影 + WorkloadReconciler，fake client 测不出 apiserver 校验类 bug，需真实集群验证）+ LaneGC（泳道回收级联 cleaners）+ **OrphanGC**（WorkloadReconciler 周期对账平台仓库，平台侧已删的 CR 级联回收，宽限 10min，防僵尸 Service 抢名；主体已泛化 `collectOrphanCRs`，dataservice 同款接入）。
- **指标采集 opt-in（2026-09-24 语义反转）**：`spec.metricsPath` 非空（须 `/` 开头绝对路径，`Validate` 拦）时 reconciler 给 Pod 注 `prometheus.io/scrape=true` + `path=metricsPath` + `port=<ContainerPort|Port>`；**空则一个注解都不注**。反转前是无条件注 `/metrics`，不暴露该端点的应用（如 nginx frontend）变永久 down target（Prometheus 报 `unsupported Content-Type "text/html"`），将来 `up==0` 告警即假告警源。存量迁移：`PUT /api/workloads/{id}` 带 `metricsPath`（指针语义：缺省=不改，`""`=撤下）；**不做 DB 回填**（回填会把无指标端点的存量负载一并补上，等于固化 bug）。
- `internal/environment/`：环境一等公民（type prod|test）+ PromoteOrder 发布阶序 + `NextPromoteTarget` + **删除依赖预检**（2026-10-19 spec 专项04 §6：`GET /api/environments/{id}/usage` 返回结构化 `{workloads,dataservices,lanes}` 清单，DELETE 409 文本逐项列出；前端有依赖直接展示清单阻断，不用重复确认弹窗代替依赖检查；**usage 路由必须先于 GET /{id}**——rest 含斜杠时 Split[0] 截出的 id 会误匹配单资源分支）。环境健康算法按类型区分：Job/CronJob 完成态 ready<replicas 是正常终态不判坏，仅 status=failed 计异常。
- 命名约定：Deployment 名 = 工作负载 ID；Pod = `<id>-<rsHash>-<podHash>`；治理 Service.Name == K8s Service 名 == 工作负载名（数据面发现靠此对齐）；lane label `paas.aitoys/lane`（值经 sanitize，K8s label/Service 名不许 `/`）。

### 泳道（Lane）

- `internal/lane/`：Lane 一等实体（standard=可 TTL 回收 / permanent；Weight 留位恒 0）；`/api/lanes`（DELETE=结构化 CloseResult `{lane, reclaimed, failedIds}`——部分回收失败不假报成功，已 closed 重试 DELETE 幂等重扫遗留；前置校验进行中 run 409；生产泳道 DELETE/PUT 需 prod:write）。
- **基线三层保护（2026-10-17，spec `docs/superpowers/specs/2026-10-17-lane-access-experience-design.md`）**：ValidateName 拒 default（`ErrLaneProtected` 403）+ handler DELETE 拒遗留 default 实体 + `ReclaimLane` 拒 default/空串——「不能只靠 UI 隐藏按钮」。
- **dataplane 三态回退（反转旧静默降级）**：`fetchInstances` 返 (insts, svcExists, err)——①Service 在且有 ready 实例→泳道版本；②在但无 ready（缩 0/Pending）→**显式空不回退**（不把失败藏进基线成功）；③不存在（未覆盖）→回退本环境基线；apiserver 错误透传不静默回退。
- **SDK 显式 default 哨兵**：`WithLane("default")` 存 `\x00default` 哨兵，`resolveLane` 据哨兵返基线**不回落 SelfLane**（容器在 feature 泳道时显式请求基线不再被吞）；未指定仍回落 SelfLane（零改动染色不变）。
- **run.Lane 真源**：触发时 `deployLaneOf` 从解析 stages 提取首个 deploy 段非 default lane 固化进 `PipelineRun.Lane`（migration 0050）；泳道关联（详情 RecentRuns/关闭前置/GC 活跃判定）一律 `run.Lane==name ∪ run.Branch==name` 双通道，不再纯字符串匹配（遗漏显式命名泳道：branch=main+lane=feature-x 关联不上）。
- **可恢复回收**：`LaneStatusStore.Closed` 查实体状态——Sweep 对 closed 泳道遗留**立即清扫不等 TTL 也不受 permanent 跳过约束**（关闭语义优先，常驻失败重试不依赖普通 TTL 路径）；GC 删除失败 ID 进 `ReclaimResult.FailedIDs` 上报。
- **地址分类读模型**：`GET /api/lanes/{id}/access` 返回 `AccessEndpoint[]`（kind=business 基线域名/preview 泳道域名带「浏览器直达不染色」诚实 note/internal FQDN 仅集群内；source=domain 声明已下发/fqdn 派生未验证；URL 固定 http:// 如实标注不猜协议）。前端 LaneDetail 访问入口分区 + Workloads `?lane=` 深链过滤（裸分支泳道读路径零副作用不建实体）。
- L2 跨泳道发现：`/dp/instances?service=x&lane=feature-x` 按三态规则（见上），均经 tenant label 校验防泄漏；SDK `LaneMiddleware`/`ApplyLaneHeader` 染色透传（入口染色归 SDK，平台 gateway 不做）。

### DevOps CI/CD

- `internal/devops/`：CodeRepo（internal=Gitea / external 校验防 RCE）/ BuildRun（builder 三模式：`k8s` DooD Job 默认集群 / `process` 本地 / `mock`）/ Image（digest 不可变）/ Release（编排基线 Workload + PreviousImageID 回滚指针 + PromotedFrom 晋升链 + LaneID）。
- 内置 Gitea（`internal/devops/gitea/`）：无头 + paas-bot basic auth；PR API（列表/diff 2MB 截断/评审/merge，`ErrPRExists` 幂等）；分支 API（404/500 归一 NotFound）。
- registry v2 适配（`internal/devops/registry/`）。
- **pipeline 引擎**（`internal/devops/pipeline/`，模板+绑定模型）：PipelineTemplate（builtin tpl-ci/tpl-cd，Version 升级覆盖）+ Pipeline（绑定 TemplateID + ParamOverrides）+ PipelineRun/StageRun；占位符 `{{app.env.prod}}`/`{{app.repo}}`/`{{run.branch}}` 经 ResolveStages 解析固化进 run（模板改了不影响在跑的 run）。
- 8 种 stage：build（buildArgs 透传防注入）/ **deploy**（产部署记录可回滚，不打版本；泳道一等参数；联调泳道非 prod 未显式 replicas 置 1）/ test（smoke+manual）/ approve / **release**（版本里程碑：git tag + Image.Version，不部署）/ **promote**（Deploy 到下一阶序环境基线）/ baseline（只合并主干）/ **canary**（并行验证式金丝雀：`canary-<runID>` 泳道 1 副本 → 等待人工 promote（基线全量滚动）/terminate（零风险退出）；CAS 认领两段锁防并发相反决策；Resume 拒绝 canary stage）。
- 单实例串行（partial unique index + HasActiveRun 409）；prod:write 静态预演（`allowProdFlow` 防 developer 经 stage 组合绕过）；webhook 触发器（token 常量时间比较，branch glob，跳过 prod:write 靠 approve 门禁）。
- **Agent 编排执行域接入（2026-10-11，spec `docs/superpowers/specs/2026-10-11-agentrun-pipeline-integration-design.md`）**：第 9 种 stage **agent**（AI 研发：需求→代码分支）桥接外部 agentrun 平台。编排权唯一归 paas——只调其 `POST /api/runs` 单步执行（**不用**其 AgentPipeline 编排/审批，防双状态机）；协议 v2 = Create+Poll+**显式 Cancel**（Abort 级联，agent run 花 token 不能只放弃等待）；产出分支经 `StageRun.Output{branch,runName}` 衔接（build `branchSource=priorAgent` / baseline `mergeFrom=priorAgent` / 成功后注册 Change open）；Retry 续接靠 `sr.Input["prevRunName"]`（**Retry 清 Output 保 Input**）+ `agentAttempt` 计数防撞名（对方 run 名=K8s CR 不可复用，第二次起名字加 `-rN` 后缀 + `workspaceFrom` 复用上次工作区）+ already exists 换名自愈 + `flushStageLog` 连带落 Input（提交后崩溃不丢续接凭据）；**幽灵分支防御**：成功后经 `AgentBranchChecker`（gitea GetBranch 桥接）校验产出分支真实存在，不存在判负（对方 runner 曾「succeeded 未推送」致下游 review 空转 15min，2026-10-13）；需求文本载体=触发 API run 级 `stageParams`（应用点在 prod 预演之前，防改 envId 绕 prod:write）。builtin 模板 tpl-agent-ship/tpl-agent-review（KindAgent）。env `PAAS_AGENTRUN_URL/TOKEN`。引擎侧配套（2026-10-13）：`flushStageLog` 长 stage（smoke/agent）日志即时落库防 running 期间前端 0 字节；`WorkloadProber` 可选接口把探活地址（集群内 FQDN）与展示域名（deployDomain 泳道域名）分离；`SweepInterrupted` 两处 UpdateRun 补 `tenant.WithTenant`（此前 sweep 自上线起因 missing tenant 全部写失败）（空=模块禁用：agent stage 报错+观测 503；Gitea 同款先例）。观测通道 `/api/agentrun/*` 反向代理（`internal/agentrun`）：用户 JWT 从 paas_access cookie 值注入 Authorization（API Key 拒 401）、**ns 服务端强制 agent 剥离客户端传入**（ns=租户边界）、上游错误 502 脱敏；权限 `agentrun:read/write`，写方法轻量审计。前端：DevOps「Agent 运行」tab（列表，不经 pipeline 的 webhook/手动 run 也在此）+ `/devops/agent-runs/:name` 详情；触发 agent 模板时表单填需求文本。执行域统一标准：领域 run 归执行域、paas 只存句柄+Output；每域一个 adapter 接口（`AgentRunner` 之于 agentrun = `Builds` 之于 builder）+ env 三件套；第 3 个长任务域接入时提取显式 StageExecutor。
- **智能研发一条龙（2026-10-13，tpl-agent-ship v2）**：`POST /api/applications/{id}/agent-ship/run`（body {request, review, ship, release}，release 依赖 ship 400）——内部 Ensure 幂等绑定 tpl-agent-ship + **run 级段裁剪**（`agentShipSegments` stage Name→段 map，AI 开发恒留；改名需同步，`TestBuiltinAgentShipSegments` 锁）→ 复用 triggerRun 全套校验（validateDeployEnvs/appGuard/prod 预演）。模板 v2 全链：AI 开发→AI 代码审查（agent `stage=review` + **`branchSource=priorAgent`**，`priorAgentBranch` 让后续 agent stage 在前序产出分支上工作）→构建→部署测试泳道（lane 固定 `agent-ship`，占位符是整值匹配无子串拼接故不用 `{{run.id}}`）→冒烟→上线审批→金丝雀→发布→合主干（`mergeFrom=priorAgent`）；v1 的「合入审批」去掉（测试部署无需卡点，release 段自带上线审批保命）。变更由 registerChange 自动注册，前端免手动预建。变更 tab「✨ 智能研发」=一条龙主入口（勾选段直跑，跳 run 详情）；变更详情「智能研发」按钮=单变更二次委派（delegateAgentRun 通道）。
- **泳道部署独立域名（2026-10-13，deployDomain）**：非 default 泳道（联调/一条龙 `agent-ship`/canary）deploy 时若配 `PAAS_DOMAIN_SUFFIX` 自动派生 `<wl-name>.<suffix>` 并 SetDomain 写回（wl.Name 含 lane 段天然不撞基线 host，reconciler 建独立 Ingress）——此前泳道版本无任何对外域名，部署后验证断链（用户实测「域名都是同一个无法区分」）。port=0（无 Service）返空串防死链；default 泳道沿用 Domain 优先/FQDN 兜底。实现在 `cmd/core/pipeline_adapters.go` deployDomain（原 DeployCanary 域名段下沉合并，canary/泳道同款）。
- **委派智能研发（契约 v2 §3.3 双入口主入口，2026-10-12，D10 裁决）**：变更页「智能研发」按钮 → `POST /api/applications/{id}/changes/{cid}/agent-run`（`AgentRunDelegator` 依赖倒置，cmd/core 桥接 agentrun client 的 `CreateRunAsUser` 用户 JWT 通道——归因 claims.sub，区别于 stage 编排通道静态 token 归因 agent:<run名>）；spec 从变更上下文组装（workBranch=变更分支/base=基线/request 缺省 Title，run 名由 agentrun 自动生成）；仅 open 状态可委派；run 名回写 `Change.AgentRunName`（migration 0047）供变更页状态块精确查询（不依赖列表过滤参数）。前端 ChangeDetail 按钮+需求对话框+运行状态块（5s 轮询）。联调前置：固定 `PAAS_JWT_SECRET`（chart `auth.jwtSecret`）+ 同步 agent ns `paas-jwt-public`（agentrun 0.13.0 本地 HMAC 验签）。
- **变更管理火车发车**（`internal/devops/change/`）：Change（分支粒度）+ IntegrationBatch（临时集成分支，冲突/测试/发车状态机，惰性终态推进无后台 goroutine）+ 既有 Release；批次触发 CI branch=集成分支名（即泳道）。
- **研发交付闭环（2026-10-17，spec `docs/superpowers/specs/2026-10-17-development-delivery-design.md`）**：四件套——①**执行计划预演**：`POST .../pipelines/{pid}/preview`（只读，resolveForRun 与 trigger 共用解析链）返回中文逐段计划 + 预锁定候选 + missing 配置项 + `planHash`；trigger 带 planHash 则触发时重算比对，不一致 409 `ErrPlanChanged`（预览到执行之间候选/参数变化可感知；webhook/无 hash 路径不受影响）。②**产物锁定（F3）**：latestReady 的镜像消费 stage（deploy/canary/promote/release）**触发时**预解析一次写 `Params["lockedImageId"]`（固化进 StageRun.Input）+ `run.Artifact` 五元组（serviceId/repoId/commit/imageId/digest/buildRunId，digest/commit 经 `ImageMetaResolver` 桥接装配层）；resolveImage 执行期优先锁定值；Retry 复用 Input 天然同源——审批等待期间新构建不再漂移实际发布产物。③**结构化决策**：`POST /api/pipelineruns/{id}/stages/{idx}/decisions`（manual-test/approve 的 confirm/reject，reject 必填理由）+ `DecisionRecord` 落 run.Decisions（type/action/reason/actor/at）；manual-test reject 走 `RejectManual`（stage fail + 理由进 Error + fail Check）；approve reject 走 Abort；canary 仍走专属端点（decisions 对 canary 400）但补记 DecisionRecord；旧 approve 端点保留同语义。smoke 探活 `CheckResult` 结构化（type/target/expected/actual/note/status）。④**交付矩阵**：`GET /api/applications/{id}/delivery-matrix` 跨域聚合 workload×env×release（relKey{envID,workloadId}，只取 default 泳线基线 service 型），前端 DeliveryMatrix 服务×环境 digest 矩阵 + 跨环境一致 chip + 发布单深链。PG：migration 0049（plan_hash/template 元数据/artifact/decisions/checks JSONB）+ scanRunInto 统一三处 scan。canary abort 窗口修复：DeployCanary 成功即写 OutCanaryWorkloadID + flushStageLog 连带 Output 落库（就绪等待窗口内 Abort 级联可读，此前泄漏到 LaneGC TTL）；已知缺口：latestReady 应用级取最新镜像，多服务应用（paas-shop）镜像归属与 canary port 可能错配（Image 无 Service 字段，正解=加字段+构建回填+按服务过滤，留后续）。
- 通知聚合 `GET /api/notifications`（实时拼装无持久化）+ 值班台/详情页路由 `/devops/*`。

### 数据服务 + 备份

- `internal/dataservice/`：6 Kind（db/cache/mq/storage/vector/search）同构领域 + KindMetas 表单元数据真源 + Connection（凭证生成持久化 / FQDN / MaskConnection）/ Engine 三模式（managed/external-shared/external-dedicated，轻量引擎 qdrant/meilisearch）；reconciler 建 Secret+Service+StatefulSet（OwnerRef 级联）+ exporter sidecar（指标按 `paas_aitoys_dataservice` label 过滤）+ **内置 metrics 引擎抓取注解**（qdrant/minio 注 `prometheus.io/scrape`，minio 另需 `MINIO_PROMETHEUS_AUTH_TYPE=public`；meilisearch 需 experimental+鉴权带不了 header，暂不采）+ **OrphanGC**（2026-09-25 补齐：平台侧删除后 CR+STS 残留无回收路径，一次对账实测 9 个孤儿实例；现与 workload 同款周期对账回收）；PG 持久层加密；占位 Kind 返 failed 不拉起（避免 port=0 死循环）。
- 绑定注入：`application.BindingInjector` 桥接，按 Kind 写 appconfig 连接条目（DATABASE_URL/REDIS_URL/...），解绑重注入最后剩余绑定。
- `internal/backup/`：备份两模式（2026-10-19 spec 专项05 §4 落地，migration 0053）——**record 记录模式**（缺省兼容：仅登记意图，completed 即「记录已创建」，无产物无恢复证明，不再生成 deterministicSize 模拟值）+ **snapshot 真实快照**（仅 postgres+托管：K8sJob `pg_dump -Fc` 落租户 ns `paas-backups` PVC，凭证走 secretKeyRef 不落 Job spec，容器内算 sha256 echo `BK_META` 行，Pacer 读 Pod 日志解析）+ **restore 恢复作业**（Mode=restore：`backupRestorer` 复制源规格建新实例 + Job 内 `pg_isready` 等待后 `pg_restore`；仅 snapshot+completed+有 ArtifactRef 可恢复，覆盖原实例不支持）。状态机 accepted→running→completed|failed 全落库，`BackupPacer`（10s 扫/15min 超时）推进，core 重启 ListActive 恢复观察；ID 改时间基（旧确定性 ID 致同资源二次创建 409）。capabilities 端点 `GET /api/dataservices/{id}/capabilities` 按引擎/接入模式判定（外部实例提示「在源系统执行」）。**PVC 产物清理留后续**（删记录不删产物）。

### 服务治理（四件套已落地）

- `internal/governance/`：Service/Instance（真源=K8s Endpoints 经 `InstanceDiscoverer`，手动注册表兜底）/ Route（Host 对外域名，ApplyRepo→K8sRouteApplier 聚合 Ingress 下发）/ CircuitBreaker（即时评估确定性生成，真实流量采集留后续）。
- **路由下发状态证据链（2026-10-19，spec 专项04 §4）**：Route 加 `ApplyStatus`（''=未下发/applied/failed）+ `ApplyError`（截 200 字符）+ `AppliedAt`（migration 0052）；ApplyRepo 装饰器投影后 `recordApply` 回写（`context.WithoutCancel` 防请求结束丢租户）。「配置已下发」≠「公网可达」——DNS/TLS/HTTP 探测平台未做，前端 tooltip 明示。存量路由状态为空显示「未下发」，写操作后才回写。
- **治理 Service 部署即注册（2026-10-13）**：service 型 workload 创建时自动注册治理 Service（`governance.AutoRegisterService`，Name=工作负载名对齐 K8s Service 命名，幂等；挂点=`workload.OnCreateRepo` 装饰器，ApplyRepo 外层 + devopsRepo 捕获前——handler 与 Release 内建基线两条创建路径全覆盖）+ 启动自愈 `backfillGovernanceServices` 补存量。动机：应用详情-治理 tab 此前只读且需跳平台页手动注册（两表断链，test-web 旅程实测）。

- `internal/configcenter/`：Namespace 双 scope（app 派生懒建 `EnsureByAppEnv` / shared）+ 版本化 Publish 不可变快照（partial unique index 防双 active）+ **LaneOverride**（lane key 级覆盖，merge 链 app×env → lane，version+overrideHash 双指纹）+ 按应用名发现（未知/无 ns/无 active 统一 `{"published":false}` 不泄漏）+ 泳道回收级联 `LaneOverrideCleaner`。
- **泳道覆盖批次原子契约（2026-10-19，spec 专项04 §3）**：`POST .../lane-overrides/batch`——`ApplyLaneOverrides(appID, envID, laneID, ops, expectedCount)` 整批全成功或全回滚（PG 真事务 / memory 备份恢复）；expectedCount 与实际数不符 409 `ErrLaneOverrideConflict`（重预览，防「预览到执行之间漂移」）；upsert 且 key 无基线 draft 时同事务写占位；remove 幂等；expectedCount<0 跳过漂移校验。发布成功文案如实标注「客户端采用情况未知」（平台无回报通道）。
- 与 appconfig 严格区分：appconfig=应用×环境静态配置重启注入；configcenter=运行时动态版本热更新。

### 可观测（三支柱 + 告警 + OTel）

- `internal/observability/`：`real/`（Prom/Loki/Jaeger 适配，env 开关 `PAAS_PROM_URL|PAAS_LOKI_URL|PAAS_JAEGER_URL` 三支柱独立混用）+ `compose/` 聚合；告警状态机/历史已迁 PG（migration 0042，重启不丢）。多租户：PromQL/LogQL/Tempo 均带 tenant/ns 过滤；应用级查询走 WorkloadLister pod 正则（`wl-<id>-.*`）。
- **采集质量诊断（2026-10-19，spec 专项04 §5）**：`GET /api/observability/metrics/quality?targetType=app|workload|dataservice&targetId=` 独立诊断端点（不动 ListMetrics 主链路，alert engine 零影响）——逐约定 metric 发 Prometheus 即时查询，返回 ok/empty/failed 三探测态 + 报告级「未接入」（source=memory，PAAS_PROM_URL 未配置）。探测 PromQL 与主查询同源（`podSeriesDefs`/`dataserviceDefs` 抽出复用，否则结论失真）；「查询成功无数据」≠「采集源故障」（empty 引导查时间范围/目标/埋点）；网络错误脱敏（不回显上游 URL）。
- **告警规则校验与维度（2026-09-25 审计修复）**：`AlertRule.Validate` 收口 targetType（app/workload/dataservice；env 无指标数据源 fail-fast 拒绝）+ metricName（5 基础 + 6 数据服务业务指标）合法集合，memory/PG 双实现同源调用（PG 曾缺 Validate 致 2 条全空脏规则入库）；评估引擎 webhook 派发改 (alert, url) 绑定对（曾用平行切片索引错位 → 跨租户泄漏）；real 模式补 workload 维度指标源（`listPodSeries` 单 wl-<id> pod 正则，与 app 级共享实现）；前端规则表单指标随维度联动（dataservice 全 10 指标）、workload 实体下拉。
- **告警规则编辑/默认规则/恢复通知（2026-09-27）**：`PUT /api/observability/alert-rules/{id}` 更新规则（ID/TenantID 以库内为准，改阈值不断告警历史关联）；启动时给零规则租户灌 `DefaultAlertRules()` 4 条基础规则（demo 门控跳过，阈值按真实单位 cores/MiB 非百分比）；webhook 在 firing 与 resolved 转变时各通知一次（消费方闭环）。
- **抓取目标来自工作负载声明**：应用级 RED 数据依赖 Pod 上的 `prometheus.io/scrape` 注解，该注解由工作负载 `spec.metricsPath` **显式声明**才注入（opt-in，见「应用主线 + 工作负载」）。排查「应用面板无数据」先查 `spec.metricsPath` 是否声明、再查目标是否 up。node-exporter/dataservice exporter 走各自 chart/sidecar 注解，不依赖此字段。
- trace 后端 = Jaeger all-in-one（非 Tempo，OOM）；`internal/observability/tracing`：env 门控 OTLP init（`PAAS_OTEL_ENDPOINT`，空=noop）+ otelhttp mux 中间件 + **ErrorTraceMiddleware**（error span 写 exception.type/message，panic 写 exception.stacktrace）；GenAI 语义约定（gen_ai.*）手写 attribute。
- trace-log 关联：`ListLogs` 支持 `?traceId=`（内存过滤，trace_id 非 Loki label）；前端 trace 列表含 traceID 深链 + 错误摘要 + 关联日志面板。
- `internal/metrics`：Prometheus registry + HTTPMiddleware（在 recovery 外层才能记 panic-500）+ `/metrics`。
- trace 按 service.name 查（非工作负载 ID）；span 属性透传 + 瀑布树形可视化（`useSpanTree` composable）。

### 安全 + 计费

- `internal/security/`：Secret（secret|certificate，静态加密）/ AuditLog（只增不删，LIMIT 1000 防御）；平台级 Secret 越权防护。
- `internal/billing/`：Quota（-1=无限）/ Usage（byApp 精确归因）/ Bill（unpaid→paid 不可变）；PriceTable mock 单价；配额强制拦截见横切。

### 智能体（AI 编排）

- `internal/ai/`：agent/tool/knowledgebase/prompt/skill 五实体（Skill=可叠加能力指令包）+ runtime（多轮记忆 conversationStore / eval 历史落库 / workflow 编排引擎 llm/condition/approve/end 节点 DB 状态驱动恢复）+ Marketplace **六类实体**（skill/prompt/tool/agent/knowledgebase/workflow；`marketplace_items` 平台级快照不可变 + fork 安装，SanitizeConfig 剔除凭证，EntityForker 依赖倒置；KB 只发配置骨架、安装时经 finder 重绑本租户 vector/storage 实例；workflow 整包快照内嵌引用的 Agent/工具副本、安装后节点引用旧ID→新ID重写 + Enabled=false）；admin 总览 `/api/admin/ai/*`（ListAll 只读）。
- **工作流定义快照（2026-10-18，审计 WF-02 修复）**：`WorkflowRun.DefSnapshot`（Start 时固化完整定义，migration 0051 JSONB；UPDATE 有意不更新该列——创建后不可变）；进程重启后 paused run 的 Approve 无等待者拉起**优先用快照**（定义已改也按启动时拓扑续跑；存量 run 无快照 fallback 当前定义）；运行详情端点返回快照、**列表瘦身不带**（体积：每条含全部节点模板）；前端 WorkflowRunView 节点解释优先快照 + 拓扑漂移提示（「定义已修改，按启动时快照解释」/「存量运行无快照」）。集群实测：定义改成 s→e 后重启 Pod，Approve 仍按快照走完 s→af→e。
- **新版控制台正确性批次（2026-10-18，任务五包 A，审计 `docs/reviews/2026-10-17-console-next/all-modules-audit.md`）**：WF-01 工作流编辑器插入节点自动重连前驱（新节点接管 end 前位置，旧实现孤立节点被后端可达性校验拒）、删除节点继承后继不写死 'e'；MD-02/03 Playground 错误对象与正文分离（`msg.error` 独立字段，失败不入后续上下文）+ 停止/断流/完成三态分开（主动停止按钮 + interrupted 标注）+ 字符计数不再冒充 tokens；AI-01 四个 AI 列表页主数据与引用独立结算（`fetchSettled`，失败≠假空态，StatePanel 错误态）；AI-02 工具测试按钮仅 MCP 显示；AC-01 成员/Key 列表失败态；AC-02 API Key 明文关闭弹窗即清除（@close）；UX-01 环境页负载依赖失败时健康值显示「未知」不派生；DS-01 备份状态「已记录」不冒充可恢复；BL-01 账单标注估算口径+「标记支付」；Prompt 创建按钮如实标「创建并激活」。
- **AI 资产就绪批次（2026-10-18，任务五包 C，spec `docs/superpowers/specs/2026-10-17-console-ai-assets-design.md`）**：①工具发现 dialog 化——`POST /api/tools/{id}/test` 契约已有（tools/list 返方法名+说明+inputSchema），前端从 toast 改弹层展示发现结果+发现时间+副作用提示；②KB 嵌入模型只列 `capabilities` 含 embedding 的模型（下拉过滤，无兼容模型=禁选+阻塞指引），维度字段如实标注「须与模型输出维度一致/创建后不可改/不一致致检索失败」；③Prompt 显式激活——`POST /api/prompts` 加可选 `activate`（缺省 true 兼容旧语义；false=`CreateDraft` 草稿版本不动现有 active，memory/PG 双实现 FOR UPDATE 串行），激活确认框列引用该名称的 Agent（本地过滤全量列表）+影响说明；④Agent 列表「依赖就绪」列——本地元数据判断（模型在目录/工具存在且启用/KB 存在/Skill 存在且启用/promptRef 有激活版），引用数据加载失败=「未知」不画健康徽标（远程探活 readiness 端点留后续）；⑤广场安装结果 dialog——按 entityType 列类型化待办（工具补凭证/KB 重绑+传文档/工作流默认停用/Agent 查就绪），「去处理」直达。附带修复：Tools/Agents/Skills 三页创建请求空 `createdAt:""` 致后端 time.Time 解析 400（submit 剥空值）。
- **模型体验批次（2026-10-18，任务五包 D，spec `docs/superpowers/specs/2026-10-17-console-model-experience-design.md`）**：能力标签≠试玩已实现——①Models 目录操作列按 capabilities 分派（chat→对话试玩/embedding→向量测试/其余→「暂不支持试玩」tooltip 说明，不给「用 chat 代替」的假入口）；②Playground embedding 模式（`isEmbeddingMode` 从模型 capabilities 派生）：自动切向量测试面板（每行一条文本批量 POST `/v1/embeddings`——平台已有端点真玩法），显示条数/实测维度/耗时/前 8 维预览，**不展示对话输入框**（非 chat 模型不盲发 chat 请求），?model= 深链带入即切换+解释条；③切模型默认新会话确认（多轮上下文不跨模型；取消回退旧模型，embedding 模式无上下文自然不触发）；④「参数与请求」popover 加请求证据区：首字延迟（无 delta=「未收到」）/总耗时/字符吞吐/「Token 用量：未返回（网关服务端计量，响应不含 usage 帧）」诚实标注不填 0；⑤Agent 虚拟模型下拉过滤停用项；⑥503 引导文案去旧菜单名（改「后台管理「推理」页」）。集群实测 text-embedding-v4 输出 **1024 维**（C2 维度实测缺口补上）。
- REST `/api/skills|agent-evals|marketplace` 等；console-user「智能体」菜单 + `Explore.vue` 广场。

### 身份 + 前端

- `internal/core/identity/`：Tenant/User/Role/APIKey CRUD（`/api/admin/*` super_admin）+ 租户自助 `/api/api-keys`（capRoles 求交零提权）+ `/api/users`（本租户）+ DeleteTenant 非空 409；写操作全审计。
- console-admin：20 管理页（身份/推理/资源总览 14 三级菜单）+ AI 编排总览 + 流水线模板管理（builtin 只读锁）；admin 跨租户写规范：独立端点 + adminGuard + tenant.WithTenant 派生 + 绕过 prod:write 但全审计 + 代建消耗目标租户配额。
- console-user：应用工作台（10 tab 三组）+ 三层 IA（资源中心=数据服务 / 工作负载 / 平台能力）+ 顶栏环境 scope（生产 gated 15min 自动回退 + 视觉强隔离）+ `useDangerConfirm`（生产输入名称确认，isProd 显式入参防顶栏漂移）+ ESLint（新代码禁 `as any`，错误文案走 `apiError/respError`）。
- **console-user-v2 2026-10-16 重构**：以 `docs/superpowers/specs/2026-10-16-console-next-ux-refactor.md` 为当前 UI 组织方式真源。任务域导航 + 独立产品列表与全局交付中心；产品五分区，AI 成熟能力复用；旧“首页只能产品列表 / 所有低频放更多 / 产品固定三面”约束已由本次用户授权替换。共享视觉与列表契约在 theme.css、PageHeader/StatePanel/TablePager、fetchList 中维护。下条保留原迁移背景，当前导航以新 spec 为准。
- console-user-v2（新 IA，`/next/`）：IA 终点 spec 第一期——产品为主线（首页=产品+待办三列抽屉；产品三面=服务/交付/设置，服务面运行主语 ServiceRunDrawer 实例/日志/伸缩/暴露闭环，交付面智能研发一条龙+最近运行，设置面绑定资源原生）；**2026-10-16 全量化+功能对等两批**：零跳老版链接——侧栏全 v2 路由（模型/数据服务/可观测一级 + 智能体/工作负载/试玩/流量/密钥/环境/组织设置），交付面深度五卡原生（DeliveryChanges 变更发车审批闭环/DeliveryBuilds/DeliveryReleases/DeliveryPipelines/DeliveryRepos），设置面四卡原生（绑定资源/ProductDynamicConfig 动态配置/ProductRunConfig 运行配置/ProductMembers 成员与受限开关），`/runs/:runId` 简版 run 详情（阶段表+日志展开+审批/终止/重试）；**功能补齐**：`/dataservices/:id` 详情（连接信息掩码/启停/伸缩/绑定应用）、可观测三支柱（告警+日志过滤+trace 瀑布 useSpanTree）、`/workloads` 全局负载视图、治理三视图（路由+服务目录实例展开+熔断器）、`/ai` 智能体聚合页（七 tab：agents/skills/prompts/tools/kbs/广场安装/Agent 运行取消）、`/builds/:id` 构建详情（SSE 实时日志+失败重试）；待办深链全落 v2；剩余深页（PR 浏览器/泳道可视化/shared ns 管理）后补；业务对外域名外链（target=_blank）除外；基建从经典版复制独立演进。
- **成员/凭证/用量口径批次（2026-10-19，任务五包 G，spec 专项06）**：①ProductMembers 管理控件按真实权限显隐（tenant:admin/\*，后端 403 兜底）+「当前用户为什么可操作」三类判定说明 + 用户选择器独立失败态（禁选不假装无用户）+ 同名以 userId 为身份（addable 过滤从 name 改 id，修同名误过滤）+ 角色变更失败回读服务端真实旧角色；②ApiKeys 明文全路径清除（@close+onUnmounted）+ 接入说明区（env→curl→排错）+ appId 归因列（真实字段）；③Billing 用量/账单独立错误态（失败≠0/空）+ 配额卡「超限拦截 vs 仅估算」标记（拦截集合=applications/workloads/dataservices）+ 未采集资源「按 0 估算」标注 + Token 未归属差额行（子集之和≠全组织总量）+「支付」正名「标记已结算（模拟）」；④Secrets 删除确认按真实作用域（平台共享才名称确认，弃顶栏 isProd）+ 引用未知提示（无引用关系查询，不按 0 引用报安全）+ 创建弹窗 @close 清明文草稿 + 审计资源 ID 点击复制 + 去 envStore watch（组织级资产不随环境切换）。
- **任务五收官（2026-10-19，A-H 八包全交付）**：包 H 视觉贯通验收（spec 总设计 §3 对表）——AI 四页（Skills/Tools/Prompts/Agents）创建/保存补 submitting 禁重复提交（防重入 + finally 复位 + 按钮态三件套，此前 KBS 有其余四页缺）；深链矩阵 14 静态路由 + 动态深链 + 核心端点全 200。累计：A 正确性、B 工作流快照（0051）、C AI 资产、D 模型体验、E 变更观测（0052）、F 数据服务备份（0053）、G 凭证用量、H 视觉贯通。真机项（亮暗主题/200% 缩放/长名称/焦点环/WCAG 点击高度）留用户抽查。
- 主模块 vs 应用详情原则：主模块=全局视图 + 维度是过滤器不是门槛 + 告警漏斗式下钻。

### 管理后台深化（D5，2026-10-20，spec `docs/superpowers/specs/2026-10-19-admin-ia-and-management-design.md`）

- **租户生命周期（D5-1）**：租户冻结/恢复（冻结拒登录+新会话，存量 API Key 同拒）+ 全资源注销预检（非空 409 列依赖）。
- **细分角色（D5-2）**：平台只读运维/资源运维/财务/安全四角色（`identity.BuiltinPlatformRoles`），服务端 `PlatformPermissionsFor` 收口；菜单 Perm 全表登记（`internal/core/auth/menus.go`，漏登记=对所有角色可见的权限泄漏，`menus_test.go` 锁）。
- **凭证生命周期（D5-3）**：API Key `expiresAt` 到期即拒（Lookup 时校验）+ `Rotate` 轮换（旧 Key 标 revoked + rotatedFrom 链）+ `lastUsedAt` 最近使用 + 会话撤销（jti 黑名单内存实现）；SSO **裁决不做**（无可验证 IdP，半成品认证路径是不可验证攻击面，记遗留债务）。
- **财务与通知（D5-4）**：预算 `budgets`（期基 YYYY-MM，强制=超限拦截推理，60s TTL 缓存护栏）+ 结算 `settlements`（供应商对账单 mock 单价）；菜单 `/resources/budgets|settlements` → `PermPlatformFinanceRead`。通知渠道 `NotificationChannel`（Kind=webhook，URL/Headers 掩码 `scheme://host/***`，`MergeSecretUpdates` 掩码占位保留原值）+ 三段抑制（渠道停用/`muteUntil` 临时静默/重复冷却 `cooldownMinutes`=0 仅状态转变）；`AlertRule.ChannelIDs` 非空忽略直连 Webhook。前端落 console-user（租户级配置，admin 是跨租户只读域）。
- **webhook 投递记录与幂等重放（D5-5，migration 0059）**：每次通过鉴权的投递落 `pipeline_webhook_deliveries`（幂等键=源 delivery ID `X-Gitea-Delivery` 缺失平台生成；状态 accepted/duplicate/skipped/rejected/failed + 中文原因——「push 了为什么没触发」可查不再靠服务端日志猜）；**token 校验失败不落库**（无租户身份，落库=开放无界写入面）。同 ID 重发标 duplicate 保留首次结果（含 runID）；重放 `POST .../deliveries/{id}/replay` 复用记录 branch/commit（trigger=replay），**已有 run 幂等 no-op 返回原 run**，权限 PermPipelineWrite + appGuard + allowProdFlow（比 webhook 严：重放有用户身份）。PG 列名用 `commit_sha`（commit 是 SQL 保留字）。
- **五路审计修复批次（2026-10-21）**：H-1 通道状态投影（`provider.ChannelStatusReader` 可选接口，Gateway failover 运行态覆盖 store 静态值不回写）；M-4 runSource 归桶（paas 编排 run 名正则 `run-<12hex>-<idx>[-rN]` 可证实才归 paas，其余独立 other 桶不冒充归因）+ L-2 未知 phase 独立桶；P6 权限对账测试锁（12 PermPlatform* 全被 platformDomains 引用）+ C1 细分角色正路径测试 + dashboard 权限登记；M-3 configcenter admin refs 查询失败置 RefsError（失败≠0 引用）；前端 H-3 三源独立结算（allSettled + 每源独立 error，失败不冒充 0/空）+ L-1 首页「统计截至」快照标注 + F2 TagsView 保留 query + R4 异步运维「受理≠生效」文案与操作记录轮询并列。留后续：P3 切租户抽屉清理（需全局 drawer 注册机制）、admin 命令面板、M-2 诊断时间戳 PG 加列。
- **复核 F01–F08 收尾（2026-10-21，复核报告 `docs/reviews/2026-10-19-admin-design/completion-check-2026-10-21.md`）**：F01 切租户残留——`useScopeChange` composable 统一 22 资源页（reset 关抽屉/清详情先于 refresh 重拉，13 详情页带 reset）；F02 旧链接——`dynamic.ts` redirect query 合并**目标 tab 优先**（`{...to.query, ...defaultQuery}`，tenantId/hash 保留）；F03 可观测单层双 tab + 如实文案（指标/日志/trace 跨租户记 M20）；F04 工作流运行收敛至「执行记录」页双 tab（AI 资产页留迁移空态）；F05 AiOverview 工作流真懒加载；F06 首页 `AttentionPanel` 三源 allSettled（失败运行/触发告警/异常负载）+ 点击直达；F07/F08 handoff 清单与复核报告双回填 + 同源预览核验（`VITE_BASE=/admin/` 构建 embed 本地 core，不放宽 CSRF）。图标对账测试 `TestMenuIconsRegisteredInAdmin`（menus.go icon ↔ main.ts 注册清单跨语言锁，漏配 CI 即红）。剩余边界（服务端分页/模型探活 API/消费归因维度/平台凭证创建抽屉）已记 handoff「剩余边界」节。
- **幽灵 workload 清理实践（2026-10-21）**：外部删 CR 后 DB 行残留（21 行 ready=0 假象）定夺清理——删应用级联（paas-shop/dsh/test-web 三应用）+ **「部署即注册」的治理 Service 表不随应用级联删除**（CascadeDeleter 边界外，需按应用前缀白名单手工注销 25 条，批量删服务前必须显式过滤防误删存活服务）；孤儿 CR 由 OrphanGC 宽限后自动回收无需手工。
- **paas-shop 重拉起 + setup.sh 平台化闭环（2026-10-21）**：全场景演示项目以平台 API 全量重建（`examples/paas-shop/setup.sh`）并补齐四缺口——①**part 1.5 test workload 段新增**（7 服务）：prod 镜像复用链 `prod_svc_image` 上游是 test workload，旧链路由外部推镜像的 deploy-paas-shop.sh 建、setup.sh 从未管；test CronJob（sweeper `*/2`+statsworker）同属 spec 缺口。②**test 六绑定**（db/cache/mq/vector/search/storage）：绑定注入落**实例自身 env 桶**（`binding_injector.go` `upsert(ds.EnvID)`），只绑 prod 实例则 test workload 缺 DATABASE_URL 起动即退。③**PAAS_AGENT_MODEL 按名解析 shop-agent**（`agent:<id>` 虚拟模型；glm-5.2 等真模型名网关也放行但绕过 agent 编排）。④**`$ds（` 全角括号并入 bash 变量名**坑（`${ds}` 显式括起）。运维侧修复两例：appconfig 变更不触发已建 CR 的 env 重渲染（`appEnvVars` 仅 reconcile 时读）——restart 端点会被后续发布 superseded，可靠手法=PUT replicas 2→1 制造 spec diff；MCP 工具 serverURL 端口错配（`:8080` 指 Service 无监听端口，Service 只开 80）致 agent 工具调用全失败，8 工具 GET+PUT 修复。终态 17 workload running、双域名 200、MQ events/stats/TTL 关单/agent 工具链全通。

## API 契约

route registry（`internal/apiroute/`）驱动 mux 注册与 spec 生成；`GET /openapi.json` 公开 + `GET /docs` Scalar（公开）。前端 `openapi-typescript` 生成 `types.gen.ts`——**现行策略：改到哪个模块才迁哪个模块的手写 interface**（不一次性重写）。

## 标准基线（审计外部基准，2026-08-26 设立）

**为什么有这一章**：守卫式审计查不出「该实现而未实现」的完备性缺口。审计五原则：
1. **完备性对表**：重要模块审计对照下表问「业界标准能力我们缺哪些」。
2. **旅程式审计常态化**：每模块至少一轮「模拟真实用户完整旅程」（模块接缝断链只有旅程能发现）。
3. **留后续复审**：每次大功能落地后扫「留后续」清单，问「这笔欠账现在该还了吗」。
4. **对账式审计常态化**（2026-09-04 立项，首案=paas-shop 删旧应用后 workload 引用孤儿镜像 + registry 实物与 DB 记录因 repo 命名变迁肉眼不可对应）：平台无 DB 外键（多租户 + Repository 分层），软外键漂移单模块审计查不出——必须跨层比对**部署引用 ↔ 平台记录 ↔ 外部实物**。固化工具 `./scripts/reconcile.sh`（A 孤儿引用 / B 幽灵记录 / C 配额漂移 / D 同 tag 不同 digest / E registry 孤儿实物概览 / F 幽灵 workload（DB 有行无 CR，drift 失效）/ G 孤儿 CR（OrphanGC 失效）/ H dataservice 孤儿与幽灵（ds OrphanGC 失效探针，2026-09-25 立）；A1/B1/C1/D1/F1/G1/H1 存在时退出码非零可挂 CI，F/G/H 依赖 kubectl 不可达时跳过）。每轮审计必须跑一次；引用形态归一化（tag / host@digest / 裸 digest）与 bash 踩坑见脚本头注释。
5. **能力曝光对账**（2026-09-05 立项，首案=广场缺 knowledgebase/workflow + backup/messaging 后端有前端零调用）：新能力落地后必须对**全部应有暴露面**逐一对账——**后端 API × 前端入口 × 示例演示（paas-shop）× admin 总览 × 用户文档 × OpenAPI 登记**。守卫式审计只验「实现的代码对不对」，旅程式审计只验「已有入口走得通」，都不问「这个能力接入了所有应有入口吗」——故 10 轮审查仍漏掉此类缺口。落地新实体/端点时逐面自问：用户从哪发现它？示例里有没有演示？admin 看得到吗？

| 模块 | 业界对标 | 当前缺口 |
|------|---------|---------|
| 工作负载部署 | K8s Deployment | HPA/PDB/优先级缺 |
| 生产发布策略 | Argo Rollouts / Flagger | 按比例切流缺（依赖流量权重）；蓝绿可借泳道底座 |
| 泳道/流量隔离 | 阿里全链路灰度 MSE | 入口流量按比例切泳道缺 |
| 可观测 | Grafana/Pyroscope | SLO 缺 |
| 秘钥管理 | Vault/KMS | 轮转/过期缺 |

**工作负载资源最低标准**：生产 Pod 必须 requests+limits（非 BestEffort）；有入流量的服务必须有 readiness probe（已满足：TCP probe）。

## 遗留债务索引（按需复审）

- **安全**：refresh rotation + logout jti 黑名单、限流多副本 Redis、密钥轮转/过期、alert webhook SSRF（低危盲 SSRF：POST-only/无回显/不跟随重定向；真防护需 dialer 级连接时 IP 校验防 DNS rebinding，简单黑名单无效——2026-09-07 评估定案不做）、各 client CheckRedirect 已收口但新 client 需用 httputil.NewClient。
- **D5 留后续（2026-10-20）**：SSO 对接不做（无可验证 IdP，半成品认证路径是不可验证攻击面；待真实 IdP 接入需求再立项）；会话撤销 jti 黑名单为内存实现（多副本需 Redis，与限流同款）；跨规则告警抑制（A 压 B 依赖图）不做；供应商结算为 mock 单价非真实账单；备份 PVC 产物清理（删记录不删产物）。
- **持久化/规模**：observability 迁 PG、RLS 生产角色非超级用户生效（dev postgres chart 超级用户 BYPASSRLS，策略已全表覆盖 migration 0043/0044）、ClickHouse 时序计量、审计分页、流水线运行历史分页、configcenter 长连接 watch/灰度下发。~~配额计数回收失衡~~（**已修复 2026-09-04**：dataservice Delete 补回收 + 启动自愈 `recalcUsageIfDrift` 对齐行数计数型资源，reconcile C1 清零）。
- **发布/部署**：batches 切流实现（D1）、蓝绿模板变体、金丝雀告警联动、promote 跨级跳迁、PVC 数据服务重启保数据（现 STS 删 pod 不删 PVC 可恢复）、异步创建流转 PG 反向同步、真实备份任务。
- **数据服务**：CR 丢失周期自愈重 Apply 缺失——平台 DB 记录 running 但 K8s STS/SVC 全没了时无自动恢复路径（2026-10-19 实测 10 个 ds 中 9 个成幽灵，手工 GET+PUT 触发 reconciler 重 Apply 全恢复；workload 有 drift EnsureIfMissing 同款，dataservice 应补周期对账「DB 有行无 STS→重 Apply」）。
- **MaaS**：通道健康检查真实探活、凭证测试 ping、Vendor 改配置后存量通道/绑定自动同步、按用户 scope 列 API Key。
- **流水线**：cron 触发器、Gitea webhook 自动注册、process/mock 模式实时构建日志、租户自定义模板编辑器、行级评论、CI 状态挂 PR、pipeline PG store 集成测试缺位（runCols 加列漏 Scan 对齐时编译期/单测均查不出，2026-10-17 曾致集群全部 run 读路径 500「19 vs 20」——加列必须三处同步 runCols/scanRunInto/INSERT，或补 test-pg 用例）。
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
