# paas-shop 阶段 2：Agent runtime 迁移 + MCP 服务

## Context

4 阶段计划的第 2 阶段（阶段 1 后端业务+MQ+Job 已落地）。目标：chatbot 从「进程内手写 LLM 循环」迁移到**调平台 agent runtime**（工具循环/RAG/trace 全交平台），paas-shop-mcp 空壳补成**真实 MCP 服务**（4 工具），多轮对话记忆在 chatbot 侧落地。

阶段 1 探索确认的关键事实：
- 平台 agent runtime 已完整暴露在标准 OpenAI 接口后：`POST /v1/chat/completions` + `{"model":"agent:<AGENT_ID>","stream":true}`（gateway/openai.go:65 分流 → ai_bridges.go adapter → runtime.ServeSSE）。**纯 HTTP，examples 零 SDK 依赖**。
- runtime 承担：system 构建（PromptRef 模板 + KB RAG 注入）、工具循环（maxSteps、tool_calls → MCP invoke → 回放）、trace（agent.run → provider.chat → tool.call 三层 span）、工具进度经 reasoning_content 推送。
- **平台约束 1**：runtime 无会话记忆，msgs 全由调用方传入 → chatbot 自缓存历史（用户裁决）。
- **平台约束 2**：PromptRef 模板变量（{{brand}}/{{question}}）不被渲染，Template 原文当 system prompt → 模板去变量（用户裁决）。
- **平台约束 3**：一个 Tool 实体 = 一个 MCP server 上按 Name 匹配的一个工具 → 4 工具 = 4 个 Tool 实体（同 serverURL 不同 name）。
- MCP 协议：JSON-RPC 2.0 over HTTP POST {serverURL}/mcp，initialize → tools/list → tools/call，同步单次（参照 examples/mcp-server/main.go 手写，无 SDK）。
- paas-shop-mcp 空壳现状：setup §8.2 建了 Tool 指向 `http://paas-shop-mcp.paas-t-acme.svc.cluster.local`，但该服务/workload/源码全不存在——平台 runtime 调它 connection refused。

## 不做（YAGNI）

- 平台 agent runtime 加 session 记忆（架构级，留平台后续切片）
- 平台 PromptRef 变量渲染（同上）
- Tool 的 http/builtin 类型支持（平台现状仅 mcp 可调用，不动）
- 订单真实建模（内存 demo 数据，与 examples/mcp-server 一致）
- chatbot 的 fallbackFAQ 降级路径（迁移后 KB RAG 由平台承担，未配 agent 时直接报配置错误）
- 阶段 3 的电商前端（chat 入口在 frontend，本阶段用 curl + traffic-gen 验证）

## 方案

### A. 新建 examples/paas-shop/mcp 服务（MCP server，4 工具）

**新建 `examples/paas-shop/mcp/main.go`**（照抄 examples/mcp-server/main.go 协议模式）：

- `POST /mcp` 手写 JSON-RPC：initialize（protocolVersion "2025-06-18"）/ tools/list / tools/call；`GET /healthz`；listen :8080。
- 结果格式 `{"content":[{"type":"text","text":"..."}]}`（与平台 client 严格匹配）。
- **4 工具**（均调 product 服务 `PRODUCT_SERVICE_URL`，默认 `http://paas-shop-product:8081`）：
  1. `query_product(id int)` — GET /products/{id}，返商品详情 JSON。
  2. `search_products(q string, category string, limit int)` — GET /products?q=&category=&limit=（阶段 1 端点），返商品列表。参数可全空 = 全量。
  3. `query_order(orderId string)` — 内存 orders map（demo 数据 3 单），返订单详情。
  4. `refund_order(orderId string, reason string)` — 内存 refunds map，返受理结果。
- 工具描述（description）写给 LLM 看：何时用哪个、参数含义（如 search 用于「找/推荐/比价」，query 用于「已知编号查详情」）。

### B. chatbot 迁移（手写循环 → 调平台 agent）

**改 `examples/paas-shop/chatbot/main.go`**（532 行 → 大幅瘦身约 250 行）：

1. **chatHandler 瘦身**：收 `{message,userId}` → 写 SSE 头 → 组装 messages（历史 + 当前 user）→ 调 `gatewayURL/v1/chat/completions` with `model=PAAS_AGENT_MODEL`（如 `agent:agent-xxx`）→ 透传 SSE（复用现有 streamLLM 骨架）。
2. **删除**：retrieveKB + fallbackFAQ（KB RAG 归平台 runtime）、tools 定义 + executeTool（工具循环归平台）、`<tool_call>` 文本解析（平台用标准 tool_calls）、二次调用逻辑。**保留**：SSE 透传、错误处理、userId。
3. **多轮记忆（chatbot 自缓存）**：`PAAS_MEMORY_MODE` env 切换——`memory`（进程内 map[userId][]Message + 上限 20 条裁剪，默认）/ `redis`（REDIS_URL 非空时自动用，key `chat:history:<userId>` TTL 24h，重启不丢）。存平台响应的 assistant 文本（不存 reasoning/tool 中间态）。
4. **env 契约**：`PAAS_AGENT_MODEL`（如 `agent:agent-xxx`，**必填**——未设启动报错退出，防静默降级到裸 LLM）、`PAAS_GATEWAY_URL`/`PAAS_LLM_API_KEY`（既有）。
5. **工具进度可视**：平台 runtime 把「🔧 调用工具 ...」经 reasoning_content 推送，chatbot SSE 透传时保留该字段（前端思考面板可见工具调用过程）。

### C. setup 脚本对齐（§8 重写）

