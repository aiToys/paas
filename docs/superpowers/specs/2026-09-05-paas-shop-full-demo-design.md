# paas-shop 全能力演示设计：订单最小闭环 + prod 上线

- 日期：2026-09-05
- 状态：已评审（用户确认方案 A + 三组分节设计 + 自审 4 处修正）
- 关联：`docs/superpowers/specs/` 既有 paas-shop 各阶段设计；CLAUDE.md 审计原则五（能力曝光对账）

## 1. 背景与目标

用户需求原话：「示例服务增加完整的电商流程和后台管理，保障系统完整，你看下增加什么能完整演示paas平台的所有功能」。

经收益评估（原则：paas-shop 存在目的是演示平台能力，不是做电商），**真缺口 4 项**：promote test→prod（prod 零部署）、MQ 事件流多消费组 × 平台 messaging 管理面对齐、CronJob 业务语义、workflow approve 节点。顺带缺口：MCP 内存假订单、Secret 静态加密零演示、用户文档零示例章节。

**明确不做**（评估后砍掉，理由=不演示任何平台能力）：
- 完整电商域：支付网关、物流、评价、会员、优惠券
- 独立后台前端项目（独立 build/登录体系）——运营视图并入现有 frontend
- CircuitBreaker 补演示（平台自身「真实流量采集留后续」半成品）
- 告警联动（告警引擎架构级留后续，同上）
- 一次性 Job 类型（与 CronJob 共享 reconciler 路径，边际收益低，记遗留）

## 2. 总体架构

```
   shop.example.com(prod)   shop.test.example.com(test)   <lane>.shop.test.example.com(泳道)
          │                        │                              │ 同一基线 bff，前端按域名染色
          ▼                        ▼                              ▼ x-paas-lane: <lane>
   ═══════════ 生产环境（env-acme-prod-bj，独立 pg/redis/nats）══════════
   bff-prod ── product-prod / recommend-prod / order-prod / chatbot-prod / frontend-prod
   sweeper-prod(CronJob) / statsworker-prod(CronJob)
   ═══════════ 测试环境（env-acme-test，shop-db/redis/nats）══════════
   bff ── product / recommend / order / chatbot / frontend
            └─ 调用经 dpdisc lane 查询，泳道命中走泳道，未命中降级基线
   泳道 feat-order-v2：仅 order 服务变更副本
   sweeper(CronJob */2) / statsworker(CronJob)
```

- **新增服务**：`order`(:8084)、`sweeper`(CronJob)
- **修改**：bff（订单代理 + 双消费组 + 泳道发现 + stats 端点）、mcp（真实订单）、frontend（AdminPanel + 下单 + 环境徽标）、statsworker（保持既有）
- **平台侧零代码改动**（2 个潜在小修记遗留，见 §13）

## 3. order 服务（`examples/paas-shop/order/`，约 400 行）

- **存储**：与商品同库（test=shop-db / prod=shop-db-prod，pgx）。表：

```sql
CREATE TABLE IF NOT EXISTS orders (
  id          TEXT PRIMARY KEY,          -- ORD-<unixnano 后 10 位>
  user_id     TEXT NOT NULL,
  status      TEXT NOT NULL,             -- pending/paid/shipped/completed/refunded/closed
  total_cents INT NOT NULL,
  items       JSONB NOT NULL,            -- [{productId, name, qty, priceCents}]
  created_at  TIMESTAMPTZ DEFAULT now(),
  updated_at  TIMESTAMPTZ DEFAULT now()
);
```

- **事务扣库存**（同库单事务，条件更新防超卖）：

```sql
BEGIN;
  INSERT INTO orders(...);
  UPDATE products SET stock = stock - $qty WHERE id = $pid AND stock >= $qty;
  -- 受影响行数 = 0 → ROLLBACK → 409 库存不足
COMMIT;
-- 提交后发 NATS 事件（跨服务通知不进事务，at-most-once 可接受）
```

- **状态机**：`pending →(pay) paid →(ship) shipped →(confirm) completed`；`pending --超时--> closed`（sweeper）；`paid|shipped →(退款审批流) refunded`（事务内回补库存 + 发 order.refunded）。非法迁移 409。
- **API**：`POST /orders`、`GET /orders[?status=]`、`GET /orders/{id}`、`POST /orders/{id}/pay|ship|refund|close|confirm`；`/healthz`；`/metrics`（下单 QPS、订单金额直方图——Grafana 业务指标演示）。
- **变更类端点鉴权**：要求 `X-Action-Token` header = env `PAAS_ORDER_ACTION_TOKEN`（见 §7）。bff 代理时附加；mcp/sweeper 直调时自带。frontend 不感知。
- 复用 `internal/` 既有包（observ/dpdisc/natspub），跟随现有服务模式。

## 4. MQ 事件流（多消费组 + 平台管理面对齐）

