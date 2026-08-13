# 可观测依赖资源增强 + 引擎 exporter + 深链接 设计

> **日期**：2026-08-13
> **背景**：(A) 应用详情→可观测→依赖资源 tab 观测项太少（仅 CPU/内存/磁盘IO/网络IO），无法排障，需加引擎业务指标 + 资源状态/Pod/日志/告警 + 下钻链接；(B) 系统所有页面分享 URL 要能定位到分享前位置（tab + 筛选 + 列表筛选）。触发起因：paas-shop 示例服务缺商品，经可观测平台 logs 定位为 nginx 缓存失效 bff ClusterIP（已修，见 runbook），过程中暴露可观测依赖资源排障能力不足。

## 现状与缺口（探查确认）

### 后端可观测能力边界
- `targetType` 支持 `app/dataservice`（workload/env 模型有但 real 未实现）
- **dataservice real 模式只有 cpu/mem/disk_io/net_io**（cAdvisor 容器级）；`rps/latency` 模型有但 dataservice 分支显式跳过（数据服务不对外 HTTP）
- **无引擎业务指标**（DB连接数/缓存命中率/MQ堆积/向量QPS）—— 需引擎 exporter，完全没有
- **logs 只支持 appId**（Pod 名 `ds-*-0` 不匹配 `wl-.*` 正则）；不支持 targetType=dataservice
- `/api/alerts` 不支持 targetId 过滤（AlertRule 模型绑定 targetType/targetId，但接口无参数）
- **无 PVC 用量指标**（kubelet_volume_stats 未接入；reconciler 只写 PVC spec 不查 status）
- **dataservice 详情无 Pod 列表 / 无 `/api/dataservices/{id}/pods` 端点**

### 前端现状
- `AppObservability.vue` 依赖资源 tab：每资源仅 4 指标卡（cpu/mem/disk_io/net_io），无链接/无 Pod/无日志/无告警
- `DataServiceDetail.vue`（706 行）已较丰富（连接/4指标卡/告警/lifecycle/scale/upgrade/绑定反查），但 metricOrder 写死 `['cpu','mem','rps','latency']`，**与后端 dataservice 实际产出（cpu/mem/disk_io/net_io）错配**——rps/latency 永显「-」，disk_io/net_io 无位
- ApplicationDetail `activeTab` 已与 `?tab=` 双向同步；部分页面（Applications/Workloads/Observability）已读 query
- 列表页搜索/分页为**纯内存筛选**，关键词不进 URL
- envStore（顶栏环境 scope）用内存 + localStorage，**URL 不带 env** → 分享链接丢失环境上下文
- 无统一 `useUrlState` composable

## 范围

两块，均完整实施（用户确认）：
- **A 依赖资源增强**：前端依赖资源 tab 重构为排障单元 + 后端引擎 exporter sidecar 体系 + Pod 端点 + logs/alerts 扩展 + PVC 用量指标
- **B 深链接**：抽 `useUrlState` composable 统一 tab/筛选/列表筛选/环境 scope 进 URL

## Section 1：引擎 exporter sidecar 注入（后端核心）

reconciler 建 STS 时按 engine 注入 exporter sidecar，复用 STS 生命周期 + 主容器 label（多租户隔离天然继承）。

| Kind/Engine | 业务指标 | exporter |
|---|---|---|
| db.postgres | 连接数/事务QPS/慢查询 | prometheuscommunity/postgres_exporter |
| db.mysql | 连接数/QPS | prom/mysqld-exporter |
| cache.redis / valkey | 命中率/内存/键数/连接 | oliver006/redis_exporter |
| mq.nats | 连接/消息速率/订阅 | natsio/prometheus-nats-exporter |
| storage.minio | 请求数/流量 | minio 内置（console 启用时开 `/minio/v2/metrics/v3`，无 sidecar）|
| vector.qdrant | 检索QPS/向量数 | qdrant 内置 `/metrics`（无 sidecar）|
| search.meilisearch | 文档数/QPS | meilisearch 内置 `/metrics`（无 sidecar）|

**注入规则**：
- sidecar 容器名固定 `exporter`，端口固定 `9100`（pg/mysql/redis/nats）
- 主容器已带 `paas.aitoys/tenant` + `paas.aitoys/workload=<ds-id>` label，sidecar 同 Pod 共享 → Prometheus 自动关联
- exporter 镜像内网化（`PAAS_IMAGE_REGISTRY`，`<registry>/library/<name>`）
- 凭证：sidecar 用 env `secretKeyRef` 复用主容器密码 env（不重新生成）
- minio/qdrant/meili 内置 metrics：主容器启动参数加 expose flag（qdrant 默认开；minio 已开 console；meili 暴露端口）

