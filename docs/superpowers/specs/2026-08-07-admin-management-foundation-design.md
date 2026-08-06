# console-admin 管理能力基线设计

> 日期：2026-08-07
> 范围：console-admin（平台运营后台）。确立 admin 管理能力的三层模型、跨租户写端点规范、横切继承原则、UI 范式与模块路线图。
> 性质：**架构基线 spec**（不写代码）。后续每个业务模块的实施 plan 引用本文件，不重复论述横切原则。第一个实施模块 = 数据服务管理（样板）。
> 相关：`docs/superpowers/specs/2026-08-02-p1-real-platform.md`（P1.4 后台重构）、CLAUDE.md「P1.4 后台管理重构」「全模块 admin 总览扩展」。

## 背景

console-admin 当前是「半个管理后台 + 半个监控大屏」：

- **平台资产类**（模型 / 供应商 / 引擎目录 / 租户 / 用户 / 密钥 / 角色）已有完整 CRUD。
- **租户业务资源类**（应用 / 工作负载 / 数据服务 / 构建 / 镜像 / 发布 / 环境 / 服务治理 / 告警 / 配额 / 账单 / 审计）全是 `/api/admin/*` 的 `ListAll` 只读总览，**点不进详情、看不到实例、无任何运维操作**。

根因：admin 的产品定位一直没界定。P1.4 当时以「跨租户写越权风险高，资源运维仍在 console-user 租户内」一刀切只读，安全考量压过了产品完整性，矫枉过正。

## 定位

**admin = 平台运营中枢**：看穿所有租户的资源运行态 + 代运维干预 + 管理平台资产 + 审计/配额/安全。
**console-user = 租户自助**：自己建自己的业务、日常开发操作。

两者职责正交，不重复。数据服务作为「基础设施」是交叉点——租户能自助建，admin 也能代建 + 全面运维。

适用双模交付：
- **SaaS 公有**：admin = 平台运营人员，跨租户监控 + 接管处置 + 工单运维。
- **私有化**：admin = 企业 IT，代部门（租户）统一建基础设施 + 故障排查。

## 三层能力模型

| 层 | 定义 | 适用操作 |
|---|---|---|
| **L1 详情可见** | 资源总览行可点进详情，看穿运行态 | 详情聚合（spec + status + 实例 + 连接/日志等） |
| **L2 运维干预** | 对**已存在**资源代运维，不改变归属 | 启停 / 重启 / 扩缩容 / 强制删除 / 查日志 |
| **L3 代建** | admin 新建资源**指定归属租户** | 仅限基础设施类（数据服务、环境） |

### 关键边界（YAGNI + 责任清晰）

- **L3 仅限「基础设施类」资源**：数据服务实例、环境。这些归属单一、上下文轻、适合平台代建分配。
- **业务编排类不做代建**：应用 / 工作负载 / 发布 / 构建。归属与上下文太重（应用绑定、环境、镜像、回滚指针交织），admin 代建易出权责泥潭；这类资源 admin 只做 **L1 详情 + L2 运维**，创建仍走租户自助。
- **不复制 console-user**：不在 admin 把租户能做的所有事重做一遍。admin 只补「看穿 + 干预 + 基础设施代建」。

## 跨租户写端点规范（所有模块统一）

```
GET    /api/admin/<resource>                # 已有：跨租户列表（L1 入口）
GET    /api/admin/<resource>/{id}           # 新增：跨租户详情（L1）
POST   /api/admin/<resource>                # 新增：代建，body 必带 tenantId（L3，仅基础设施类）
POST   /api/admin/<resource>/{id}/<action>  # 新增：运维操作（L2），action ∈ {stop,start,restart,...}
DELETE /api/admin/<resource>/{id}           # 新增：强制删除（L2）
```

### 鉴权与上下文铁律

