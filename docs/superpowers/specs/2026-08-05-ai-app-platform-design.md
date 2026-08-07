# AI 应用平台能力总纲设计

> 日期：2026-08-05
> 范围：MaaS 推理平台之上叠加的 AI 应用能力全套。确立 AI 应用平台的能力全景、分层架构、八大核心缺口方案、领域模型、路线图与横切原则。
> 性质：**架构基线 spec**（不写代码）。后续每个能力（知识库 / 工具·MCP / 记忆 / Agent / Prompt / Guardrails / 评估 / 工作流）的实施 plan 引用本文件，不重复论述分层与横切原则。第一个实施模块 = 知识库 RAG（P1）。
> 相关：`docs/superpowers/specs/2026-07-26-maas-platform-foundation-design.md`（MaaS 基座）、`docs/superpowers/specs/2026-08-02-p1-real-platform.md`（P1.2 模型管理）、CLAUDE.md「MaaS 端到端闭环」「数据服务资源」「可观测」。

## 背景

当前 AI 服务只做「模型代理」：`Model -> Channel -> Provider` 三层抽象聚合第三方供应商（airouter 聚合百炼/千帆/豆包），对外暴露 OpenAI 兼容推理端点（`/v1/chat/completions` + `/v1/models`），含鉴权 / 计量 / SSE 流式 / 请求级 failover。这是 **MaaS 推理平台**，让用户能"调模型"。

但用户在平台上**构建 AI 应用**还差一层：知识库、工具、记忆、Agent 编排，以及生产所需的检索质量、AI 治理、复杂编排。本 spec 盘点从「推理平台」升级为「AI 应用平台」的完整能力缺口与方案。

## 定位

**AI 应用平台 = 让用户在平台内构建端到端 Agent 应用**：模型代理是底座，之上叠加能力层（KB/工具/记忆/Prompt/多模态）、编排层（Agent/工作流/HITL）、治理层（Guardrails/可观测/评估），全部复用已有 PaaS 基座（多租户 / 计费 / 可观测 / DevOps / 数据服务），延续轻资产路线（不自建模型 / 不自建向量引擎，复用供应商 API + 已有 dataservice）。

适用双模交付：
- **SaaS 公有**：租户在平台建 Agent 应用，按用量计费。
- **私有化**：企业内建知识库 + Agent，数据不出域，对接企业内部 MCP 工具。

## 设计原则

| 原则 | 落地方式 |
|------|----------|
| **轻资产** | 不自建 embedding/向量/文档解析/内容审核模型，全部复用供应商 API + 已有 dataservice（vector/storage） |
| **应用为主线** | KB / Agent / 工作流等资源绑定应用，复用 application bindings 体系 |
| **插件架构** | `internal/ai/` 一个插件涵盖八子领域，经 `pkg/plugin.CoreDeps` 依赖倒置注入，非独立微服务 |
| **不做低代码编排** | Agent + Workflow 全声明式配置（YAML/JSON），API 驱动。延续 [[maas-product-direction]] API-First |
| **控制面/数据面解耦** | 控制面管 Agent 定义/配置（PG），数据面管向量/记忆/批量任务（dataservice/minio/K8s Job），控制面挂了数据面不丢 |
| **多租户隔离** | 所有 AI 资源带 tenant_id，Guardrails/eval/prompt 按租户策略与命名空间隔离 |
| **Apache 2.0** | 文档解析/rerank 等第三方库选 license 兼容（unipdf 等 AGPL 规避，选 Apache/MIT 或供应商 API） |
| **横切继承** | prod:write / 配额 / 审计 / 错误脱敏 / useDangerConfirm 等平台机制自动生效，AI 切片只关注业务逻辑 |

## 能力全景

