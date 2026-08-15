# paas-shop 阶段 3：电商前端完善（搜索/详情/消息中心）

日期：2026-08-15。4 阶段计划第 3 阶段（阶段 1 后端业务+MQ、阶段 2 Agent+MCP 已完成；阶段 4 CI/CD 验证待启动）。

## 目标

把 paas-shop 前端从「商品列表 + 推荐 + AI 客服」升级为完整电商 demo 体验：**搜索/分类过滤、商品详情、消息中心**。后端能力大部分已就绪（搜索/详情端点既有），仅 bff 补事件订阅 + 查询端点。

## 现状

- 前端 `examples/paas-shop/frontend/`：单文件 App.vue（225 行），Vue3+Vite+TS，无 vue-router、无 UI 库（手写 CSS）。已实现：商品列表（`/api/products`）、推荐侧栏（`/api/recommend`）、AI 客服弹窗（`/api/chat` SSE + reasoning 折叠）。
- bff：代理 products/recommend/chat，SSE 透传。
- MQ：product 服务发 `shop-events`（product.created/updated/bulk-seed 等 JSON 事件）；recommend 已订阅（group=recommend-consumer）。
- 搜索端点：`GET /api/products?q=&category=&limit=`（bff 已代理，前端未用）；详情：`GET /api/products/{id}`（bff 已代理）。

## 方案

### A. 搜索 + 分类过滤（纯前端）

- header 下加搜索条：文本输入（防抖 300ms，回车立即）+ 分类 chip（全部/外设/显示/音频/配件，从商品列表动态派生去重，不硬编码）。
- 调既有 `GET /api/products?q=&category=&limit=50`；空结果显示「未找到」提示。
- 清空搜索恢复全量。

### B. 商品详情抽屉（纯前端）

- 点商品卡右侧滑出抽屉（overlay，同 chat 弹窗模式）：大字价格/库存/分类/描述/创建时间 + 「问客服」按钮（预填「{商品名}怎么样」打开 chat 弹窗）。
- 数据：列表点击时已持有全字段，**直接用已有数据，不再发详情请求**（列表与详情字段一致；`GET /api/products/{id}` 留给 chatbot 工具链消费，前端不重复调）。
- ESC / 点遮罩关闭。

### C. 消息中心（bff 补端点 + 前端铃铛）

**bff（`examples/paas-shop/bff/main.go`）**：
- 启动时若 `NATS_URL` 非空则 `nc.QueueSubscribe("shop-events", "bff-consumer", ...)`（独立 group，与 recommend 不互抢；多副本各持一份缓冲可接受）。
- 内存环形缓冲最近 100 条事件（slice + 索引循环覆写，mutex 保护），每条记录 `{type, productId, name, category, at, receivedAt}`。
- `GET /api/events?limit=20`：倒序返回最近事件（最新在前），JSON 数组。NATS 不可达时返回空数组（降级不 5xx，与 product publish 降级同构）。

**前端**：
- header 铃铛图标 + 未读红点（localStorage 记 `paas:events:lastSeen` 时间戳，receivedAt > lastSeen 计未读）。
- 点开下拉面板：事件列表（类型中文映射：product.created→商品上新 / product.updated→商品变更 / product.bulk-seed→批量导入 / 其他→原样），10s 轮询（silent，onUnmounted clearInterval）；打开面板即清零未读。

### D. 前端结构重组（DRY）

App.vue 拆组件（`frontend/src/components/`）：
- `SearchBar.vue`（props: categories; emits: search(q, category)）
- `ProductCard.vue`（props: product; emits: click）
- `DetailDrawer.vue`（props: product|null; emits: close, ask(product)）
- `ChatWindow.vue`（整体迁移现有 chat 弹窗逻辑；props: none—内部管理 open 状态，expose openWith(preset) 方法）
- `NotificationCenter.vue`（内部轮询 + 未读）

App.vue 保留数据编排（products/recommends 加载 + 搜索状态）。

### 不做（YAGNI）

- 购物车/下单 UI（mcp 订单是内存演示数据，做 UI 下单链路属编造业务）
- vue-router / pinia（单页形态保持）
- SSE 事件推送（事件低频，10s 轮询够）
- 分页/无限滚动（limit=50 覆盖 demo 数据量）
- 事件持久化（重启丢失可接受，demo）

## 横切

- examples 隔离铁律：不 import 平台内部包；bff 事件订阅复用 `internal/natspub`（examples 内部包，允许）。
- trace/observability：bff 新端点经既有 `observ.Handler` 自动埋点；NATS 订阅 log 用 slog。
- 降级路径：NATS 不可达 → `/api/events` 返 `[]`；搜索/详情失败 → 前端既有 error 展示模式。

## 验证（e2e）

1. `pnpm-equivalent npm run build` 前端通过（vue-tsc）+ examples 全量 go build/vet/test 绿。
2. 部署（既有 runbook：Gitea 同步 → BuildRun bff+frontend → Release）。
3. e2e：搜索「键盘」过滤 → 点卡片详情抽屉 → 「问客服」预填发起 chat → 改一个商品（curl PUT product）触发事件 → ≤10s 铃铛红点 + 列表出现「商品变更」。