**指标映射**（`real/metrics.go` dataservice 分支扩展，按 kind+engine 选，targetID=`<ds-id>-0` pod）：

| 领域名 | 适用 | PromQL 来源 |
|---|---|---|
| `connections` | db/cache/mq | `pg_stat_activity_count` / `redis_connected_clients` / `mysql_threads_connected` / `nats_connz_connections` |
| `qps` | db/cache/vector/search | `pg_stat_database_xact_commit` / `redis_commands_processed_total` / `qdrant_search_avg_rate` / `meili_search_total` |
| `hit_rate` | cache | `redis_keyspace_hit_rate` |
| `lag` | mq | `nats_jetstream_pending` |
| `vectors` | vector | `qdrant_collection_vectors_count` |
| `disk_usage` | 全部 | `kubelet_volume_stats_used_bytes / kubelet_volume_stats_capacity_bytes`（PVC 用量%，新增，无需 exporter）|

real 查不到（exporter 未就绪/未部署）→ 静默跳过不返该 series（已有 `continue` 机制），前端卡片显示「-」，诚实降级不造伪。

**Prometheus scrape**（`deploy/observability/prometheus-values.yaml`）：K8s SD 按端口名 `exporter` 自动发现 sidecar，relabel 注入 namespace（租户）+ workload 标签。

## Section 2：前端依赖资源 tab 重构（需求 A）

`AppObservability.vue` 依赖资源 tab 每卡片升级为「排障单元」：

```
┌──────────────────────────────────────────────────┐
│ [数据库] pg-main ●running 1/1副本 磁盘42%  →     │  头部：类型/名称/状态/副本就绪/PVC用量/下钻
│ ┌CPU─┬内存─┬连接数┬QPS─┬磁盘IO┬网络IO┐          │
│ │0.2核│110MiB│ 12 │45/s│2KB/s│1KB/s │  sparkline │  按kind动态选指标卡
│ └────┴────┴────┴────┴────┴────┘          │
│ Pod: pg-main-0 ●Running 0重启 节点kb2            │  Pod实例（GET /api/dataservices/{id}/pods）
│ 告警: ⚠ 连接数>80 firing                        │  该资源告警（/api/alerts?targetType=dataservice&targetId=）
│ 日志: [error] too many connections ... 5s前     │  最近日志（/api/observability/logs?targetType=dataservice&targetId=）
│                      [查看完整监控/日志/告警 →]  │  下钻 DataServiceDetail（带 ?app= 上下文）
└──────────────────────────────────────────────────┘
```

**按 kind 动态选指标**（替代写死 4 项）：
```ts
const DEP_METRICS: Record<string, MetricDef[]> = {
  db:      [cpu, mem, connections, qps, disk_io, net_io],
  cache:   [cpu, mem, hit_rate, qps, connections],
  mq:      [cpu, mem, connections, lag, qps],
  storage: [cpu, mem, disk_io, net_io],
  vector:  [cpu, mem, qps, vectors, disk_io],
  search:  [cpu, mem, qps, disk_io],
}
// disk_usage（PVC用量%）单列头部徽标
```

**新增 3 数据加载**（loadDeps 内每资源并行）：pods / 该资源告警 / 最近 3 条日志。

**下钻**：卡片头部「→」+ 底部「查看完整监控/日志/告警 →」跳 `router.push('/resources/${kind}/${id}?app=' + appId)`，DataServiceDetail 已支持 `?app=` ctxApp。

**卡片错配修复**：AppObservability + DataServiceDetail metricOrder 移除 dataservice 无意义的 `rps/latency`，改按 kind 动态选。

## Section 3：后端端点扩展（支撑前端 + exporter）

