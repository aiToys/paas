# paas-shop 阶段 1：后端业务功能 + MQ 链路 + Job + appconfig

## Context

用户目标（4 阶段总体）：把 paas-shop 示例从「能跑的 4 个微服务」升级为**全链路可演示的电商 demo**，每个 PaaS 子系统都有真实业务落地，所有改动走平台 CI/CD 上线。

本 spec 是**阶段 1**，承接探索确认的现状缺口：

- **MQ 链路全断**：平台已建 NATS `shop-mq` 数据服务 + topic `shop-events` + consumer group `recommend-consumer`，但 product/recommend 的 Go 代码**零 NATS 客户端对接**（go.mod 无 `nats.go`）。MQ 在平台层是「裸实例 + 元数据 CRUD」，业务侧悬空。
- **搜索缺失**：product 只有全量 `SELECT ... ORDER BY id`，无关键字/category/分页搜索端点。
- **无业务消息/通知**：无订单、无消息中心、无异步通知业务。
- **shop 自身不用 Job/appconfig**：仅 traffic-gen 用，shop 4 个后端配置靠绑定注入，无 appconfig 管理、无批处理 Job。

阶段 1 范围（本 spec）：补齐后端业务功能 + 打通 MQ 真实链路 + 引入 Job/CronJob + 抽 appconfig。Agent/MCP（阶段 2）、前端（阶段 3）、CI/CD 验证（阶段 4）不在本 spec。

## 不做（YAGNI / 留后续阶段）

- Agent runtime 迁移、MCP 服务（阶段 2）
- 电商前端重写（阶段 3）—— 本阶段用 curl 验证后端接口
- CI/CD 端到端验证脚本（阶段 4）—— 本阶段用 setup 脚本构建部署
- 全文检索（pg tsvector/trigram）—— 商品量小（seed 5 条 + 增量），ILIKE 足够，tsvector 留后续
- 订单领域完整建模（订单状态机/库存扣减事务/支付）—— 电商订单是 demo 演示，非生产，用最小可演示模型（见下「领域取舍」）
- MQ 消费失败重试/死信队列 —— NATS core 无原生 DLQ，demo 容忍丢消息，留后续

## 领域取舍（最小可演示）

不做完整电商订单系统，而是用「**商品变更 → 事件 → 异步处理**」这条最短链路演示 MQ + Job + appconfig：

```
product 商品变更（POST /products 或 seed）
  → NATS 发布 product.changed 事件到 shop-events
  → 两类消费：
      (A) recommend（长驻 service）：订阅 recommend-consumer group，收到事件失效对应缓存（演示 MQ→Cache 联动）
      (B) stats-worker（CronJob）：定时从 product DB 聚合统计（分类商品数/库存），写回 appconfig 供推荐策略读取
```

这条链路把 **DB（product）+ Cache（recommend）+ MQ（NATS shop-events）+ CronJob（stats-worker）+ appconfig（业务配置 + 统计回写）** 全部真实串联，且每段都能独立验证。订单/消息中心前端留阶段 3。

## 方案

### A. product：加搜索 + 加 NATS producer

**`examples/paas-shop/product/main.go`**

1. **schema 扩展**（`migrateAndSeed`）：
   - `products` 表加 `created_at TIMESTAMPTZ DEFAULT now()` + `CREATE INDEX IF NOT EXISTS idx_products_category ON products(category)`。
   - seed 5 条补 `created_at`（增量 seed 幂等：count>0 跳过，已存在不动）。

2. **搜索端点**（`productsHandler` GET 分支扩展）：
   - 解析 query：`q`（关键字，name ILIKE）、`category`（精确）、`limit`（分页，默认 20，上限 100）。
   - 动态拼 SQL：`SELECT ... WHERE 1=1` + 条件追加（参数化 `$1/$2`，防注入，与现有 pgx 风格一致）。
   - `ORDER BY created_at DESC`（新商品优先）。
   - **page size 读 appconfig**：`limit` 默认值从 env `PRODUCT_PAGE_SIZE`（appconfig 注入）取，缺省 20。

3. **NATS producer**：
   - 新增 `examples/paas-shop/internal/natspub/` 共享包：连 `NATS_URL`（env，已由 shop-mq 绑定注入），`Publish(subject, payload)` + 连接重试 + 优雅关闭。
   - product 启动时建连接（NATS_URL 缺失则降级：log warning 不阻断，向后兼容未绑 MQ 场景）。
   - POST /products 成功后发布 `product.changed` 事件（JSON `{type:"created", productId, name, category, at}`）。
   - seed 阶段批量 seed 后发一条 `product.bulk-seed` 聚合事件（避免每条一条）。
   - **subject 规划**：统一 topic `shop-events`，事件内 `type` 字段区分（product.changed / product.bulk-seed），消费方按 type 过滤（NATS core 单 topic + payload type 字段，比多 subject 简单，与 platform messaging model 的 topic 概念对齐）。

