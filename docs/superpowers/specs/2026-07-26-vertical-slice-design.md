# 设计规格：MaaS 端到端垂直切片

- **状态**：已批准
- **日期**：2026-07-26
- **目标**：端到端打通"前端发起推理 → Gateway 路由 → MaaS 插件 → Provider → 流式返回"全链路，验证插件契约与数据流是否真能跑通。**最大未知数 = 插件契约 + 数据流**，本切片专攻它。

## 切片范围（YAGNI 简化边界）

每层允许简化，但全链路必须通。可在普通开发机、**无 GPU、无 K8s** 下运行。

| 层 | 本切片实现 | 简化点 | 留给后续 |
|----|-----------|--------|---------|
| Identity/鉴权 | 内存仓储 + 单个硬编码 API Key | 不做多租户/RBAC | Plan 2：PG + RBAC |
| 推理 Provider | echo provider（OpenAI 兼容，回显输入） | 不接真实推理 | 下一切片：真实 vLLM |
| K8s 编排 | **不依赖 K8s**——进程内 DeployController | 跳过 CRD/envtest | 下一切片：K8s + CRD |
| Gateway | 真做：OpenAI 兼容路由 + 鉴权 + Token 计量(log) | 计量仅 log | Plan 2：PG/ClickHouse |
| 前端 | console-user Playground 接真实 Gateway 流式 | 仅 Playground | 后续：完整三套 |

## 架构

```
console-user Playground
      │ POST /v1/chat/completions (SSE)
      ▼
API Gateway ── auth(API Key) ── router(模型→provider) ── meter(token log)
      │
      ▼
MaaS 插件 (进程内 DeployController)
      │
      ▼
echo Provider (实现 Provider 接口；预留 vLLM Provider)
```

## 关键设计

1. **Gateway 是 Core 的数据面入口组件**（本期进程内，非独立部署）。挂载 OpenAI 兼容端点 `/v1/chat/completions`（流式 SSE）与 `/v1/models`。
2. **Provider 接口**：`Chat(ctx, req) (<-chan Chunk, error)`，流式返回。echo provider 实现：把最后一条 user message 逐 token 回显。接口预留 vLLM provider。
3. **插件契约复用**：MaaS 插件实现 `pkg/plugin.Plugin`，在 `Init` 阶段拿到 Gateway 注入点，注册 echo provider 到路由表。这验证"插件能否真实接入 Core"。
4. **API Key**：`sk-paas-dev-key`（环境变量 `PAAS_API_KEY` 可覆盖），Gateway 中间件校验 `Authorization: Bearer <key>`。
5. **OpenAI 兼容协议**：请求 `{model, messages:[{role,content}], stream}`；流式响应 `data: {choices:[{delta:{content}}]}\n\n`，结尾 `data: [DONE]`。

## 成功标准

- `curl` 带 API Key 调 `/v1/chat/completions` 收到 SSE 流式回显。
- console-user Playground 发送消息，看到流式输出。
- Token 计量 log 输出本次请求 token 数。
- MaaS 插件通过插件契约注册并运行（`core 启动完成，已运行插件: map[maas:true]`）。
