# Changelog

本项目所有重要变更记录于此文件。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Added
- DevOps 真实构建流水线（P2-2）：新增 `internal/devops/builder` 包，`Pipeline` 接口 + `Mock`（sha256 派生，与历史一致）+ `Real`（git clone tempdir → docker build → docker push → 解析 RepoDigest 不可变 digest）。`PAAS_DEVOPS_REAL=true` 注入 Real（凭证从 `PAAS_REGISTRY`/`PAAS_GIT_TOKEN`/`PAAS_REGISTRY_USER`/`PAAS_REGISTRY_PASS` 读），空则 Mock（现状不变）。memory/pg Store 的 runBuild 改调 `Pipeline.Build` 拿 digest/tag/log，状态机（pending→running→success/failed）仍由 Store 管，失败优雅降级标 failed。安全：docker login 用 `--password-stdin`（密码不经 argv，防进程列表泄漏）；git clone 到 `os.MkdirTemp` 隔离目录；所有参数经 os/exec（不经 shell）防注入。单元测试覆盖纯函数 + 失败降级；`//go:build integration` e2e 测试（`PAAS_DEVOPS_E2E=1`）跑全链路（本地 registry + 临时 git 仓库）。
- DataService K8s 数据面纳管（P1-3）：新增 DataService CRD（`api/core/v1alpha1/dataservice_types.go`，spec: kind/engine/spec map，status: ready/phase/image）+ `internal/controller` DataServiceReconciler（watch CRD → 按 Kind+Engine 选容器镜像 → CreateOrUpdate StatefulSet + 回写 status.phase）+ `DataServiceK8sApplier` 实现 `dataservice.Applier` + `ApplyRepo` 装饰 PG/memory repo。`startManager` 启 Workload+DataService 双 reconciler，`PAAS_KUBECONFIG` 非空生效。Kind→镜像占位表覆盖 6 类 11 引擎（db→postgres/mysql、cache→redis/valkey、mq→kafka/rabbitmq/rocketmq、storage→minio、vector→milvus/qdrant、search→elasticsearch/opensearch），未知组合记 failed 不拉起。fake client 测试（创建/幂等/未知 engine/镜像覆盖/applier apply-delete）。
- OTel 应用埋点（P2-3）：新增 `internal/observability/tracing` 初始化全局 TracerProvider（OTLP/HTTP exporter，对接 Tempo/Jaeger/Collector）。`PAAS_OTEL_ENDPOINT` env 开关（非空接后端，空=noop 行为不变）。`cmd/core` run() 启动初始化 + 退出 shutdown flush；mux 经 `otelhttp` 中间件包装，每请求自动建 span（http.method/status_code/server.address），探针/契约/文档端点过滤避免噪音。W3C traceparent 透传播（跨服务全链路）。引入 go.opentelemetry.io/otel + sdk + otlptracehttp + contrib/otelhttp（均 Apache 2.0）。控制面自身链路可被 `/api/observability/traces`（接 Tempo）观测，端到端可观测闭环。
- envtest 集成测试骨架（P3-3）：`internal/controller/envtest_test.go`（`//go:build integration` 门控）拉起本地 etcd+apiserver 验证 WorkloadReconciler 真实创建 Deployment。Makefile 加 `test-envtest` 目标（setup-envtest 装 KUBEBUILDER_ASSETS 后运行）。无 binary 时 skip，CI 配 binary 后可跑。编译验证通过，默认 make test 不受影响。
- RLS 行级安全（P3-1 机制层）：migration 0013 对核心业务表（applications/workloads/users/dataservices/environments）ENABLE ROW LEVEL SECURITY + 渐进式 POLICY（`tenant_id = current_setting('app.tenant_id', true)`，未设 session 放行不破坏现有查询层过滤，已设则数据库强制隔离作纵深防御）。安全网模式，查询层仍主过滤。完整 force 注入（连接级 SET app.tenant_id）归后续。
- 数据面治理 SDK（P2-1 铺路）：新增 `pkg/sidecar` 轻量客户端（纯 net/http 零依赖），第三方业务进程用 Register/Heartbeat/Deregister/KeepAlive 接入 governance（服务注册 + 心跳保活）。对齐 governance.Instance 契约，Bearer 鉴权。httptest 覆盖注册/心跳/注销/错误/KeepAlive 取消退出。
- 计量采集（P3-2）：`gateway.Meter` 加 `OnTokens` 用量回写钩子，流式推理结束后 token 计量回写 `billing.IncUsage(ResTokens)`（按租户累计）。main.go serveHTTP 接线 meter→billing。推理用量实时落 billing usage（/api/billing/usage 可见 tokens 增长）。
- dataservice K8s 纳管接口铺路：新增 `dataservice.Applier` 接口 + `ApplyRepo` 装饰器（与 `workload.Applier` 同构，Create/Update/Delete 后投影数据面，nil-transparent）。为 P1-3 真实引擎纳管（DataService CRD + Reconciler + K8sApplier）铺路，复用 workload（spec 4）已验证模式。
- 数据服务备份管理：新增 `internal/backup` 领域（Backup 归属 dataservice 资源，full/incremental 类型，Create 即 completed mock）+ 内存实现（seed）+ handler（`/api/backups` List/Create/Delete，确定性 SizeMB）+ 复用 dataservice:read/write 权限。PG 路径 memory fallback。OpenAPI 登记 3 端点。
- MQ topic/消费组管理：新增 `internal/messaging` 领域（Topic 归属 MQ 数据服务资源 + ConsumerGroup）+ 内存实现（seed）+ handler（`/api/mq-topics`、`/api/consumer-groups` 扁平路由 CRUD）+ 删 Topic 级联清消费组。方法级权限复用 `dataservice:read/write`（MQ 属数据服务）。PG 路径暂用 memory fallback（后续迁）。OpenAPI 登记 6 端点。前端 topic 管理 UI 接入留下一切片。
- console-admin system 页对接 core：`system/role/api.ts` 对接 core `/api/roles`（内置角色只读，中文名映射 + 假分页 + CSV 导出）；`system/user/api.ts` 对接 core `/api/users`（CRUD + 字段映射 + 假分页 + 批量删除）；core `/api/system/menus` 下发 system/user + system/role 入口（登录后侧栏可见）。一致性检查脚本加 core 后端白名单（对接 core 的端点不走 MSW mock）。
- 平台总览聚合端点：`internal/dashboard` 包提供 `GET /api/dashboard/stats|charts|activities`（console-admin 首页消费，URL 已对齐 admin 期望，admin 零改动）。统计源自 identity（用户数/API Key 数/租户数），趋势近 7 天确定性派生，分布按租户用户数。解决 admin 登录后首页 dashboard 404，显示真实平台数据。挂 BearerAuth + tenant:admin。
- identity 管理 CRUD API：Repository 扩展平台级管理方法（ListTenants/DeleteTenant/ListUsers/UpdateUser/DeleteUser/ListAPIKeys/DeleteAPIKey，跨租户）+ `internal/core/identity/handler.go`（tenants/users/api-keys/roles 的 CRUD，11 端点）+ `UpdateUser` 支持密码更新（PasswordHash 非空则更新）。identity 模型加 json tag（小驼峰，`PasswordHash` 用 `json:"-"` 永不序列化）。API Key 创建返明文一次、列表掩码（`sk-xxx****`）。挂 BearerAuth + `Require("tenant:admin")`（super_admin 通行，developer/viewer 403）。PG 删除走 FK CASCADE。OpenAPI 登记 11 端点。
- console-admin 身份对接 core（密码登录 + JWT）：core 新增 `internal/core/auth` 包（JWT HMAC-SHA256 自实现零依赖 + bcrypt 密码哈希，引入 `golang.org/x/crypto/bcrypt` BSD-3 兼容）+ 5 端点（`POST /api/auth/sessions` 登录、`POST /api/auth/tokens/refresh` 刷新、`DELETE /api/auth/sessions` 登出、`GET /api/auth/users/me` 当前用户、`GET /api/system/menus` 菜单下发）对齐 admin JwtAuthProvider 期望（admin URL 零改动）。identity 模型扩展（User 加 Email/PasswordHash/Status）+ Repository 加 `GetUserByName`/`GetUser` + migration 0012。`gateway.BearerAuth` 双通道中间件（JWT/APIKey 按 token 形态分发，注入同一 ctx，下游零改动——程序化 API Key 调用与浏览器 JWT 共存）。seed admin/123456（t-acme 管理员）。`PAAS_JWT_SECRET` env（空则随机生成+警告）。OpenAPI 登记 5 端点。
- console-admin 前端对接 core：MSW 开关修复（`VITE_ENABLE_MOCK=true` 显式开启，默认走真实后端）+ env 统一（VITE_USE_MOCK→VITE_ENABLE_MOCK）+ vite proxy（`/api`、`/v1`→core:8080）+ 响应拦截器兼容 core `{data:T}`/`{error:msg}` 格式（core 契约不动，适配只在 admin 侧）+ token storage key 品牌 `paas:*` + 登录页 dev 预填 admin/123456。登录闭环端到端验证（登录→JWT→profile→菜单下发→进入首页 layout）。

