# 服务运行与实例运维设计

日期：2026-10-17。状态：页面主要框架已实现，运行态增强与运维接口待其他 agent 实施。

## 1. 范围和目标

用户要求先由本轮固定页面框架与主要交互，其他 agent 补全具体能力。现有业务代码和其他 agent 的修改必须保留。本轮没有实现重启、驱逐、终端服务，也没有修改集群权限或部署。

目标：开发者进入一个具体服务的具体环境后，能回答“运行在哪里、哪里异常、从哪里取证、操作作用于谁、是否真正完成”。应用提供上下文，服务承载操作，交付承载发布过程。

现状的三个结构问题：

1. `/workloads` 每行实际是 Workload，却称“运行实例”，与 Pod 混淆。
2. 640px 抽屉同时承担配置修改、部署和排障，缺少可分享、可持续使用的运行工作区。
3. 详情接口无法区分运行态读取失败与没有 Pod；Pod Phase 也不能等同于容器 Ready 或业务健康。

## 2. 信息架构与目标对象

```text
产品 → 服务 → 某环境 × 某泳道的 Workload
                         ├─ 运行概览
                         ├─ 实例 → 某个 Pod → 某个容器
                         ├─ 日志
                         └─ 操作记录
全局工作负载列表 ──────────┘
```

- 保留 `/workloads`，名称改为“工作负载”。不再新增一个重复 Pod 主菜单。
- 完整运行页 `/workloads/:id?view=overview|instances|logs|operations`，浏览器实际带 `/next/`。
- 产品服务行和既有环境、泳道页面仍可快速打开 `ServiceRunDrawer`；抽屉提供“打开完整运行视图”。
- 产品、服务、环境、泳道以真实 workload 信息为准；“全部环境”不能任取第一个工作负载执行写操作。
- 顶栏环境只是浏览范围。详情页不因顶栏变化而静默换操作对象；资源目标固定展示。
- 服务发现 `/governance/services/:id` 保留其注册治理含义，不承担 Deployment 运维。

## 3. 主页面框架

```text
工作负载 / 产品 / 服务
服务名称                             刷新  运行设置  前往交付
┌ 环境 · 生产/测试 · 基线/泳道 · 工作负载类型 · Workload ID ┐
来源、观测时间与读取状态
┌ 运行概览 | 实例 | 日志 | 操作记录 ──────┐ ┌ 运行操作 ──┐
│ 就绪/期望副本 · 运行状态 · 待关注 Pod    │ │ 调整配置    │
│ 故障提示 → 实例/日志                    │ │ 滚动重启    │
│ 当前配置：镜像、域名、端口、采集路径     │ ├ 继续排障 ──┤
│ Pod 表：原因、Ready、重启、节点、操作    │ │ 指标 / 日志 │
└────────────────────────────────────────┘ │ 请求链路    │
                                           └─────────────┘
```

视觉遵循现有语义 token、PageHeader、StatePanel、原生表格横向滚动与亮暗主题。主要内容优先，右栏只放运行操作和证据入口。窄屏单列，不压缩 Pod 名称或把操作列挤出页面。可点击入口使用按钮或链接，禁止仅可鼠标点击的文字行。

本轮不在详情页直接新建部署入口：先链接产品交付，避免复用 CD 对话框时误以为配置的流水线一定部署到本页环境。未来直接部署按钮必须先展示流水线解析后的实际服务、环境和泳道，匹配后才能执行。

## 4. 已生成的页面骨架