1. **全挂 `adminGuard`**（super_admin，复用现有 `gateway.IsPlatformAdmin`）。
2. **租户归属从资源本身取**：所有按 id 操作的端点先 DB 查资源拿 `tenantID`，**不信任请求体**。下游（K8s / 配额 / 注入）以资源所属租户的身份执行。
3. **代建 body `tenantId` 必填**：校验租户存在 → 消耗该租户配额 → 以该租户 ctx 创建（与租户自助走同一 Repository 方法，仅 ctx 来源不同）。
4. **独立端点，不复用租户 API**：admin 端点在 mux 独立注册，权限 / 审计 / 跨租户语义零污染。租户侧 `/api/<resource>/{id}/<action>` 不变。
5. **`<action>` 白名单**：每个模块在 handler 显式枚举合法 action（switch 分发），未知 action 400，杜绝任意操作注入。

## 横切机制继承（admin 特例）

| 横切 | 租户侧现状 | admin 侧 |
|---|---|---|
| **prod:write** | 强制（developer 生产只读） | **绕过**（super_admin 有权干预生产），但 UI 弹危险确认 + 审计记录 `admin:prod-intervene` |
| **配额** | Create 消耗 / Delete 回收 | 代建消耗目标租户配额（`billing.CheckAndInc` 以目标租户 ctx）；强制删除回收（`CheckAndInc(-1)`） |
| **审计** | identity/security 写操作记 | **admin 所有写操作必记**：actor=`UserIDFrom(ctx)`（super_admin）、`target_tenant=资源所属租户`、action 带 `admin:` 前缀、detail 含操作类型与目标 env |
| **多租户隔离** | Repository 强制 tenant 过滤 | `ListAll` 仅 admin 路径调用；单资源操作按 id 跨租户定位，资源不存在统一 404（不泄漏） |
| **错误脱敏 / 响应契约** | `{data:T}` / `WriteServiceError` 500 脱敏 | 同源继承（`httputil.WriteData`/`WriteDataCreated`/`WriteServiceError`） |
| **凭证掩码** | list/detail 掩码 | admin 详情同样掩码（`MaskConnection` 等），明文仅内部注入用 |

### 审计落点

- 复用 `security.AuditStore.RecordAudit`。
- 新增 admin 审计适配器（与 `identityAuditAdapter` / `authAuditAdapter` 同源模式，依赖倒置），handler 写操作成功路径调用。
- `AuditLog.Actor` = super_admin userID；新增字段或约定：`Detail` 内带 `target_tenant`（资源所属租户，区别于 actor 的「平台」租户归属）+ `admin:<action>` 前缀的 action。
- 前端：资源详情抽屉底部「操作历史」折叠区，读 `/api/admin/audit-logs?resourceType=&resourceId=` 过滤。

## admin UI 范式（前端统一）

console-admin 复用现有 SearchTable + FormDrawer + useCrud 四件套：

- **资源总览页**：表格行 `@row-click` 或行内「详情」按钮 → **详情抽屉**（不跳页，`el-drawer` size 大）。
- **详情抽屉分区**：
  1. 基本信息（ID / 名称 / 类型 / 租户 / 环境 / 状态 / 创建时间）。
  2. **实例/运行态**（L1 核心）：数据服务 Pod 级表格、工作负载 Pod 级 + 日志、连接信息（掩码）。
  3. **运维操作按钮组**（L2）：按状态动态启用/禁用（如 running 才能停、stopped 才能启）。
  4. 「操作历史」折叠（审计）。
- **代建**（仅数据服务 / 环境页）：顶部「新建」按钮 → FormDrawer 含**租户选择器**（必选）+ 资源规格字段。提交 = `POST /api/admin/<resource>` body `{tenantId, ...spec}`。
- **危险操作**：复用 `useDangerConfirm`（console-admin 侧实现等价；生产资源 `isProd:true` 红警示 + 输入名称确认），super_admin 也不跳过 UI 确认。
- **轮询**：实例/运行态区块在抽屉打开时 10s 轮询，`onUnmounted clearInterval`（与 console-user 同款清理纪律）。

## 实例/运行态数据来源（L1 关键）

