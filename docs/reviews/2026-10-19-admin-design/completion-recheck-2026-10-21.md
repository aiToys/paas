# 管理后台第二次完成情况复核

日期：2026-10-21（Asia/Shanghai）。基线：当前 `console-v2` 工作树、原统一设计、实施清单和第一次复核报告。

## 结论

**上一轮的若干具体交互缺陷已修，但整体仍不能标记全部完成。** 临时文件规范已完成并验证生效；后台管理的原设计范围仍有明确延期项，新增首页也仍有实际功能缺口。

本次只检查和验证，并新增本报告；没有修改业务代码、提交、部署或触发生产操作。日志全部存放在项目根目录 `tmp/admin-recheck-2026-10-21/`，Git已忽略。

## 对上次问题的重新判定

| 问题 | 当前证据 | 本轮判定 |
|---|---|---|
| F01 切租户详情残留 | `useScopeChange`先reset再refresh；Applications/Dataservices/Workloads等已清抽屉与详情ID；新增3项回归通过 | 所查具体页面与基础规则已修；不据此推断全部复杂抽屉生命周期均已验收 |
| F02 旧链接tab优先级 | dynamic.ts已改为`{...to.query,...defaultQuery}`，新增4项回归通过 | 已修，原tenantId/hash保留 |
| F03 可观测 | 外层规则/事件双tab，AlertRules不再嵌套；页面明确指标/日志/trace待聚合API | 导航缺陷已修，原M20管理排障范围仍未完成 |
| F04 执行记录 | AgentRuns新增工作流运行tab，资产页不再承载完整运行列表 | 入口已收敛；运行精确详情、节点下钻、受控处置不能仅凭列表视作完成 |
| F05 分页/URL/懒加载 | 工作流按tab首次加载已改；清单仍明确服务端分页和统一URL筛选延期 | 部分完成 |
| F06 首页/诊断/用量归因 | AttentionPanel已增加三源关注事项；模型探活、模型/Key归因仍在清单剩余项 | 部分完成，首页还有下述R01—R03 |
| F07 设计取舍 | 已回填26入口、备份只读和SSO不做等取舍 | 回填已做，部分勾选与实际范围仍不一致，见R04 |
| F08 运行制品与验收 | 实施方记录了先前同源预览；本轮8080/5177未监听，只有用户端5176在运行 | 本轮未独立重做新后台浏览验收，不否定先前记录，也不将其当本轮实测；PG/CI/真实运维仍缺证据 |

## 仍需修复或明确延期的项目

### R01 首页关注事项未精确定位实体（P1）

`frontend/console-admin/src/modules/dashboard/components/AttentionPanel.vue:45-81` 点击处理只跳：

- 失败运行 → `/resources/pipelineruns`
- 告警 → `/operations/observability?tab=events`
- 异常负载 → `/resources/workloads`

没有传入运行/负载/规则ID、目标租户或环境，也没有显式切换浏览范围；PipelineRuns/Workloads详情仍依赖列表按钮设置局部detailId。因此这只是“进入列表”，不是设计要求的“打开准确运行/实体”。如果当前浏览范围是其他租户，还可能看不到刚点的事项。

修复要求：携带可信实体ID和归属上下文，目标页根据URL打开准确详情；保持服务端归属校验，不能为实现深链改变登录身份。验证全租户与已选另一租户两种场景。

### R02 首页先取前50条再筛异常，会漏报（P1）

同文件 `load()` 先调用三个列表函数 `{page:1,size:50}`，随后对`records`筛`failed`或就绪副本不足，再取5条。

`resources/api.ts` 的`filterPage`已先分页切片，PipelineRun/Workload请求未按异常状态过滤：如果前50条都正常、第51条失败，关注事项会漏掉该失败；所有源均成功且本页筛不到异常时，还会显示“暂无需要关注的事项”。

修复要求：先在服务端或完整可证明覆盖的数据集中筛异常，再限制展示数量；缺少完整覆盖时明示采样/覆盖范围，不能宣称全平台无事项。至少验证“50条正常+第51条异常”。

### R03 告警判断条件被固定显示成≥（P2）

AttentionPanel第71行固定渲染`value ≥ threshold`，而`AdminAlert.operator`及后端规则支持`>`、`>=`、`<`、`<=`。例如低水位告警实际`3 < 5`却显示`3 ≥ 5`，会误导排障。

修复要求：按返回的operator显示实际判断条件，并为未知operator提供诚实回退。

### R04 清单状态仍存在事实不一致

- 可观测、用量完整维度被勾为`[x]`，但备注和剩余项仍明确指标/日志/trace、模型/Key归因未完成；应拆分子项，避免将“承认缺口”视作交付功能。
- “平台凭证创建抽屉”被标未完成，但`ProviderFormDrawer.vue:68-99`已有新建凭证按钮、`PlatformSecretCreateDialog`和created回填；应明确究竟缺的是独立admin契约/角色控制，还是UI功能，不能笼统写UI未做。
- 实施记录声称147项单测，本轮实际为**29文件、143项**；136+新增7项也应是143。应修正文档计数，测试通过事实不受影响。

### 尚未完成的原设计范围

实施清单仍明确保留：服务端分页与统一URL筛选、模型通道受控探活、模型/Key消费归因、指标/日志/trace后台聚合、命令面板等；备份后台仍为只读，是记录在案的缩减范围。没有这些能力与独立验收证据，不能对原完整方案宣称“全部完成”。

### R05 全量lint未通过（交付检查）

实际运行`pnpm lint`退出码1：4 errors、2182 warnings。

- `src/app/main.ts:24:8`：未使用的`locale`导入（工作树改动文件）。
- `src/modules/operations/views/Diagnostics.vue:321:12`：未使用的catch参数`e`（本轮新增管理页面）。
- `src/lib/http/interceptors.test.ts:17:10,46:12`：两处explicit-any；该文件不在当前修改列表，属于既有问题，不能归因于本次改造。

因此不能沿用“lint全绿”结论；修复应保持范围清楚，不用全库自动格式化掩盖差异。warnings不等同于此次退出失败原因。

## 本轮验证

| 检查 | 结果 |
|---|---|
| console-admin `pnpm test` | 通过：29个文件、143项测试，含F01/F02新增回归 |
| console-admin `pnpm build` | 通过，包含vue-tsc与Vite构建 |
| console-admin `pnpm lint` | 未通过：4个错误、2182个警告，错误位置与归属见R05 |
| 后端相关包 | `go test ./cmd/core ./internal/core/auth/... ./internal/core/gateway/... ./internal/core/identity/... ./internal/ai/admin/... ./internal/backup/... ./internal/configcenter/... ./internal/diagnostics/... ./internal/billing/... ./internal/security/... ./internal/web/admin/...` 全部通过；PG目录显示无测试文件不代表集成验收 |
| 临时文件规范 | CONTRIBUTING、AGENTS与CLAUDE入口规则存在；`git check-ignore -v`确认根目录`tmp/`被忽略；Docker上下文排除tmp；根目录未发现散落png/jpg |

没有运行PG集成、CI、真实供应商探测、集群写入或恢复演练；本轮没有重启现有服务。原始测试/构建/检查日志位于`tmp/admin-recheck-2026-10-21/`，属于本地临时证据。

## 后续修复索引

本报告保留修复前的复核事实。R01–R05的代码修复、后端1000条窗口补查与本地验证结果见[修复记录](repair-2026-10-21.md)；原完整方案的延期能力及独立环境验收仍未完成。