| 文件 | 职责 | 现在可以做什么 |
|---|---|---|
| `frontend/console-user-v2/src/views/WorkloadRuntime.vue` | 运行工作区、上下文、路由分区、辅助入口 | 读现有详情，链接现有交付与可观测，打开既有运行设置 |
| `src/components/RuntimeInstanceTable.vue` | Pod 列表与筛选、诊断入口 | 展示已返回 Pod，按未就绪/失败/曾重启筛选 |
| `src/components/RuntimeInstanceDrawer.vue` | 实例详情、日志、事件、终端布局 | 实例字段与日志可读；事件/终端/重建明确待接通 |
| `src/components/RuntimeLogs.vue` | 日志快照与异步状态 | 选择 Pod、当前/上次终止、行数，读取旧日志接口 |
| `src/domain/serviceRuntime.ts` | Ready 展示与精确观测链接 | Running + 0/0 不显示为健康；链接保留环境/泳道 |
| `src/components/runtime-tabs.css` | 本工作区复用的分区样式 | 同一导航层级和焦点样式 |

表内 `src/` 路径相对 `frontend/console-user-v2/`。其他 agent 应沿这些组件接入，不重做页面骨架。

当前诚实降级：来源未确认提示始终存在；页面请求时间不冒充集群观测时间；空数组显示“未返回实例数据”；没有能力的按钮禁用并有可见原因；操作记录不把空白伪装成“从未发生操作”。旧实例结构没有 UID 和容器信息，因此不能据旧字段启用终端或重建。

## 5. 实例诊断的最终交互

实例表列固定为：Pod 名称/异常原因、Ready 与状态、累计重启、节点/IP、日志/事件/终端。待接口提供 UID、容器、conditions 后补入详情，不把基础表铺成十几列。

| 情况 | 默认诊断 | 操作边界 |
|---|---|---|
| Pending / 拉取镜像失败 | 事件与原因 | 不提供终端 |
| CrashLoopBackOff / 最近退出 | 上次终止日志、退出码、事件 | 仅 Running 容器可执行终端 |
| Running 但 Ready 不足 | 探针事件、容器日志 | 不标记为业务健康 |
| Terminating | 状态、事件、已有日志 | 不新开终端或重复重建 |
| Succeeded / Failed | 退出码、日志 | 不提供终端 |
| 无采集来源、读取失败 | 显示未知及重试 | 不显示 0 个健康实例 |

日志必须区分错误与正文，使用文本渲染，切换工作负载/Pod/容器后丢弃旧响应。多容器时明确选择容器，当前日志与上次终止日志分别请求。第一期快照足够；实时 follow 作为后续独立接口，不用轮询伪装流。

## 6. 操作语义

### 整个服务：滚动重启

- 仅适用于 `type=service` 的 Deployment。Job 的重跑、CronJob 的立即执行是不同动作，后续独立设计。
- 明确展示实际产品、服务、环境、泳道、工作负载 ID、副本和更新策略；填写原因。生产输入目标名称确认，环境类型未知按生产级要求处理。
- 副本为 0 时提示先恢复副本；未接集群/无权限/有进行中操作时说明原因。
- 服务重启走持久化期望状态 → CRD → controller。新增不可重复的 `restartToken`，必须贯通领域模型、memory/PG、CRD、K8sApplier、Pod template annotation。
- `K8sApplier` 当前完整重赋 `crd.Spec`，因此不可只 patch CRD 的临时字段，否则普通更新会清除 token 并触发额外滚动。
- PG 同一事务写 workload token 与专用 `workload_operations` 记录；memory 对应加锁。服务端幂等键与 expected revision 防并发覆盖。同一 workload 同时只受理一个维护操作，冲突返回 409。
- 持久化操作由可恢复的投影/观察任务推进；投影失败可重试，core 重启后扫描非终态恢复。不能沿用“Apply 失败仅记日志但立即显示完成”。不引入通用工作流引擎。
- 状态：`accepted → reconciling → running → succeeded|failed|superseded`；观察超时为 `unknown`，保留继续核实入口。HTTP 202 只表示已受理。
- 成功需确认 token 对应的 Deployment revision、observedGeneration、新副本 updated/ready/available 达到期望且旧版本已退场；不能只看 ready=N。并发发布改变目标 revision 时明确 superseded，不误报重启完成。

