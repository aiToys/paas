# 模型市场真实化 实施计划

> Spec: `docs/superpowers/specs/2026-07-26-model-marketplace-design.md`

## 任务拆分（TDD）

### T1: provider 三层数据模型（`pkg/provider/model.go` 新建 + 契约演进）
- 新建 `Model` / `Channel` 结构（见 spec）
- `GatewayRegistrar`: `Register(model, Provider)` → `RegisterModel(*Model) error`
- `Provider` 契约不变

### T2: Gateway 升级（`internal/core/gateway/gateway.go`）
- `models map[string]*provider.Model`
- `RegisterModel` / `Resolve`（按优先级取首个 healthy）/ `Models` / `MarkChannelStatus`
- 测试：优先级路由、故障降级、未知 model

### T3: MockProvider + catalog seed（`internal/maas/mock.go` + `catalog.go` 新建）
- MockProvider 按预设文本流式吐出
- seed 覆盖 8 个模型，每个 1-2 通道
- `plugin.go` Init 改为加载 catalog + RegisterModel

### T4: Gateway handler 适配（`internal/core/gateway/openai.go` + `cmd/core/main.go`）
- `ChatCompletions` 改 `Resolve`，失败时 `MarkChannelStatus` 降级
- `ListModels` 富信息（owned_by=vendor）
- 新增 `GET /api/models` handler（完整富信息）
- main.go 挂载 `/api/models`

### T5: 前端 Marketplace 接真实 API（`frontend/console-user/src/views/Marketplace.vue`）
- 删 mock，`onMounted` 接 `/api/models` + loading 骨架
- Model 接口对齐；active→healthy channels 数
- category 由 capabilities 映射
- 「部署」→「试用」跳 `/playground?model=<id>`

### T6: 端到端验证 + 开源标准收尾
- curl 三端点 + Playwright 前端
- `go test -race` / `golangci-lint` / `gofmt` / 前端 build + tsc
- CLAUDE.md「垂直切片」更新
- 提交（拆分：后端抽象 / 前端接 API / 文档）

## Global Constraints
- Go 1.22、Apache 2.0 兼容、无新外部依赖
- 注释中文、与现有风格一致
- 不引入 GPL/AGPL
