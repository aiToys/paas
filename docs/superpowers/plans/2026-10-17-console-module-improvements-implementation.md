# 新版全模块专项完善：开发交接与验收计划

日期：2026-10-17。状态：仅设计，以下任务未实施，复选框不代表当前代码完全没有相关能力。

阅读顺序：[全模块检查](../../reviews/2026-10-17-console-next/all-modules-audit.md) → [专项总设计](../specs/2026-10-17-console-module-improvements-design.md) → 所负责专项。以开发时最新实现核对，保留其他 agent 已有修改。

## 1. 开发原则

当前工作区有运行、交付、泳道、可观测等并行改动。开始前核对 AGENTS、分支、dirty 状态、接口与浏览器实际资源版本；不覆盖、格式化或回退无关文件。用户本轮授权是检查与设计，本文件供后续开发任务使用，本轮没有业务代码实现。

保留 `/next/` 路径、旧 query 深链、API 包装、租户与权限语义。新增后端能力留在既有领域插件；不混入业务示例或 Core。任何契约字段都是提案，先确认实现再生成类型，不在浏览器补假数据。新增依赖检查 Apache 2.0 兼容许可。

任务以改进包交接；可由不同开发 agent 领取，但本文件不自动派工，也不授权提交、push、部署或执行生产数据操作。

## 2. 顺序与包边界

| 包 | 先完成什么 | 主要触点 | 依赖 / 完成标准 |
|---|---|---|---|
| A 正确性与读取 | WF-01 插入删除；MD-02/03 错误与字符计量；AI-01/AC-01/UX-01 失败态；模拟备份/账单命名；明文清除 | Workflow、Playground、AI 列表、ProductMembers、ApiKeys、Billing、Environments、DataServiceOperations | 可先基于现有契约做；不等待图编辑/计费系统 |
| B 工作流执行 | 定义 snapshot/revision、恢复与历史、校验定位、运行深链；真实拓扑与变量面板 | internal/ai/workflow、api/workflow、Workflow/WorkflowRunView、router | A；后端快照先于历史可复现承诺；旧 run 有迁移边界 |
| C AI 资产 | 依赖就绪、KB embedding 兼容性、工具方法/schema、Prompt 显式激活与版本影响、安装后待办 | internal/ai 各领域、marketplace、views/ai | A；版本/引用 API 后开放相应控件 |
| D 模型体验 | 支持端点与参数/价格元数据、按能力分派试玩、请求诊断与真实 usage | maas/provider/gateway、Models、Playground | A；先核实际 API 支持，不因 capability 标签就实现假玩法 |
| E 变更与观测 | 泳道变更集和冲突；采集质量；路由协调状态；环境依赖与统计 | configcenter、observability、governance、environment 与对应 UI | A；保留现有运行/泳道/配置编辑器；按领域分别落地 |
| F 数据服务 | capabilities 与 operation；首个真实备份引擎及恢复验证 | dataservice、backup、controller、DataServiceDetail/Operations | A；真实 provider/隔离验证环境是恢复能力前置 |
| G 凭证与用量 | 最小权限/有效期/轮换、Secret 引用、计量时间口径 | identity/security/billing/gateway、访问与用量 UI | A；真实结算/支付单独立项，不塞进前端优化 |
| H 视觉与贯通验收 | 共用页头、密度、响应式、焦点、跨模块深链；运行/交付/泳道增量验收 | theme、共用组件、navigation、专项页面 | 在已确认行为上收敛视觉；不因 UI 统一重写领域状态机 |

推荐首批 A+B 的工作流快照、E 的部分失败契约；C/D/E/F/G 可在各自契约边界明确后独立推进。H 在每包中持续检查，最后做跨模块验证，不在最后一天才处理无障碍。

## 3. 具体任务

### A：立即可做的修正

- [ ] 在线上插入节点自动重连；复杂删除显示边预览；删除/改名不写死 e；不绕过后端图校验。
- [ ] Playground 消息状态与错误对象分开；失败不入上下文；主动停止/断流/完成分开；字符计数改准确名称。
- [ ] AI 主列表与引用独立读取；统一 fetchList/StatePanel，防 null、格式错误和非 2xx 变假空态；慢响应丢弃保护按需补。
- [ ] 成员、Key、Billing、环境各分区区分失败/空/旧数据；依赖失败不派生健康值。
- [ ] 工具测试按实际支持类型显示；MCP 发现结果不只 toast；输入 apiKey 遮罩并保留替换语义。
- [ ] API Key/Secret 明文关闭/切页/退出清除；组织作用域不随顶栏错误变化。
- [ ] 记录备份、示例计价、状态标记与真实执行/支付用词对齐；Prompt 暂保留自动激活时，按钮明确“创建并激活”。

### B～G：契约先行的任务

