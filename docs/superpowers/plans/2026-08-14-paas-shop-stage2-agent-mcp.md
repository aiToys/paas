# paas-shop 阶段 2：Agent runtime 迁移 + MCP 服务 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** chatbot 从「进程内手写 LLM 循环」迁移到调平台 agent runtime（`POST /v1/chat/completions {model:"agent:<id>"}`），新建 paas-shop-mcp 真实 MCP 服务（4 工具），多轮记忆在 chatbot 侧落地。

**Architecture:** chatbot 瘦身为「组装 messages（历史+当前）→ 调 agent 虚拟模型 → SSE 透传」；工具循环/RAG/system 构建全交平台 runtime；paas-shop-mcp 照抄 examples/mcp-server 协议模式，4 工具中 query_product/search_products 调 product 服务，query_order/refund_order 内存 demo。setup §8 重写（Prompt 去变量 + 4 Tool 实体 + PAAS_AGENT_MODEL 注入），deploy 加 mcp workload。

**Tech Stack:** Go 1.26 标准库（examples 独立 module，零新依赖——redis 用已有 github.com/redis/go-redis/v9）+ bash 部署脚本调平台 REST API。

**Spec:** `docs/superpowers/specs/2026-08-14-paas-shop-stage2-agent-mcp-design.md`

## Global Constraints

- **examples 独立 module**：所有 Go 改动在 `examples/` 下，`go build ./...` 在 `examples/` 目录跑。**禁止 import 任何 `github.com/aitoys/paas/` 平台内部包**（全部走 HTTP）。
- **平台约束 1**：runtime 无会话记忆，msgs 全由调用方（chatbot）传入。
- **平台约束 2**：PromptRef 模板变量不渲染，Template 原文当 system prompt → 新模板**无占位符**。
- **平台约束 3**：一个 Tool 实体 = MCP server 上按 Name 匹配的一个工具 → 4 工具 = 4 个 Tool 实体（同 serverURL 不同 name）。
- **PAAS_AGENT_MODEL 必填 fail-fast**：chatbot 启动时未设直接报错退出（防静默降级到裸 LLM）。
- **MCP 协议**：JSON-RPC 2.0 over HTTP POST {serverURL}/mcp；initialize 返 protocolVersion "2025-06-18"（兼容客户端）；tools/call 结果 `{"content":[{"type":"text","text":"..."}]}`。
- **Release service 字段用短名**（阶段 1 教训）：mcp workload 的 service 字段=`mcp`，发镜像走 `POST /api/applications/paas-shop/releases`。
- **不执行 git 操作**：默认只改文件不 commit（用户全局规则）；集群 Gitea push 仅在用户授权部署时执行。
- **部署 runbook**：改代码 → 同步集群 Gitea（port-forward 13000，paas-bot/<PAAS_GITEA_BOT_PASSWORD>）→ BuildRun → Release → 验证。

---

## File Structure

| 文件 | 动作 | 职责 |
|---|---|---|
| `examples/paas-shop/mcp/main.go` | 新建 | MCP server（4 工具，JSON-RPC 手写） |
| `examples/paas-shop/mcp/mcp_test.go` | 新建 | tools/call 逻辑单测（httptest 模拟 product） |
| `examples/paas-shop/chatbot/main.go` | 重写 | 调 agent:{id} + SSE 透传 + 多轮记忆 |
| `examples/paas-shop/chatbot/memory_test.go` | 新建 | 历史裁剪 + 记忆模式单测 |
| `examples/scripts/setup-paas-shop.sh` | 改 | §8 重写（Prompt 去变量 + 4 Tool + Agent 更新 + PAAS_AGENT_MODEL）+ 构建循环加 mcp |
| `examples/scripts/deploy-paas-shop.sh` | 改 | 加 paas-shop-mcp workload |

**包路径**：`github.com/aitoys/paas-examples/paas-shop/mcp`、`.../paas-shop/chatbot`。

---

## Task 1: 新建 paas-shop-mcp 服务（4 工具 MCP server）

**Files:**
- Create: `examples/paas-shop/mcp/main.go`
- Test: `examples/paas-shop/mcp/mcp_test.go`

**Interfaces:**
- Consumes: env `PRODUCT_SERVICE_URL`（默认 `http://paas-shop-product:8081`）。product 服务阶段 1 端点 `GET /products/{id}` + `GET /products?q=&category=&limit=`。
- Produces: `POST /mcp`（initialize/tools/list/tools/call）+ `GET /healthz`，listen `:8080`。4 工具：`query_product` / `search_products` / `query_order` / `refund_order`。

