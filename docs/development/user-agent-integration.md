# 用户端官方 Agent 接入约定

用户端 Agent 通过顶栏 AI 助手使用；`/ai` 的租户自建 Agent 配置保持独立。首个官方 Agent 为智能诊断。

## 用户看到的统一模型

- **会话**：固定 Agent、资源目标和所属用户，维护连续问答。
- **消息**：`Message{id,sessionId,runId,role,text,status,resultKind,run}`；稳定 ID 支持刷新恢复、增量合并和历史分页。
- **执行**：每轮一个 run，后台负责授权、幂等、并发、取消、超时、租约和原子终态。
- **结果**：自然语言 `Reply` 加可选版本化 `Artifact`；未知结果类型至少能显示自然语言，不把新结果强制解析为诊断报告。

消息从已持久化的 run.question/reply/report 投影，旧诊断任务可以直接恢复为问答；不在前端维护第二份永久记录，也不双写两份消息真源。保留策略与模型上下文长度互不绑定。

## 注册与执行

在 `cmd/core/assistant.go` 装配、启动 Worker 之前，通过 `svc.RegisterAgent` 注册官方目录描述与执行器。注册表是服务端可信代码，不允许租户通过 Prompt、工具配置或 Agent CRUD 覆盖。

```go
err := svc.RegisterAgent(assistant.AgentDesc{
    Key: "builtin:release-analysis",
    Name: "发布分析",
    Version: "1",
    Description: "围绕已授权工作负载分析发布影响",
    OutputKind: "release_analysis",
    TargetType: "workload",
    ReadOnly: true,
}, assistant.ExecutorFunc(func(ctx context.Context, in assistant.RunInput,
    emit func(tool string, start bool)) (assistant.ExecuteResult, error) {
    // 业务适配器必须限定在 in.Session.Target 内。
    // 需要模型时使用 in.Invoke(ctx, preset, messages, scopedTools)：
    // 每轮权限、配额、预算准入、用量与进度已接线；nil tools 表示纯对话。
    // 不另建绕过配额的模型客户端，不接受客户端系统消息或工具定义。
    return assistant.ExecuteResult{
        Reply: "对当前授权目标的自然语言回答",
        Artifact: json.RawMessage(`{"kind":"release_analysis","version":"1"}`),
    }, nil
}))
```

上面仅说明接入契约，不会自动启用发布分析能力。执行器须自行提供对应业务证据、结果校验和有意义的回归测试；不要注册尚不可用的占位功能。

首期目标适配器为 workload/Deployment。新增数据服务、任务或其他资源的 Agent 时，需先补对应的后端目标授权解析和前端目标选择适配，不能把资源 ID 当作 namespace/URL/查询语句直接透传。

## 消息和模型上下文

- UI 按完整会话显示全部已保存轮次，按轮次分页加载更早问答。
- 模型仅装入最近若干轮有界问答；历史数据必须标明为历史，不能冒充最新检查。
- 智能诊断的「讨论已有证据」不重新调工具；「检查最新状态」固定当前轮窗口并重新取证。
- 调用 `in.Invoke` 时模型来自平台配置，工具集按次注入，不修改共享 Runtime。
- 需要不同模型/权限或目标类型时先扩展可信装配与准入契约，不能由用户输入覆盖。

## 结构化结果适配

在 `AssistantConversation.vue` 根据 `resultKind` 选择受控结果组件。组件只负责展示，不能执行结果中任意命令、HTML 或外链。诊断结果示例为 `kind=diagnosis_report, version=1`；自然语言始终作为消息正文保留。

建议动作链接到既有平台资源页面，交由原有业务接口重新鉴权。只读 Agent 不因输出建议自动获得写权限。

前端公共会话类型从真实路由 OpenAPI 生成到 `src/api/assistant.gen.ts`；`api/assistant.ts` 对状态枚举、事件 payload 和诊断结果做展示侧收窄。新增字段先改 Go DTO/route registry，再生成类型。

## 配置与运行边界

- `PAAS_DIAGNOSE_MODEL`：诊断预设模型。
- `PAAS_ASSISTANT_HISTORY_DAYS`：会话保留天数，默认 30。
- `PAAS_ASSISTANT_EVIDENCE_DAYS`：原始证据片段保留天数，默认 7。
- `GET /api/assistant/settings`：向用户说明持久化和保留策略；内存模式明确提示重启会丢失。
- 生产持久化依赖现有 PostgreSQL，新增迁移 `0061_assistant_conversation`；不修改已经存在的 `0060` 历史迁移。

模型 usage 缺失时标记估算；沿用平台金额预算的估价口径，不把估算当供应商账单。强 token 配额逐轮预留后结算，失败/取消同样记录已知消耗；异常进程退出期间无法获得的供应商实际用量仍需要平台统一对账，不能宣称费用精确或 exactly-once。

## 新 Agent 必须验证

1. 三轮问答、关闭/刷新/重新登录后恢复，历史分页及稳定消息 ID。
2. 同租户不同用户、跨租户、目标替换、授权撤销与敏感内容隔离。
3. 取消、超时、进程失联、重复请求、并发准入和迟到 Worker fencing。
4. 数据源无数据、未配置、不可达及模型坏输出，不能推断为健康。
5. 模型/工具预算、失败用量、结构化结果校验与只读边界。
6. 真实数据源与真实模型的独立验收；模拟响应、构建通过和部署成功分别记录。
