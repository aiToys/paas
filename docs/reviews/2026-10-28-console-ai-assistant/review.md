# 控制台 AI 助手需求验收复核

日期：2026-10-28

后续修复：本轮已继续完成对话与执行改造，详见同目录 `implementation.md`；下文保留首次审查时的发现，不表示当前代码仍原样存在这些问题。

基线：当前工作区，HEAD `7a9f817`；包含未提交及未跟踪的新实现文件。

对照：`docs/superpowers/specs/2026-10-27-console-ai-assistant-diagnosis-design.md`。

结论：**产品入口和主要页面已落地，但尚未完成设计要求，不满足整体验收。** 取证、取消、恢复、隔离及费用控制存在影响使用或安全的缺口。本轮只审查、验证和记录，未修改业务实现、提交或部署。

## 需求覆盖

| 需求 | 结论 | 说明 |
|---|---|---|
| 顶栏机器人、全局助手侧栏 | 已有实现 | `App.vue` 已接入，首期展示官方智能诊断 |
| 官方 Agent 与普通租户 Agent 分离 | 已有实现 | 静态目录、按次注入 scoped 工具集；普通 Agent 平台工具限制仍在 |
| 工作负载/实例/告警上下文 | 部分完成 | 工作负载 ID 有传递；实例 UID/容器未带入，告警只加通用入口 |
| 同目标追问、历史与完整页 | 部分完成 | 后端有摘要上下文；重开面板丢会话/混旧 run，历史分页过滤存在问题 |
| 202 异步、真实工具进度 | 已有实现 | worker 执行、工具 hooks 及轮询已接入 |
| 停止、并发限制、多副本恢复 | 未完成 | 取消在执行后才检查；准入非原子；heartbeat/reclaim 未接线 |
| 证据驱动根因分析 | 未完成 | 日志正文只存账本，未进入模型；空指标变零；报告质量校验不足 |
| 租户/用户/资源隔离 | 部分完成 | 业务 ID 固定、tenant 查询、禁任意查询、Trace 受限已有；账号缓存/空 owner/动态授权仍有缺口 |
| 脱敏与有界证据 | 部分完成 | 部分工具片段脱敏、截断已有；用户问题未脱敏、模型信封和账本不统一 |
| 预算、用量、工具调用上限 | 未完成 | 只有发起前余额检查，scoped runtime 未计费，无预算预留/结算和 24 次硬上限 |
| 审计、7 天清理、降级提示 | 未完成 | 未发现 assistant 审计/保留清理实现，数据源缺失存在 panic |
| OpenAPI 完整响应及生成类型 | 部分完成 | 路由已登记，多数响应没有 WithResp，前端 assistant 类型为手写 |
| 浏览器/PG/真实模型/故障注入验收 | 本轮未验证 | 现有脚本/部署日志不能替代本轮独立验收，下面问题已经足以阻断完成判定 |

## 阻断问题

### F1 [P1] 未配置可观测源时可能直接使 core 进程退出

定位：`cmd/core/main.go:1420`、`internal/diagnose/scoped/toolset.go:369`、`internal/observability/real/rawquery.go:146`。

`obsDiagRaw.prom/loki` 是具体指针，未配置时为 typed nil。传入 `mountAssistant` 的接口参数后，`ts.loki == nil` / `ts.prom == nil` 判定不生效。调用 `RawLoki` / `QueryInstant` 会解引用 nil。Worker goroutine 无 recover，因此在真实后台执行路径触发将退出整个 core，而非返回缺源报告。

临时探针 `TestReviewUnconfiguredLokiMustNotPanic` 已复现 nil pointer panic；测试本身用 recover 避免测试进程退出。

修复要求：装配时对具体指针判空再装入接口，缺源返回明确状态；Worker 对非预期 panic 也应收敛为任务失败，不能击穿控制面。

### F2 [P1] 日志已读到，但模型看不到日志正文

定位：`internal/diagnose/scoped/toolset.go:160`、`:363`、`:400`。

