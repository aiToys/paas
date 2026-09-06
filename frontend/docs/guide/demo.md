# 示例演示：paas-shop 全能力旅程

paas-shop 是平台的完整能力演示应用（电商域）：一份代码、六个微服务，贯穿应用主线、DevOps、数据服务、可观测与 AI 编排。本章是一条 **30 分钟演示旅程**——下单 → 事件流 → 超卖防护 → 超时关单 → 退款审批 → 泳道对比 → promote 上线 → 环境对比，每步标注平台控制台的停留点。

演示资源由示例库的初始化脚本一键创建（幂等，可重复执行；`PAAS_API` / `PAAS_TOKEN` 环境变量可覆盖默认平台地址与凭证，默认值面向内置演示集群）。演示账号见[快速开始](./quickstart)。

## 三套域名

| 域名 | 环境 | 用途 |
|------|------|------|
| `shop.example.com` | 生产环境（Production） | 生产流量 |
| `shop.test.example.com` | 测试环境（Staging） | 日常联调基线 |
| `shop.test.example.com` + `x-paas-lane: feat-order-v2` | 泳道（全链路灰度，Feature Lane） | 变更验证 |

- 演示域名经 Ingress host 匹配；本机访问用 `curl -H 'Host: shop.example.com' http://<集群入口>/`，或配置 hosts 解析。
- 泳道不是独立域名：平台 Route 无泳道维度（host 只有基线域名），染色入口是**基线域名 + `x-paas-lane` 请求头**（或页面顶栏泳道输入框）——`*.shop.test.example.com` 泛子域名需 Ingress 额外配 wildcard 泛解析方可使用。
- 页面顶栏环境徽标一眼区分：红「生产」/ 绿「测试」/ 蓝「泳道」。
- 泳道域名取 hostname 首段为泳道名，前端自动为所有请求附加 `x-paas-lane` 染色头。

## 演示旅程

### 1. 下单（3 分钟）

打开测试域名，点商品卡片进入详情抽屉，依次「下单」「支付」。

```bash
# 验证订单与库存扣减（返回裸数组，按创建时间倒序）
curl -H 'Host: shop.test.example.com' http://<集群入口>/api/orders
```

**停留点**：控制台「应用」→ paas-shop → 「部署」看 order 服务实例；「平台能力 → 可观测」链路追踪看 bff → order 调用链（瀑布树展开每跳 span），应用监控看 order RED 指标（请求率 / 错误 / 延迟）。

### 2. 事件流（3 分钟）

下单事务提交后发 NATS 事件（类型一律过去式：`order.created` / `paid` / `shipped` / `confirmed` / `refunded` / `closed`）。页顶「消息」铃铛滚出 `order.created`；切到运营视图（商店页切换按钮）「概览」tab，订单总数 / GMV / 状态分布随之变化。

**停留点**：「资源中心 → 消息队列」进 shop-nats 详情，「消息主题」展开 `shop-events`——两个消费组各自全量消费：`bff-consumer`（通知中心）+ `stats-consumer`（经营统计，bff 内常驻累计）。

### 3. 超卖防护（2 分钟）

运营视图「商品」tab 上新一个库存为 0 的商品，再对它下一笔单 → 409 库存不足。order 在同库单事务内做条件更新（`UPDATE ... WHERE stock >= qty`），受影响行数为 0 即回滚——数据库层防超卖，不靠应用层锁。

### 4. 超时关单（3 分钟）

下一笔单但不支付。测试环境订单 TTL 为 2 分钟（`ORDER_TTL_MINUTES`：测试 2 分钟立等可取，生产环境 30 分钟），sweeper 定时任务（CronJob，测试侧每 2 分钟、生产侧每 5 分钟调度）扫出超时单：置 `closed` + 回补库存 + 发 `order.closed`。

**停留点**：「工作负载 → 定时」看 sweeper——CronJob 类型工作负载、Pod 周期创建与完成、点开看本次执行实时日志。

### 5. 退款审批（5 分钟）

大额退款走人工审批工作流，运营动作发生在 PaaS 控制台：

