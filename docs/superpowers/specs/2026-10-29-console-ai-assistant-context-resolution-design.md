# AI 助手 V2：自然语言优先与诊断上下文解析设计

日期：2026-10-29

状态：实现交接稿；本文件用于交给其他 Agent 实现，不包含本轮代码变更。

关联设计：[2026-10-27-console-ai-assistant-diagnosis-design.md](2026-10-27-console-ai-assistant-diagnosis-design.md)。本稿只覆盖交互入口、目标解析、告警/Trace 接入和兼容改造；现有会话、消息、Worker、预算、审计、租户隔离与只读诊断约束继续有效。

## 1. 产品决策

AI 助手采用“自然语言优先、服务端解析上下文、必要时最小追问”的交互。

- 顶栏入口打开后不强制先选择工作负载。
- 用户可以直接描述症状，也可以从工作负载、告警或 Trace 详情页带入上下文。
- 服务端将自然语言中的服务名、告警 ID、Trace ID、时间范围等线索解析成可授权的诊断上下文。
- 唯一匹配时展示“已识别范围”，再开始只读诊断。
- 多个匹配时在对话流中展示候选卡片；用户选择前不得调用诊断工具。
- 无法解析时只追问一个必要问题，不打开完整资源选择器阻断对话。
- 模型可以提取线索和判断意图，但不得决定租户、namespace、PromQL、LogQL、模型、工具或最终权限。

目标是让助手像对话式值班入口，同时保持服务端强制的租户边界和证据范围。

## 2. 当前实现需要改变的点

当前实现将 `CreateSessionRequest.Target` 作为必填，并在 `AssistantConversation.vue` 中显示必选工作负载下拉框；`Session.Target` 也是固定的 `AuthorizedTarget`。这适用于工作负载详情页，但会阻断顶栏自然语言、告警和 Trace 入口。

改造要求：

- 保持已有带 workload target 的会话和历史完全兼容。
- 新会话允许没有已解析目标，状态为 `draft` 或 `need_resolution`。
- 不复制或改写已有消息真源；解析卡片、候选选择和范围确认作为会话事件或消息附件保存。
- 已经解析并开始运行的会话仍固定诊断上下文；更换服务、告警或 Trace 创建新的诊断上下文。

## 3. 诊断上下文模型

建议在 `internal/assistant` 增加受控的锚点与解析结果类型，命名遵循仓库风格：

```go
type DiagnosticAnchor struct {
    Kind string // workload | alert | trace | log
    ID   string
}

type DiagnosticContext struct {
    Anchors           []DiagnosticAnchor
    ResolvedResources ResolvedResources
    WindowStart       time.Time
    WindowEnd         time.Time
    ResolutionStatus  string // resolved | need_choice | unavailable
    ResolutionReason  string
}
```

`ResolvedResources` 至少包含 tenant、application、environment、workload、service、Pod UID、版本和解析时间。客户端提交的 anchor 只能是业务 ID 或 Trace ID；客户端不得提交 namespace、集群地址、selector、任意查询语句或模型覆盖。

已存在的 `TargetRef` 继续兼容，服务端收到后转换为 `DiagnosticContext`，不再把客户端内容直接当作授权目标。

## 4. 入口和解析流程

```text
自然语言或页面上下文
  → 提取意图和线索
  → ContextResolver 查询业务资源
  → 校验 tenant/user/application 权限
  → 0 个结果：need_clarification
  → 1 个结果：resolved
  → 多个结果：need_choice
  → 构造不可由客户端修改的 AuthorizedContext
  → 生成诊断计划并执行只读工具
```

解析优先级：

1. 页面显式传入的 workload、alert、trace 上下文。
2. 文本中的明确 ID：Trace ID、告警 ID、工作负载 ID。
3. 服务端业务名称和环境名称的受控匹配。
4. 模型提取的候选线索，仅作为候选，必须经过服务端查询和权限校验。

自然语言解析失败不能默认扫描当前租户，也不能根据模糊服务名猜测生产环境。

## 5. 告警和 Trace 适配器

### 告警

新增窄接口，例如：

```go
type AlertResolver interface {
    Resolve(ctx context.Context, tenantID, alertID string) (DiagnosticContext, error)
}
```

解析链路：告警 ID → 规则、标签、触发时间 → 应用/环境/服务 → 工作负载和 Pod UID → 告警窗口内的事件、日志、指标、发布记录。

告警无法关联到当前用户可访问的工作负载时，返回不可用原因，不把告警文本直接交给跨租户工具。

### Trace

新增受控 Trace 读取接口，例如：

```go
type TraceResolver interface {
    Resolve(ctx context.Context, tenantID, traceID string) (DiagnosticContext, error)
}
```

Trace 解析至少读取根 Span、错误 Span、耗时 Span、服务、版本、Pod 资源属性和时间范围。Trace ID 必须经过租户归属和应用权限校验；单独的 Trace ID 不能直接成为授权目标。

Trace 能关联多个服务时返回候选服务；Trace 后端无法证明租户归属时拒绝诊断。后续工具可以在授权范围内查询关联日志、事件、指标和发布记录，但不得扩大到整条未授权 Trace 或全租户资源。

## 6. API 契约

