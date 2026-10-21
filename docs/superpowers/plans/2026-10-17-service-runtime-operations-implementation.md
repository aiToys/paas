# 服务运行页面与运维能力交接清单

日期：2026-10-17。设计真源：[服务运行与实例运维设计](../specs/2026-10-17-service-runtime-operations-design.md)。

## 已由本轮固定

- 新页面 `/next/workloads/:id`，概览/实例/日志/操作记录四分区，真实环境和泳道上下文。
- 工作负载列表与快览抽屉进入完整页面的入口；全局名称修正。
- `RuntimeInstanceTable`、`RuntimeInstanceDrawer`、`RuntimeLogs`、`serviceRuntime`，沿用现有 theme token。
- 当前详情和日志读取；既有运行设置保留。新增运维能力使用可解释的不可用状态。
- 不要重写主框架、导航层级或引入第二套组件系统；不要恢复假计数、假终端或直接操作节点的 SSH 按钮。

以下为任务划分，不代表已经派发其他 agent。开始前必须再次检查 dirty tree，避免覆盖同时开发的文件。

## 任务 A：运行态、实例与事件接口

建议责任：后端/数据面 agent。先做本任务再接操作按钮。

- [x] 新增 `/api/workloads/{id}/runtime`：采集来源、观测时间、错误状态、能力和容器/Pod UID 字段。
- [x] 保留既有 detail 响应，扩展 StatusReader 或新接口，更新所有实现与测试。
- [x] Pod reason/conditions/Ready 分开展示；不能只取最后一个容器错误或依据 0/0 推断就绪。
- [x] 所有读取验证租户和应用范围；namespace、owner chain 由后端核实。
- [x] 增加 UID 精确过滤的事件读取和必要的 events RBAC。
- [x] 旧 logs 增加 container/podUID，错误结构与正文区分，保持老调用兼容。
- [x] route registry/OpenAPI 与前端生成类型同步。

验收：非集群明确 unavailable；读失败不变零；跨租户 404；UID 变化 409；双容器日志选中正确容器；事件失败不是“无事件”。

## 任务 B：服务滚动重启与可恢复操作记录

建议责任：后端/控制器 agent。与 A 的 runtime 字段对齐后实施。

- [x] 明确 restartToken 的领域/PG/memory/CRD/Applier/controller 全路径，追加 migration，不覆写存量 migration。
- [x] 同事务保存期望状态和 `workload_operations`，请求幂等、revision 校验、同目标并发冲突。
- [x] 可恢复投影与观察任务；Core 重启扫描非终态；失败与 superseded 有明确结果。
- [x] 成功必须按此次 token 对应的 Deployment revision 判断，不能复用旧 Ready。
- [x] 仅 Deployment 支持，副本 0、未知环境、无数据面或权限不足时返回明确能力原因。
- [x] 操作列表/详情接口、权限、审计、OpenAPI。

验收：相同幂等键仅一次 rollout；普通 Apply 不清 token；Core 重启不丢进度；新发布覆盖时不是假成功；生产按真实环境鉴权。

## 任务 C：单实例重建

建议责任：数据面 agent。依赖 A、B 的目标解析和操作记录基础。

- [x] UID 前置条件、租户/workload/managed-by labels 和 owner chain 校验。
- [x] 使用 Eviction，保留 PDB/优雅退出，增加受控 pods/eviction RBAC。
- [x] 拒绝裸 Pod 和不适用 workload；PDB 429 不转强删。
- [x] 受理与替代实例就绪分别跟踪；并发变更造成无法确认时显示待核实。

验收：无 pods delete 权限也能正常工作；PDB 阻断有原因；错误 UID 不驱逐；连接中断与新 UID 在确认中写清。

## 任务 D：容器终端服务

建议责任：终端/安全 agent。依赖 A 的 UID、容器和能力，不依赖浏览器自行拼 K8s URL。

- [x] 默认关闭的 feature flag，与最小 pods/exec RBAC 同步。
- [x] `workload:exec` + 应用权限 + 生产额外权限；不自动授权全部 developer。
- [x] 会话创建、同源 WS 升级、关闭接口与 client-go 远程命令桥接。
- [x] 租户/用户/UID/容器绑定、单次握手、超时、并发限制、退出与撤权关闭。
- [x] 无 shell 失败、Pod 退出、代理中断和过期错误语义；不自动连新 Pod。
- [x] 审计元数据，日志脱敏；URL 不放 token，K8s 凭证不入浏览器。
- [x] 通过 Helm ingress 真实验证 WebSocket 传输和尺寸变化，不只测 localhost。