- CI 质量门加固：新增 `test-pg` job（GitHub Actions postgres service 跑全 11 模块 PG 集成测试，`-p 1` 串行避免 resetSchema 共享 db 互清）、`coverage` job（`coverprofile` + artifact 上传 + 日志打印总覆盖率）、`release-image` job（推 tag `v*` 触发，buildx 多平台 `linux/amd64,linux/arm64` 推 `ghcr.io/aitoys/paas-core`，自动 semver 三 tag）；`build` job 现依赖 `test-pg` 通过，`release-image` 依赖全部检查 job（任一失败不发布）。
- OpenAPI `/docs` 交互文档 + 契约请求体全覆盖：新增 `GET /docs`（Scalar 嵌入式 HTML，公开无鉴权，拉 `/openapi.json` 渲染，try-it-out）；补登记 devops/configcenter 漏登记的 4 个 POST Operation（repositories/buildruns/releases 创建、configcenter items upsert）；8 个写操作补 `WithReqBody`（workload 扩缩容 / governance instances·routes·breakers / configcenter namespace / billing quota / dataservices / chat/completions）。无请求体的写操作（rollback/pay/heartbeat/publish/generate）按 REST 语义不强加 requestBody。schema 数 33→36。
- 可观测接真实后端（observability real store）：新增 `internal/observability/real`（MetricsStore/LogsStore/TracesStore 纯 net/http 适配 Prometheus/Loki/Tempo HTTP API）+ `internal/observability/compose`（聚合 Repository：alert rules 始终 memory，metrics/logs/traces 按 `PAAS_PROM_URL`/`PAAS_LOKI_URL`/`PAAS_TEMPO_URL` 非空接真实后端、空则惰性 mock，三支柱独立可混用；`ListAlerts` 基于 metrics reader 即时评估）+ 细粒度 reader 接口（`MetricsReader`/`LogsReader`/`TracesReader`/`RuleStore`）。后端不可达降级返空 + 日志（不 5xx/panic）。未配 URL 行为与现状完全一致。
- 真实 K8s 数据面纳管：新增 Workload CRD（`api/core/v1alpha1`，期望状态 spec/status）+ `internal/controller` WorkloadReconciler（watch CRD → CreateOrUpdate Deployment/Job/CronJob + GPU `podAntiAffinity` 反亲和 + 回写 status.ready）+ `workload.Applier` 装饰器（`ApplyRepo` 包装 Repository，Create/Update/UpdateImage/Delete 时投影 Workload CRD 期望状态；devops Release 编排透明继承）+ `cmd/core` manager（`PAAS_KUBECONFIG` 开关启 controller-runtime，空则保持 PG/memory 现状）。控制面/数据面解耦（Deployment 归 K8s 管，manager 挂了不删）。引入 controller-runtime v0.24.1 + k8s.io v0.36.0（均 Apache 2.0）。fake client 测试覆盖创建/幂等/GPU 反亲和/CronJob + ApplyRepo 装饰器。
- airsync 离线交付工具：新增 `cmd/airsync` CLI（bundle/install/verify/doctor，stdlib flag 零依赖）+ `internal/airsync`（manifest.json 含每文件 sha256 完整性校验 + bundle 打包 tar.gz + install docker load/retag/push + helm install + tar slip 防护）+ `deploy/charts/paas` Helm chart（core Deployment + postgres StatefulSet + service + ingress，values 参数化 image.registry/db.url/ingress）。公网/私有两路径共用同一 chart（仅 image.registry 不同）。控制面可打包为离线交付件（私有化双模交付）。`airsync verify` 端到端验证（合法通过 + 篡改检出）；`helm lint` 通过；`airsync doctor` 检查 docker/helm/kubectl。airsync 自研 Apache 2.0；调 docker/helm/kubectl CLI（不引 client 库）。bundle/install 端到端集成需 registry/K8s 集群。
- OpenAPI 契约（单一真源）：`internal/apiroute` Registry 同时驱动 Go 1.22 method-scoped mux 注册与 OpenAPI 3.0 spec 生成（`Register` mux+spec，`Operation` 仅 spec 供 composite 子操作）；手写 Go→JSON Schema reflector（零外部依赖），命名类型 `$ref` 去重，`{data:T}`/`{error:string}` 响应包裹建模，perm→Bearer scope 映射。`GET /openapi.json` 暴露完整契约（55 路径 / 75 操作 / 33 schema）。前端 `openapi-typescript`（devDep）+ `pnpm gen:api` 生成 `types.gen.ts`，`fetchJSON<T>` 泛型 helper 消费生成类型。
- 持久化（增量）：`identity` + `application` 迁移至 PostgreSQL（`pgx` 连接池 + `golang-migrate` embed SQL 启动自动迁移）；其余 9 模块仍内存，按 `PAAS_DB_URL` 切换后端（为空则纯内存、与现状一致）。显式 `WHERE tenant_id` 多租户隔离；PG 表空才 seed（幂等）。docker-compose 内置 `postgres:16-alpine` + 持久卷，重启不丢数据。集成测试 `//go:build integration` 门控，`make test-pg` 运行，默认 `make test` 零依赖保持绿。
- 持久化（剩余 9 模块）：environment / appconfig / dataservice / workload / devops / governance / configcenter / billing / security 全部迁 PostgreSQL（observability 保持内存，接真实后端时再迁）。多值字段全 JSONB（Spec/Meta/Methods/Snapshot/Limits/Counts/Items）；`buildAllStores` 收口全 11 模块按 `PAAS_DB_URL` 切「全内存/全 PG」两路径，Repository 接口对 handler 透明；devops Release 编排经 workload.Repository 接口对存储后端透明。billing `CheckAndInc` 用 `FOR UPDATE` 行锁保证配额检查-递增原子（`-race` 并发验证）；configcenter `Publish` 事务内 version 单调；security 平台级 Secret 用两个 partial unique index + tenant_id NULL，Resolve 仅平台级返明文；GenerateBill 只覆盖 unpaid（已支付账单不可变）。`storage/pg/helpers.go` 抽出共享辅助消除 11 处重复。`make test-pg` 加 `-p 1` 串行（各包 resetSchema 共享同一 database）。
- 配额强制拦截（横切）：`billing.CheckAndInc` 原子「检查超限 + 递增」原语，应用 / 工作负载 Create 前拦截，超限回 429 不创建，Create 失败自动回滚用量；前端写操作遇 429 统一引导去「配额与账单」调整上限。
- 容器化部署：多阶段 `Dockerfile`（distroless 静态镜像，非 root）+ `docker-compose.yml` 一键启动 + `.dockerignore`。
- Playground 多轮对话：下发完整对话历史维持上下文；新增推理参数调节（temperature / max_tokens）与清空对话。
- 安全页面支持平台级凭证（供应商 API Key）管理：作用域选择（租户私有 / 平台共享），平台级仅管理员可写。
- MaaS 对接第三方供应商：`OpenAICompatibleProvider` 一个适配器覆盖 OpenAI / DeepSeek / 通义千问（OpenAI 兼容协议，纯 net/http + SSE 解析，不自建 vLLM）。
- 平台级凭证：`security.Secret` 加 `Scope`（platform 全租户共享 / tenant 租户私有），平台级仅 tenant-admin 可写；经 `Resolve` 取明文（仅内存）注入第三方通道。
- Gateway 请求级 failover：主通道返回限流/不可用类错误自动切备通道，全部失败才 503。
- 5 个错误 sentinel（`ErrCredentialMissing` / `ErrCredentialInvalid` / `ErrUpstreamRateLimit` / `ErrUpstreamUnavailable` / `ErrUpstreamConfig`）驱动降级分类。
- `CredentialResolver` 契约 + `CoreDeps.SecretResolver()` 注入点（依赖倒置，破除 maas→security import）。
- 平台能力横切：治理四件套全部落地（注册中心 / 配置中心 / API 网关路由 / 熔断器）。
- 可观测三支柱：Metrics（惰性时序）+ Logs（聚合）+ Traces（链路）+ Alerts（即时评估）。
- 安全：租户级密钥/证书（掩码）+ 审计日志自动记录。
- 配额计费：租户级配额 + 用量 + 账单生成/支付闭环。
- 数据服务资源中心：DB/缓存/MQ/存储/向量/搜索，通用领域 + Kind 区分（DRY）。
- 生产安全横切：环境类型感知 RBAC（prod:write）+ gated 15 分钟超时 + 视觉强隔离 + 统一危险操作确认。