```
┌──────────────────────────────────────────────────────────────────┐
│  应用层    用户构建的 Agent 应用（端到端 AI 业务）                │
├──────────────────────────────────────────────────────────────────┤
│  编排层    Agent Runtime · 多Agent工作流 · HITL · 定时/事件触发  │ ← 本轮缺口
├──────────────────────────────────────────────────────────────────┤
│  能力层    LLM · 知识库 · 工具/MCP · 记忆 · Prompt管理 · 多模态   │ ← 部分缺口
├──────────────────────────────────────────────────────────────────┤
│  治理层    Guardrails · AI可观测 · 评估评测 · 成本归因            │ ← 本轮缺口
├──────────────────────────────────────────────────────────────────┤
│  基础层    MaaS Provider · 向量库 · 对象存储 · 凭证 · 计量（已就绪）│
└──────────────────────────────────────────────────────────────────┘
```

### 现状盘点

| 能力 | 状态 | 说明 |
|------|------|------|
| LLM 推理 | ✅ 已就绪 | MaaS Provider + OpenAI 兼容 + SSE + reasoning_content + failover |
| 模型管理 | ✅ 已就绪 | Model/Channel/Vendor CRUD + DB 驱动 catalog + gateway 增量刷新 |
| 向量库基础设施 | ✅ 已就绪 | dataservice vector kind（qdrant/milvus），reconciler 落 StatefulSet |
| 对象存储基础设施 | ✅ 已就绪 | dataservice storage kind（minio） |
| Embedding 模型 | ❌ 缺口 | catalog 收敛时 bge-m3 被当演示移除，需补回（airouter 聚合的 embedding） |
| 知识库 RAG | ❌ 缺口 | 本轮 P1 |
| 工具 / MCP | ❌ 缺口 | 本轮 P2 |
| 记忆 | ❌ 缺口 | 本轮 P3 |
| Agent 编排 | ❌ 缺口 | 本轮 P3 |
| Prompt 管理 | ❌ 缺口 | 本轮 P2 |
| 多模态文档解析 | ❌ 缺口 | 本轮 P1（RAG 前置） |
| 检索质量（rerank/混合检索） | ❌ 缺口 | 本轮 P1 |
| Guardrails | ❌ 缺口 | 本轮 P4 |
| AI 可观测（LLM trace） | ⚠️ 部分 | OTel 已有 HTTP span，缺 GenAI 语义 + prompt 日志 + 质量监控 |
| 评估评测 | ❌ 缺口 | 本轮 P4 |
| 多 Agent 工作流 + HITL | ❌ 缺口 | 本轮 P5 |
| 异步/批量/事件触发 | ❌ 缺口 | 本轮 P5 |

## 领域模型

```
Application（已有主线）
  ├─ bindings: KnowledgeBase / Agent / Workflow（新增绑定类型）
  │
KnowledgeBase（知识库）
  ├─ Documents[]（文档：上传/切片/embedding/检索）
  ├─ retriever config（向量+BM25+rerank 策略）
  └─ embedding_model_ref / reranker_ref → MaaS Channel
  │
Tool（工具）
  ├─ type: mcp | http | builtin
  ├─ mcp: server_url + credential_ref → security Secret
  ├─ http: endpoint + method + auth
  └─ builtin: 平台预置（查工作负载/查应用/...）
  │
Memory（记忆）
  ├─ session: 会话级上下文（PG，按 tenant+agent+session）
  └─ long_term: 长期语义（向量 collection，跨会话召回）
  │
Prompt（提示词）
  ├─ versions[]（语义版本，active 标记）
  ├─ template（含 {{var}} 变量）
  └─ ab_experiments[]（流量分流 + 效果指标）
  │
Agent（智能体）
  ├─ model_ref → MaaS Channel
  ├─ system_prompt_ref → Prompt@version
  ├─ kb_refs[] → KnowledgeBase
  ├─ tool_refs[] → Tool
  ├─ memory_policy（会话窗口 / 长期记忆开关）
  ├─ guardrail_ref → GuardrailPolicy
  └─ 暴露为 model ID "agent:{id}" → /v1/chat/completions
  │
GuardrailPolicy（护栏策略）
  ├─ input_rules[]（注入检测 / PII / 越权指令）
  ├─ output_rules[]（PII 脱敏 / 内容安全 / citation 校验）
  └─ tool_rules[]（工具权限校验 / 参数审计）
  │
Workflow（工作流，P5）
  ├─ nodes[]（agent_call / tool_call / condition / parallel / approval / sub_workflow）
  ├─ triggers[]（api / cron / event）
  └─ runs[]（状态持久化，可恢复/可观测）
  │
EvalSuite（评估，P4）
  ├─ test_cases[]（input + expected + assertions）
  ├─ scorers[]（exact / semantic(LLM-as-judge) / rule / tool_correctness）
  └─ runs[]（评测报告 + 版本对比）
```