Pod/Loki 日志正文作为 `snippet` 传给 `Ledger.Record`，返回模型的 envelope 却只包含 `data`、`note`、`evidenceId`；日志 `data` 只有行数/Pod 等字段。Runtime 只把这个返回字符串作为 tool message 喂回模型，未将账本片段补入上下文。

已复现：账本包含 `review-database-authentication-failed`，模型信封只有 `{"lines":1,...}`，不含该错误内容。因此日志驱动的根因分析实际无法完成。

修复要求：用同一份已脱敏、已截断的证据内容生成模型工具结果与持久化账本，保证模型能引用并解释实际错误；同时统一执行单工具和总上下文容量限制。

### F3 [P1] “停止”不会停止运行，也不会避免已取消排队任务消耗模型

定位：`internal/assistant/service.go:242`、`internal/assistant/worker.go:109`、`:123`。

Cancel 只写 `CancelFlag`，Worker 完整调用 `Execute` 返回后才读一次；claim 前、模型调用中、工具调用间均未读取或触发 cancel ctx。已取消的 queued 任务仍会被 claim 并执行，超时任务甚至可能以 timed_out 结束。

临时探针 `TestReviewCancelledQueuedRunMustNotExecute` 已复现“先取消，再 claim，执行器仍被调用”。

修复要求：取消排队任务原子转终态；运行任务由取消 watcher/每轮检查取消 Provider 与工具 ctx；终态竞争检查取消标志并正确结算已发生用量。

### F4 [P1] 心跳与失联回收没有接入，重启会永久占住任务和租户名额

定位：`internal/assistant/worker.go:55`、`:118`；`internal/assistant/pg/store.go:327`、`:359`、`:366`。

PG 有 `Heartbeat` 和 `ReclaimExpired` 方法，但非测试代码没有调用它们。ClaimNext 只领取 queued；服务停止时特意保留 running。进程崩溃/重启后该任务无人转终态，`CountTenantActive` 一直为 1，用户新诊断被拒绝。

ClaimNext 没有把 lease_owner 返回给调用者，Run 也无 fencing 字段；终态更新只检查状态，没有校验领取者代次。单独存在 SQL 方法不能证明多副本恢复已完成。

修复要求：接入周期心跳、过期回收、领取者 token 和 fenced 更新；回收后不自动重跑付费请求；验证 kill worker 后能释放名额。执行前应按 60 秒排队期限判断，当前代码错误地使用 60+180 秒。

### F5 [P1] 并发准入非原子，可突破同会话/租户任务上限

定位：`internal/assistant/service.go:180`、`:211`；`internal/assistant/repo.go:126`；`internal/assistant/pg/store.go:152`。

活动任务查询、租户/全局计数与 CreateRun 是分离操作。两个请求可同时通过全部检查再插入；memory 的各方法分别加锁不能覆盖这段检查-插入，PG 也没有事务锁或活动会话约束。

临时探针用并发 barrier 稳定复现同一会话成功受理 2 个任务。相同幂等键的并发请求在 memory 也缺唯一约束，PG 唯一索引冲突没有转为读取原 run。

修复要求：仓储级原子准入，跨副本统一保证会话、租户、全局额度及幂等；检查失败必须 fail closed，不能忽略存储错误。

### F6 [P1] 空指标和失效证据会产生误导性诊断

定位：`internal/diagnose/scoped/toolset.go:439`；`internal/assistant/executor.go:147`、`:184`；`frontend/console-user-v2/src/components/assistant/DiagnosisReport.vue:28`。

已复现三条独立路径：

1. Prometheus 成功返回空数组时，工具仍填 `cpuCores=0/memWorkingSetBytes=0`，状态 `ok`；单指标失败时也会保留默认 0。
2. 两条不同工具的证据即便状态分别是 error/missing，只要 ID 存在，也会被评为 `sufficient`。
3. 所有根因假设因虚构引用被剔除后，非空 verdict 仍保留“系统健康，无异常”，与 inconclusive 质量及设计纪律相冲突。

