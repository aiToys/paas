# 模型市场真实化 设计规格

> 对应产品方向 [[maas-product-direction]]：应用为主线框架下走 API-First MaaS。
> 本切片把 MaaS 从「echo 玩具」推进到「多通道富信息模型市场」，为下一切片（真实 vLLM 纳管 + K8s 部署）铺好抽象。

## 目标

把当前扁平的 `map[model]Provider` 抽象升级为 **Model → Channel → Provider 三层**，让模型市场展示真实富信息（供应商/能力/上下文/定价/健康状态），并支持一个逻辑模型挂多个推理通道（按优先级 + 健康路由）。前端模型市场从写死 mock 接到真实 API。

## 非目标（明确不做，YAGNI）

- 不接真实 vLLM（下一切片）
- 不做负载均衡/亲和/复杂降级策略（本期路由=按优先级取首个健康通道）
- 不做主动健康检查 ping（框架预留，被动状态为主）
- 不做运行时模型 CRUD（注册由配置 seed 驱动；CRUD 留待 CRD/operator 切片）
- 不引入 K8s/CRD

## 架构

三层抽象：

```
Model（逻辑模型）         模型市场展示主体，富信息卡片
  └─ Channel（通道）×N     一个模型挂多个推理实例，各有 status/priority
       └─ Provider         纯推理执行者（保持 Chat 契约，echo/mock/未来 vLLM 都实现它）
```

Gateway 路由表从 `map[string]Provider` 升级为 `map[string]*provider.Model`。`Resolve(model)` 按通道优先级取**首个 healthy** 通道的 `impl`。

## 数据模型

`pkg/provider/model.go`（新建）：

```go
type Model struct {
    ID            string     `json:"id"`            // 路由键
    Name          string     `json:"name"`
    Vendor        string     `json:"vendor"`
    ContextWindow int        `json:"contextWindow"`
    Capabilities  []string   `json:"capabilities"`  // chat/embedding/vision/tool/reasoning/code
    InputPrice    float64    `json:"inputPrice"`    // 元/百万 token
    OutputPrice   float64    `json:"outputPrice"`
    Description   string     `json:"description,omitempty"`
    Channels      []*Channel `json:"channels"`
}

type Channel struct {
    ID       string   `json:"id"`                 // 如 "qwen2.5-7b#mock-a"
    Type     string   `json:"type"`               // echo/mock/vllm
    Priority int      `json:"priority"`           // 小=高
    Status   string   `json:"status"`             // healthy/degraded/offline
    Endpoint string   `json:"endpoint,omitempty"`
    impl     Provider `json:"-"`                  // 实际执行者，不序列化
}
```

`Provider` 契约不变（`Name() + Chat(ctx, ChatRequest) (<-chan Chunk, error)`），echo 零改动。

## Gateway 升级（`internal/core/gateway/gateway.go`）

```go
type Gateway struct {
    mu     sync.RWMutex
    models map[string]*provider.Model
}

func (g *Gateway) RegisterModel(m *provider.Model)
func (g *Gateway) Resolve(model string) (provider.Provider, error)  // 按优先级取首个 healthy
func (g *Gateway) Models() []*provider.Model
func (g *Gateway) MarkChannelStatus(model, channelID, status string) // 调用失败被动降级
```

`provider.GatewayRegistrar` 契约演进：`Register(model, Provider)` → `RegisterModel(*Model) error`。

## 配置 seed（`internal/maas/catalog.go` 新建）

Go struct 列表定义模型目录，`MaaSPlugin.Init` 启动加载并 `RegisterModel`。新增 **MockProvider**（按预设文本流式吐出），让不同模型有不同回复，演示比 echo 全回显更真实；echo 保留作最简验证。seed 覆盖前端现有 8 个模型（qwen2.5-7b / qwen2.5-72b / deepseek-v3 / deepseek-r1 / llama3.3-70b / glm-4-9b / qwen2.5-coder-32b / bge-m3），每个挂 1-2 通道。未来接 CRD/operator 只改加载源，抽象不动。

## API

| 端点 | 用途 | 说明 |
|---|---|---|
| `GET /v1/models` | OpenAI 兼容 | `{object:"list", data:[{id, object:"model", owned_by: vendor}]}`，给 SDK |
| `GET /api/models`（新） | 模型市场富信息 | 完整 Model 列表含 channels，给前端 |
| `POST /v1/chat/completions` | 推理 | 内部从 `Get` 改 `Resolve`，经 API Key 鉴权 |

两 API 都经 `APIKeyAuth` 中间件。

## 前端 `Marketplace.vue`

- 删除写死的 8 个 mock，`onMounted` 接 `GET /api/models`，加 loading 骨架态
- Model 接口对齐后端（加 channels/pricing/contextWindow）
- **视觉不动**（已 10 轮打磨）；`active` 实例数 → `channels.filter(status==='healthy').length`
- category 筛选由 `capabilities` 映射：dialogue→chat, reasoning→reasoning, code→code, embed→embedding
- 卡片「部署」按钮改为 **「试用」** → 跳 `/playground?model=<id>` 预选该模型（部署按钮留待 vLLM/K8s 切片）
- 错误态：加载失败 ElMessage 提示

## 测试

- `gateway`：`Resolve` 优先级路由（高优先 channel 故障时降级到次优先 healthy）；`MarkChannelStatus` 后路由切换；未知 model 返回 error
- `maas`：seed 注册后 `Models()` 返回预期数量（8）；每个 model `Resolve` 都能拿到 Provider；channel 数与 seed 一致
- `handler`：`/api/models` 返回富信息含 channels；`/v1/models` 兼容 OpenAI 格式（含 owned_by）
- `echo` + `mock` provider：Chat 流式正常关闭、首块声明 role

## 开源标准要求

- 全量 `go test ./... -race` 通过
- `golangci-lint run ./...` 0 issues
- `gofmt` / `goimports` 干净
- 前端 `pnpm build` 通过、`vue-tsc --noEmit` 无错
- CLAUDE.md「垂直切片」章节同步更新（echo → 多通道模型市场）
- 不引入 GPL/AGPL 依赖（无新外部依赖）

## 验证（端到端）

core 启动后：
```bash
curl -H "Authorization: Bearer sk-paas-dev-key" http://localhost:8080/api/models       # 富信息
curl -H "Authorization: Bearer sk-paas-dev-key" http://localhost:8080/v1/models        # OpenAI 兼容
curl -N -H "Authorization: Bearer sk-paas-dev-key" -H "Content-Type: application/json" \
  -d '{"model":"qwen2.5-7b","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://localhost:8080/v1/chat/completions
```
Playwright 验证前端模型市场加载真实模型卡片。