## 八大核心缺口详述

### 1. 多模态文档解析（RAG 前置，硬缺口）

**现状**：知识库切片前要把 PDF/Word/Excel/PPT/HTML 转结构化文本，当前完全没有。

**方案**：`internal/ai/document` 纯管道--文件传 minio -> 按 MIME 分发解析器：

- **PDF**：优先调供应商文档解析 API（通义/智谱 doc parsing，纯 HTTP）；离线/私有化 fallback 内置库（选 Apache/MIT license，规避 unipdf 等 AGPL）。
- **Office（docx/xlsx/pptx）**：内置纯函数（`unioffice` 等 MIT 库）或供应商 API。
- **HTML/Markdown**：内置纯函数（`golang.org/x/net/html`）。
- **扫描件/图片**：vision 模型 OCR（复用 airouter glm-4v）。
- **输出统一**：`{content, chunks[], metadata{source,page,mime}}`，切片器按 token / 语义边界切（可配 chunk_size / overlap）。

**复用**：dataservice storage（minio 存原文）+ MaaS vision（OCR）。

**API**：
- `POST /api/applications/{id}/knowledgebases/{kbId}/documents`（上传，multipart）
- `GET .../documents/{docId}`（状态：parsing/indexed/failed）
- `GET .../documents/{docId}/chunks`（查看切片，调试用）

### 2. 检索质量三件套（RAG 从能用变好用）

**现状**：KB 只做向量检索，召回质量有限。

**方案**：

- **Reranker**：检索后重排序。复用供应商 rerank API（bge-reranker 等）或 cross-encoder。KB retriever 配置可选 reranker，Tool type=`rerank` 复用 Provider 抽象。
- **混合检索**：向量召回 + BM25 关键词召回。qdrant 已支持 hybrid search；或 PostgreSQL `tsvector` 全文检索作零依赖兜底 -> RRF（Reciprocal Rank Fusion）融合两路结果。
- **查询改写**：LLM 改写用户查询（HyDE 假设文档 / 多查询扩展 / 对话历史指代消解），提升召回。复用 MaaS Provider，KB retriever `query_rewrite` 配置开关。

**复用**：dataservice vector（qdrant hybrid）+ PG tsvector（零依赖兜底）+ MaaS LLM（查询改写）。

**API**：
- `POST /v1/knowledgebases/{id}/retrieve`（query + top_k + 返 chunks+score，供 agent runtime 内部调 + 外部直接用）
- 检索策略在 KB 配置里设，检索 API 透传

### 3. Prompt 管理（AI 应用的"代码"，硬缺口）

**现状**：prompt 散落在 agent 配置里，无版本/复用/A-B。

**方案**：`internal/ai/prompt` 领域--

- **Prompt 模板**：含变量 `{{var}}`，租户内命名空间唯一。
- **版本管理**：语义版本 + active 标记。agent 引用 `prompt_id@version`（或 `@latest`）而非内嵌，改动可回滚。复用 configcenter 的版本/发布模式（DRY 同款：active 单例，旧版本保留）。
- **A/B 实验**：按流量比例分流（如 80% v1 / 20% v2），记录效果指标（token / 反馈 / 完成率）。
- **Prompt 评测**：跑测试集自动评分（与 EvalSuite 联动）。
- **变量校验**：模板定义的变量集 vs agent 传入的变量，缺失报错（防运行时模板渲染失败）。

