# 管理后台优化完成情况复核

复核日期：2026-10-21（Asia/Shanghai）。依据：2026-10-19 统一设计、实施交接清单及本次实际工作树。

## 结论

**设计交付已完成；后台改造已产生大量可构建、可测试的实现，但整体尚未完成，不能按全部验收通过交付。**

本次只核查、执行本地构建/测试并新增本报告，没有修业务代码、修改原实施清单状态、提交、部署或执行资源运维。当前分支 `console-v2`，HEAD 为 `c395e2e`；大量 admin 与后端实现仍是未提交/未跟踪文件。未提交本身不代表功能失败，但现有 HEAD 不能代表本次所测完整工作树。

## 修复状态（2026-10-21 同日回写）

F01–F08 已按「建议下一批收尾」处理完毕（同日第二批），本节为状态回写，原审计记录保持不动。实施清单同步回填于 `docs/superpowers/plans/2026-10-19-admin-optimization-handoff.md`。

| 项 | 状态 | 说明 |
|---|---|---|
| F01 租户切换残留 | **已修** | 新增 `useScopeChange` composable（reset 先于 refresh），20 个 fetchList 页 + 2 个 load 页接入，13 个详情页带 reset（关抽屉/清详情 id）；`useScopeChange.spec.ts` 3 用例。浏览器实证：t-acme 应用详情抽屉打开 → 切 t-globex → 抽屉自动关闭 + 列表刷新为该租户 2 应用 |
| F02 旧链接跳错分区 | **已修** | `dynamic.ts` query 合并顺序反转为目标优先（`{...to.query, ...defaultQuery}`），tenantId/hash 保留；`dynamic.spec.ts` 4 用例。浏览器实证：`/resources/buildruns?tab=releases&tenantId=t-acme` → `pipelineruns?tab=builds&tenantId=t-acme`（构建 tab selected）；登录回跳链复测正常（此前观察到的 404 无法复现，疑旧缓存） |
| F03 可观测收口 | **已修** | Observability.vue 重写为单层双 tab（规则/事件）；文案如实（指标/日志/trace 待 M20 聚合 API）；AlertRules.vue 去内层嵌套。浏览器实证：`?tab=events` 深链 selected；内存模式 501 降级呈现为错误条不冒充空表 |
| F04 执行记录收敛 | **已修** | AgentRuns.vue 改双 tab（Agent 运行/工作流运行），工作流 tab 首载 lazy + 失败独立错误条；AiOverview 工作流运行 tab 留迁移说明空态。浏览器实证：深链 + API 链路（内存模式空数据 200） |
| F05 规模化/懒加载 | **部分** | AiOverview 工作流真懒加载（watch tab 首载）；服务端分页 + URL 统一筛选记 D2 后续（当前全量+前端过滤，<1000 条可接受）；sessionStorage scope 维持现状 |
| F06 首页关注/诊断/归因 | **部分** | AttentionPanel 三源 allSettled（失败运行/触发告警/异常负载）+ 每源独立错误条 + 点击直达 + 快照标注，浏览器实证 5 条异常负载与直达跳转；模型探活 API 与模型/Key 消费归因后端无数据源，不做假入口（记边界） |
| F07 取舍回写 | **已完成** | handoff 清单逐项回填；26 入口 vs 设计 23（对应已有管理 API 的能力未删减）、备份后台只读（范围缩减）、SSO 裁决不做均记录在案 |
| F08 制品一致性 | **已完成** | 同源安全预览（不放宽 CSRF/Origin）：`VITE_BASE=/admin/` 构建 → cp 产物至 `internal/web/dist/console-admin` → `make build` → 本地 core 8080 同源 serve；登录 + F01/F02/F03/F04/F06 逐页实证如上 |

验证汇总（第二次复核更正）：console-admin为29文件、143项单测（含新增7项），`pnpm build`通过；当时lint实测仍有4个错误，原“全绿”记录不成立。后续修复见`repair-2026-10-21.md`；`go test ./internal/core/auth/...` 含新增 `TestMenuIconsRegisteredInAdmin` 通过。遗留 `types.ts:6` space-before-tab 缩进属他人未提交改动，按「保留他人改动」约定未动。

## 已看到的实质实现

