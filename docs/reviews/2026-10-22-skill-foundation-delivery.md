# Skill 模板、演示与对外技能交付

实现基线：`console-v2` / `4200415`。本次为 [AI 审计](./2026-10-22-ai-services-skill-audit.md) 后的首批实现，源码提交为 `45453ca`；用户随后授权提交部署，实际结果见 [部署记录](./2026-10-22-skill-deployment.md)。

## 已交付

| 范围 | 内容 | 入口或真源 |
|---|---|---|
| 平台指令模板 | 知识库问答、结构化结果、故障证据整理 | `skills/` 中的标准 `SKILL.md`；Go embed 随二进制提供只读目录 |
| 平台对外技能 | OnePaaS 接入、应用交付、运行排障 | 同一目录中的三个 `onepaas-*` 包，供有 HTTP 能力的外部 Agent 使用 |
| PaasShop 场景 | 商品导购、订单查询，含退款请求不得直接执行的检查用例 | `examples/paas-shop/skills/` 与 `scenarios.json` |
| 新版控制台 | 我的技能、平台模板与对外技能、说明与示例、复制、下载、绑定 Agent 后前往 Playground | `/next/ai/skills`；不自动发起模型推理 |
| 元数据安装 | 结构化 HTTP 请求创建 Skill/工具/Agent，两个 setup 入口共用 | `examples/paas-shop/install_ai.py`，重跑不覆盖 Skill 指令，不重复创建同名资源 |
| 基础正确性 | memory ID 碰撞拒绝；显式禁用状态在 memory/PG 保留；删除/停用的服务端引用预检 | Skill handler/repository；引用查询失败时 503，已知引用时 409 |

模板从标准源文件提取正文；名称、版本、运行方式、许可与内容 SHA-256 随目录展示。平台指令模板安装为租户副本，使用租户与 slug 派生的稳定 ID；并发安装、重复安装及模板上游更新均不覆盖用户编辑。自建请求不能伪造 `installedFrom`，修改已有条目保留来源。外部技能不可作为平台内指令安装。

`.dockerignore` 保留正式 Skill 源文件；目录由 embed 打包，无需在线访问第三方技能市场。源码实现阶段完成本地编译；后续容器构建与集群验证见部署记录，未制作 airsync 离线交付包。

## API 与类型

- `GET /api/skills/templates`：只读目录，保留既有鉴权。
- `POST /api/skills/templates/{slug}/install`：复制平台指令模板到当前租户。
- `GET /api/skills/templates/{slug}/download`：标准 Markdown 文件，OpenAPI 明确 `text/markdown` 原始字符串响应，不套 CRUD 包裹。
- `GET /api/skills/{id}/references`：当前租户引用它的 Agent 摘要。

`pnpm gen:skills [本地 openapi.json 路径]` 从同一 route registry 契约提取 Skill 路径及 schema 引用闭包，生成 `src/api/skills.gen.ts`。默认读取本机 `http://localhost:8080/openapi.json`。新模板页面消费生成类型，不将存量全量类型的历史差异混入本次变更。

## 演示运行

操作者准备 `PAAS_BASE`、`PAAS_TOKEN`、`PAAS_SHOP_MCP_URL` 后，从仓库根目录运行：

```bash
python3 examples/paas-shop/install_ai.py
```

安装器只创建元数据，不部署服务、不调用模型、不执行退款。它管理演示 `shop-agent` 的工具列表为 `search_products`、`query_product`、`query_order`，并追加两个 Skill；保留已有模型、Prompt、KB 和 Skill 指令，停用的依赖或 Agent 不会自动重新启用。已有工具绑定其他服务时阻断，接口错误和重定向不会当作空列表继续创建。部分安装失败保留已创建资源，修复后按名称重跑；没有把该跨资源安装宣称为事务。

完整 setup 脚本仍保留原有基础设施/负载/流量发生器行为，因此仅验证 Skill 时优先单独运行安装器。历史退款工作流改为使用同一只读 `shop-agent` 分析；只有精确匹配的 `refund_order` 工具才会加入流程，缺失时跳过，不能用首个 MCP 方法替代。

## 本次验证

- 主仓 `make test`：78 个有测试的包通过 race 检查；后续 Markdown 契约和权限分支调整分别重跑 `cmd/core`、`internal/apiroute` 及 Skill 包，均通过。
- Skill PostgreSQL 集成：本任务新建、仅本机可达的临时 PostgreSQL，使用独立 schema；真实执行完整迁移后验证禁用保留、主键碰撞、状态更新和租户隔离，通过并清理 schema。
- Python 安装器：5 个本机 fake HTTP 回归，覆盖重复安装、用户编辑保留、只读工具绑定、失败/重定向阻断和停用状态。
- 安装器连接本任务的真实 Skill/Tool/Agent handler，连续运行两次输出相同资源 ID；不连接真实 MCP。
- 8 个标准技能包通过 `skill-creator` 格式验证；未引入新运行时依赖。
- 前端类型检查、构建、变更文件 ESLint 和 19 项 UX 回归通过；脚本 `bash -n`、Go 格式和 diff 检查通过。
- 浏览器加载实际 `Skills.vue`，连接隔离的内存 handler：目录显示六项并区分运行方式；复制结构化模板后看到租户副本；绑定已有 Agent 后跳转带 `agent:{id}` 参数的试用入口；两项演示 Skill 显示引用 `shop-agent`；引用中删除被阻断。验证页的 Playground 为跳转验证占位，不代表完整控制台或真实推理验收。

临时页面、截图、日志、缓存和验证程序在根目录 `tmp/2026-10-22-skill-delivery/`，不纳入 Git。本任务本地预览服务已停止，临时 PostgreSQL 容器已删除。

## 剩余边界

- 平台只支持指令正文注入，不提供任意 Skill ZIP/Git 导入、脚本执行、按需文件读取或自动技能选择；本次标准文件下载不等于完整技能执行沙箱。
- 引用保护是服务端预检，不是 Agent 与 Skill 跨 Store 的事务/FK 约束；新增绑定与删除并发的强一致性仍需后续设计。
- 只读客服移除直接退款工具，不代表业务端已具备不可绕过的审批；直接 MCP/Tool 调用权限、审批上下文、业务幂等及已有工作流内容仍需单独治理和验收。
- 未修改模型通道 failover、广场整包安装事务、KB 落点与维度、评估配置快照、全局执行记录；这些继续沿用审计待办。
- 源码实现阶段未进行真实供应商/SDK 推理、集群安装或线上业务验收；后续集群部署的实际范围见部署记录。商品/订单场景仍需在真实测试数据上确认工具调用与答案事实；安装器输出 `inferenceVerified=false`。