order 在**事务提交后**发 NATS 到现有 subject `shop-events`：

| 事件 | 触发 | 消费者 |
|------|------|--------|
| order.created | 下单成功 | bff-consumer（通知中心）+ stats-consumer（GMV 内存累计）|
| order.paid / order.shipped | 支付/发货 | bff-consumer |
| order.refunded / order.closed | 退款/关单 | bff-consumer |

- **bff 内两个消费组**（不同 QueueSubscribe group，各自全量）：`bff-consumer`（100 条环形缓冲 → 通知中心）+ `stats-consumer`（内存累计订单数/GMV/状态分布，暴露 `GET /api/stats`，frontend 概览 tab 直读）。
- **设计修正记录**：GMV 统计消费者必须是常驻进程（CronJob 跑完退出丢事件），故放 bff 内而非 statsworker。「CronJob 产出→appconfig→UI」演示点保留给 statsworker 既有 category 统计行为。
- **平台管理面对齐**：setup 在平台 `mq-topics`/`consumer-groups` 登记对应记录（shop-events + bff-consumer/stats-consumer），console 数据服务详情页「消息主题」section 与真实业务流对上。

## 5. order-sweeper CronJob（`examples/paas-shop/sweeper/`，约 150 行）

- 复用 statsworker 模式（单次跑完退出），schedule `*/2 * * * *`。
- 逻辑：扫 `pending AND created_at < now()-ORDER_TTL_MINUTES` → 事务内置 `closed` + 回补库存 + 发 `order.closed`。
- `ORDER_TTL_MINUTES` env：**test=2（演示立等可取）、prod=30**。
- test/prod 各部署一个（连各自库）。顺带恢复 statsworker 部署。
- 演示点：工作负载列表 CronJob 类型 + Pod 周期创建/完成 + 实时日志。

## 6. 退款审批 workflow（approve 节点 × 平台即运营界面）

```
shop-refund-approval: start → llm（shop-assistant 分析退款理由，输出建议+金额摘要）
  → approve（暂停等人工）→ tool refund_order（MCP 真实调 order 退款+回补库存）→ end
```

- 触发在**平台 console**（工作流页手动 run，inputs: orderId/reason）。frontend 运营视图「退款」按钮显示引导层（附订单号一键复制）——运营动作发生在 PaaS 控制台，是叙事高潮。
- 拒绝路径 = abort 运行（人工否决即终止，与真实电商大额退款一致）。
- 与既有 `shop-ticket-triage` 的关系：其 `query_order` 工具随 §7 真实化自动升级。
- **环境边界**：mcp 是平台 AI 层单实例（连测试环境的 order），**退款/query 演示走 test 订单**；生产环境数据隔离正是演示点（prod 退款实操留进阶指南）。mcp 不部署 prod 版。
- `POST /orders/{id}/confirm` 端点保留（状态机完备）但 UI 不做按钮——演示旅程不需要（shipped 已可讲述）。

## 7. MCP 真实化 + Secret 注入

- `mcp/main.go` 删除内存硬编码订单 map（ORD-1001~1003），`query_order`/`refund_order` 改 HTTP 调 order 服务（dpdisc/服务发现同 bff 模式）。MCP serverURL 带端口 `:8080`（既有踩坑）。
- **Secret**：平台建 app 级 Secret `PAAS_ORDER_ACTION_TOKEN`（scope=app，静态加密落库 `enc:v1:`），经 appconfig 注入 bff/order/sweeper/mcp 四个服务共享——演示静态加密 + 多消费者注入。e2e 断言：PG `SELECT` 看 `enc:v1:` 前缀。

## 8. 泳道演示（「下单服务 v2 改版」旅程）

1. console 创建泳道 `feat-order-v2`（standard）。
2. 部署变更服务：order v2（可感知新行为：下单响应带「预计送达时间」+ 页面横幅）到泳道——**只部署 order**。
3. 泳道域名访问：前端从 hostname 首段提取 lane，所有 API 调用带 `x-paas-lane`（扩展现有 `lane.ts` laneFetch——「入口染色归 SDK」的示例落地）。
4. 链路分叉：bff 经 **dpdisc `/dp/instances?service=x&lane=y`** 查询（平台数据面发现 API，降级逻辑内建）——order 命中泳道版（v2 响应），product/recommend 未命中降级基线。**只部署变更服务+降级基线是核心演示点**。
5. 对比：`shop.test.example.com` 同操作 → v1 无新字段。
6. 平台佐证：泳道管理页、trace `paas.lane` 属性、日志 lane 过滤；演示毕关闭泳道级联回收（TTL/手动）。
- 泳道流量不落 prod（生产拒建泳道），域名方案体现：泳道域只在 `*.shop.test.example.com` 下。
- **实施首验证点 A**：pipeline 自动建的泳道 workload 名 = `<appID>-<svc>-svc-<lane>`（BaselineWorkloadName 规则）——验证 `/dp/instances` lane 查询能否命中该命名。若不对齐：演示改为显式建泳道 workload（名可控），平台修复（EndpointsReader 按 lane label 匹配）记遗留。