### Security
- 修复 governance 实例心跳（PUT /api/instances/{iid}/heartbeat）缺失 prod:write 校验：生产环境实例心跳现按实例→服务→env 链路校验生产写权限（fail-closed）。
- 修复 environment DELETE fail-open 漏洞：EnvType 查不到（不存在/跨租户）时不再放行，统一 fail-closed 要求 prod:write。
- 新增 backup 生产写校验：生产数据服务的备份创建/删除现经 resourceID→envID→EnvType 链路要求 prod:write（developer 生产只读），未知资源 fail-closed。
- 修复 ChatCompletions 请求体 DoS 风险：限制请求体 1MiB（MaxBytesReader）+ 校验 model/messages 必填。
- 修复 identity 跨租户越权：普通 tenant-admin 不再能枚举/创建/删除跨租户用户与 API Key、不可授予 super_admin（平台超管仍可跨租户）。
- 修复 JWT refresh 绕过：被禁用用户（status≠active）的 refresh token 现直接拒绝（403）。
- 修复 controller 不可变字段错误：Deployment.Spec.Selector 与 Job.Spec.Template 仅在创建时赋值，避免 reconcile 死循环。
- 修复跨应用/跨 namespace 越权删除（appconfig DELETE、configcenter DELETE item）。
- 修复 5 模块 allowProd fail-open 漏洞（环境查不到时统一 fail-closed）。
- 修复 observability/billing/application 并发读写共享底层数组/map 导致的 race。
- 修复 gateway 流式 nil panic、ctx 不感知导致断连后仍计费。