### B. recommend：加 NATS consumer（长驻订阅失效缓存）

**`examples/paas-shop/recommend/main.go`**

1. **NATS consumer**（新增）：
   - 连 `NATS_URL`，`nats.QueueSubscribe("shop-events", "recommend-consumer", handler)`（用 platform 建的 consumer group 名，clustering 模式多副本分担）。
   - handler 收到 `product.changed` / `product.bulk-seed` → `DEL recommend:*`（按 user 失效所有推荐缓存；按 category 精细失效留后续，demo 用全失效最简）。
   - NATS_URL 缺失降级（log，不阻断），保持未绑 MQ 时 recommend 仍可独立跑（向后兼容）。
2. **GET /recommend 不变**（缓存 miss 仍调 product），只是缓存现在会被 product 变更主动失效，演示「事件驱动缓存一致性」。

### C. stats-worker：新 CronJob 服务（定时聚合统计 + 回写 appconfig）

**新建 `examples/paas-shop/statsworker/main.go`**

1. **职责**：CronJob 触发，单次运行（`/svc` 入口，跑完退出），连 product DB 聚合统计。
2. **统计内容**：`SELECT category, count(*), sum(stock) FROM products GROUP BY category` → 得分类商品数/库存。
3. **回写 appconfig**：调平台 API `POST /api/applications/paas-shop/configs`（带 `PAAS_API_KEY` + appconfig 已知 key 前缀 `STATS_`），写 `STATS_CATEGORY_SUMMARY`（JSON 字符串）+ `STATS_TOTAL_PRODUCTS`（数量）。
4. **CronJob workload**：`type:"cronjob"`，schedule `*/10 * * * *`（10 分钟一次，演示足够），command `/svc`。
5. **演示价值**：CronJob 定时任务能力 + appconfig 双向（不只读注入，还能业务回写）+ Job 与 service 协作。

### D. appconfig：抽业务配置 + worker 回写

**抽业务配置进 appconfig**（替代硬编码/裸 env）：

| key | 服务 | 默认值 | type | 说明 |
|---|---|---|---|---|
| `PRODUCT_PAGE_SIZE` | product | 20 | env | 搜索分页大小 |
| `RECOMMEND_COUNT` | recommend | 3 | env | 推荐返回数量 |
| `RECOMMEND_CACHE_TTL` | recommend | 300 | env | 缓存 TTL（秒） |
| `STATS_CATEGORY_SUMMARY` | statsworker 回写 | "" | env | 分类统计 JSON（worker 写） |
| `STATS_TOTAL_PRODUCTS` | statsworker 回写 | "" | env | 总商品数（worker 写） |

- 这些 key 经 appconfig 管理（而非裸 Workload env），变更后 workload 重建注入（演示「应用配置」能力）。
- setup 脚本建应用时 POST 这些 appconfig（参照 traffic-gen appconfig 模板 §10.1）。
- worker 回写用同一 API（带 write 权限 Key）。

### E. 构建扩展 + 部署脚本

1. **go.mod**：`examples/` 下 `go get github.com/nats-io/nats.go && go mod tidy && go mod vendor`。
2. **新增服务**：`examples/paas-shop/statsworker/` + `examples/paas-shop/internal/natspub/`，均 import `github.com/aitoys/paas-examples/paas-shop/internal/observ`（trace 复用）。
3. **Dockerfile.backend 不变**（L13 `./paas-shop/${SERVICE}` 已通配）。
4. **构建驱动**：setup 脚本多服务构建循环从 `for SVC in product recommend chatbot bff` 扩展到 `... bff worker statsworker`（worker = statsworker 短名，避免改名）。
5. **workload 创建脚本**：仓库缺核心 4 服务 + 新服务的 workload 创建脚本，本阶段补 `examples/scripts/deploy-paas-shop.sh`（参照 create-workloads.sh 模板）：
   - 5 个 service workload（bff/product/recommend/chatbot/worker 若常驻）或 4 service + 1 cronjob（statsworker）。
   - 绑定 shop-db（DATABASE_URL）/shop-cache（REDIS_URL）/shop-mq（NATS_URL）到相关服务。
   - **statsworker 是 CronJob**（type:cronjob），需 product DB 访问（绑 shop-db）+ appconfig 写权限（注入 PAAS_API_KEY via appconfig secret）。
6. **shop-mq 绑定补全**：现 setup 脚本建了 shop-mq 数据服务但**没建应用绑定**（探索确认），本阶段补 `POST /api/applications/paas-shop/bindings {type:"mq", name:"shop-mq"}` → 触发 binding_injector 注入 NATS_URL 到 product/recommend。