1. 运营视图「订单」tab 找一笔已支付订单，点「退款」→ 引导层复制订单号；
2. 控制台「AI 服务 → 工作流」运行 `shop-refund-approval`，inputs 填 `orderId` / `reason`；
3. llm 节点（shop-assistant 智能体）分析退款理由，输出建议与金额摘要；
4. approve 节点暂停等人工——**确认** → tool 节点经 MCP（Model Context Protocol）真实调 order 退款：订单置 `refunded` + 事务回补库存 + 发 `order.refunded`；**拒绝** = abort，运行即终止。

**停留点**：「平台能力 → 安全」审计日志看退款操作全记录（操作者 / 时间 / 内容，只增不删）。

> 边界：MCP 工具连接测试环境订单，退款演示走测试单；生产环境数据隔离正是第 7 步的演示点。

### 6. 泳道对比（5 分钟）

泳道（全链路灰度）是**环境内的流量染色**，不是环境复制——只部署变更服务，未命中请求自动降级基线。

1. 控制台「环境」→ 测试环境详情创建泳道 `feat-order-v2`（standard 联调泳道，TTL 自动回收）；
2. 把 order v2 部署到泳道——**只部署 order**（v2 为演示用自制变更：给下单响应加「预计送达」字段并在页面横幅展示）；
3. 访问 `shop.test.example.com` 并携带泳道染色——页面顶栏泳道输入框填 `feat-order-v2` 切换（或 `curl -H 'Host: shop.test.example.com' -H 'x-paas-lane: feat-order-v2' <集群入口>/api/orders...`）：bff 经数据面发现 API 按泳道查 order 命中 v2 副本；product / recommend 未命中降级基线；
4. 对比：清空泳道输入框（或不带 `x-paas-lane` 头）同操作 → v1 响应无新字段。

**停留点**：「环境」详情泳道区看泳道归属工作负载；「可观测」链路看 `paas.lane` 属性、日志按 lane 过滤；页面顶栏「实际命中实例」指纹在切换后变化，即流量走了不同副本。演示毕关闭泳道，级联回收全部泳道内工作负载。

### 7. promote 上线（6 分钟）

流水线 `shop-release` 四阶段：**build** → **deploy**（测试环境基线）→ **release**（版本里程碑：git tag + 镜像版本）→ **promote**（按发布顺序 promote order 晋升生产环境）。

先演示生产防护：开发者身份触发（`sk-acme-dev`）→ promote 阶段被生产写权限（`prod:write`）拦下，403。换管理员身份（`sk-acme-admin`）重新触发，全程通过。

**停留点**：「DevOps → 运行记录」进运行详情全屏页（GitHub Actions 式），stage 逐步推进 + 实时日志。

上线后访问 `shop.example.com`（红「生产」徽标）下单——生产环境独立库（shop-db-prod 独立 PostgreSQL / Redis / NATS），测试环境的订单在这里不可见：**环境级数据隔离**。

### 8. 收尾（3 分钟）

- **对账**：`./scripts/reconcile.sh` 全绿——部署引用 ↔ 平台记录 ↔ registry 实物三方比对，无孤儿引用 / 幽灵记录 / 配额漂移；
- **静态加密**：app 级 Secret `PAAS_ORDER_ACTION_TOKEN` 落库即密文（`enc:v1:` 前缀，AES-256-GCM），经应用配置注入环境内全部工作负载，由 bff / order / mcp 三个服务共享消费——「应用 → 配置」看注入条目；
- **备份**：「资源中心 → 数据库」shop-db-prod 详情的备份记录。

## 进阶指南

- **变更管理——发布火车（Release Train）**：多分支变更合入集成批次一起验证、一起发车。与泳道场景合流：变更分支 → 泳道验证 → 集成批次（临时集成分支即泳道）→ 批次 CI → approve 随车发布 → promote。见[变更管理](../features/change)。
- **金丝雀（canary）发布**：流水线 canary 阶段自动建 `canary-<runID>` 泳道跑 1 副本，并行验证不影响基线；人工决策——promote 基线全量滚动，或 terminate 零风险退出。与第 6 步同一泳道底座，见[代码构建与流水线](../features/devops)。

## 延伸阅读

- [应用与工作负载](../features/app) / [环境与发布](../features/env) / [泳道（灰度隔离）](../features/lane)
- [数据服务](../features/dataservice) / [可观测](../features/observability) / [智能体编排](../features/ai)