- [ ] **Step 1: 写失败测试 — tools/call 各工具逻辑**

Create `examples/paas-shop/mcp/mcp_test.go`:
```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fake product 服务：详情 + 搜索两端点。
func fakeProduct(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/products/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"name":"机械键盘 X1","price":299,"category":"外设","stock":5}`))
	})
	mux.HandleFunc("/products", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			_, _ = w.Write([]byte(`[{"id":1},{"id":2}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":1,"name":"机械键盘 X1"}]`))
	})
	return httptest.NewServer(mux)
}

func TestCallToolQueryProduct(t *testing.T) {
	srv := fakeProduct(t)
	defer srv.Close()
	productURL = srv.URL
	out := callTool("query_product", map[string]any{"productId": "1"})
	if want := "商品详情"; !contains(out, want) {
		t.Fatalf("query_product 应含 %q，got %s", want, out)
	}
	if !contains(out, "机械键盘") {
		t.Fatalf("query_product 应含商品名，got %s", out)
	}
}

func TestCallToolSearchProducts(t *testing.T) {
	srv := fakeProduct(t)
	defer srv.Close()
	productURL = srv.URL
	out := callTool("search_products", map[string]any{"q": "键"})
	if !contains(out, "机械键盘") {
		t.Fatalf("search 应返回匹配商品，got %s", out)
	}
	// 参数全空 = 全量
	out = callTool("search_products", map[string]any{})
	if !contains(out, `"id":2`) && !contains(out, "2") {
		t.Fatalf("空参数应返回全量，got %s", out)
	}
}

func TestCallToolQueryOrder(t *testing.T) {
	out := callTool("query_order", map[string]any{"orderId": "ORD-1001"})
	if !contains(out, "shipped") {
		t.Fatalf("query_order 应含状态，got %s", out)
	}
	out = callTool("query_order", map[string]any{"orderId": "NOPE"})
	if !contains(out, "不存在") {
		t.Fatalf("未知订单应提示不存在，got %s", out)
	}
}

func TestCallToolRefundOrder(t *testing.T) {
	out := callTool("refund_order", map[string]any{"orderId": "ORD-1001", "reason": "质量问题"})
	if !contains(out, "退款") {
		t.Fatalf("refund_order 应受理，got %s", out)
	}
	// 受理后 query_order 应带 refundStatus
	out = callTool("query_order", map[string]any{"orderId": "ORD-1001"})
	if !contains(out, "refunding") {
		t.Fatalf("受理后查询应带退款状态，got %s", out)
	}
}