| 范围 | 当前代码证据 | 判断 |
|---|---|---|
| 两层菜单与旧链接 | `internal/core/auth/menus.go` 六职责域；dynamic.ts 支持 redirect；交付、用户授权、引擎等 tab 容器已存在 | 基础组织已落地，兼容参数优先级仍需修正 |
| 租户运营 | TenantDetailDrawer、TenantOffboardingDialog、scope store、TenantScope；冻结/恢复、会话/Key 生命周期与迁移可见 | 已有实现，切范围清理和完整交互验收不足 |
| 平台权限 | platform.go 的分域读写角色、菜单裁剪、服务端管理权限路径及对应测试 | 已有实现；本次未完成不同平台角色的真实浏览验收 |
| 模型与凭证 | PlatformSecretCreateDialog、供应商/通道就地补建、状态说明、引用相关改动 | 配置链已补；显式上游诊断仍缺 |
| AI | 工作流定义/运行后台 API、AI资产页工作流tab、广场独立页面及平台下架契约 | 已有只读/部分治理；运行入口与深度详情未按设计收拢 |
| 配置与治理 | ConfigcenterDrawer、admin配置详情、路由与熔断后台读取和页面 | 已补只读可见性，不能等同于全部应急运维闭环 |
| 告警 | AlertRules 内部挂载 AlertEvents；后台当前告警/历史事件接口 | 已接入，外层文案仍说待接入；指标/日志/trace/质量排障未齐 |
| 资源与运维 | 工作负载滚动重启/操作记录、reconcile dry-run、环境泳道tab、备份恢复只读总览 | 部分运维闭环已补；备份恢复后台只读且仍独立侧栏 |
| 用量与财务 | 用量快照、配额、预算、供应商对账、审计分页/导出相关代码 | 已实现部分；模型/Key/周期消费分析仍不足 |
| 首页与诊断 | 推算趋势已移除、真实审计活动、数据源错误态、独立Diagnostics页 | 数据真实性改善；首页关注事项优先设计未落实 |

## 阻止标记“全部完成”的问题

### F01：切换租户只刷新列表，旧资源详情没有清理（P1）

`frontend/console-admin/src/modules/resources/views/Applications.vue:81-83`、`Dataservices.vue:118-120` 的 scope watcher 只调用 fetchList，没有关闭 detailVisible、清空 detailId 或选中行；其他资源页也存在同类实现。

因此打开某租户资源详情后，范围状态改变时，详情本身不随之失效；这是上下文误导风险，**不是据此宣称 API 越权**。若抽屉遮罩使顶栏无法直接点击，仍须验证详情内跨域跳转、后台范围变化等路径；本次为源码确认，未完成该场景浏览复现。

对应验收：设计第7.1节、第10节第2项。修复应统一清理抽屉、勾选行、环境/产品过滤与旧响应，且不改变资源真实归属。

### F02：旧链接原tab覆盖目标tab，与迁移规范相反（P1）

`frontend/console-admin/src/lib/router/dynamic.ts:23-35` 使用：

```ts
query: { ...defaultQuery, ...to.query }
```

设计要求目标tab优先。例：`/resources/buildruns?tab=releases&tenantId=t-acme#x` 应进入交付记录的构建tab，但当前合并结果会选择发布tab；tenantId/hash应该保留，不能把迁移目标覆盖掉。普通无tab旧链接仍可正常重定向，不能笼统称“旧链接全坏”。

对应验收：第5.1节、第10节第1项。现有dynamic.spec.ts只有3项基线测试，不足以证明此类新迁移规则正确。

### F03：可观测仅完成规则/告警事件部分，排障链路未齐（P1）

`operations/views/Observability.vue:8-29` 仍只有外层 rules tab，且告知当前告警/事件/指标/日志/trace待后续；实际 `resources/views/AlertRules.vue:45-56` 已在内层接入“告警与事件”。这应认定为**已有部分实现且层级/文案未收口**，不是告警事件完全没做。

当前后台缺少完整的跨租户指标、日志、链路、采集质量页面与管理契约，不能完成告警→实体→日志→trace的设计验收。两层tab还共用同一个query.tab，需在浏览验收中覆盖events直达和刷新。

对应 M20、D3、第10节第9项。

### F04：执行与资产仍未按任务收敛，缺少深入运行处置（P1）

`resources/views/AgentRuns.vue:35-45` 仍仅为外部AgentRun列表；工作流运行实际位于 `AiOverview.vue:149-174` 的“AI资产”tab，没有收进统一“执行记录”任务入口。

工作流定义/运行读取接口已实现，但本次所见主要是列表和节点异常tag，不能证明脱敏的准确运行详情、失败节点下钻与受控中止全部完成；原生Agent运行持久化也不能由外部AgentRun列表推断已有。

对应 M08/M09、D3和设计第8.4节。

### F05：全局范围、URL与规模化列表仍未完成（P1）

- scope store 使用sessionStorage，列表/详情大多仍局部ref；还未形成设计要求的统一URL筛选/实体详情回放。
- `resources/api.ts:52-65` 仍用全量列表本地filterPage；`AiOverview.vue:245-250,268-270` 仍批量拉数据并截取size=1000。
- `AiOverview.vue:316-317` 同时onMounted加载五类资产和工作流两类数据，与注释“懒加载”并不等价。

因此返回定位、分享筛选、超过1000项的完整引用统计及服务端分页验收仍不能算完成。不能把小数据下的可用性当规模化验收。

