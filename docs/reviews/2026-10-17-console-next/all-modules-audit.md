# 新版控制台全模块专项检查

检查日期：2026-10-17。对象：`frontend/console-user-v2`，base `/next/`。

## 结论与证据边界

新版的任务域导航可以保留。产品承载服务上下文、交付中心承载跨产品过程、工作负载承载实例运维，这一分工比重新堆叠资源菜单更清楚。本轮优先完善 AI 开发、模型试用、配置变更反馈、数据服务运维、凭证及用量语义；已有运行、交付、泳道专项转入增量验证。

这是**全入口覆盖、关键流程源码抽查与设计评审**，不是每个按钮均已实测的验收报告。快照为分支 `console-v2`、HEAD `97790a1` 加当前大量未提交修改；其他 agent 的运行/交付/泳道实现已经计入。不能用该 HEAD 单独复现全部检查对象。

本轮浏览器库存中的本地预览显示“无法访问此站点”，本地只读 HTTP 探测返回 `HTTP=000`（连接失败）。访问集群标签页的工具请求超时，未取得当前页面 DOM。没有执行登录、创建、运行、支付、删除、发布或集群写入。此限制不能推导为线上业务故障，也不能据本轮源码评审声称视觉、权限和运行链路已验收。

证据分类：**S**=本轮源码确认；**D**=基于源码提出的设计；**V**=仍需页面/运行环境验证。本报告发现均为 S 或 S→D；视觉建议不是当前截图测量结果。旧版仅作为此前功能迁移与任务归属的参照，本轮未重新逐页实测旧版。

## 覆盖矩阵

一级页面来自 `src/navigation/index.ts` 加首页，详情页来自 `src/router.ts`。下面路径均省略 `/next`；AI 子模块单列，避免把“有 AI 开发入口”视作全部检查完成。

| 模块 / 页面 | 当前已具备（S） | 判断与完善重点（D / V） | 专项 |
|---|---|---|---|
| 首页 `/` | 产品入口、事项、快捷开始、分区错误态 | 保留；事项需区分通知事件与尚未处理的任务，保留告警实体定位 | 共性、04 |
| 产品 `/products`、详情 | 服务运行、交付、共享资源、配置、成员权限；创建与绑定 | 保留主框架；绑定影响预览、首次使用引导、成员读取失败不能显示无成员 | 04、06 |
| 交付中心 `/delivery` | 待处理、运行、变更、批次；产品内仓库/PR、流水线、构建镜像、发布 | 已有专项；新增计划 hash、产物锁定、多视图、详情实现应做状态/深链/权限验收 | 已有交付专项 |
| 工作负载 `/workloads`、运行详情 | service/job/cronjob 筛选；概览/实例/日志/操作记录；终端与重启新增代码 | 已有专项；按形态给操作，核验真实能力与异步结果，不再列为“只有占位按钮” | 已有运行专项 |
| 模型目录 `/models` | 搜索、能力/供应商过滤、价格、通道、分页 | 能力与试玩方式未匹配；静态通道状态与当前调用资格需分开 | 03 |
| Playground `/playground` | 对话、流式、推理内容、停止、参数、模型/Agent 选择 | 错误进入后续对话上下文；字符数标为 tokens；非 chat 模型不能盲发 chat | 03 |
| AI 开发 `/ai/:section?` | 8 子模块、异步组件、旧 query 别名 | 保留顶层入口；子模块按资产/执行任务收敛；未知 section 不能静默展示智能体 | 01、02 |
| 数据服务 `/dataservices`、详情 | 6 类资源、托管/外部接入、连接、运维页、指标、Pod、备份记录、MQ 主题 | 基本框架可保留；按引擎与接入模式提供能力，明确模拟备份，补异步执行证据 | 05 |
| 可观测 `/observability` | 总览/指标/日志/链路/告警；趋势图、TraceID 精查、span 树与日志关联 | 不再补一套指标页；优先补采集覆盖、数据源失败/无数据/部分数据区分 | 04 |
| 流量治理 `/governance`、服务发现详情 | 路由、服务、实例、熔断；路由下发 Ingress | 配置开关不等于入口可达；实例来源、下发状态与外部连通证据需分开 | 04 |
| 配置中心 `/config-center/:nsId?` | 应用/共享配置、发布快照、历史、回滚、引用 | 保留已有 diff 与覆盖能力；补泳道批量原子性、消费者采用证据与实际目标提示 | 04 |
| 环境 `/environments`、详情与泳道 | 环境管理、部署矩阵、泳道实体；泳道已有入口分类与关联交付 | 环境统计依赖失败不能显示健康；复核全链路染色、基线保护与清理证据 | 04、已有泳道专项 |
| 密钥与凭证 `/secrets` | 掩码、作用域、创建/删除、审计、分页与独立错误态 | 引用与轮换影响缺少可视说明；组织资产操作不应依赖无关顶栏环境 | 06 |
| API 密钥 `/api-keys` | 创建、一次性明文、角色、撤销 | 请求失败与空列表混淆；补用途/权限说明、关闭清除明文；生命周期需后端支持 | 06 |
| 用量与配额 `/billing` | 配额、应用归因 Token、账单生成/状态更新、配额权限 | 当前计价与周期口径不足以呈现为真实结算支付；数据来源和时间口径优先 | 06 |

