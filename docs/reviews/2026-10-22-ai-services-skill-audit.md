# AI 服务与 Skill 现状审计及完善建议

日期：2026-10-22。基线：`console-v2`，HEAD `0cfc165`。这是源码审计与建议，未修改业务实现、未提交、未部署。

审计前已存在的修改：`examples/paas-shop/internal/observ/observ.go`、`frontend/console-user-v2/src/composables/useSpanTree.ts`、`frontend/console-user-v2/src/views/Observability.vue`。本次保留这些修改。

## 1. 结论与产品边界

1. **需要内置一组可验证的 Skill 模板和场景包。** 当前只有创建、绑定、注入和广场复制机制，没有官方模板目录及演示初始化。生产提供只读模板供安装，演示环境显式安装场景包，不向所有租户强塞业务演示数据。
2. **演示按业务任务提供 Skill，不要求每个微服务都提供。** 商品导购、订单查询、退款申请各有完整任务，适合做 Skill；BFF、流量生成器、统计 worker 属实现组件，无需为数量凑 Skill。服务能力由 API/MCP 暴露，Skill 编排使用步骤。
3. **PaaS 应对外提供平台操作 Skill。** 首批覆盖接入与查询、应用交付、运行排障；Skill 通过已有租户 API 或后续平台 MCP 执行动作。凭证、权限、环境、审批、审计继续由服务端约束，不能由自然语言指令授予权限。
4. **区分平台内指令 Skill 与标准目录包。** 当前实体是可组合的指令文本；标准 Agent Skills 至少包含带元数据的 `SKILL.md`，可附 scripts/references/assets。平台当前未实现这些文件的管理、按需读取或脚本执行，不能声称已兼容任意标准 Skill 包。[Agent Skills 格式规范](https://agentskills.io/specification)

MCP 提供工具和上下文交换协议，Skill 提供任务流程。平台作为 MCP 客户端消费演示服务的工具，与平台对外提供 MCP 服务，是两个方向。[MCP 架构说明](https://modelcontextprotocol.io/docs/learn/architecture)

## 2. 已有能力与实际缺口

| 范围 | 本次确认的现状 | 证据 |
|---|---|---|
| Skill | 租户内 CRUD；说明、指令、分类、场景、示例和来源字段；绑定 Agent 后全部顺序注入 | [实体](../../internal/ai/skill/model.go)、[运行时](../../internal/ai/agent/runtime.go) |
| 模板与 seed | memory Skill/广场初始为空；装配未提供官方 Skill 模板 seed | [持久化装配](../../cmd/core/persistence.go)、[Skill 内存仓储](../../internal/ai/skill/memory/store.go)、[广场内存仓储](../../internal/ai/marketplace/memory/store.go) |
| 演示资源 | 脚本创建 Prompt、MCP Tool、KB、Agent；未创建 Skill，Agent 请求未带 skills | [PaasShop 初始化](../../examples/scripts/setup-paas-shop.sh)、[早期示例初始化](../../examples/scripts/create-resources.sh) |
| 广场复用 | Skill、Agent、工作流可发布快照和 fork；Agent 内嵌 Skill/Prompt/Tool 并重写引用 | [fork 编排](../../internal/ai/marketplace/forker.go) |
| 依赖失败 | 显式引用的 Skill 缺失/禁用、Prompt 不可用、KB 检索失败、MCP 发现失败均阻断 | [buildSystem/buildTools](../../internal/ai/agent/runtime.go) |
| Prompt | 已有版本、草稿/立即激活选择和显式激活；Agent 当前仅支持静态系统模板，含变量会阻断 | [Prompt 页面](../../frontend/console-user-v2/src/views/ai/Prompts.vue)、[运行时](../../internal/ai/agent/runtime.go) |
| 工具 | 定义允许 MCP/HTTP/builtin；Agent 实际调用仅支持 MCP，选择器已禁选不支持的类型 | [工具模型](../../internal/ai/tool/model.go)、[Agent 页面](../../frontend/console-user-v2/src/views/ai/Agents.vue) |
| 工作流 | 有条件、人工审批、运行记录和持久化定义快照 | [工作流模型](../../internal/ai/workflow/model.go) |
| 外部平台入口 | 已有 REST/OpenAPI；本次 cmd/internal/正式技能文件检索未发现平台对外的 SKILL.md 包或 MCP server 路由。现有 MCP server 在 examples，平台内是 client | [路由装配](../../cmd/core/main.go)、[MCP client](../../internal/ai/tool/mcp/client.go)、[演示 MCP](../../examples/paas-shop/mcp/main.go) |

历史设计中的“依赖静默跳过”“没有工作流定义快照”“Prompt 只能创建即激活”已有后续修复，不应再作为当前缺陷。新版 `/next/ai/:section?` 实际使用 `AiWorkspace.vue` 及 `views/ai/*`，未被引用的旧 `AiHub.vue` 不作为当前用户路径证据。

## 3. 建议内置的场景

| Skill 建议名 | 任务与依赖 | 可复核的验收结果 |
|---|---|---|
| `shop-product-guide` | 根据预算、类别、库存查商品；使用 search_products/query_product | 给定查询能引用实际商品；无匹配时如实说明；不编造价格库存 |
| `shop-order-query` | 查订单并解释状态；使用 query_order | 查询已知/不存在订单，分别引用真实结果或说明未找到 |
| `shop-refund-request` | 收集订单与理由，给出建议并进入审批流程 | 审批前订单不变；拒绝/中止不退款；审批后只执行一次；直接调用不能绕过门禁 |
| `knowledge-answer` | 基于 FAQ 知识库回答，明确无证据边界 | 索引失败不伪装可用；能追溯检索来源；无依据时不编造 |

前三个归 PaasShop 场景所有者维护；最后一个可做平台通用模板。首期先跑通商品导购与订单查询，再接退款审批和 RAG。每个场景包含 Skill、Agent、工具绑定说明、必要 Prompt/KB、输入、预期事实及检查步骤，不能只给一段示例答案。

服务覆盖原则：product/order 是业务能力来源，mcp 是统一工具适配层，chatbot 是消费入口；recommend 可在存在独立推荐任务时补充。bff、frontend、statsworker、traffic-gen 不要求分别提供 Skill。业务能力包留在 `examples/`，不进入 Platform Core。

## 4. PaaS 对外 Skill 的具体方案

| 能力包 | 首期范围 | 执行边界 |
|---|---|---|
| `onepaas-get-started` | 检查连接、识别当前租户/环境、查询应用与模型、指导接入 | 凭证来自环境或用户配置；包里不写演示 Key；无连接时报告未验证 |
| `onepaas-app-delivery` | 检查仓库/构建/镜像依赖，预演、触发已授权流水线、读取结果 | 明确应用/环境/版本；服务端执行既有权限与审批；构建成功不等于部署成功 |
| `onepaas-runtime-diagnose` | 读取工作负载、实例、事件、日志和链路，输出证据及建议 | 默认读取；重启/重建等动作携带具体目标和服务端操作约束，保留审计 |

首期可由标准 Skill 使用已有 REST/OpenAPI，不以新建 MCP server 为先决条件。后续平台 MCP 封装稳定的领域动作；不把全部管理 CRUD 原样暴露为通用管理员工具。平台内“组合 Agent 的 Skill”与平台外“使用 PaaS 的 Skill”分别标明运行环境与依赖。

建议发布目录 `skills/onepaas-*/SKILL.md`，附 `references/` 中的 API 使用和验收说明；业务演示目录 `examples/paas-shop/skills/`。这些是建议新增目录，本次未创建技能实现。

官方源以标准目录包为真源，平台目录/广场快照由其生成或导入，避免另写一份 instructions 产生漂移。首期只支持文本指令与引用说明；遇到必须执行 scripts 的包应标明不支持，不能直接在控制面执行任意脚本。若以后支持脚本，应接独立执行域并明确网络、凭证与输出契约。

内置目录使用稳定 slug、来源、版本、内容 hash、许可及依赖元数据。发布只读模板后，由用户安装成租户副本；更新模板不覆盖已编辑的副本。版本等扩展可放标准允许的 metadata 或平台清单，不把平台自定义字段宣称为标准必填。

生产模板目录与演示初始化分开：前者不依赖演示租户，后者由 `seed.demo` 或显式安装操作控制；幂等判断使用来源/slug/版本，不用“整张表空才 seed”，重跑也不覆盖用户编辑。离线交付时模板需随二进制资源或正式镜像路径打包，不能只放在当前被 `.dockerignore` 排除的 docs 中。

## 5. 完善项与优先级

P0 为隔离问题；P1 为演示或运行闭环必须解决的问题；P2 为标准化及持续运营能力。下列风险均区分源码事实与本地复现，不代表线上已发生。

| 编号 | 优先级 | 当前问题与影响 | 建议与验收 |
|---|---|---|---|
| AI-01 | P0，内存模式 | Skill Create 接受外部 ID，仅检查租户内同名，随后直接写全局 map；另一个租户相同 ID 会覆盖原记录。PG 有主键约束，不能混同 | 服务端生成 ID 或在锁内拒绝任何已占用 ID；验证跨租户/本租户碰撞不覆盖，PG/memory 语义一致 |
| AI-02 | P1 | 演示脚本无 Skill；TOOL_IDS 拼成相邻字符串而无逗号，导致 Agent JSON 非法；新旧脚本分别查 shop-agent/shop-assistant，退款工作流可能被跳过 | 统一幂等安装入口，结构化生成 JSON、检查非 2xx；部署前离线解析请求；重跑不重复建，缺依赖明确失败 |
| AI-03 | P1 | 退款审批是一个可选工作流，但另一个脚本同时把 refund_order 直接绑定给客服 Agent；runtime 可直接 tools/call。存在直接写入口，不能宣称所有退款都经过审批 | 日常 Agent 只读，退款由专用流程执行；服务端绑定具体订单/参数/操作者/有效期的审批上下文，限制重复执行。Skill 文本不能充当门禁 |
| AI-04 | P1 | Skill 删除只在前端读 Agent 列表后拦截；API 直接 Delete，仓储无反向引用约束；引用查询失败时 UI 仍可能显示未引用。停用也会令关联 Agent 下次运行失败 | 增加服务端引用/影响查询及删除约束，引用查询失败显示未知；更新/停用展示影响，按明确策略阻断或处理 |
| AI-05 | P1 | Skill memory 与 PG Create 均强制 enabled=true，UI 新建中的禁用选择失效 | 创建 DTO 区分未传与 false，保留默认兼容；显式 false 必须保存为禁用，安装来源状态也有明确策略 |
| AI-06 | P1 | Agent runtime 直接使用 m.Channels[0] 构建 Provider，没有复用普通推理网关的候选排序、offline 过滤和 failover | 抽取共享模型调用/路由能力，避免 HTTP 回调自己；验证首通道离线/限流时的路由及错误，另行核对计量与追踪，不能由网关已有能力推定 Agent 自动具备 |
| AI-07 | P1 | 广场 Agent 整包依次创建 Skill/Prompt/Tool/Agent，中途失败无统一回滚或安装 operation；部分解析/Prompt 创建错误还会被略过，重试可能留下额外副本 | 预检依赖、按事务/补偿明确原子边界；稳定 operation/idempotency；结果列已创建和失败对象，不能仅返回成功或裸错误 |
| AI-08 | P1 | KB 创建默认取首个向量/对象实例并固定初始 1024 维；广场 KB 安装也自动选租户首个实例，无法表达安装者的环境与实例选择 | 显式选择落点，验证权限/网络/就绪及模型实际维度；更改嵌入配置采用新索引验证与切换 |
| AI-09 | P1 | EvalRun 只有 AgentID 和结果，没有冻结 Agent、Skill、Prompt、模型、用例版本；contains/exact/regex 只检查文本，无法证明调用正确工具或退款按审批执行 | 保存配置及用例快照/hash、实际调用和审批证据；事实/副作用规则与文本匹配分开，未接语义评审就准确标为规则评估 |
| AI-10 | P2 | Skill 无版本、内容 hash、触发说明/依赖契约、导入导出、模板安装和试用闭环；所有已绑指令每次全部注入 | 先明确支持的标准包子集并提供文本导入/导出；绑定版本/更新策略、试用及运行资产清单；按需加载在有规模证据后再做，不先加一层不透明模型选择 |
| AI-11 | P2 | 工具配置允许 HTTP/builtin，但运行仅 MCP；MCP client 是固定 /mcp、JSON request-response 子集，不含完整 SSE/session/通知 | 页面与 API 返回真实 capabilities；保留现有不兼容引用的阻塞说明；在明确版本/传输兼容矩阵下增强，不能宣称任意 MCP server 都可接 |
| AI-12 | P2 | `/ai/runs` 当前仍是外部 AgentRun 列表；工作流记录在其自身页面，Agent 对话没有统一持久运行证据 | 先提供来源分区与独立错误态，再按真实契约补记录；展示实际资产版本、trace/request ID、耗时、tokens、工具结果；缺少采集时显示未采集 |
| AI-13 | P1，质量门禁 | Skill 包本身及其 memory/PG 无测试；AI PG 仓储大多无测试，根 Makefile 和 CI 的 PG 集成测试命令未覆盖 AI 路径 | 补隔离、碰撞、状态、引用、安装失败及版本的测试；PG 用隔离 schema/库验证并纳入 CI，不能仅编译通过 |

关键定位：AI-01/05 在 [Skill memory Create](../../internal/ai/skill/memory/store.go) 和 [PG Create](../../internal/ai/skill/pg/store.go)；AI-04 在 [Skill handler](../../internal/ai/skill/handler.go) 与 [Skill 页面](../../frontend/console-user-v2/src/views/ai/Skills.vue)；AI-06 在 [Agent runtime](../../internal/ai/agent/runtime.go)；AI-07 在 [installAgentBundle](../../internal/ai/marketplace/forker.go)；AI-08 在 [KB 页面](../../frontend/console-user-v2/src/views/ai/KnowledgeBases.vue) 及同一 forker；AI-09 在 [评估模型与服务](../../internal/ai/eval/model.go)；AI-13 在 [Makefile](../../Makefile)、[CI](../../.github/workflows/ci.yml)。

## 6. 页面与实施顺序

沿用现有 AI 开发导航，不为每个服务新增 Skill 菜单。Skill 页增加“我的技能 / 平台模板”视图；详情包括来源、支持格式、版本、指令、依赖、示例、被谁使用及“绑定到 Agent 后试用”。导入时先展示兼容性和缺依赖。Skill 没有自己的工具执行器，不添加会让人误解为独立服务调用的“运行 Skill”按钮。

分批交付：

1. 修复 AI-01/02/04/05，补最小回归；统一演示初始化，先实现商品导购和订单查询两个可运行场景。退款写入口在展示“必须审批”前完成 AI-03。
2. 建立官方模板目录和来源/版本契约，接到现有 Skill/广场安装流程；完善安装失败恢复、依赖预检、试用、显式 KB 选择和共享模型路由。
3. 发布三个平台操作标准 Skill，首期走 REST；按实际需求增加平台 MCP 和包文件支持；补运行与评估证据。无需先建设一个通用脚本执行平台。

每批验收分别记录源码与本地检查、CI/构建、部署、真实上游或 SDK 联调、业务结果。场景验收至少包括首次安装、重复安装、关闭演示、失效依赖、非授权租户、审批前后及跨环境操作。

## 7. 本次验证与限制

- 运行了现有 `go test ./internal/ai/... -count=1`。除 Agent 包因沙箱禁止 httptest 回环监听而中断外，其余有测试的包通过；随后获准仅在本机监听，单独重跑 `go test ./internal/ai/agent -count=1` 通过。不是 race、PG 集成或真实模型验证。
- 临时本地程序已复现：创建 enabled=false 返回 true；跨租户同 ID Create 无错误，原租户 Get 返回不存在；直接 DELETE 返回 200，Agent 仍保留已删除 Skill 的引用。程序仅调用 memory/handler，不连接线上或模型。
- 按脚本的 TOOL_IDS 拼接方式离线构造请求，JSON 解析报缺少逗号；这是静态语义复现，未执行部署脚本。
- 未进行浏览器交互、真实供应商推理、集群资源写入、退款调用或业务验收。未运行 PG 集成测试，因此 PG 的细节结论为源码审查。
- 线上只读 AI 资产查询被自动审批拒绝：明文 HTTP 向尚未明确验证的目的地发送 Bearer 演示 Key 有凭证泄露风险；未绕过拒绝。因此无法断言当前线上 Skill 数量或演示实际状态。
- 本任务日志和复现程序位于被忽略的根目录 `tmp/2026-10-22-ai-skill-audit/`，不纳入 Git；正式交付为本报告。
