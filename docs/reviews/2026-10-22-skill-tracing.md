# Skill 加载链路证据

实现基线：`console-v2` / `4d05819`。本记录覆盖源码与本地验证；用户随后要求先部署 K8s、测试完成后提交，实际制品与真实客服验收见 [集群部署记录](./2026-10-22-skill-trace-deployment.md)。

## 行为与证据边界

Agent 原有行为保持：每轮从当前租户仓储读取绑定的 Skill，拼接指令，准备知识库及工具后发起模型请求。现在每次实际读取产生一个 `paas.skill.load` INTERNAL 子 Span，与该次 `agent.run` 使用同一 TraceID 和正确父子关系。没有专门的技能加载工具，不虚构 `load_skill` 调用或技能脚本执行。

成功读取只证明本轮的指令上下文准备步骤。Agent 同时记录配置技能数、成功加载数和上下文是否准备完成；技能加载后仍可能因后续 KB/工具依赖失败而终止。缺失、停用、仓储未注入或存储错误分别记录脱敏错误类型，保留原有失败阻断，不继续调用模型。

## 字段契约

| 字段 | 含义 |
|---|---|
| `gen_ai.skill.name` | 实际读取的技能名称；成功加载时记录 |
| `gen_ai.skill.source.uri` | 实际加载的租户私有平台实体，使用 `paas://skills/{转义后的ID}` 标识；不是临时文件路径 |
| `paas.skill.id` | 绑定实体 ID；读取失败时仍可定位目标 |
| `paas.skill.load.mode` / `.status` | `instruction_injection`；`loaded` 或 `failed` |
| `paas.skill.instructions.sha256` | 对本轮实际读取的 `Instructions` UTF-8 原始字节计算 SHA-256；不包含能力标题、元数据或其他 Skill |
| `paas.skill.updated_at` | 仓储返回的副本更新时间，存在时记录 |
| `paas.skill.template.slug` / `.version` / `.sha256` | 仅从受控 `builtin:<slug>@<version>#<hash>` 来源解析；该哈希是完整原模板 Markdown 的哈希，与当前正文哈希计算范围不同 |
| `paas.ai.skills.requested_count` / `.loaded_count` | 该次 Agent 运行配置的技能数及已加载数，保留部分成功信息 |
| `paas.ai.context.ready` | 系统指令、KB、工具定义均准备完成后置 true；不表示供应商调用或回答成功 |
| `error.type` | `skill_store_unavailable`、`skill_not_found`、`skill_disabled`、`skill_store_error` |

这是元数据快照：不存正文、完整 Prompt、Skill 描述、工具参数和结果，不把任意旧 `installedFrom` 文本或含凭证 URI 写入遥测。实际正文修订由哈希标识；模板来源版本不能替代当前副本版本。没有新建不可变正文版本库，旧内容被覆盖后不能仅凭哈希还原。

Agent Span 补充名称，真实工具 Span 补充 `gen_ai.operation.name=execute_tool`，以及模型实际提供的 `gen_ai.tool.call.id`；保留原有 `agent.run`、`provider.chat`、`tool.call` 名称以兼容已有查询。不将此增补宣称为全部 GenAI 约定迁移完成。

参考 2026-10-22 查阅的官方开发中约定：[Agent 与 Skill spans](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-agent-spans.md)、[GenAI 属性](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/model/gen-ai/registry.yaml)。`gen_ai.*` 使用适用的官方属性；`paas.*` 是平台扩展。需固定后续接入版本，不将官方 main 分支视为稳定发布版本。

## 页面

新版可观测链路展开区新增「Skill 加载证据」，展示每次加载的状态和修订，并能选中对应原始 Span。按本条 Trace 的父 Span 解析 Agent 名称和上下文状态，适配同一链路多个 Agent；不从当前 Agent 配置回填历史记录。上下文状态缺失保持未知，原始错误优先于错误的 loaded 标签，旧链路缺证据时明确显示未上报。

沿用既有 Trace API 的 Tags map，没有新增 API、数据库字段、迁移或运行时依赖。Jaeger 查询层已支持这些属性透传；全局采样和后端保留策略继续生效。

## 本地验证

- 主仓全量 `go test ./... -race -count=1`：78 个有测试的包通过；随后新增工具属性断言，单独重跑 Agent race 通过。
- SDK recorder 测试核对实际父子关系、注入正文与哈希一致、编辑后修订变化、模板版本与正文修订分离、错误分类与部分成功、KB 后续失败、正文与底层存储错误脱敏、工具操作和调用 ID。
- 前端类型检查、变更文件 ESLint、21 项 UX 测试及新版控制台生产构建通过。UX 回归覆盖多个 Agent、失败与未知状态、非法哈希、来源版本和内容修订分离。
- 仓库 `make lint` 最终 0 issues。除新增代码规范修复，还补齐先前 Skill 测试遗漏的服务器 Close/仓储 Update 错误检查，并整理一处既有测试导入格式；随后 Agent、Skill、Core race 回归全部通过。
- 浏览器加载实际新增组件，输入真实 Runtime.Run + 本机 Echo + OTel SDK recorder 导出的记录，核对上下文成功、技能加载后 KB 准备失败、旧链路缺证据，并验证选择加载 Span。该验证不涉及真实供应商、线上 Jaeger 或实际商城对话。
- 首次执行受沙箱本机端口限制失败；在允许本机监听的环境重跑通过。pnpm 启动器依赖获取失败后，改用已安装的本地工具完成前端验证。网络检查中 npm 镜像返回 200，GitHub 一次 TLS 握手超时；未把网络错误当成功。

本地实现阶段的日志、生产构建产物、SDK 记录、浏览器夹具与截图位于根目录 `tmp/2026-10-22-skill-tracing/`。随后授权的 K8s 部署已验证实际 OTLP/Jaeger 链路、部署页面和三次真实 PaasShop 客服对话，详见部署记录；未执行 Git push。