**改 `examples/scripts/setup-paas-shop.sh` §8**：
1. **§8.1 Prompt 模板去变量**：shop-cs 模板重写为无占位符人设（「你是 PaasShop 智能客服…」，问题描述由 user 消息携带）。幂等更新（PUT 或删建）。
2. **§8.2 4 个 Tool 实体**（同 serverURL 不同 name）：`query_product` / `search_products` / `query_order` / `refund_order`——**实体 name 必须与 MCP 工具名逐字一致**（已源码验证 `runtime.go` buildTools 按 `mt.Name == t.Name` 匹配 ListTools 结果，不一致时 fallback 用实体名调用 → unknown tool；故不加 shop- 前缀，租户内唯一即可）。均 type=mcp config.serverURL=`http://paas-shop-mcp.paas-t-acme.svc.cluster.local`。旧 `shop-tools` 单实体 DELETE（实体名匹配不到任何 MCP 工具，已废）。
3. **§8.3 Agent 更新**：shop-agent 的 tools 数组改 4 个新 Tool ID。
4. **§8.4 chatbot env 注入**：appconfig 加 `PAAS_AGENT_MODEL=agent:<AGENT_ID>`（env test）。
5. **§8.5 输出 AGENT_ID**（已有）。

### D. 部署（deploy-paas-shop.sh 扩展）

**改 `examples/scripts/deploy-paas-shop.sh`**：
- 加第 6 个 workload `paas-shop-mcp`（type=service，port 80 → containerPort 8080，镜像 `$REGISTRY/paas-shop:mcp-<tag>` 或经 Release 编排）。
- **workload 的 service 字段 = `mcp`**（与其它服务一致的短名约定，阶段 1 踩坑教训）。
- 构建循环（setup §9）加 `mcp`（SERVICE=mcp → `./paas-shop/mcp`）。
- chatbot workload 无需重建（env 经 appconfig 注入，改 PAAS_AGENT_MODEL 后 rolling）。

### E. 数据流总览（阶段 2 闭环）

```
用户/traffic-gen → bff /api/chat → chatbot /chat
  → chatbot: 历史(redis/memory) + user msg → POST /v1/chat/completions {model: agent:xxx, stream}
    → 平台 gateway → agent dispatcher → runtime.Run
      → buildSystem（shop-cs 无变量模板 + KB RAG 注入）
      → runLoop: glm-5.2 决策 → tool_calls → MCP client
        → POST http://paas-shop-mcp.../mcp (tools/call)
          → query_product/search_products → product 服务（阶段 1 搜索）
          → query_order/refund_order → 内存 demo
      → 最终答案流式返回
    ← SSE（content + reasoning_content 工具进度）
  ← chatbot 透传 SSE + 存 assistant 文本进历史
← bff ← 用户

trace（Jaeger）: HTTP → bff → chatbot → agent.run → [provider.chat × N] + [tool.call × M]
```

## 横切约束

- examples 独立 module，禁止 import 平台内部包（全部走 HTTP）。
- MCP serverURL 从 **paas-core Pod** 发起（跨 ns paas → paas-t-acme），集群内 DNS 可达（阶段 1 已验证跨 ns 调用 product 服务正常——bff/product 同 ns；paas-core 调 dataservice 也是跨 ns，先例成立）。
- PAAS_AGENT_MODEL 必填 fail-fast（防静默降级）。
- 4 Tool 实体的 name 与 MCP server 的 tools/list 返回的 name 逐字一致（runtime 按 Name 匹配）。

## 关键文件清单

| 文件 | 动作 | 内容 |
|---|---|---|
| `examples/paas-shop/mcp/main.go` | 新建 | MCP server 4 工具（JSON-RPC 手写，照抄 mcp-server 模式） |
| `examples/paas-shop/chatbot/main.go` | 重写瘦身 | 调 agent:{id} + SSE 透传 + 多轮记忆（memory/redis 双模式） |
| `examples/scripts/setup-paas-shop.sh` | 改 | §8 重写（Prompt 去变量 + 4 Tool + Agent 更新 + PAAS_AGENT_MODEL 注入） |
| `examples/scripts/deploy-paas-shop.sh` | 改 | 加 paas-shop-mcp workload（service 字段=mcp） |
| 构建循环 | 改 | setup §9 加 SERVICE=mcp |

## 验证（一次改对）

1. **单测**：mcp 4 工具的 tools/call 逻辑（httptest 模拟 product）。
2. **构建**：`cd examples && go build ./... && go vet ./... && go test ./...`。
3. **同步 Gitea + 构建 + 部署**（阶段 1 runbook：port-forward clone → push → BuildRun(SERVICE=mcp/chatbot) → Release(service=mcp/chatbot 短名)）。
4. **e2e**：
   - `kubectl exec paas-core -- curl paas-shop-mcp.../healthz` 200。
   - 平台侧调 agent：`POST /v1/chat/completions {model:"agent:<id>",messages:[{role:user,content:"有没有键盘"}]}` → SSE 流式 + 内容含商品（search_products 工具被调用）。
   - 经 bff 完整链：`POST /api/chat {message:"查一下 1 号商品多少钱"}` → 流式回答含价格（query_product 被调用）。
   - **多轮**：先问「1 号商品多少钱」→ 再问「那它有货吗」（指代「它」）→ 第二轮能基于历史答对（记忆生效）。
   - **trace**：Jaeger 查 shop-agent 链路，agent.run → provider.chat → tool.call 树完整。
   - traffic-gen agent 模式（AGENT_MODEL=agent:{id}）持续跑通。

## 留后续（阶段 3-4）

- 电商前端 chat 窗（消费 chatbot SSE）
- 平台 agent session 记忆 / Prompt 变量渲染（平台侧切片）
- MCP 工具结果富类型（image/resource）
- 订单工具接真实订单域（阶段 3 前端有下单后）