Deployment 更新由其更新策略控制。PDB 并不限制 Deployment 自身滚动更新；页面不得承诺有 PDB 就零停机。

### 单个 Pod：重建此实例

- 命名“重建此实例”，提示会丢失当前连接与容器临时状态，新 Pod 的名称/IP/UID 可能变化。
- 服务端从 workload 和租户解析 namespace，校验 managed-by、tenant、workload labels、真实 owner chain 与传入 Pod UID。
- 使用 Eviction API，保留 graceful termination，尊重 PDB。429 阻断后展示原因和重试，不自动 fallback 删除 Pod。
- 仅允许平台托管且有支持重建语义的控制器管理的实例；不对裸 Pod、已完成 Job 给通用重建按钮。
- 驱逐受理不等于替代 Pod 已就绪。操作记录观察旧 UID 退场和相同 controller 归属的新 Pod 就绪；无法唯一确定归属/已有并发扩缩容时展示结果待确认。

### 容器终端

- 界面称“容器终端”，采用 K8s exec；不把 SSH 节点登录伪装为容器调试。不开放节点 SSH、特权调试容器和任意主机命令。
- 选 Pod → 选 Running 容器 → 选择允许的 shell（`/bin/sh`、`/bin/bash`）→ 展示目标和原因 → 连接。镜像没有 shell 就明确失败，不自动注入镜像。
- 当前框架只是未连接状态展示；后续可使用 MIT 的 xterm.js，具体版本和依赖许可证在实施时锁定。不得用文本框拼假终端。
- 服务端使用 client-go remotecommand，流仅经平台代理；K8s 凭证不发到浏览器。交互式终端不能声称为“只读”。
- 会话绑定 tenant/user/workload/Pod UID/container；60 秒握手有效期、单次消费；默认空闲 5 分钟、最长 30 分钟，用户最多 2 个并发会话，可配置。
- WebSocket 升级前检查登录态、权限、同源 Origin；不能依赖普通写请求 CSRF 中间件替代升级校验。URL 不带 Bearer token。角色撤销、会话过期、Pod 退出立即关闭，不自动重连到新 UID。
- 通道消息限定 input/output/resize/exit/error/heartbeat，限制消息大小、终端尺寸和缓冲。clipboard 必须由用户手势触发。
- 审计记录连接/拒绝/结束、操作者、目标 UID/容器、理由、开始结束时间和退出原因。交互式 stdin 不等于完整命令审计；首版不记录命令正文或宣称全会话录像。

## 7. 增量 API 契约

以下是待实施契约，并非现有可用接口。全部普通响应 `{data:T}`，错误 `{error:string}`；由 route registry 登记后生成前端类型。

| 方法与路径 | 输入或输出要点 |
|---|---|
| `GET /api/workloads/{id}/runtime` | workload、observation、instances、capabilities、activeOperation；兼容保留现有 detail 接口 |
| `GET /api/workloads/{id}/instances/{pod}/events?podUID=...` | 对 UID 精确匹配的结构化事件；时间、type、reason、message、count |
| `GET /api/workloads/{id}/logs` | 在原 pod/tail/previous 上增量支持 container、podUID；正文继续 text/plain |
| `POST /api/workloads/{id}/restart` | reason、expectedRevision、Idempotency-Key；返回 202 与 operation |
| `POST /api/workloads/{id}/instances/{pod}/recreate` | podUID、reason、Idempotency-Key；返回 202 与 operation |
| `GET /api/workloads/{id}/operations` | 分页、按时间倒序；不混入交付发布记录 |
| `GET /api/workloads/{id}/operations/{operationID}` | 受理、执行、实际结果、关联证据和观察时间 |
| `POST /api/workloads/{id}/instances/{pod}/terminal-sessions` | podUID、container、shell、reason；返回会话 ID、过期时间和同源 WS 路径 |
| `GET /api/terminal-sessions/{sessionID}/stream` | 经 cookie 鉴权的 WS 升级，握手复核权限与 Pod UID |
| `DELETE /api/terminal-sessions/{sessionID}` | 只允许所有者或相应管理权限关闭 |