验收：跨租户、假 Origin、重复握手、过期/撤权全部拒绝；真实容器可交互并正确退出；无高权限节点访问。

## 任务 E：前端接入，沿用已生成框架

建议责任：前端 agent。对接 A 后优先消除来源不明状态，再依次开放 B/C/D。

- [x] `WorkloadRuntime.vue` 改消费新 runtime；根据 observation 显示来源、时间、过期或不可用。
- [x] `RuntimeInstanceTable.vue` 使用服务端 reason/conditions，保持表列克制；按 UID 做稳定 key 和选择校验。
- [x] `RuntimeLogs.vue` 加容器选择、UID；保持错误独立和请求竞态保护。初期仍是日志快照。
- [x] `RuntimeInstanceDrawer.vue` 实现事件；终端区替换为锁版本的 MIT xterm.js，销毁时关闭会话和尺寸观察器。
- [x] 滚动重启确认：真实服务/环境/泳道/副本/策略/原因，生产输入名称。
- [x] 重建确认：UID/容器临时数据/连接中断说明，PDB 失败展示，不出现强制按钮。
- [x] 操作记录展示受理、执行、完成时间、操作者、理由、目标和证据；202 文案“已受理”。
- [x] 所有可用性来自 capabilities，但操作服务端仍鉴权；不能靠隐藏按钮保证权限。
- [x] 页面可见时按需轮询，离开/关闭即停止，未启用的模块不轮询。
- [x] 后续优化既有 `ServiceRunDrawer` 的伸缩/域名确认与权限展示，使用资源实际环境，未知环境严格处理。

验收：从运行详情进入具体实例日志 ≤2 次动作；切顶栏环境不换目标；快速切换响应不串；空/失败/403/409/429/503 有明确状态；不重新设计页面主布局。

## 协作与验证顺序

```text
A 运行态契约 ─┬─ B 滚动重启 ─ C 重建 ─┐
              ├─ D 终端 ──────────────┼─ E 按能力逐项接入 → 真集群验收
              └─ E 读取/日志/事件 ────┘
```

- 后端任务优先限定自己的包和文件；共同修改 handler/main/RBAC 前先沟通合并点。
- 前端 E 拥有上述新框架组件，其他任务不同时改同组件。
- 本计划不授权提交、push、改线上权限或部署；实施验证与发布分别按会话授权处理。
- 本地回归：在 `frontend/` 下运行 `pnpm --filter @paas/console-user-v2 test:ux`、`lint`、`build`。
- 后端跑对应 handler/repository/controller 测试，PG 单独验证。实际集群的 PDB、exec、ingress 和权限需另有真实证据。
- 构建成功与页面截图只证明前端框架可用，不能宣称服务运维链路已完成。

## 本轮框架验证

- `test:ux`：14 项通过，含 Running+0/0、未完全就绪和观测链接目标不丢失。
- `lint`：通过，无新增警告；`build`：vue-tsc 和 Vite 构建通过。
- 实际本地 5176：工作负载列表进入运行详情，展示现有详情并明确来源未知；没有返回 Pod 时未显示“服务已停止”。
- 隔离验收代理：仅本机 9083/5178，拒绝全部写请求。使用名称带“交互验收样例”的工作负载，验证三 Pod 表格、未就绪原因、曾重启筛选、诊断详情、日志 503 错误独立展示、上次终止日志成功，以及终端按钮禁用。
- 390/768/1024/1440px DOM 尺寸检查无页面横向溢出；窄屏表格独立横向滚动；1440px 表格及全部操作列无需横向滚动。亮色实际页与暗色隔离样例均检查。
- 未测试真实集群重启、PDB 驱逐、事件或 exec；这些能力尚未实现。
- 截图位于 `docs/reviews/2026-10-17-console-next/`：`service-runtime-overview-local.jpg` 是实际本地页面；`service-runtime-instances-test-data.jpg` 和 `service-runtime-terminal-framework-test-data.jpg` 为明确标注的测试数据，不是集群验收证据。