**API**：
- `GET/POST /api/prompts`（命名空间下 CRUD）
- `POST /api/prompts/{id}/versions`（新版本）
- `PUT /api/prompts/{id}/versions/{v}/activate`
- `POST /api/prompts/{id}/ab-experiments`（开 A/B）
- `POST /api/prompts/{id}/render`（调试：传变量 -> 渲染结果）

**复用**：configcenter 版本模式（DRY）+ eval（评测）+ observability（A/B 指标）。

### 4. AI 专项可观测（LLM trace 是核心）

**现状**：OTel 已有 HTTP span，但缺 GenAI 语义约定（`gen_ai.*` attributes）、prompt/响应日志、质量监控。

**方案**：

- **LLM trace**：按 [OTel GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/) 打 span：
  - `gen_ai.system` / `gen_ai.request.model` / `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens` / `gen_ai.response.finish_reason`
  - 一次 agent 调用形成 trace 树：`agent_invoke` -> {`llm_call`, `tool_call`, `vector_search`, `memory_op`}，Tempo 可查（复用已有 OTel + Tempo 部署）。
- **Prompt 日志**：每次 LLM 调用记 `(prompt, response, tokens, latency, model, finish_reason)` 到 `ai_call_logs`（PG），供调试 + 审计 + 评测采样。脱敏策略与 Guardrails 联动（PII 不入库明文）。
- **质量监控**：工具调用成功率、幻觉率（citation 校验结果）、用户反馈率 -> Prometheus 埋点（`paas_agent_tool_success_total` / `paas_agent_hallucination_total` / `paas_agent_feedback_total`）+ Grafana 面板。
- **Agent 维度成本归因**：billing `IncUsage` 加 `byAgent` 维度（已有 `byApp`），按 agent 归因 token 成本，`GET /api/billing/usage` 返 `usage.byAgent`。

**复用**：observability（OTel/Prom/Tempo/Grafana，已部署）+ billing（计量扩展）+ security（审计）。

### 5. Guardrails 安全护栏（AI 专项安全，硬缺口）

**现状**：有 HTTPS / 鉴权 / 限流 / 错误脱敏，但无 prompt 注入防护 / PII 脱敏 / 内容安全。agent 调工具可能被诱导越权。

**方案**：`internal/ai/guardrail` 中间件链--

- **输入护栏**：
  - Prompt 注入检测（规则匹配"忽略之前指令"等模式 + 模型分类）
  - PII 检测（身份证 / 手机 / 邮箱 / 银行卡，正则 + 命名实体）
  - 越权指令检测（诱导 agent 调非常规工具 / 读他人数据）
- **输出护栏**：
  - PII 脱敏（输出 masking，与 appconfig `SecretMask` 同款 `••••••`）
  - 内容安全（涉政涉黄，接供应商内容审核 API 或规则）
  - Citation 校验（KB 检索结果 vs LLM 输出一致性，防幻觉）
- **工具护栏**：agent 工具调用前校验权限（agent 的 `tool_refs` 是否含该工具）+ 参数审计（记 audit_logs，复用 identityAuditAdapter）。
- **架构**：guardrail 作为 Agent Runtime 的前置/后置中间件，链式执行（`input_rules -> agent -> output_rules`），策略可配置（租户级 / agent 级，覆盖关系：agent > tenant > default）。

**API**：
- `GET/POST /api/guardrails`（策略 CRUD）
- `PUT /api/agents/{id}`（agent 绑定 guardrail_id）

**复用**：security（凭证 / 审计）+ gateway（中间件模式）+ MaaS（内容审核 API / 分类模型）。

### 6. Agent 评估评测（eval，质量保障）

**现状**：无离线/在线质量评测，agent 上线靠肉眼。

**方案**：`internal/ai/eval` 领域--

- **测试集**：`EvalSuite` 含 `test_cases[]`（input + 期望 output + assertions[]）。
- **评分器**：
  - `exact`：精确匹配
  - `semantic`：语义相似（LLM-as-judge，复用 MaaS）
  - `rule`：规则断言（正则 / JSON schema / 工具调用正确性）
  - `tool_correctness`：工具调用序列是否符合预期