### AI 八个子模块

| 子模块 | 当前能力（S） | 完善重点 |
|---|---|---|
| 智能体 | 模型、Prompt、Skill、工具、KB 组装；试运行、Playground、评估用例与评估历史 | 不重复新增“评估功能”；依赖就绪检查、配置优先级、版本固定、评估与配置关联 |
| 工作流 | start/LLM/tool/condition/approve/end；表单连线；触发、确认、中止、节点输出 | 修复插入/删除连接；图与配置联动、变量选择、定义快照、运行深链 |
| 知识库 | 向量库/对象存储绑定、上传、解析状态、检索测试、Agent 引用 | embedding 模型/维度选择、依赖验证、来源定位、失败后恢复；保留已有检索测试 |
| 工具 | MCP/HTTP/builtin 配置、引用、MCP tools/list 测试、发布 | 测试按钮按能力；方法和 schema 可见、凭证输入保护、测试与实际调用分开 |
| 提示词 | 同名多版本、激活、历史、发布广场 | 新建版本自动激活有影响；改为显式激活与差异/变量预览、引用方影响 |
| 技能 | 指令、说明、示例、引用、启停、删除引用提醒、广场 | 保留功能；依赖影响、详情及版本，统一列表与异常态 |
| 资源广场 | 类型/分类、快照预览、安装 fork、依赖复制、凭证剔除提示 | 安装前差异/依赖与安装后配置向导、失败部分结果；不做安装即运行的承诺 |
| 执行记录 | 外部 `/api/agentrun/runs` 的记录、日志、产物、取消、详情 | 不包含内部 workflow runs，也非全部 chat 调用；以来源区分聚合，保留各自状态机 |

产品子域检查包括：服务运行、共享资源绑定、运行/动态配置、成员权限；交付子域包括：智能研发、变更批次、流水线定义/运行、仓库 PR、构建镜像、发布回滚；可观测五区与泳道访问入口均纳入。未对平台管理员控制台、landing 和每个外部插件内部 UI 作本轮验收。

## 优先问题清单

P1：影响正确操作、执行可信度或结果解释，优先闭环。P2：关键任务效率与能力增强。P3：规模化优化。优先级用于安排开发，不等价于已确认生产事故等级。