### 创建会话

扩展现有接口，`context` 可选；保留旧 `target` 请求的兼容解析：

```json
POST /api/assistant/sessions
{
  "agentKey": "builtin:runtime-diagnosis",
  "context": {"kind": "trace", "id": "4bf92f3577b34da6"}
}
```

响应必须包含 `resolutionStatus` 和当前上下文摘要。未解析会话不得伪造 `AuthorizedTarget`。

### 发送问题

```json
POST /api/assistant/sessions/{id}/runs
{
  "question": "这次请求为什么变慢？",
  "mode": "investigate"
}
```

已解析时保持现有 `202 + run` 行为；未解析或多匹配时返回结构化的 `409 target_resolution_required`，包含 `question`、`candidates`、`resolutionStatus`，不得创建运行任务或扣除模型预算。

### 选择候选

增加一个受保护的解析确认接口，或按仓库路由风格实现等价端点：

```json
POST /api/assistant/sessions/{id}/context/resolve
{
  "candidateId": "server-generated-candidate-id"
}
```

候选 ID 必须由服务端生成并绑定 tenant/user/session，不能由客户端伪造 workload、namespace 或 Pod UID。确认后返回新的 `resolved` 上下文；目标变更创建新会话，不覆盖已有运行记录。

OpenAPI、前端生成类型、错误响应、审计事件和幂等约束必须同步更新。

## 7. 前端交互改造

修改 `frontend/console-user-v2`：

- 删除空会话时必选工作负载的下拉框。
- 首屏直接展示自然语言输入框和快捷提示：分析这条告警、分析这次 Trace、检查这个服务、查看最近发布影响。
- 从详情页通过结构化 context 打开助手，禁止抓取整页 DOM 或上传日志正文。
- 发送后展示“已识别上下文”卡片，包含来源、服务、环境、时间范围和证据类型。
- 多候选时展示内嵌选择卡片，选择前不显示“正在检查”或工具进度。
- 无目标时显示最小追问，例如“请补充环境”或“请提供告警 ID/Trace ID”。
- 解析成功后显示固定范围条；用户点击“更换目标”创建新会话。
- 历史列表显示来源类型和目标摘要；旧 workload 会话照常恢复。
- `explain` 模式使用已有证据，不调用新工具；`investigate` 模式使用解析后的最新上下文重新取证。

## 8. 安全和预算边界

- ContextResolver 是唯一生成 `AuthorizedContext` 的入口。
- 每个工具调用前后继续复核 tenant、application、workload、Pod UID 和容器关系。
- 空上下文、跨租户上下文、过期候选、无法归属的 Trace 和告警一律 fail closed。
- 未解析请求不创建 run、不预留 token、不写入供应商调用记录。
- 解析文本和提示词需要脱敏；日志和 Trace 证据只按现有有界脱敏策略进入模型。
- 诊断 Agent 继续只读，不增加自动修复动作。

## 9. 扩展契约

官方 Agent 目录增加输入能力描述：

```json
{
  "key": "builtin:runtime-diagnosis",
  "inputKinds": ["natural_language", "workload", "alert", "trace"],
  "supportedContextKinds": ["workload", "alert", "trace"],
  "readOnly": true
}
```

后续 Agent 只需注册支持的输入类型、上下文解析器和 Executor，即可复用会话、历史、候选选择、授权、预算、取消和审计基础设施。

## 10. EARS 验收标准

1. 当用户没有选择目标并输入唯一可解析的服务或工作负载时，系统应展示服务端解析的目标和时间范围，并允许开始只读诊断。
2. 当用户输入告警 ID 或 Trace ID 时，系统应在租户和权限校验通过后生成对应诊断上下文；校验失败时不得调用工具。
3. 当解析得到多个候选目标时，系统应展示候选卡片；用户选择前不得创建 run、调用工具或消耗模型预算。
4. 当输入无法解析目标时，系统应提出一个最小澄清问题，不默认扫描租户资源。
5. 当用户打开已有 workload 会话时，系统应恢复原有连续消息、报告和证据元数据。
6. 当用户使用 `explain` 模式时，系统应只使用历史报告和消息，不调用新的诊断工具。
7. 当任意客户端伪造 namespace、PromQL、LogQL、模型、工具或候选资源时，服务端应拒绝并记录审计事件。
8. 当新增第二个官方 Agent 时，系统应复用上下文解析、候选选择、会话和历史协议，不复制一套助手页面和任务仓储。

## 11. 实现顺序

1. 后端新增 `DiagnosticContext`、解析状态、候选和结构化错误，兼容旧 workload 会话。
2. 实现自然语言中的 ID/名称线索解析与 workload resolver。
3. 接入告警 resolver 和 Trace resolver，补租户归属、资源关联和时间窗口测试。
4. 改造 assistant API、OpenAPI、PG JSONB 字段/迁移和审计。
5. 改造 `AssistantConversation.vue`、store、深链上下文和候选卡片。
6. 增加回归测试：旧会话恢复、零目标、单候选、多候选、告警、Trace、跨租户、预算零消耗和第二 Agent 扩展。
7. 通过后再进行真实集群验收；真实模型验收需单独确认诊断证据发送目的地。