对应 M27/M28、D2及第10节第2/4/5项。

### F06：首页目标、模型诊断和消费分析仍有缺口（P1/P2，按原设计优先级）

- `dashboard/views/Home.vue:3-45` 仍是欢迎卡→统计/图表→动态/快捷入口，尚无设计要求的“需要关注”来源实体与异常直达；去假数据改进已做，首页任务组织未全部完成。
- `model/views/Detail.vue:13-20,39-47` 明示“无主动探活”，最近诊断仅为请求失败投影，没有显式连接诊断操作与探测时间；配置成功≠可推理的语义已正确表达，但M06/M07不是全完成。
- `resources/views/UsageOverview.vue:6-8` 明示用量当前快照、mock单价，以及模型/API密钥维度未采集；这些是诚实边界，仍说明M22中的模型/Key/时间归因没有交付。

### F07：部分设计取舍改变但未回写确认（需收口，不以入口数量单独判失败）

当前后台有26个可见页面入口，目标设计为23；新增独立“备份与恢复”“预算”“供应商对账”。备份并未进入数据服务第三tab，`DataservicesTabs.vue` 仍只有实例/引擎两tab。预算/对账本属D5深化，但已优先出现。

`Backups.vue:143-144` 明确后台仅只读，代发起恢复仍在用户端；这避免了越权捷径，但与原型“后台受控恢复”是范围取舍，需要明确记录为缩减/延期，不能把总览展示算成全部恢复管理闭环。

原实施清单依然全为 `[ ]`，审阅目录仍只有设计记录，没有新实现批次完成证据；应由实施负责人回填实际完成、延期与取舍，而不是依靠本报告替其补签验收。

### F08：运行制品与源码仍不一致，真实验收未完成（交付门槛）

本次浏览 `http://localhost:8080/admin/` 后看到“订单数 / 总营收 / 活跃用户”和原快捷入口；当前源码和本次构建已经不同。因此该运行页面不是本次完整前端实现的验收证据。

为区分制品与源码，在独立本机5177端口启动当前源码预览；登录被现有后端拒绝为 `forbidden: cross-site request`。这表明当前开发预览Origin与同源校验没有对齐，不等同于生产登录失败；本次没有放宽安全校验或修改现有服务。

当前用户打开的4186 `prototype.html` 是设计原型，也不能作为后台实现已完成的证据。本次未部署，没有真实供应商诊断、PostgreSQL迁移/集成测试、K8s重启/回收/恢复演练或多角色浏览验收。

## 本次执行的验证

| 验证 | 结果 |
|---|---|
| `frontend/console-admin` 内 `pnpm build` | 通过，含vue-tsc；Vite构建成功；有第三方PURE注释警告，不阻断构建 |
| 同目录 `pnpm test` | 通过：27个测试文件、136项测试；含API一致性脚本。存在测试环境Vue/jsdom警告，退出码0 |
| `go test ./internal/core/auth/... ./internal/core/gateway/... ./internal/core/identity/... ./internal/ai/admin/... ./internal/backup/... ./internal/configcenter/... ./internal/diagnostics/... ./internal/billing/... ./internal/security/... ./internal/web/admin/...` | 除backup初次受沙箱端口绑定限制外，其余通过；backup在允许httptest本机监听后重跑通过 |
| `go test ./cmd/core ./internal/dashboard ./internal/observability ./internal/governance ./internal/workload ./internal/environment ./internal/maas` | 通过；dashboard包无测试文件，不能将其计为已测业务行为 |
| 允许本机httptest端口后重跑 `go test ./internal/backup ./cmd/core ./internal/observability ./internal/maas` | 全部通过，排除第一次backup的环境限制 |
| 浏览当前运行后台 | 登录成功；确认旧前端制品仍在运行 |
| 浏览当前源码预览 | 页面可打开，登录被跨站请求校验阻止；未形成新页面交互验收 |
| `git diff --check` | 未通过：现有 `frontend/console-admin/src/lib/router/types.ts:6` 有space-before-tab缩进问题；本次未修改该业务文件 |

没有执行PG集成、race全量、CI或生产/供应商/恢复演练，本地通过不能替代这些证据。

本次临时启动的5177源码预览已停止，未停止或替换原有8080服务。

## 建议下一批收尾

1. 先修F01/F02，并补针对新范围和旧链接的回归；完成当前源码可访问的同源安全预览/验证环境，不关闭CSRF/Origin防护。
2. 完成M20排障最小闭环，将工作流执行迁入执行记录并补准确详情；优先补现有D1—D4缺口，暂缓继续新增D5入口。
3. 回填实施清单，注明26入口/备份只读等取舍；以真实页面、多角色与独立环境证据逐项签收。生产写入、供应商调用、恢复演练需按授权边界另行执行。