- **数据服务实例**：core clientset 读 K8s StatefulSet + Pod（reconciler 已建 STS，跨租户按 `paas.aitoys/tenant` + resource.id label 定位）。返 Pod 名 / 状态 / 就绪 / 重启 / 启动时间 / 节点 / IP。
- **工作负载实例 / 日志**：复用现有 `workload.StatusReader.Instances / PodLogs`，admin 版按 resource.id 跨租户定位（同样 label 校验防越权，`previous=true` 取上次终止日志）。
- **降级**：无 clientset（纯内存 / dev 非 k8s）返空数组 + 友好提示「未接入集群数据面」（与 observability real 同构，不 5xx / panic）。

## 模块实施路线图

每个模块独立 plan，引用本基线 spec，按数据服务样板推进：

| 优先级 | 模块 | 能力 | 备注 |
|---|---|---|---|
| **P1（样板）** | 数据服务 | L1 详情+实例 + L2 启停/重启/扩缩/删 + L3 代建 | 用户首例，最完整，定模式 |
| P1 | 环境 | L3 代建 + L1 详情 | 轻量，随 P1 一起 |
| P2 | 工作负载 | L1 详情+实例+日志 + L2 扩缩/重启/删 | |
| P2 | 应用 | L1 详情（运行态聚合）+ L2 删 | 不代建 |
| P3 | DevOps | 构建 L1+L2(重试/删) / 镜像 L1 / 发布 L1+L2(回滚/删) | 不代建 |
| P4 | 服务治理 | 服务/路由/熔断 L1 + L2(启停/删) | 不代建 |
| P4 | 可观测 | 告警规则 L1 + L2(启停/删) | metrics/logs/traces 时序不适详情 |
| P4 | 配额 / 账单 | L1 详情 + L2(调整配额 / 标记已付) | 调整配额即管理 |
| P4 | 安全 | L1 详情（密钥掩码）+ L2(删) | 不代建 |

模型 / 供应商 / 引擎目录已是完整 CRUD，**不重做**。租户 / 用户 / 密钥已是完整 CRUD，**不重做**。

## 验证策略（每个模块 plan 落实）

- **后端单测**：admin 端点跨租户读取（资源所属租户正确）、未知 action 400、代建消耗目标租户配额（超额 429）、强制删除回收配额、审计落库（actor + target_tenant + admin: 前缀）、prod 绕过（super_admin 可写生产）。
- **前端**：vue-tsc + pnpm build 三套通过。
- **k8s e2e**（`./scripts/deploy-k8s.sh`）：admin 登录 → 跨租户资源详情可看实例 → 运维操作生效（启停 reflected 到 K8s）→ 代建实例落目标租户 + 消耗配额 → 审计记录可见。

## 非目标（留后续）

- **平台共享实例池**：admin 建的实例归属「平台」供多租户共享分配/回收。本期代建仅「代某租户建」，共享池留后续。
- **admin 操作的细粒度审计查询 / 导出**：本期只落库 + 详情抽屉折叠区展示。
- **批量运维操作**（批量启停 / 批量删）：本期单资源操作，批量留后续。
- **admin 代建的「代签发放」工作流**（租户审批 admin 代建请求）：本期 admin 直接代建 + 审计，无审批流。
- **应用 / 业务编排类代建**：明确不做（见关键边界）。

## 第一个实施 plan 边界

紧随本 spec 的 plan = **数据服务管理 P1**：
- 后端：`GET/POST /api/admin/dataservices`、`GET/DELETE /api/admin/dataservices/{id}`、`POST /api/admin/dataservices/{id}/{stop|start|restart|scale}`、实例信息从 K8s 读、代建消耗目标租户配额、审计落库。
- 前端：`modules/resources/views/Dataservices.vue` 加行点击详情抽屉（实例 + 连接掩码 + 运维按钮 + 操作历史）+ 代建 FormDrawer（租户选择器 + 规格）。
- 环境 L3 代建随该 plan 一起（轻）。
- 作为 P2+ 模块的实施样板。