- **评测运行**：跑 agent 对全部 case，记录输出 + 评分 -> 评测报告（通过率 / 平均分 / 分维度）。
- **版本对比**：prompt / agent 配置改动后回归，对比两版报告（防回归）。
- **在线评估**：生产采样 + LLM-as-judge 打分 -> 趋势监控（接 AI 可观测）。

**API**：
- `GET/POST /api/eval/suites`（测试集 CRUD）
- `POST /api/eval/suites/{id}/runs`（触发评测）
- `GET /api/eval/runs/{id}`（报告）

**复用**：MaaS（LLM-as-judge）+ observability（报告存储 + 趋势）+ Prompt（版本对比）。

### 7. 多 Agent 工作流 + Human-in-the-loop（P5）

**现状**：单 agent ReAct，复杂业务（"审批-执行-复核"）无法表达。

**方案**：`internal/ai/workflow` 声明式状态机--

- **节点类型**：
  - `agent_call`：调 agent
  - `tool_call`：直接调工具
  - `condition`：条件分支
  - `parallel`：并行执行
  - `approval`：**人工审批节点（HITL）**--执行到此处暂停，等人工 approve/reject
  - `sub_workflow`：子工作流（复用）
- **状态持久化**：PG `ai_workflow_runs`，可恢复 / 可观测（节点执行 trace）。
- **触发**：API / 定时（复用 CronJob 模式）/ 事件（webhook + 治理事件总线）。
- **HITL 流程**：执行到审批节点暂停 -> 通知（复用 observability 通知通道，留后续）/ 记 `pending_approval` 状态 -> 人工 `POST /api/workflows/runs/{id}/approve` -> 恢复执行。
- **设计取舍**：不做可视化拖拽（延续 API-First），YAML/JSON 定义工作流。`workflow` 独立实体（与单 Agent 区分，YAGNI 边界清晰）。

**API**：
- `GET/POST /api/workflows`
- `POST /api/workflows/{id}/run`（触发）
- `GET /api/workflows/runs/{id}`（状态 + 节点执行）
- `POST /api/workflows/runs/{id}/approve`（HITL 审批）

**复用**：devops（异步状态机模式）+ governance（webhook/事件）+ observability（trace）。

### 8. 异步/批量推理 + 事件触发 Agent（P5，生产场景）

**现状**：只有同步流式 `/v1/chat/completions`，长任务 / 批量无法处理。

**方案**：

- **异步推理**：`POST /v1/agents/{id}/invoke?async=true` 返 taskID -> 轮询 `GET /v1/ai/tasks/{id}` 或 webhook 回调。复用 devops 异步 Job 模式（goroutine + PG 状态机 + baseCtx 进程退出 cancel）。
- **批量推理**：`POST /v1/batch/completions`（CSV/JSONL 输入 -> 批量调 LLM -> 结果写 minio -> 通知）。复用 devops BuildRun 的 K8s Job 模式跑批量（DooD，与 builder 同款）。
- **事件触发**：Agent 订阅事件（治理 webhook / 计费阈值 / 定时 cron）-> 自动调起。复用 governance 事件 + CronJob。

**API**：
- `POST /v1/agents/{id}/invoke?async=true` + `GET /v1/ai/tasks/{id}`
- `POST /v1/batch/completions`（提交批量任务）
- `POST /api/agents/{id}/subscriptions`（事件订阅）

**复用**：devops（异步 Job / 批量 K8s Job）+ governance（webhook / 事件）+ dataservice storage（批量结果）。

## 辅助能力（第二梯队）