runtime 核心结构示意：

```ts
type Capability = { allowed: boolean; reasonCode?: string; message?: string }
type Observation = {
  source: 'kubernetes' | 'external' | 'unavailable'
  state: 'fresh' | 'stale' | 'unavailable'
  observedAt?: string // 真实数据面采集时间
  error?: string     // 脱敏错误，不能返回上游凭证
}
// instance 增量字段：uid、phase、reason、conditions、deletionTimestamp、
// owner、containers[{name,state,ready,restarts,lastExitCode,lastTerminationReason}]
// capabilities：logs、events、restart、recreate、exec；可按 Pod/容器细化。
```

权限不足返回 403；跨租户和不可见应用统一 404；UID/revision 不匹配或重复进行中 409；驱逐被 PDB 阻断 429；未启用或数据面不可用 503。未知源的空数组不能返回 `state=fresh`。allowed 是展示提示，操作请求仍须重新鉴权。

## 8. 权限与部署能力

- 读实例/事件/日志：`workload:read`，叠加租户隔离和应用可见性。
- 重启/重建：`workload:write` + 应用写权限；实际生产环境再要求 `prod:write`，不得信任客户端 env 参数。
- 终端新增 `workload:exec`，实际生产同样要求 `prod:write`，developer 不自动获得。当前 tenant:admin 通配语义必须纳入验证。
- 终端默认 feature flag 关闭，RBAC 跟随配置门控；可用性同时取决于 flag、平台权限、应用权限和集群能力。
- 现有 Pod 只读及 pods/log RBAC 不足以支持新能力。事件需 get/list/watch；驱逐需 `pods/eviction:create`；exec 按 client-go 实际传输需要配置 `pods/exec` verbs 并验证。禁止顺手授予 pods delete、通配资源或集群管理员。
- 日志/事件读取、运维写请求的 namespace 均由服务端解析；浏览器不能指定任意 cluster/namespace。

## 9. 验收要求

1. 从运行详情到具体实例日志不超过 2 次动作；日志读取不跳老版。
2. 查看/修改目标始终显示服务、环境和泳道；切顶栏环境不改变详情 ID。
3. Running+0/0、Ready 缺失、Pending、Failed、Terminating 各有正确状态，不出现绿色假健康。
4. 读失败和无实例分开；刷新失败保留旧数据并标过期，不覆盖成零。
5. 服务切换、Pod 切换和关闭抽屉时旧请求不能覆盖新目标；日志错误不出现在正文内。
6. 重复请求不会重复重启；PG/core 重启后仍能恢复观察；202 和最终完成分别显示。
7. 拒绝跨租户/跨应用/错误 UID/无权限/不当 Origin；PDB 拒绝不强删。
8. 多容器日志、无 shell 容器、权限撤销、终端断线、代理超时须有可理解的结果。
9. 验证亮暗主题、1440/1024/768/390px、键盘焦点与弹层关闭行为。
10. 单元/fake client、真实 K8s 集群、ingress WebSocket 分开报告；截图和 HTTP 200 不能替代运维能力验收。

## 10. 官方依据

- [容器内 shell 与多容器 exec](https://kubernetes.io/docs/tasks/debug/debug-application/get-shell-running-container/)
- [Pod disruption、Eviction 与 PDB 的边界](https://kubernetes.io/docs/concepts/workloads/pods/disruptions/)
- [K8s RBAC 子资源授权](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)
- [client-go remotecommand](https://pkg.go.dev/k8s.io/client-go/tools/remotecommand)
- [xterm.js MIT 许可证](https://github.com/xtermjs/xterm.js/blob/master/LICENSE)