- [ ] 输出现有/新增 API、字段、权限、状态枚举、迁移与兼容方案对照；更新 route registry/OpenAPI 并生成 TS。
- [ ] 工作流快照覆盖创建、暂停、重启恢复、历史展示与存量无快照；依赖快照与定义快照区别。
- [ ] KB 模型维度、来源与索引版本；Prompt 待激活/固定引用；安装预检/失败结果按专项定义落地。
- [ ] 模型 supportedEndpoints/参数/价格口径与网关真实支持一致；usage 字段缺失时展示未知。
- [ ] 配置 batch/revision/幂等/删除语义；观测质量元数据；路由配置/协调/外部探测证据各自实现。
- [ ] 数据服务能力与操作记录；真实备份产物/完整性/恢复验证，不把记录持久化当恢复能力。
- [ ] API Key scope/期限/使用时间与 Secret 引用/轮换；用量时间窗与价格版本；真实支付无渠道则维持明确估算/模拟。

## 4. 路由与共享文件协作约束

router、navigation、theme、api.ts 是高冲突区。各包优先在所属视图/API 文件修改，确需共享改动先声明范围；接口依赖不能复制临时实现到多个组件。

建议提取的 UI 组件仅在至少两个真实场景复用：依赖摘要、引用对象列表、状态与读取时间、操作结果列表。图编辑、计价和审批业务不放进通用组件。已有 RuntimeInstance/MetricChart/StatePanel/TablePager 继续使用，不另造同类组件。

新工作流详情与运行路由必须置于合适的具体匹配位置；旧 `/ai/workflows`、`/ai?tab=...`、外部 `/ai/runs/:name` 不改变原义。新增 query 不覆盖产品交付的 tab/section/focus；旧链接解析失败进入明确提示，不能静默跳别的资产。

## 5. 最小验收矩阵

| 维度 | 至少覆盖 |
|---|---|
| 身份 | 租户管理员、开发者、观察者；应用受限成员/非成员；跨租户不可见 |
| 作用域 | 全部/测试/生产浏览范围；资源实际环境不同于顶栏；环境默认配置与环境基线；普通/永久泳道 |
| 数据 | 首次无数据、真实空、403、404、429、503、200 非 JSON、null 列表、部分失败、过期数据 |
| 执行 | 受理/运行/待人工确认/成功/失败/中止/进程重启；双击与幂等；定义/计划/资源版本漂移 |
| 资产 | enabled 但依赖失效；模型协议不匹配；旧历史无快照；已安装但待配凭证；同名不同用户 |
| 视觉 | 1440/1280/1024/768 宽、亮暗、长名称、大输出、200% 缩放、键盘焦点与状态播报 |

测试用固定可追溯夹具，不在产品页嵌示例假状态。静态源码正则不能替代关键交互测试：工作流插删、失败消息历史、配置部分提交等必须验证状态与实际请求。

后端变更运行相关领域测试和必要的 memory/PG 一致性验证；有迁移使用独立测试库。前端在 `frontend/` 目录使用仓库真实脚本，例如先核 `pnpm --filter @paas/console-user-v2 run`，再执行对应 typecheck/test/build；不套用其他 workspace 的命令。纯文案/布局不堆镜像实现的测试。

真实 shell、Pod 重启、流量染色、备份恢复等另列环境验证，选择隔离测试目标并遵循实际开发任务授权；本轮文档不授权执行生产写操作。使用模拟数据进行浏览器 QA 时标明夹具与模拟边界。

## 6. 已有专项增量验收

- [ ] [服务运行](../specs/2026-10-17-service-runtime-operations-design.md)：确认当前 TerminalPanel 与新 API 已接入到真实 runtimeReader/权限/RBAC；缺 shell、多容器、连接失效与操作终态。
- [ ] [研发交付](../specs/2026-10-17-development-delivery-design.md)：planHash/digest、等待审批后产物不漂移；多视图选中 stage 一致，PR/变更/发布/回滚可恢复深链；多服务部分失败。
- [ ] [泳道访问](../specs/2026-10-17-lane-access-experience-design.md)：新入口分类与后端 access 一致，普通/永久/基线的保护与回收一致；SDK 染色与 fallback、关闭结果可解释。

以上已有新增代码，不能作为空白模块再重写。完成一个包只表示该包的验收通过，不等价于整个控制台/上线版本通过。

## 7. 交付报告模板

每包报告：处理的发现 ID；实现与未实现边界；变更文件/API/迁移；本地验证；CI 结果（如有）；环境验证的对象/版本/时间/结果；已知风险与剩余验收。自动执行成功、Agent 任务结束与用户任务验收分别列出。

本轮设计文档校验仅检查链接、结构和覆盖，不把这些检查称为应用功能测试。

## 8. 可直接交给开发 agent 的一句话提示词

> 请先阅读 `/Users/wangtao/data/github.com/aitoys/paas/docs/reviews/2026-10-17-console-next/all-modules-audit.md`、`/Users/wangtao/data/github.com/aitoys/paas/docs/superpowers/specs/2026-10-17-console-module-improvements-design.md` 与本实施计划，核对最新代码和其他 agent 的未提交改动，按 A→领域专项→H 的顺序实施新版控制台完善，优先修复工作流连线与快照、Playground 消息与计量、失败态和模拟能力误导，保留已有运行/交付/泳道框架，契约不足先补后端再开放 UI，并分别报告本地检查和真实环境验收证据。