## 9. prod 上线（promote 编排）

### 关键平台事实（已验证，勿推翻）

1. **`BaselineWorkloadName(app, svc, lane)` 不含环境维度**（`internal/devops/model.go:313`），而 K8s Service 名 = workload 名（`workload_controller.go:382` applyService，selector 创建后不可变）。若 test/prod 同服务 baseline 同名 → 同 namespace Service 撞名，prod selector 锁死指向 test Pod → **prod 永不可达**。
2. **规避（零平台改动）**：利用 `workload.Service`（逻辑服务标识，跨环境一致）与 `workload.Name`（物理实例名）分离——prod baseline 由 setup **显式创建**：Name=`paas-shop-<svc>-prod`，Service 字段与 test 同值。promote 的 CreateRelease 按 `(app, env, lane, service)` 找到 prod baseline → UpdateImage 滚动更新。
3. **BindingInjector 按数据服务自身 EnvID 分桶注入 appconfig**（`cmd/core/binding_injector.go:156`）——绑定 shop-db（test）只注入 test 桶，绑定 shop-db-prod 只注入 prod 桶。双绑定双实例 = 环境级数据隔离，reconciler 按 workload EnvID 取对应桶注入 Pod env。
4. **NextPromoteTarget**：`ORDER BY promote_order ASC, id ASC`（同序取 id 最小）。prod-bj(20)/prod-sh(20) 同序 → **setup 把 prod-sh 调成 25**（定位多区域容灾），晋升链干净：test(10) → prod-bj(20)。
5. PromoteRelease 复用 CreateRelease(env=targetEnvID)，依赖 pipeline 内前置 release stage 的 OutReleaseID。

### setup 脚本（prod 全套资源初始化，七步）

| 步骤 | 内容 | 演示点 |
|------|------|--------|
| ① prod 数据服务 | shop-db-prod(pg)/shop-redis-prod/shop-nats-prod（EnvID=env-acme-prod-bj）+ 应用双绑定 | 环境级数据隔离 |
| ② prod baseline | 6 个 workload（bff/product/recommend/order/chatbot/frontend，`-prod` 后缀名，Service 字段=test 同值，EnvID=prod-bj，**带 ResourceSpec 生产禁 BestEffort**；mcp 属平台 AI 层不上 prod）+ sweeper-prod/statsworker-prod CronJob（TTL=30） | prod:write 门禁（dev key 403 → admin 通过） |
| ③ prod 域名 Route | `shop.example.com`：`/`→frontend-prod、`/api`→bff-prod | 治理 Route 按 host 聚合 path；一份 frontend 镜像双环境复用（Ingress path 分流，替代 nginx 反代） |
| ④ bff 生产环境配置 | `PRODUCT_SERVICE_URL=paas-shop-product-prod` 等 `*_SERVICE_URL` 指向 -prod 服务名 | 应用配置按环境隔离 |
| ⑤ prod-sh 发布顺序调 25 | PUT 环境 PromoteOrder（promote_order=25） | 晋升链无歧义（同序取 id 最小，见平台事实 4） |
| ⑥ 备份记录 | 对 shop-db-prod 建一条 backup 记录 | backup 前端入口演示 |
| ⑦ Secret | `PAAS_ORDER_ACTION_TOKEN`（app 级） | 静态加密 |

### 演示流水线（⑥ 之后）

租户自定义 pipeline `shop-release-<svc>`（模板+绑定模型本身是卖点）：`build → deploy(test) → release → promote(prod)`。演示门禁：sk-acme-dev 触发被 prod:write 拦 → sk-acme-admin 通过。

### frontend 双环境复用

frontend 镜像同源双部署（test/prod 各一 workload），API 经 Ingress path 分流（`/api`→bff），不依赖构建参数。验证点：现有 nginx.conf 若有 /api 反代则保留无害（Ingress 直接路由 /api 不经 frontend）。

## 10. 域名方案（三套区分，外部示例域名）

| 域名 | 指向 | 用途 |
|------|------|------|
| `shop.example.com` | 生产基线（frontend-prod + bff-prod path 分流） | 生产流量 |
| `shop.test.example.com` | 测试基线（同构） | 日常联调 |
| `<lane>.shop.test.example.com` | **同一基线 bff**（泳道不部署入口） | 泳道染色入口 |

- **隐私约束**：文档/setup 脚本/注释对外材料统一 `*.example.com`，不出现内网真实域名；脚本头注释说明「演示域名经 Ingress host 匹配，本机 `curl -H 'Host: ...'` 或 hosts 解析访问」。
- Route/Ingress host 与文档同源（单一真源，防漂移）。

