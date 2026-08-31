# 智能体工作流编排（Workflow）设计

日期：2026-08-31 · 状态：待批准 · 前置讨论：模块评估（6.5/10，编排为主失分项）+ 插件化公证（逻辑边界已够，不上 Plugin 契约）

## 问题

智能体模块七实体（Agent/Prompt/Skill/Tool/KB/Eval/Marketplace）齐备，但只能**单 Agent 循环**——多步业务流程（查→判断→人工审→生成→投递）无法在平台表达，用户只能硬编码进应用。对标 Dify Workflow / Coze 画布，编排是「Agent 管理平台 → Agent 应用开发平台」的分水岭。

## 决策

- **D1 首刀不做画布**：表单化节点列表（同 PipelineDesigner 模式），DAG/并行留后续。线性 + 条件分支已覆盖 80% 场景。
- **D2 复用模式而非代码**：借鉴 pipeline 引擎已验证的「定义/运行分离 + 异步状态机 + 输出链 + 暂停恢复」，但独立实现于 `internal/ai/workflow`（领域差异大：LLM 节点流式/变量传递语义 vs CI stage）。
- **D3 LLM 节点复用 agent runtime**：workflow 不自建推理循环，LLM 节点绑定一个 Agent 执行（带工具/护栏/记忆全套能力）；纯文本提示节点用轻量 echo（不留）——首刀 LLM 节点=绑定 Agent。
- **D4 人工节点复用 approve 模式**：暂停等确认，POST /workflows/{id}/runs/{rid}/nodes/{idx}/approve 恢复。
- **D5 触发**：手动（UI）+ API（POST，供应用调用）。webhook/cron 留后续。

## 数据模型

```
WorkflowDef    { ID/TenantID/Name(租户内唯一)/Desc/Nodes[]/Enabled }
  NodeDef      { ID(Def 内唯一)/Type/Name/Config/NextID/Branches[] }
  NodeType: start | llm(AgentID, InputTemplate) | tool(ToolID, ArgsTemplate)
          | condition(Expr: var op value; Branches: [{When, NextID}], Else)
          | approve(Message) | end
WorkflowRun    { ID/TenantID/WorkflowID/Status/Inputs/NodeRuns[]/CreatedAt }
  NodeRun      { NodeID/Status/Output/StartedAt/FinishedAt/Error }
  Status: running | paused | succeeded | failed | aborted
```

**变量传递**：节点配置用 `{{inputs.x}}`（流程输入）与 `{{nodes.<nodeID>.output}}`（上游输出）模板，执行前渲染（复用 configcenter 占位符解析思路，简单 strings 替换 + 未定义报错）。LLM 节点 Output=最终文本；tool 节点 Output=JSON 结果字符串；condition 读变量决定分支。

**migration 0040**：`ai_workflows`（nodes JSONB）+ `ai_workflow_runs`（inputs/node_runs JSONB），tenant_id 隔离 + RLS 同款。

## 执行引擎（`internal/ai/workflow/engine.go`）

- `Engine.Start(def, inputs) → Run`，goroutine 推进：当前节点执行 → 写 NodeRun → 按 NextID/Branches 推进。
- **llm**：经依赖倒置接口 `AgentRunner.Run(ctx, agentID, prompt) (string, error)`（cmd/core 桥接 agent.Runtime，Run 收集 chunks 拼接）；错误→run failed（不自动重试，首刀）。
- **tool**：经 `ToolInvoker` 接口桥接 tool.Repository 现有 HTTP/MCP 调用路径。
- **condition**：纯函数比较（== / != / contains），无表达式引擎（YAGNI）。
- **approve**：置 paused + 等端点恢复（单实例内存信号 + PG 轮询兜底，与 pipeline approve 同款取舍）。
- **暂停恢复/中止**：Approve 端点 + Abort 端点；abort 清 running 节点（借鉴 pipeline StageAborted 教训）。
- 运行上限：节点数 ≤ 50、单节点超时 120s（防失控循环）。

## REST（`/api/workflows`，agent:read/write 权限复用）

- CRUD：`GET/POST /api/workflows`、`GET/PUT/DELETE /{id}`
- `POST /{id}/runs`（body=inputs，同步返回 run 记录，异步执行）
- `GET /{id}/runs`（历史）+ `GET /runs/{rid}`（详情含 NodeRuns）
- `POST /runs/{rid}/approve?node=`（恢复）+ `POST /runs/{rid}/abort`
- OpenAPI 登记；审计 workflow_create/delete/run/approve/abort。

## 前端（console-user）

- `views/Workflow.vue`：列表 + 表单化编辑器（节点卡片纵向列表：类型 select + Agent/Tool 下拉 + 条件表单 + 上下连线选择 NextID），运行历史抽屉。
- `WorkflowRunView.vue`：节点时间线（复用 PipelineRunView 视觉模式）+ 输入/输出查看 + approve/abort 按钮。
- 菜单：AI 服务组加「工作流」。
- Agent 下拉数据源复用 `/api/agents`；工具复用 `/api/tools`。

## 明确不做（YAGNI 边界）

画布拖拽、DAG 并行、循环节点、子流程、webhook/cron 触发、节点重试、变量类型系统（全 string）、运行分页、市场发布工作流。

## 验收

1. e2e：建「客服工单分流」工作流（llm 分类 → condition 分流 → llm 生成回复 → 人工确认 → end），手动触发全链路走通；approve 暂停/恢复；abort 清理；条件两分支各验证一次。
2. dogfooding：paas-shop 一个多步场景迁到 workflow（可选，验收后评估）。
3. `go test ./...` + 三套前端 build + k8s 部署。