此外，真实 `Ledger.Coverage` 没接入报告，前端按“ok 优先”重算会覆盖错误，未执行源和 Trace 隔离降级说明可能不展示。

修复要求：逐信号区分零值/空/错误；验证证据状态与结论相容性；无有效支持时重写顶层结论；服务器输出包含全部数据缺口的覆盖摘要，前端不自行抹平错误。

### F7 [P1] 助手推理没有接入实际计量和预算结算

定位：`internal/assistant/service.go:191`；`internal/ai/agent/runtime.go:522`；`internal/assistant/worker.go:143`、`:210`。

发起前只有 `BudgetCheck` 查询，没有原子预算预留、每轮预算检查或结算。新 `RunScoped` 路径不调用既有 `MeterFn`，Worker 只在自己的 run 记录一个问题+证据字符下界估算，不写平台计量/预算消费；取消/失败连该估算也没有。

故用户可以连续运行诊断而不增加该路径的平台计量，发起前余额检查也不会因这些运行正确下降。常量 `LimitToolCallsPerRun=24` 仅声明，Runtime 没有对应累计硬限制。

修复要求：每轮收集真实 usage 或明确估算，以 runId 幂等计量；发起预算预留，全部终态结算；工具调用数量在服务端实际计数并截止。

### F8 [P1] 退出/切换账号后仍保留上一用户的诊断缓存

定位：`frontend/console-user-v2/src/stores/assistant.ts:25`、`:81`；`frontend/console-user-v2/src/App.vue:48`、`:73`、`:78`。

App 的 logout/expired/profile watcher 只清环境，不调用助手 reset。Pinia 的 `activeRun/events/preset/panelOpen` 继续存在；再次登录后重开面板，组件立即从 store 回显旧报告。对于已完成任务甚至不会再次向服务端校验，跨账号可见的缓存无法由后端 404 补救。

修复要求：身份边界统一销毁助手状态、面板状态和 preset，停止轮询并让正在进行的请求结果失效；401/403 同样处理。当前 reset 自身也未清空 preset/panelOpen。

### F9 [P1] 用户问题未脱敏，owner 为空时放宽了私有会话边界

定位：`internal/assistant/service.go:156`、`:205`、`:214`、`:318`。

已复现：`password=review-secret-value` 原样成为 run.question、排队事件 payload，后续直接进入模型输入；没有应用现有脱敏函数。

另一个函数级探针复现：同租户 `userID=""` 的上下文可读取 u1 私有会话，因为 `sameOwner` 将空身份当通配。真实 API 是否可利用还取决于凭证能否以无 user ID 通过鉴权；但这不符合首期 owner 私有、无明确共享授权即拒绝的设计。

修复要求：问题/追问在落库与送模型前脱敏；要求明确用户身份，或将程序化调用固定到可审计主体，不用空身份跳过 owner 校验。

### F10 [P1] 面板重开后丢失会话，与旧 run 混用，无法可靠更换目标

定位：`frontend/console-user-v2/src/App.vue:164`；`frontend/console-user-v2/src/stores/assistant.ts:31`；`frontend/console-user-v2/src/components/assistant/AssistantPanel.vue:55`、`:73`、`:97`。

面板使用 v-if，关闭会卸载组件，组件内 session/selectedWl 丢失；store 却保存 activeRun。重开时显示旧 run，只在 preset.sessionId 存在时恢复会话。普通全局入口的 selectedWl 可能为空，追问按钮点击后在 canSubmit 静默返回；资源页入口则可能新建会话，丢失追问历史。

从目标 A 完成诊断后转到 B 点击诊断，openPanel 只改 preset、不切 activeRun，页面仍显示 A 的报告，不能先建立 B 的明确新会话。已有 run 视图也没有更换目标入口。`openFull` 未先关闭遮罩，完整页跳转发生在遮罩下面。

修复要求：明确区分当前显示会话与后台任务；以 sessionId 恢复同一会话；新建/换目标显式开新会话视图；打开完整页关闭遮罩；支持从顶栏为当前资源推导候选上下文。