func TestToolNamesListed(t *testing.T) {
	// 4 工具名与平台 Tool 实体 name 逐字一致（runtime 按 Name 匹配）
	for _, n := range []string{"query_product", "search_products", "query_order", "refund_order"} {
		if _, ok := toolSchemas[n]; !ok {
			t.Fatalf("toolSchemas 缺工具 %s", n)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd examples && go test ./paas-shop/mcp/ -v`
Expected: FAIL（`callTool`/`toolSchemas` 未定义）。

- [ ] **Step 3: 实现 mcp/main.go**

Create `examples/paas-shop/mcp/main.go`——协议骨架照抄 `examples/mcp-server/main.go`（rpcReq/rpcResp/handleMCP/writeJSON 同款），差异：
1. **`toolSchemas` 包级 map**（测试直接断言）：`map[string]map[string]any`，4 个工具的 name/description/inputSchema。描述写给 LLM 看：`search_products`「按关键字/分类搜索商品，用于找商品/推荐/比价，参数可全空返回全量」、`query_product`「按商品 ID 查详情（已知编号时用）」、`query_order`/`refund_order` 同 mcp-server 现描述。
2. **`tools/list`** 遍历 `toolSchemas` 生成列表。
3. **`callTool(name, args)`** 分支：
   - `query_product`：同 mcp-server `queryProduct`（GET /products/{id}，用 `httpClient`（`observ.NewClient()`）带超时，非裸 http.Get）。
   - `search_products`：读 args `q`/`category`/`limit`（limit 用 `fmt.Sprintf("%v", ...)` 兼容 json.Number/string），`url.Values` 拼 query（url-encoded），GET `productURL+"/products?"+vals.Encode()`，返 `"商品搜索结果: "+body`。
   - `query_order`/`refund_order`：照抄 mcp-server 内存 orders（3 单 ORD-1001/1002/1003）+ refunds map + mu 锁。
4. **initialize** 返 `protocolVersion: "2025-06-18"`（平台 client 用的版本，spec 锁定）。
5. **main**：读 `PRODUCT_SERVICE_URL` env；`/healthz`；`/metrics`（`observ.MetricsHandler()`）；`observ.Init("paas-shop-mcp")` + `observ.Recover(observ.Handler("mcp", mux))`；listen `:8080`。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd examples && go test ./paas-shop/mcp/ -v`
Expected: PASS（6 个测试）。

- [ ] **Step 5: 验证编译**

Run: `cd examples && go build ./... && go vet ./paas-shop/mcp/`
Expected: 无报错。

---

## Task 2: chatbot 重写（调平台 agent + SSE 透传 + 多轮记忆）

**Files:**
- Modify: `examples/paas-shop/chatbot/main.go`（整体重写）
- Test: `examples/paas-shop/chatbot/memory_test.go`

**Interfaces:**
- Consumes: env `PAAS_AGENT_MODEL`（**必填**，如 `agent:agent-xxx`）、`PAAS_GATEWAY_URL`/`PAAS_LLM_BASE_URL`、`PAAS_LLM_API_KEY`/`PAAS_API_KEY`、`PAAS_MEMORY_MODE`（`memory` 默认 / `redis`）、`REDIS_URL`（redis 模式用，shop-cache 绑定注入）。
- Produces: `POST /chat {message,userId}` → SSE 流式（透传平台 content + reasoning_content）。
- 内部函数（测试依赖）：`trimHistory(msgs []chatMsg, max int) []chatMsg`（上限裁剪，保 system 在首）；`type chatMsg struct{ Role, Content string }`。

- [ ] **Step 1: 写失败测试 — 历史裁剪**

Create `examples/paas-shop/chatbot/memory_test.go`:
```go
package main

import "testing"

func mk(role string) chatMsg { return chatMsg{Role: role, Content: "x"} }

func TestTrimHistoryKeepsSystemFirst(t *testing.T) {
	msgs := []chatMsg{mk("system")}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, mk("user"), mk("assistant"))
	}
	out := trimHistory(msgs, 20)
	if len(out) != 20 {
		t.Fatalf("裁到上限 20，got %d", len(out))
	}
	if out[0].Role != "system" {
		t.Fatalf("system 必须保留在首位，got %s", out[0].Role)
	}
	// 裁剪后首轮必须是 user（assistant 开头语义不完整）
	if out[1].Role != "user" {
		t.Fatalf("裁剪后应从 user 起，got %s", out[1].Role)
	}
}

func TestTrimHistoryShortNoop(t *testing.T) {
	msgs := []chatMsg{mk("system"), mk("user")}
	if got := trimHistory(msgs, 20); len(got) != 2 {
		t.Fatalf("未超限不应裁剪，got %d", len(got))
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd examples && go test ./paas-shop/chatbot/ -v`
Expected: FAIL（`chatMsg`/`trimHistory` 未定义）。

- [ ] **Step 3: 重写 chatbot/main.go**

整体重写（保留 observ 引用与 SSE 头写法），结构：

1. **全局**：`gatewayURL`/`apiKey`/`agentModel`/`memoryMode`；`httpClient = observ.NewClient()`、`streamClient = observ.NewStreamingClient()`。
2. **main**：`observ.Init` → 读 env（gatewayURL/apiKey 解析逻辑保留现状）→ `agentModel = os.Getenv("PAAS_AGENT_MODEL")`，**空则 `slog.Error("PAAS_AGENT_MODEL 未设置（chatbot 需平台 Agent 虚拟模型 agent:<id>）") + os.Exit(1)`** → `memoryMode`（REDIS_URL 非空且 `PAAS_MEMORY_MODE=redis` 用 redis；默认 memory；`PAAS_MEMORY_MODE=redis` 但 REDIS_URL 空则 warn 降级 memory）→ redis 模式 `redis.NewClient(&redis.Options{Addr: REDIS_URL})` + ping → mux（`/healthz` `/metrics` `/chat`）→ listen `:8083`。
3. **`type chatMsg struct{ Role string; Content string }`** + 序列化 helper `toMsgs([]chatMsg) []map[string]any`。
4. **记忆接口（小接口 + 两实现）**：
   ```go
   type historyStore interface {
       Load(ctx context.Context, userID string) []chatMsg
       Append(ctx context.Context, userID string, user, assistant string)
   }
   ```
   - `memHistory`：`sync.Mutex` + `map[string][]chatMsg`（`chat:history` 无 TTL），Append 时 `trimHistory(..., 20)`。
   - `redisHistory`：`rdb *redis.Client`，key `chat:history:<userId>` TTL 24h，Load 时 `json.Unmarshal`，Append 时先 Load → 追加 → trim → `SetEX`。错误一律 log + 返回已知部分（降级不崩）。
5. **`trimHistory(msgs []chatMsg, max int) []chatMsg`**：len ≤ max 原样返；否则保 msgs[0]（system）+ 末尾 max-1 条；若裁剪后 msgs[1] 非 user，从 msgs[1] 起向后丢到首个 user。
6. **chatHandler**：收 `{message,userId}`（校验同现状）→ `hist := store.Load(ctx, userID)` → `msgs := append(hist, chatMsg{"user", message})` → 调 `POST {gatewayURL}/v1/chat/completions` body `{"model": agentModel, "messages": toMsgs(msgs), "stream": true}`（复用现有 streamLLM 骨架：SSE 头 + scanner 逐行透传 `data:` payload + flusher + `[DONE]`）→ **透传时累积 assistant content**（解析每 chunk 的 `choices[0].delta.content` 拼接）→ 流结束后 `store.Append(ctx, userID, message, assistantText)`。
7. **删除**：`retrieveKB`/`buildFallbackContext`/`fallbackFAQ`/`ensureKB`/`executeTool`/`parseToolCallsFromContent`/`toolCallRe`/`callLLM`/`streamContent`/`LLMResp`/`ToolCall`/`kbID`/`productURL`/`model`/`PAAS_MODEL`。SSE 解析只留 content 累积 + 原样透传（含 reasoning_content 帧透传——不解析不拦截）。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd examples && go test ./paas-shop/chatbot/ -v`
Expected: PASS（2 个测试）。

- [ ] **Step 5: 验证整体编译 + vet**

Run: `cd examples && go build ./... && go vet ./paas-shop/...`
Expected: 无报错。

---

## Task 3: setup 脚本 §8 重写 + 构建循环加 mcp

**Files:**
- Modify: `examples/scripts/setup-paas-shop.sh`（§8 段 L105-125 + 构建循环 L137 附近）

**Interfaces:**
- Consumes: 平台 REST `/api/prompts` `/api/tools` `/api/agents`。
- Produces: 无变量 shop-cs 模板（幂等更新）+ 4 个 Tool 实体 + shop-agent 更新（tools 数组 + PAAS_AGENT_MODEL appconfig）。

- [ ] **Step 1: 重写 §8.1 Prompt（去变量 + 幂等更新）**

把 §8.1 改为「查已存在则 PUT 更新，否则 POST 创建」：
```bash
# 8.1 Prompt：无占位符人设（平台不渲染模板变量，问题描述由 user 消息携带）
PROMPT_ID=$(curl -s -H "$H" "$B/api/prompts?name=shop-cs" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((p['id'] for p in items if p.get('name')=='shop-cs'),''))" 2>/dev/null)
PROMPT_BODY='{"name":"shop-cs","template":"你是 PaasShop 智能客服，友好专业。规则：\n1. 回答商品、订单、售后相关问题\n2. 找商品/推荐/比价用 search_products；已知商品 ID 查详情用 query_product；查订单用 query_order；退款用 refund_order\n3. 不确定时诚实告知，不编造\n4. 回答简洁","variables":[]}'
if [ -n "$PROMPT_ID" ]; then
  curl -s -X PUT -H "$H" -H "Content-Type: application/json" "$B/api/prompts/$PROMPT_ID" -d "$PROMPT_BODY" >/dev/null && echo "  prompt updated: $PROMPT_ID"
else
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/prompts" -d "$PROMPT_BODY \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  prompt:',d.get('id'))" 2>/dev/null
fi
```
（注：`/api/prompts` 列表查询参数以平台 OpenAPI 为准——实现时先 `curl /openapi.json` 确认是 `?name=` 还是全量列表后 python 过滤；若 PUT 端点不存在则改为「DELETE 旧 + POST 新」并记录。）

- [ ] **Step 2: 重写 §8.2 为 4 个 Tool 实体（幂等）**

```bash
# 8.2 4 个 Tool 实体（已验证 runtime.go:141-146 按 mt.Name == t.Name 匹配——
#     Tool 实体 name 必须与 MCP server 工具名逐字一致，不能加 shop- 前缀）
MCP_URL="http://paas-shop-mcp.paas-t-acme.svc.cluster.local"
TOOL_IDS=""
for t in \
  "query_product:查商品详情（按商品 ID 返回名称/价格/库存）" \
  "search_products:搜索商品（按关键字/分类，用于找商品/推荐/比价）" \
  "query_order:查询订单状态（按订单号返回详情）" \
  "refund_order:对订单发起退款" ; do
  TNAME="${t%%:*}"; TDESC="${t#*:}"
  TID=$(curl -s -H "$H" "$B/api/tools" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((x['id'] for x in items if x.get('name')=='$TNAME'),''))" 2>/dev/null)
  BODY="{\"name\":\"$TNAME\",\"description\":\"$TDESC\",\"type\":\"mcp\",\"config\":{\"serverURL\":\"$MCP_URL\",\"apiKey\":\"\"},\"enabled\":true}"
  if [ -n "$TID" ]; then
    curl -s -X PUT -H "$H" -H "Content-Type: application/json" "$B/api/tools/$TID" -d "$BODY" >/dev/null
  else
    TID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/tools" -d "$BODY" | python3 -c "import sys,json;print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null)
  fi
  echo "  tool: $TNAME -> $TID"
  TOOL_IDS="$TOOL_IDS \"$TID\""
done
# 旧 shop-tools 单实体删除（实体名 shop-tools 匹配不到 MCP 工具，已废）
OLD_TOOL=$(curl -s -H "$H" "$B/api/tools" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((x['id'] for x in items if x.get('name')=='shop-tools'),''))" 2>/dev/null)
[ -n "$OLD_TOOL" ] && curl -s -X DELETE -H "$H" "$B/api/tools/$OLD_TOOL" >/dev/null && echo "  deleted old tool: shop-tools"
```
**关键（已源码验证，非猜测）**：`internal/ai/agent/runtime.go` buildTools 用 `mt.Name == t.Name` 匹配 MCP ListTools 结果——实体 name 与 MCP 工具名不一致时 fallback 用实体名调用 → unknown tool。故 4 个实体 name 直接用 `query_product` 等裸名（租户内唯一即可）。

- [ ] **Step 3: 重写 §8.3 Agent 更新（幂等）**

```bash
# 8.3 shop-agent：已存在则 PUT 更新 tools，否则 POST 创建
AGENT_ID=$(curl -s -H "$H" "$B/api/agents" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((a['id'] for a in items if a.get('name')=='shop-agent'),''))" 2>/dev/null)
KB_ARG="[]"; [ -n "$KB_ID" ] && KB_ARG="[\"$KB_ID\"]"
AGENT_BODY="{\"name\":\"shop-agent\",\"description\":\"PaasShop 商品客服 Agent（RAG+MCP 工具）\",\"model\":\"glm-5.2\",\"promptRef\":\"shop-cs\",\"tools\":[$TOOL_IDS],\"knowledgeBases\":$KB_ARG,\"maxSteps\":5,\"enabled\":true}"
if [ -n "$AGENT_ID" ]; then
  curl -s -X PUT -H "$H" -H "Content-Type: application/json" "$B/api/agents/$AGENT_ID" -d "$AGENT_BODY" >/dev/null && echo "  agent updated: $AGENT_ID"
else
  AGENT_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/agents" -d "$AGENT_BODY" | python3 -c "import sys,json;print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null)
  echo "  agent: $AGENT_ID"
fi
echo "PAAS_AGENT_MODEL=agent:$AGENT_ID"
```

- [ ] **Step 4: §8.4 chatbot appconfig 注入 PAAS_AGENT_MODEL**

在 §8.5 绑定段之前加：
```bash
echo "=== §8.4 chatbot env 注入（PAAS_AGENT_MODEL）==="
curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/configs" -d \
  "{\"key\":\"PAAS_AGENT_MODEL\",\"value\":\"agent:$AGENT_ID\",\"type\":\"env\",\"envId\":\"$ENV_TEST\"}" && echo "  cfg: PAAS_AGENT_MODEL=agent:$AGENT_ID"
```

- [ ] **Step 5: 构建循环加 mcp**

`for SVC in product recommend chatbot bff statsworker; do` → `for SVC in product recommend chatbot bff statsworker mcp; do`。

- [ ] **Step 6: 语法检查**

Run: `bash -n examples/scripts/setup-paas-shop.sh`
Expected: 无语法错误。

---

## Task 4: deploy 脚本加 paas-shop-mcp workload

**Files:**
- Modify: `examples/scripts/deploy-paas-shop.sh`（§1 service 循环）

- [ ] **Step 1: service 循环加 mcp**

在 §1 的 for svc 列表追加（port 80 → containerPort 8080，**无 name 字段但 service 字段=mcp**——按阶段 1 教训，workload 创建 body 用与其它服务同款结构，name=`paas-shop-mcp`）：
```bash
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-mcp\",\"image\":\"$REGISTRY/paas-shop/mcp:$TAG\",\"replicas\":1,\"port\":80,\"containerPort\":8080}" \
```
（注：阶段 1 部署时 workload 的 `service` 字段是平台从构建 buildArgs 派生的短名；此处 name 即 `paas-shop-mcp`、service 短名 `mcp` 由 BuildRun 的 SERVICE=mcp 派生。若部署后发现 service 字段不一致，以 `GET /api/workloads` 实际值为准修正 Release 的 service 参数。）

- [ ] **Step 2: appconfig 段同步（与 setup 一致，幂等）**

deploy 脚本 §4 appconfig key 列表追加 `PAAS_AGENT_MODEL`（值需 AGENT_ID——deploy 脚本加一行查询：
```bash
AGENT_ID=$(curl -s -H "$H" "$B/api/agents" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((a['id'] for a in items if a.get('name')=='shop-agent'),''))" 2>/dev/null)
```
key 行追加 `"PAAS_AGENT_MODEL:agent:$AGENT_ID:env"`）。

- [ ] **Step 3: 语法检查**

Run: `bash -n examples/scripts/deploy-paas-shop.sh`
Expected: 无语法错误。

---

## Task 5: 整体构建验证

- [ ] **Step 1: examples 全量 build + vet + test**

Run: `cd examples && go build ./... && go vet ./... && go test ./...`
Expected: 全绿（mcp 6 测试 + chatbot 2 测试 + 既有测试）。

- [ ] **Step 2: go mod verify**

Run: `cd examples && go mod verify`
Expected: `all modules verified`。

---

## Task 6: 部署 + e2e（用户授权后执行）

- [ ] **Step 1: 同步集群 Gitea**（port-forward 13000 → clone paas-shop-examples → rsync（含 vendor）→ push）

- [ ] **Step 2: BuildRun（SERVICE=mcp + SERVICE=chatbot）+ 轮询 success**

- [ ] **Step 3: Release 部署（service 短名 mcp/chatbot）+ 跑 setup §8（更新 Prompt/Tool/Agent/PAAS_AGENT_MODEL）**

- [ ] **Step 4: e2e**
  1. `kubectl exec paas-core-xxx -n paas -- curl http://paas-shop-mcp.paas-t-acme.svc.cluster.local/healthz` → 200。
  2. 平台侧直调 agent：`POST /v1/chat/completions {model:"agent:<id>",messages:[{role:"user",content:"有没有键盘"}],stream:true}` → SSE 内容含商品（search_products 被调用）。
  3. 经 bff 完整链：`POST /api/chat {message:"查一下 1 号商品多少钱"}` → 流式回答含价格（query_product）。
  4. **多轮指代**：同一 userId 先「1 号商品多少钱」再「那它有货吗」→ 第二轮答对库存（记忆生效）。
  5. Jaeger 查 shop-agent trace：`agent.run → provider.chat → tool.call` 树完整。
  6. traffic-gen agent 模式（AGENT_MODEL=agent:{id}）跑通。

- [ ] **Step 5: 记录 memory**（阶段 2 落地 + 部署踩坑）

---

## Self-Review 结果

**1. Spec 覆盖**：方案 A（mcp 4 工具）→ Task 1 ✓；方案 B（chatbot 瘦身+记忆）→ Task 2 ✓；方案 C（setup §8 重写）→ Task 3 ✓；方案 D（deploy mcp workload）→ Task 4 ✓；验证（单测/构建/部署/e2e）→ Task 5/6 ✓。

**2. Placeholder 扫描**：Task 3 的「以平台实际字段为准」（prompts 列表参数 / toolName 匹配字段）不是占位符——是计划明确的**实现时验证点**，因平台 OpenAPI 细节需 curl 确认，已给出两条路径的完整代码。

**3. 类型一致性**：`callTool(name string, args map[string]any) string` Task 1 定义+测试一致 ✓；`chatMsg{Role,Content}` + `trimHistory([]chatMsg, int) []chatMsg` Task 2 定义+测试一致 ✓；`historyStore` 接口 Load/Append 签名两实现一致 ✓；MCP 工具名 4 处（toolSchemas/setup Tool 实体/e2e 断言）逐字一致 ✓。