### F. 数据流总览（阶段 1 闭环）

```
[POST /products] or [seed]
   └─ product
       ├─ INSERT DB (PostgreSQL via shop-db)
       └─ natspub → NATS shop-events (product.changed)
                     ├─ recommend (QueueSubscribe recommend-consumer)
                     │    └─ DEL recommend:* (缓存失效，事件驱动一致性)
                     └─ (statsworker 不订阅，定时全量聚合)

[*/10 cron] statsworker (CronJob)
   └─ SELECT category,count(*),sum(stock) → POST appconfig STATS_*

[GET /recommend?userId=x]
   └─ recommend → cache HIT? : cache MISS → GET /products → pick → SET cache
        (缓存现会被 product 变更主动失效)

[GET /products?q=键&category=外设&limit=20]
   └─ product → ILIKE + category 搜索（limit 读 PRODUCT_PAGE_SIZE appconfig）
```

## 横切约束

- **examples 隔离**：只引用标准库 + nats.go + pgx/redis/otel（已有），**不引用任何 paas 内部包**（agent runtime 留阶段 2，MCP 工具经 HTTP）。worker 回写 appconfig 走平台 REST API（HTTP + API Key），非内部包。
- **向后兼容降级**：NATS_URL 缺失时 product/recommend 都要能独立跑（log warning 不 panic），保证未绑 MQ 的最小部署（如单测、无 MQ 集群）不崩。
- **多租户**：product DB 表加 tenant 维度？—— **不加**。examples 是单租户 demo（绑定到 acme 租户），平台多租户隔离在数据面层（namespace paas-<tenant>），业务代码不感知租户。与现有 product/recommend 一致。
- **Job PodTemplate 不可变**：statsworker CronJob 改 env 需删旧建新（reconciler applyCronJob 创建时注入一次）。设计文档注明，部署脚本支持重建。
- **appconfig 明文注入**：worker 回写 appconfig 用 secret Key 经 appconfig secret 类型注入（不裸 env），与 security 模块一致。

## 关键文件清单

| 文件 | 动作 | 内容 |
|---|---|---|
| `examples/go.mod` + `vendor` | 改 | 加 `github.com/nats-io/nats.go` 依赖 |
| `examples/paas-shop/internal/natspub/natspub.go` | 新建 | NATS 连接 + Publish 共享包（降级容错） |
| `examples/paas-shop/product/main.go` | 改 | schema 加 created_at+索引、搜索端点、NATS producer |
| `examples/paas-shop/recommend/main.go` | 改 | NATS QueueSubscribe 失效缓存、RECOMMEND_* 读 appconfig |
| `examples/paas-shop/statsworker/main.go` | 新建 | CronJob 统计聚合 + 回写 appconfig |
| `examples/scripts/deploy-paas-shop.sh` | 新建 | 5 workload 创建 + 绑定 shop-db/cache/mq + appconfig |
| `examples/scripts/setup-paas-shop.sh` | 改 | 补 shop-mq 应用绑定、构建循环加 statsworker、appconfig key |

## 验证（一次改对）

1. **单元/构建**：`cd examples && go build ./... && go vet ./...`（natspub 降级路径 + product 搜索 SQL 参数化 + worker 聚合逻辑）。
2. **vendor 一致**：`go mod tidy && go mod vendor` 无 diff。
3. **部署**（[[paas-shop-change-to-deploy-runbook]]）：代码同步集群 Gitea → setup/deploy 脚本触发 BuildRun（多服务 buildArgs）→ Release 部署 → 验证。
4. **e2e**（dev 集群，paas-shop 应用）：
   - `GET /api/products?q=键` → 返回匹配商品（搜索 ✓）
   - `POST /api/products` 后立即 `GET /api/recommend` → recommend 缓存已失效重新算（MQ→Cache 联动 ✓，观察 recommend 日志收到 product.changed）
   - 等 10 分钟（或手动触发 CronJob）→ `GET /api/applications/paas-shop/configs` 看到 `STATS_*` 被回写（CronJob + appconfig 回写 ✓）
   - trace（Jaeger）能看到 product→NATS→recommend 的事件链（observ span 覆盖）
5. **降级**：临时解绑 shop-mq → product/recommend 仍正常跑（log warning，不崩）。

## 留后续（阶段 2-4）

- Agent runtime 迁移 + MCP 服务（阶段 2）
- 电商前端（列表/详情/搜索/聊天/消息中心）（阶段 3）
- CI/CD 端到端流水线验证脚本（阶段 4）
- 订单完整领域 + 消息中心业务 + pg tsvector 全文检索 + NATS 精细失效（按 category）+ MQ DLQ
