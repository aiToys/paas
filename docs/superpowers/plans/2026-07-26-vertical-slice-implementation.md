# 垂直切片 Implementation Plan

> **For agentic workers:** 用 superpowers:executing-plans 或 subagent-driven-development 执行。步骤用 `- [ ]` 跟踪。

**Goal:** 端到端打通"前端 → Gateway → MaaS 插件 → echo Provider → 流式返回"，验证插件契约与数据流。

**Architecture:** Gateway 作为 Core 进程内组件挂载 OpenAI 兼容端点；MaaS 插件实现 `plugin.Plugin`，在 Init 阶段把 echo provider 注册进 Gateway 路由表；前端 Playground 用 fetch+ReadableStream 消费 SSE。

**Tech Stack:** Go 标准库 net/http + encoding/json、SSE、Vue3 fetch streaming。

## Global Constraints

- 不引入 K8s / controller-runtime / GPU 依赖（本切片进程内）。
- 复用 Plan 1 的 `pkg/plugin.Plugin` 契约，不得修改契约本身。
- API Key 默认 `sk-paas-dev-key`，`PAAS_API_KEY` 环境变量可覆盖。
- 注释中文；Conventional Commits；未经用户要求不擅自 commit。

## 新增文件

```
internal/maas/
├── provider.go          # Provider 接口 + Chunk 类型
├── echo.go              # echo provider
├── echo_test.go
├── plugin.go            # MaaS 插件（实现 plugin.Plugin）
└── plugin_test.go
internal/core/gateway/
├── gateway.go           # Gateway 结构 + 路由表
├── auth.go              # API Key 中间件
├── openai.go            # /v1/chat/completions 流式 + /v1/models
├── meter.go             # Token 计量(log)
└── *_test.go
cmd/core/main.go         # 改：加载 MaaS 插件、挂载 Gateway 路由
frontend/console-user/src/views/Playground.vue  # 改：真实流式调用
```

---

### Task 1: Provider 接口 + echo provider

- Create `internal/maas/provider.go`：`Chunk{Role,Content string}`、`Provider` 接口 `Name()`/`Chat(ctx, ChatRequest) (<-chan Chunk, error)`、`ChatRequest{Model,Messages []Message}`。
- Create `internal/maas/echo.go`：`EchoProvider` 把最后一条 user message 按字符切片逐 token 推送到 channel。
- Test `echo_test.go`：喂入 messages，收集 channel 内容，断言回显了最后一条 user content。
- 验证：`go test ./internal/maas/ -v`。

### Task 2: Gateway 核心 + 路由表

- Create `internal/core/gateway/gateway.go`：`Gateway{providers map[string]maas.Provider; mu}`，`Register(model, provider)`、`Get(model)`、`Models()`。
- Test：注册后 Get 命中、未注册返回 false。

### Task 3: API Key 鉴权中间件

- Create `internal/core/gateway/auth.go`：`APIKeyAuth(key string) func(http.Handler) http.Handler`，校验 `Authorization: Bearer <key>`，失败 401 JSON。
- Test：正确 key 放行、错误/缺失 key 返回 401。

### Task 4: Token 计量

- Create `internal/core/gateway/meter.go`：`Meter` 结构 + `Record(tenantID, model string, tokens int)`，本期 `log.Printf`。
- Test：Record 调用后内部计数正确（暴露 `Count()` 便于断言）。

### Task 5: OpenAI 兼容流式 handler

- Create `internal/core/gateway/openai.go`：
  - `ChatCompletions(gw, meter) http.HandlerFunc`：解析请求 → `gw.Get(model)` → 调 `provider.Chat` → 按 SSE 推 `data: {choices:[{delta:{content}}]}` → 结尾 `data: [DONE]`；累计 content 字符数作为 token 计量。
  - `ListModels(gw) http.HandlerFunc`：返回 `{data:[{id,object:"model"}]}`。
- Test：用 httptest 发非流式请求，断言响应含回显内容 + `data: [DONE]`。

### Task 6: MaaS 插件

- Create `internal/maas/plugin.go`：`MaaSPlugin` 实现 `plugin.Plugin` 全部方法；`Manifest` 返回 `{Name:"maas"}`；`Init` 接收 `CoreDeps`（需扩展 CoreDeps 以注入 Gateway —— 见下）。
- **CoreDeps 扩展**：在 `pkg/plugin/plugin.go` 的 `CoreDeps` 接口加 `Gateway() GatewayRegistrar`，`GatewayRegistrar{Register(model, Provider)}`。`noopCoreDeps` 暂返回 nil，由 cmd/core 注入真实实现。
  - 为避免 import 循环（pkg/plugin 不能 import internal/maas），定义 `GatewayRegistrar` 为 `pkg/plugin` 中的接口，`internal/core/gateway.Gateway` 实现它。
- Test：插件 Init 后，传入的 registrar 收到一次 `Register("echo", ...)`。

### Task 7: Core 启动组装

- 改 `cmd/core/main.go`：构造 `*gateway.Gateway` + `Meter`；构造 `realCoreDeps{gw}`（实现 CoreDeps.Gateway）；实例化 `maas.MaaSPlugin` 并传入 `run()`；HTTP mux 挂载 `APIKeyAuth` 包裹的 `/v1/chat/completions` 与 `/v1/models`。
- 验证：`go build && go test ./...`；启动后 `curl /v1/models` 返回模型列表。

### Task 8: 前端 Playground 接真实 Gateway

- 改 `frontend/console-user/src/views/Playground.vue`：用 `fetch` POST `/v1/chat/completions`（带 `Authorization: Bearer sk-paas-dev-key`、`stream:true`），用 `ReadableStream` 逐 chunk 解析 SSE 并追加到 output。
- vite proxy 已配 `/v1 → :8081`；改 Gateway 监听 8081（或前端 proxy 指 8080）。

### Task 9: 端到端验证

- 启动 core（:8080）+ console-user dev（:5174）；Playground 发消息看流式输出。
- curl 流式验证。
- Commit（语义化拆分）。