## 其他待补项

1. **[P2] 资源聚焦与授权复核**：FocusRef 仅存不验证、不用于执行；实例入口只把 Pod 名写到自然语言，没有传 Pod UID/容器。TargetResolver 仅查 workload，没有应用访问检查；Worker 只有 tenant ctx，没有在 claim/工具调用前复核用户、冻结状态、角色或目标可见性。Pod 只按 selector/name 检查，没有 owner 链与 UID 保护。
2. **[P2] 查询窗口不是固定 UTC 窗口**：run 保存 start/end，执行时转回分钟数；Loki/事件在调用时按 now 回看，Pod 日志不按窗口截取，Loki 还允许模型覆盖 sinceMin。用户看到的窗口与真实证据可能不一致。
3. **[P2] 历史先分页再按 owner 过滤**：PG 先取租户最新 20 个，memory 先取全局 20 个，service 再过滤。其他用户/租户活跃时，自己有历史也可能显示为空；前端也没有加载更多。
4. **[P2] 持久化终态和事件不是原子操作**：先发 report.ready 后写报告，且 UpdateRunStatus 除 ErrConflict 外的错误被吞；失败会出现“已有结果事件但任务仍 running”。需要事务/outbox、明确报错和恢复。
5. **[P2] SQL 隔离不完整**：部分 repo 方法只按 ID/sessionID 查/改，owner 只由 service 比较；新增 RLS policy 依赖 `app.tenant_id`，当前 DB 包未见设置事务级该参数，非超级用户模式应专门验证，不能依赖测试超级用户绕过 RLS。
6. **[P2] 平台 Agent 可用性、HTTP 契约**：目录从不计算模型未配置/无工具能力的禁用原因；大量错误统一返回 400；JSON decoder 忽略作用域等未知字段，与“拒绝”契约不一致。
7. **[P2] 无助手审计与保留清理**：没有接入发起/停止/完成/拒绝审计，也没有 7 天 TTL 清理；memory 模式限制未在 UI 明示。
8. **[P2] 交互/可访问性**：抽屉是自建遮罩，未见焦点限制、Esc 关闭、关闭后焦点恢复；无实际 Agent 选择、完整对话轮次列表、真实时间窗选择。接口加载失败在面板被吞成“没有工作负载”，不符合区分失败和空态的要求。

## 本轮验证与边界

已验证：

- `internal/assistant`、`internal/diagnose`、`internal/diagnose/scoped`、`internal/diagnose/tools`、`cmd/core` 的现有 `go test -race` 通过。
- `internal/ai/agent` 首次测试被沙箱阻止 httptest 监听端口，允许本机监听后重跑通过；不是代码失败。
- 前端直接调用已安装的 `vue-tsc --noEmit` 通过，原有 21 个 UX 契约测试通过。
- 新增 **9 个只位于 tmp 的审查探针，9 个均复现缺陷**。通过 Go overlay 加入包内执行，没有改动正式测试或业务源码。结果为预期断言失败，不是测试通过。

临时产物均在根目录 `tmp/console-ai-assistant-review/`：`assistant_probe_test.go`、`scoped_probe_test.go`、`overlay.json`、`probes.log` 及验证日志。

复现命令：

```bash
go test -race -overlay tmp/console-ai-assistant-review/overlay.json \
  ./internal/assistant ./internal/diagnose/scoped -run TestReview -v
```

前端最初经 pnpm 启动被包管理器版本切换的网络/签名获取失败阻止，随后直接使用本地已安装工具完成验证；未跳过签名校验，也未修改包管理器配置。该尝试产生的根目录缓存已迁入本任务 tmp。

本轮未运行实际 PG 数据库集成测试、浏览器交互、真实 Provider 或集群故障注入，也未重新部署。已有 PG 测试受 integration tag 门控，普通 `go test` 的该包显示无测试不代表 PG 通过。

建议先处理 F1–F10，再补其他待补项和真实验收；目前不能标记“需求完成”。