| 端点 | 改动 | 说明 |
|---|---|---|
| `GET /api/dataservices/{id}/pods` | **新增** | reconciler client 查 STS Pod（label `paas.aitoys/workload=<id>`），复用 workload Instances 模式返 `{name,status,ready,restarts,node,ip,age}`，越权校验（先 Get 数据服务确认租户）。|
| `/api/observability/logs` | **加 targetType** | handler 加 `targetType/targetId`，real/logs.go Pod 正则对 dataservice 用 `ds-<id>-0`，memory 按 targetType 路由。|
| `/api/observability/alerts` | **加 targetId 过滤** | ListAlerts 支持 `?targetType=&targetId=`（compose 已按 series targetId 匹配，前端过滤改后端过滤减传输）。|
| real/metrics.go | **加 PVC + 引擎指标** | dataservice 分支扩 `disk_usage`（kubelet_volume_stats）+ 按 kind/engine 加业务指标 PromQL。|
| dataservice_controller | **sidecar 注入** | STS 按 engine 加 exporter sidecar 容器。|
| prometheus scrape | **加 sidecar 发现** | K8s SD 按端口名 `exporter` 自动 scrape。|

## Section 4：深链接（需求 B，横切）

**统一 composable** `src/composables/useUrlState.ts`：双向同步状态项 ↔ URL query（`router.replace` 避免历史栈膨胀；读 `route.query[key]`；回 watch `route.query[key]` 同步，支持前进/后退）。

**query 约定**（全局统一）：

| 页面 | query 参数 | 说明 |
|---|---|---|
| ApplicationDetail | `tab` | 已有，保留 |
| Applications | `q` | 搜索词，新增 |
| Workloads | `env`/`app`/`type` | env/app 已有补全，type 新增 |
| Observability | `app` | 已有 |
| 数据服务列表 | `q` | 新增 |
| 顶栏环境 scope | `env`（全局）| **新增**——envStore switchEnv 写 `env=<id>` 到 URL，App.vue 初始化从 `route.query.env` 恢复 |

**环境 scope 进 URL 是关键**：当前 envStore URL 不带 env → 分享链接丢失环境上下文。改 switchEnv 写 URL，初始化从 URL 恢复。

**列表筛选统一**（Applications/Workloads/DataServices）：搜索框 `v-model` 接 `useUrlState('q','')`，分页（如有）接 `useUrlState('page',1)`。

**落地优先级**：先 ApplicationDetail（已有 tab）+ 顶栏 env scope + Applications 列表筛选（高频分享），其余页面渐进接入。

## 横切约束

- **诚实降级**：所有新增指标/日志/Pod 在 exporter 未就绪或 real 模式查不到时静默返空，前端显示「-」，不造伪指标（与平台一贯原则一致）。
- **多租户隔离**：sidecar 同 Pod 继承 `paas.aitoys/tenant` label；Pod 端点/logs/alerts 全部按 ctx tenant 过滤；PVC 指标按 namespace（=租户）。
- **镜像内网化**：exporter 镜像统一 `PAAS_IMAGE_REGISTRY`（amd64，经 daocloud 中转），与引擎主镜像同路径。
- **best-effort 不阻断**：sidecar 启动失败不阻断数据服务主容器（reconciler sidecar 创建失败仅 log）。
- **渐进深链接**：不一次性重构所有页面，按分享高频优先级接入。

## 验证（一次改对）

1. **后端**：`make test` 全绿（含 sidecar 注入 fake client 测 + pods 端点 + logs targetType 测）；real 模式 PG 端到端：建 db 数据服务 → exporter sidecar 起 → Prometheus scrape 到 connections/qps → 前端依赖资源卡显示真实业务指标。
2. **前端**：`pnpm build` console-user 通过（useUrlState 类型 + 依赖资源 tab 重构）。
3. **深链接 e2e**：分享 `/console/applications/<id>?tab=observability` → 接收方打开直达可观测 tab；分享带 `?env=env-prod&q=shop` 的应用列表 → 直达生产环境筛选 shop。
4. **排障闭环**：paas-shop 依赖资源 tab 一屏看到 pg/redis 的连接数/QPS/告警/日志，点下钻进 DataServiceDetail 完整排障。
5. **部署**：`./scripts/deploy-k8s.sh`（k8s-always-latest 常设授权）。

## 留后续

- `rps/latency/errorRate` 对 dataservice 无业务意义（数据服务不对外 HTTP），永久不补
- workload/env 维度 real 模式实现（当前只 app/dataservice real）
- engine 业务指标更细粒度（慢查询 TopN、索引碎片、连接池占用）需 exporter 高级配置
- 深链接完整覆盖所有页面分页/排序/过滤
- sidecar exporter 资源限额（requests/limits）调优
- Prometheus scrape 健康检查（exporter 未起时告警）