| 能力 | 方案 | 复用 | 阶段 |
|------|------|------|------|
| **语音多模态** | ASR/TTS 接供应商（通义/豆包），agent 语音入口；`/v1/audio/transcriptions` + `/v1/audio/speech` | MaaS Provider | P6 |
| **Feedback 飞轮** | 用户点赞/点踩 -> `ai_feedback` 表 -> 喂给 eval + 训练数据导出 | security(审计) + eval | P6 |
| **多语言 SDK** | `sdk/paas-ai-{go,py,ts}`，封装 agent invoke / RAG / 工具调用 | 复用 sdk/paas-registry 独立 module 模式 | P6 |
| **智能模型路由** | 按 成本/质量/延迟 动态选模型（已有 failover，加 weighted routing） | gateway 路由层 | P6 |
| **图像生成** | airouter 万相已支持，补到 catalog + `POST /v1/images/generations` | MaaS | P6 |
| **平台 MCP Server** | 平台 API（应用/工作负载/数据服务/计费）暴露为 MCP server，供外部 AI 客户端（Claude Desktop/IDE）调用 | MCP 协议 + gateway | P6 |

## Agent 运行时数据流

```
client POST /v1/chat/completions {model:"agent:{id}", messages:[...]}
  -> gateway 鉴权(Bearer/cookie/APIKey) + 计量锚点
  -> Guardrails 输入护栏(注入/PII/越权检测)
  -> AgentRuntime.Run(ctx, agentID, messages):
       ├─ Memory: 取会话上下文(短期) + 长期记忆召回(向量)
       ├─ Prompt: 加载 prompt_id@version 模板 + 变量填充
       ├─ loop ReAct/FunctionCalling:
       │    LLM(MaaS Provider, OTel gen_ai span, prompt 日志)
       │      -> function_call?
       │         yes -> 工具护栏(权限校验 + 参数审计)
       │              -> Tool.Execute(MCP/HTTP/builtin, OTel span)
       │              -> 结果回灌 LLM
       │         no  -> KnowledgeBase.retrieve(向量+BM25+Rerank, OTel span) 增强
       │              -> break
       ├─ 流式返回(含 reasoning_content, 复用 SSE handler)
       ├─ Memory: 存会话 + 提取长期事实向量化入向量库
       ├─ Guardrails 输出护栏(PII脱敏/内容安全/citation校验)
       └─ AI可观测: prompt日志 + token计量(byAgent) + 质量埋点
  -> gateway -> client(SSE chunk)
```

**关键设计**：Agent 注册为 model ID `agent:{agentID}`，复用现有 gateway 的鉴权 / 计量 / SSE / failover，客户端 `model:"agent:app-support"` 即调起 agent，对外就是「一个更聪明的模型」--零新端点、零客户端改动。

## 数据库 schema 草案

PG migration 增量（`internal/storage/pg/migrations/00XX_ai_*.up.sql`），全部带 `tenant_id` 多租户隔离：