| ID | 级别 | 源码事实与影响 | 证据定位（仓库相对路径） | 设计 |
|---|---|---|---|---|
| WF-01 | P1 | 新建 start→end 后 addNode 只插数组、不重连前驱；后端可达性校验会拒绝孤立节点。删节点重连写死 e，终点改名会留下错误引用 | `frontend/console-user-v2/src/views/ai/Workflow.vue` 的 addNode/removeNode；`internal/ai/workflow/model.go` Validate | 01 |
| WF-02 | P1 | Run 只持有 workflowId；本进程执行拿定义值副本，但进程重启后 paused 审批恢复重新 Get 当前定义。UI 也用当前定义解释旧运行 | `internal/ai/workflow/model.go` WorkflowRun；`engine.go` Start/Approve；`WorkflowRunView.vue` nodeMeta | 01 |
| WF-03 | P2 | 工作流运行仅在工作流抽屉；全局执行记录只请求外部 agentrun。通用“执行记录”名称容易让用户误以为是全部 AI 调用 | `views/AgentRuns.vue`、`api/agentrun.ts`、`router.ts`、`ai/WorkflowRunView.vue` | 01 |
| AI-01 | P1 | 多个 AI 列表主数据与引用数据 Promise.all，任一引用失败整体不赋值；catch 仅 toast，随后可能出现“暂无资产” | `ai/Agents.vue` load；`ai/Tools.vue` load；`ai/Skills.vue` load；`ai/KnowledgeBases.vue` load | 02、共性 |
| AI-02 | P1/P2 | 已有 MCP tools/list 测试，但 HTTP/builtin 仍展示通用测试入口且可被 Agent 选中；后端测试与 Agent 调用目前仅支持 MCP | `ai/Tools.vue` testTool/template；`ai/Agents.vue` 工具选择；`internal/ai/tool/handler.go` serveTest；`internal/ai/agent/runtime.go` buildTools | 02 |
| AI-03 | P1 | KB 默认 embeddingModel/Dim 为固定文本与数值，数据服务选择未给可调用/兼容性证据；不是“缺上传与检索” | `ai/KnowledgeBases.vue` form/openCreate/loadDS | 02 |
| AI-04 | P1 | 提示词新增同名版本自动激活；Agent 按名称取 active 模板。创建版本与改变使用中配置耦合 | `ai/Prompts.vue`；`internal/ai/prompt/memory/store.go` Create；`internal/ai/agent/runtime.go` buildSystem | 02 |
| AI-05 | P1 | Agent buildSystem 对 Skill 缺失/禁用、KB 检索失败采用静默跳过；对话仍可能正常返回，用户难知运行能力已降级 | `internal/ai/agent/runtime.go` buildSystem | 02 |
| AI-06 | P2 | Prompt 声明变量语法，但 Agent 运行直接采用 p.Template 文本，当前组装路径未见变量渲染；预览与运行含义需对齐 | `internal/ai/prompt/model.go`；`internal/ai/agent/runtime.go` buildSystem | 02 |
| MD-01 | P1 | 所有模型统一跳对话；模型能力标签与实际支持的 API 未形成约束 | `views/Models.vue`；`views/Playground.vue` send | 03 |
| MD-02 | P1 | HTTP 失败文案存入 assistant.content；下次发送只过滤空 content，把失败文案继续放入模型消息历史 | `views/Playground.vue` hist/HTTP error 分支 | 03 |
| MD-03 | P1 | lastTokens 用 Unicode 字符计数，UI 显示 tokens；不具备精确 Token/费用含义 | `views/Playground.vue` delta 累加与 tokens 标签 | 03 |
| CF-01 | P1 | 泳道发布逐 key 立即写入，无批次版本/原子提交；中途失败可能部分生效；新增 key 还写基线草稿。删除态需明确取消覆盖/删除有效值语义 | `components/SharedConfigEditor.vue` confirmPublish | 04 |
| CF-02 | P2 | 发布快照/配置合并已存在，页面称“生效中”；缺少客户端采用版本证据，不能等同全部实例已使用 | `views/ConfigCenter.vue` Published/template；`SharedConfigEditor.vue` | 04 |
| OB-01 | P1 | real metrics 对多种上游失败 log+continue，最终可返回空/部分 series，前端有 HTTP 错误面板也无法解释被吞掉的采集错误 | `internal/observability/real/metrics.go` List 与各 target 查询；`views/Observability.vue` capture | 04 |
| GV-01 | P2 | 路由开关以 enabled 为主，创建提示十几秒下发；没有分层表达配置、控制器下发、外部探测 | `views/Governance.vue` create/toggle；`internal/governance/model.go` Route | 04 |
| DS-01 | P1 | UI 已写明只管理备份记录；后端 create 仍直接 completed 与 deterministicSize。仍不能作为可恢复证据 | `components/DataServiceOperations.vue` backup/hint；`internal/backup/handler.go` create | 05 |
| AC-01 | P1 | 成员/用户列表失败转换成 []；“暂无成员”会误导权限管理判断。API Key 加载也仅 toast | `components/ProductMembers.vue` load；`views/ApiKeys.vue` load | 06 |
| AC-02 | P2 | API Key 一次性展示关闭后未显式清除 ref；凭证缺用途、引用影响与轮换闭环 | `views/ApiKeys.vue` createdKey/dialog；`views/Secrets.vue` | 06 |
| BL-01 | P1 | 账单按当前用量×固定 PriceTable，period 不是时段查询；pay 更新 paid 状态，没有支付网关凭证 | `internal/billing/model.go` PriceTable；`memory/store.go`、`pg/store.go` GenerateBill/PayBill；`views/Billing.vue` | 06 |
| UX-01 | P1/P2 | 环境页非 2xx 可能保留空/旧数组并派生健康；租户级凭证删除的确认强度取顶栏环境；作用域与操作目标应统一 | `views/Environments.vue` load/health；`views/Secrets.vue` remove | 04、06 |
| UX-02 | P2 | 首页 alert 通知仅跳可观测首页；新建资源成功后多数返回列表；嵌套页头、无分页 AI 表、固定宽弹窗增加扫描与切换成本 | `navigation/index.ts` notificationLink；各 AI 页面模板 | 共性 |

这些发现并不表示已发现跨租户越权。前端权限提示与后端授权是两个检查层次，后端权限仍需以角色矩阵实测。

## 已有专项的最新位置

| 专项 | 当前代码进展（S） | 下一步（V），不能直接宣称完成 |
|---|---|---|
| 服务运行 | TerminalPanel、runtime capabilities、重启/重建 API、操作记录及 controller 代码已出现 | 集群 shell 可用/缺 shell、断线、权限、容器重选；操作完成与 rollout 证据；Job/CronJob 不套 Deployment 重启 |
| 研发交付 | planHash、锁定 artifact/digest、步骤/图/耗时视图、矩阵、变更/PR/发布详情 | 预览后产物漂移、409 重预览、审批后产物一致性、多服务部分失败、回滚作用范围 |
| 泳道访问 | 业务/联调/内部入口分区、getLaneAccess、关联运行、基线保护相关后端修改 | SDK 全链路染色与 fallback、关闭清理终态、永久泳道保护；入口请求失败应有错误态而非消失 |

入口可达、HTTP 2xx、单测通过、真实环境执行成功分别记证据，不能互相替代。仓库 AGENTS 的历史能力描述与正在修改的实现可能不同步；本轮以当前实现为准并标明未验收。

## 设计交接

从 [专项总设计](../../superpowers/specs/2026-10-17-console-module-improvements-design.md) 进入六个专项；开发顺序、范围、契约及验收场景见 [实施交接](../../superpowers/plans/2026-10-17-console-module-improvements-implementation.md)。本轮只新增这些设计文档。