### Fixed
- CRD 真实集群 list/watch 失败（`v1.ListOptions is not suitable for converting to "core.aitoys.github.com/v1alpha1"`）：根因是 `groupversion_info.go` 用裸 `runtime.NewSchemeBuilder`，未把 metav1 参数类型（ListOptions 等）注册到 GroupVersion，client list/watch 的 parameter codec 无法识别本 GV。改用 controller-runtime 的 `scheme.Builder`（AddToScheme 时自动注册 metav1 参数类型）。fake client 不走 parameter codec，故单元测试无法暴露——经真实 K8s 集群端到端验证发现并修复。
- CRD group/version 为空导致无法 apply：根因是 `make manifests` 用 `paths=./api/...` 通配符使 controller-gen 无法从包路径推断 group/version。改为具体包路径 `paths=./api/core/v1alpha1` + package 级 `+groupName=core.aitoys.github.com` 注解，CRD 现正确生成 `core.aitoys.github.com` group（`core.aitoys.github.com_workloads.yaml` / `_dataservices.yaml`）。
- controller-runtime manager metrics server 默认占 :8080 与 core HTTP 服务冲突致 manager 退出。改为 `PAAS_METRICS_ADDR`（默认 :8081，设 0 禁用）；并注入 zap logger（否则 reconcile 错误被吞无法排查）。

[Unreleased]: https://github.com/aitoys/paas/commits/main