```sql
-- 知识库
CREATE TABLE ai_knowledgebases (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, app_id TEXT,
  name TEXT NOT NULL, embedding_channel_id TEXT, reranker_channel_id TEXT,
  retriever_config JSONB,  -- {hybrid:true, top_k:10, query_rewrite:true}
  created_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(tenant_id, name)
);
CREATE TABLE ai_documents (
  id TEXT PRIMARY KEY, kb_id TEXT NOT NULL REFERENCES ai_knowledgebases ON DELETE CASCADE,
  name TEXT, mime TEXT, status TEXT,  -- parsing/indexed/failed
  object_key TEXT,  -- minio key
  chunk_count INT, metadata JSONB, created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 工具
CREATE TABLE ai_tools (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL,
  name TEXT, type TEXT,  -- mcp/http/builtin
  config JSONB,  -- {server_url, credential_ref} / {endpoint,method,auth} / {builtin_id}
  UNIQUE(tenant_id, name)
);

-- 记忆（会话级；长期语义走向量库 collection，不入此表）
CREATE TABLE ai_memory_sessions (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, agent_id TEXT, session_id TEXT,
  messages JSONB,  -- [{role,content,...}]
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Prompt
CREATE TABLE ai_prompts (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT, namespace TEXT,
  UNIQUE(tenant_id, namespace, name)
);
CREATE TABLE ai_prompt_versions (
  id TEXT PRIMARY KEY, prompt_id TEXT NOT NULL REFERENCES ai_prompts ON DELETE CASCADE,
  version TEXT, template TEXT, is_active BOOLEAN DEFAULT FALSE,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(prompt_id, version)
);
CREATE TABLE ai_prompt_ab_experiments (
  id TEXT PRIMARY KEY, prompt_id TEXT NOT NULL, variants JSONB,  -- [{version, weight}]
  status TEXT, metrics JSONB, created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Agent
CREATE TABLE ai_agents (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, app_id TEXT,
  name TEXT, model_channel_id TEXT, system_prompt_id TEXT, system_prompt_version TEXT,
  kb_ids TEXT[], tool_ids TEXT[], memory_policy JSONB, guardrail_id TEXT,
  UNIQUE(tenant_id, name)
);

-- Guardrails
CREATE TABLE ai_guardrails (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT,
  input_rules JSONB, output_rules JSONB, tool_rules JSONB,
  UNIQUE(tenant_id, name)
);

-- Prompt 日志（AI 可观测）
CREATE TABLE ai_call_logs (
  id BIGSERIAL PRIMARY KEY, tenant_id TEXT NOT NULL, agent_id TEXT, session_id TEXT,
  model TEXT, prompt TEXT, response TEXT,  -- 脱敏后
  input_tokens INT, output_tokens INT, latency_ms INT, finish_reason TEXT,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX ON ai_call_logs (tenant_id, agent_id, created_at DESC);

-- 工作流（P5）
CREATE TABLE ai_workflows (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT, definition JSONB,
  UNIQUE(tenant_id, name)
);
CREATE TABLE ai_workflow_runs (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, workflow_id TEXT,
  status TEXT,  -- running/paused/approval_pending/succeeded/failed
  current_node TEXT, context JSONB, started_at TIMESTAMPTZ, ended_at TIMESTAMPTZ
);

-- 评估（P4）
CREATE TABLE ai_eval_suites (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT,
  test_cases JSONB, scorers JSONB
);
CREATE TABLE ai_eval_runs (
  id TEXT PRIMARY KEY, suite_id TEXT NOT NULL, target_type TEXT, target_id TEXT,  -- agent/prompt
  status TEXT, report JSONB,  -- {pass_rate, avg_score, per_dim}
  created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Feedback（P6）
CREATE TABLE ai_feedback (
  id BIGSERIAL PRIMARY KEY, tenant_id TEXT NOT NULL, agent_id TEXT, session_id TEXT,
  message_id TEXT, rating TEXT,  -- up/down
  comment TEXT, created_at TIMESTAMPTZ DEFAULT NOW()
);
```

**横切正确性**：
- 全表带 `tenant_id`，Repository 强制按 ctx 租户过滤（与现有 11 模块一致）。
- `Create` 以 ctx 租户为准忽略请求体（防越权写）。
- 跨租户访问统一 not found 不泄漏。
- KB / Agent 绑定应用（`app_id`），复用 application bindings 体系（新增 binding type）。
- `ai_call_logs.prompt/response` 入库前经 Guardrails 脱敏（PII 不入明文）。
- billing `IncUsage` 扩展 `byAgent` 维度（与 `byApp` 同款 JSONB）。

## 路线图