## 11. frontend 改动（AdminPanel + 环境徽标，约 500 行 TS）

- **AdminPanel.vue**（tab 切换，跟随现有无 router/pinia/UI 库模式）：
  - 概览 tab：经营卡片（今日订单数/GMV/状态分布，读 `GET /api/stats`）+ 最近事件时间线（复用 /api/events）。
  - 订单 tab：列表（状态筛选/徽标）+ 详情（items 明细/时间线）+ 操作：发货、关单、退款（引导层→平台 console 工作流，附订单号复制）。
  - 商品 tab：列表（价格/库存/状态）+ 库存行内编辑 + 上新（复用 POST /products）。
- **环境徽标**（顶栏全站）：hostname 推断——`shop.example.com`→红「生产」；`shop.test.example.com`→绿「测试」；`<lane>.` 前缀→蓝「泳道」+ 横幅。双环境对比一眼区分。
- **买家侧**：商品详情抽屉加「下单/支付」按钮。
- **lane.ts**：laneFetch 扩展按 hostname 自动染色。

## 12. 文档站（frontend/docs 新章节）

- 「示例演示」章节：三套域名表、30 分钟演示旅程脚本（下单→事件流→关单→退款审批→泳道对比→promote 上线→环境对比），每步标注平台 console 页面与观测页停留点（trace `paas.lane`、order RED 指标、日志 lane 过滤）。
- 「进阶指南」可选章节：变更管理（Change/Batch **发布火车 Release Train**，场景与泳道 v2 合流：变更分支→泳道验证→批次集成→approve 后随火车发布→promote）+ 金丝雀 canary stage。
- 全部用 `*.example.com`。
- **术语与业界对齐**（文档站写作约束）：统一「生产环境/测试环境」（不用内部速记「prod 域/test 域」）、「泳道（全链路灰度）」、「发布火车（Release Train）」、「发布顺序（promote order）」；专有名词首次出现标注英文原文。

## 13. 测试与验证

- **单测**（examples 内）：order 状态机迁移合法性（纯函数表驱动）；bff 泳道 lane 提取/降级解析函数。
- **平台零代码改动** → 回归 `make test` + 三前端 build。
- **e2e 演示旅程**（部署后脚本化断言）：
  1. test 域名下单（库存扣减）→ 通知中心滚 order.created → 概览 tab 数字变化
  2. 超卖下单 → 409
  3. sweeper 2min 关单 → 库存回补 → order.closed 事件
  4. console 触发退款 workflow → llm 建议 → approve → refund 真实执行（订单 refunded + 库存回补）
  5. 泳道域名 v2 响应/横幅 vs 基线 v1；trace 带 paas.lane
  6. promote 后 prod 域名独立库下单（test 数据不可见）
  7. reconcile.sh 对账全绿；Secret 落库 `enc:v1:` 断言；审计日志含退款操作
- **实施顺序**（验证点前置，防错误假设上盖楼）：
  1. 验证 A：泳道 workload 自动命名 × `/dp/instances` lane 查询对齐（§8）
  2. 验证 B：promote 找显式命名的 prod baseline（§9 规避路径）
  3. order 服务 + 单测 → 4. bff/mcp/frontend → 5. setup（§9 七步）→ 6. 泳道/流水线/工作流 → 7. 文档 → 8. 部署 + e2e

## 14. 遗留债务（平台侧，本次不做）

1. `BaselineWorkloadName` 不含环境维度——同 app 双环境自动建 baseline 会撞 K8s Service 名（selector 锁死）。正解：名带 env 维度或 namespace-per-env。
2. 泳道自动命名与泳道发现约定的对齐性（视验证 A 结果）。
3. 一次性 Job 类型示例。
4. 告警联动、金丝雀告警（依赖告警引擎架构升级）。

## 15. 收益评估记录（决策依据存档）

| 项 | 决策 | 理由 |
|----|------|------|
| 独立 order 微服务（方案 A） | ✅ | 「往应用加一个微服务要几步」本身是 PaaS 最佳叙事 |
| 完整电商（支付/物流/评价/会员/优惠券） | ❌ | 纯业务复杂度，不演示平台能力 |
| 独立后台前端 | ❌ | AdminPanel 并入现有 frontend 即可 |
| prod 独立 pg/redis/nats | ✅ | 环境级数据隔离是 promote 演示的核心配套 |
| 对外展示主线（prod 常驻） | ✅ | 30 分钟可讲述、流程完整 |
| GMV 统计进 bff 消费组 | ✅（修正） | CronJob 常驻矛盾，bff 双组保住多消费组演示 |
| 变更管理/金丝雀进阶指南 | 📄 仅文档 | 塞主线超载，场景已合流可后补实操 |