| 阶段 | 内容 | 核心交付 | 前置 |
|------|------|----------|------|
| **P1 知识库基座** | KB 领域 + 文档解析(#1) + embedding 补 catalog + 混合检索+rerank(#2) | RAG 独立可用，`/v1/knowledgebases/{id}/retrieve` | - |
| **P2 工具·MCP + Prompt管理(#3)** | Tool 领域 + MCP client/runtime + Prompt 版本/A-B + agent 引用 prompt | agent 有工具和"代码" | P1 |
| **P3 记忆 + Agent Runtime** | Memory(会话+长期) + 单 Agent + ReAct 循环 + 暴露 `agent:{id}` 虚拟模型 + **trace span + guardrail 中间件位预埋** | 端到端单 agent | P1+P2 |
| **P4 AI 治理** | Guardrails(#5) 完整规则 + AI可观测(#4) prompt日志/质量监控 + 评估(#6) | 生产可用、可观测、安全 | P3 |
| **P5 生产编排** | 多Agent工作流+HITL(#7) + 异步/批量/事件触发(#8) | 复杂业务场景 | P4 |
| **P6 生态** | 平台 MCP Server + SDK + Feedback 飞轮 + 语音/图像生成 | 平台化扩展 | P5 |

### 关键时序主张

- **P1 把 #1 文档解析 + #2 检索质量与 KB 一起做**：避免 KB 先做向量检索、后期补 rerank/混合检索返工（retriever 接口要一次设计到位）。
- **P4 的 Guardrails + AI 可观测必须与 P3 Agent Runtime 同期前置设计**：trace span 位（gen_ai attributes）和 guardrail 中间件位要在 Runtime 落地时就埋好，后补成本高（要重走所有 LLM 调用点）。P3 预埋骨架，P4 填规则。
- **P5 工作流独立实体**：与单 Agent 区分，避免单 Agent 膨胀成编排器（YAGNI 边界清晰）。

## 与项目约束契合

| 约束 | 落地 |
|------|------|
| 轻资产（不自建 vLLM） | #1 文档解析 / #2 rerank / #5 内容审核 / #8 语音全部复用供应商 API，不自建模型 |
| 插件架构 | `internal/ai/` 一个插件八子领域，`CoreDeps` 加 `AgentRuntime()`/`VectorStore()`/`Embedder()` 注入点（依赖倒置） |
| 控制面/数据面解耦 | Agent 定义/配置（PG）是控制面；向量/记忆/批量任务（dataservice/minio/K8s Job）是数据面，控制面挂了数据面不丢 |
| 多租户隔离 | 全表带 tenant_id，Repository 强制过滤；Guardrails/eval/prompt 按租户命名空间隔离 |
| 不做低代码编排 | Agent + Workflow 全声明式 YAML/JSON，API 驱动 |
| Apache 2.0 | 文档解析/rerank 库选 license 兼容（unipdf 等 AGPL 规避） |
| 横切继承 | prod:write / 配额回收 / 审计 / 错误脱敏 / useDangerConfirm 自动生效 |

## 不做项（YAGNI）

- **可视化编排器**：不做拖拽式工作流编辑器（延续 API-First，[[maas-product-direction]]）。
- **自建 embedding/向量/文档解析模型**：复用供应商 + 已有 dataservice。
- **模型微调/训练**：轻资产路线，不做训练能力（如需走供应商托管微调 API 留后续）。
- **复杂记忆管理**：不做遗忘曲线 / 优先级衰减等复杂策略（先存取，后优化）。
- **AGPL 依赖**：unipdf 等 AGPL 文档库规避，选 Apache/MIT 或供应商 API。
- **实时流式 Agent 间通信**：P5 工作流用 PG 状态机 + 轮询，不上 actor 模型 / 消息队列（YAGNI）。

## 留后续

- 多模态：视频理解 / 实时语音对话（流式 ASR+TTS）。
- 记忆：遗忘策略 / 优先级衰减 / 跨 agent 共享记忆。
- 评估：在线 A/B 自动流量提升 / 主动学习采样。
- 工作流：可视化调试 / 子工作流版本管理 / 长流程补偿事务。
- MCP：平台 MCP Server 资源权限细粒度化 / 多租户 MCP server 隔离。
- Feedback 飞轮：自动转训练数据 / 与 eval 闭环。
- 智能路由：基于质量反馈的自适应模型选择。

---

**核心主张**：上一轮的 KB/MCP/Memory/Agent 是"能跑起来"，本 spec 的 #1/#2/#3/#4/#5 是"能用得好、敢上生产"，#6/#7/#8 是"能撑复杂业务"。P1 先做 KB 基座（含文档解析+检索质量，避免返工），P3 Agent 落地时同步预埋 P4 的 trace+guardrail 骨架。
