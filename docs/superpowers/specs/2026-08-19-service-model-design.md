# 服务模型统一设计：「应用 → 服务 → 环境」三层心智

- 日期：2026-08-19
- 状态：已评审（设计对话逐节确认）
- 关联：`2026-08-17-observability-multi-dimension-design.md`（主模块 vs 应用详情维度原则）、`2026-08-09-pipeline-deploy-release-lane-design.md`（deploy/release 分离 + 泳道 L1）

## 1. 背景与问题

用户创建测试应用后「看不到服务在哪里创建」。诊断结论：**「服务」是用户最核心的心智，却是平台的隐式实体**。

当前概念现状：

| 概念 | 实体 | 问题 |
|------|------|------|
| 应用 Application | `internal/application` | 一等实体，清晰 |
| 工作负载 Workload | `internal/workload`（service/job/cronjob） | K8s 视角实体，用户心智里应是「服务的部署实例」 |
| 治理服务 Service | `internal/governance` | 与 Workload(service) 同名不同物，靠命名约定关联 |
| 多服务构建 | 流水线 buildArgs.SERVICE 参数 | 完全隐式，UI 无「本应用有几个服务」呈现 |
| 前端（vue） | 无 | 平台外手工部署，体验断层 |
| Agent/MCP | 无抽象 | 对平台只是普通 Workload，MaaS 优势未显性化 |

核心痛点（按严重度）：

1. 服务是隐式实体：用户「加一个服务」在 UI 上没有答案。
2. 双「服务」同名：Workload(service) vs 治理 Service，命名约定脆弱。
3. 多服务应用无呈现：要靠构建记录 buildArgs 反推。
4. 前端部署无路径。
5. Agent/MCP 无一等入口。

## 2. 目标与非目标

**目标**：

- 统一用户心智为「应用 → 服务 → 环境」：服务是用户声明的一等实体，环境×泳道维度的部署是服务的实例化。
- 治理 Service 由服务实体派生，双「服务」概念归一，命名约定退役。
- 前端静态托管（Vercel 式：产物进 minio、网关 serve、秒级回滚）。
- Agent 作为轻量服务类型（runtime/模型/工具注入），显性化 MaaS 优势。

**非目标**：

- 不重写 Workload/CRD 数据面（K8s 语义对齐保留，Workload 降为实现细节）。
- 不做 Agent 专属向导/试玩/统计（Playground 已有试玩，后续叠加）。
- 不做多副本生产级静态 CDN（dev 单点 serve，生产演进路径标注）。

## 3. 方案取舍

| 方案 | 描述 | 结论 |
|------|------|------|
| 1. Service 作「服务定义」 | 声明实体 + Workload 挂 ServiceID | **采纳**：与 K8s 期望态哲学一致，改动集中 |
| 2. Service 取代 Workload | 用户面只剩「服务」 | 否决：数据面全链路重写，风险极高 |
| 3. 纯前端聚合（P0） | 无新实体 | 否决：治不了根因（治理统一/静态托管无从谈起） |

存量兼容策略：**演进式**。Workload 加 `ServiceID`（nullable 向后兼容），存量启动幂等回填生成 Service；不破坏 dev 集群 paas-shop 全链路（变更管理/流水线/泳道）验证资产。

## 4. Phase 1：Service 实体与存量回填

### 4.1 领域模型

新模块 `internal/service/`：

```go
Service {
  ID, TenantID, AppID
  Name        // 应用内唯一，DNS-1035（作 K8s 资源名前缀）
  Type        // web | backend | agent | static | cron
  RepoID      // 关联 CodeRepo（static 可空）
  RepoPath    // 仓库内路径（monorepo 多服务：services/bff）
  BuildArgs   // map（多服务构建 SERVICE=bff）
  Port        // web/backend/agent 对外端口（0=不建 Service/Ingress）
  Replicas    // 期望副本（部署默认值）
  Env         // 服务级环境变量（部署时与 appconfig 合并注入）
  ModelRef    // agent 类型：绑定模型 ID
  Tools       // agent 类型：MCP 工具名列表
}
```

- Repository 双实现（memory/pg，租户隔离，模式克隆既有模块）；migration 新表 `services` + `workloads` 加 `service_id` 列。
- REST：`/api/applications/{id}/services` CRUD（composite 分发与 workloads 同款）；权限 `service:read/write` 并入 BuiltinRoles。
- **不接 prod:write**：服务是声明非运行态，声明经部署才生效，prod 门禁留在部署环节（与治理 Route 同语义）。
- Workload 加 `ServiceID`（nullable）；CRD spec 加 `serviceId`（`make manifests` 重生成，纯标签不影响 reconciler 逻辑）。

### 4.2 部署关联

- 流水线 deploy stage / Releaser.Deploy 的「找/建基线 Workload」改为优先按 `(app, env, lane, serviceID)` 匹配；新建时回填 ServiceID + 从 Service 带出 Port/Replicas/Env。
  - **Phase 1 实现注记**：Env/BuildArgs 带出归 Phase 2（workload 模型无 Env 字段，需与 appconfig 合并注入，另 `Releaser.Deploy` 流水线链路尚未传 ServiceID——按名匹配自洽，Phase 2 接通）。

### 4.3 存量回填（幂等 seed）

参照 builtin 模板版本升级机制：启动遍历无 ServiceID 的 Workload，按现有命名（`BaselineWorkloadName` 逻辑）反推服务名，`GetOrCreate` Service（workload.Type 映射：service→backend），回填 ServiceID。冲突时加 `-legacy` 后缀并在 UI 标注可改名。

### 4.4 前端

应用详情新增「服务」tab（置顶默认）：先聚合 Workload 数据（按 ServiceID/名称分组）出服务卡片；「新建服务」向导（选类型→仓库路径→端口/模型/工具）。OnServiceCreate 自动绑 CI 流水线（扩展 OnAppCreate 模式）。「部署」tab 降级为服务详情内「实例」抽屉；侧栏「工作负载」保留作跨应用运维视图。

## 5. Phase 2：流水线绑定服务（多服务构建显式化）

- pipeline 实体加 `ServiceID`（nullable = 全部服务）；StageRun 加 `ServiceID`。
- 绑定单服务：run 自动注入该 Service 的 RepoPath/BuildArgs/Port（dockerfile/buildContext 相对 RepoPath 解析）。
- 绑定全部（`serviceScope=all`）：矩阵构建——每 Service 一个 build stage，一次触发全量构建（取代手动逐个触发 N 次）。**并发限 3**，超出排队。
- imageSource 过滤：`priorBuild` 优先取同 Service 的 build 产物（StageRun.ServiceID 匹配）；`latestReady` 按 Service 过滤。
- 占位符新增 `{{service.repoPath}}`/`{{service.buildArgs}}`，ParamResolver 扩展。
- 兼容：ServiceID 为空行为不变（手填 buildArgs 照旧），存量 binding 不迁移。
- UI：服务卡「构建/部署」= 触发绑定该服务的 run；designer 显示服务绑定。

## 6. Phase 3：治理 Service 派生统一

- Service 创建（或回填）且 Type 非 static 时自动 `GetOrCreate` 治理 Service：Name 沿用现命名函数（K8s Service/Endpoints 命名链不变）、记 ServiceID、AppID 填所属应用。
- 治理页降级「发现视图」：列表只读展示派生 Service + 实例（K8s Endpoints 真源）；手动 CreateService API 保留（SDK/外部纳管）但 UI 移除新建入口。Route/Breaker 不变（天然关联服务实体）。
- 命名约定退役：governance InstanceDiscoverer 与 dataplane 泳道发现按 `paas.aitoys/service` label 优先，缺 label 回退命名推导（存量兼容）。

## 7. Phase 4：前端静态托管（StaticSite）

### 7.1 实体

`StaticSite { ID, TenantID, AppID, ServiceID, Versions []StaticVersion }`；`StaticVersion { ID, ArtifactURL, Size, Commit, BuildRunID, CreatedAt, Active }`。产物存平台级 minio 数据服务 bucket `paas-static`；无 minio 时创建拒绝并提示。

### 7.2 构建链

static 服务的 build stage 跑既有 K8s Job（DooD）：node:22 内网化镜像 `npm ci && npm run build` → 产物打 tar 上传 minio（凭证经 env 注入）→ BuildRun 记 ArtifactURL → 产 StaticVersion。**不产 Image**。

### 7.3 serve 链（网关静态路由）

- core 网关加 `internal/webstatic` handler：按 host/path 匹配 StaticSite → minio 拉取（LRU 内存缓存 + 版本 URL `Cache-Control: immutable`）→ serve，含 SPA fallback、gzip、mime 推断。
- 路由复用治理 Route Host 机制：static 服务创建自动生成 Route（host=用户域名，path=/，关联 ServiceID）；hermes ingress 通配转发到 core，域名零额外配置。
- 回滚：切 Active version 秒级（无容器重建）；版本列表一键回滚。
- StaticSite 不进 Workload/CRD。
- REST：`GET /api/applications/{id}/static-sites/{sid}`（含版本列表）、`POST .../versions/{vid}/activate`。
- 生产演进路径（标注）：多副本时 minio 直出 + core 只做路由。

## 8. Phase 5：Agent 类型

- Service.Type=`agent` + ModelRef + Tools。
- 部署时 reconciler 注入：`PAAS_AGENT_MODEL`（模型 ID，经 gateway endpoint/凭证链）、`PAAS_AGENT_TOOLS`、`PAAS_AGENT_RUNTIME_IMAGE`（env 可配，默认内网化 runtime 镜像）+ 既有 OTel/DP 注入。用户镜像实现 agent runtime 协议，system prompt 经 Env 注入。
- 服务卡显示模型/工具 tag。不做向导/试玩/统计。

## 9. 测试策略

- 后端单测（memory）：回填幂等、矩阵构建展开与并发限制、imageSource 按 Service 过滤、治理派生、webstatic handler（SPA fallback/缓存/mime）。
- fake client 测：reconciler ServiceID label、agent 注入 env。
- pg 集成测试同款覆盖。
- e2e（dev 集群）：paas-shop 四服务回填归位 + 矩阵构建全绿 + 服务卡部署 + 治理页零手动注册 + 静态站点发布回滚。
- 回归红线：全量 `go test` + 三套前端 build + paas-shop CI/CD/变更管理链路不回归。

## 10. 风险与边界

| 风险 | 处置 |
|------|------|
| 矩阵构建并发放大 | 并发限 3 起步排队串行 |
| 网关静态 serve 单点 | dev 接受，生产演进路径已标注 |
| 回填服务名与用户手建重名 | GetOrCreate 冲突加 `-legacy` 后缀，UI 标注可改名 |
| minio 未部署时静态托管不可用 | 创建时 fail-fast 提示，不静默降级 |
| Service 两实体（声明+Workload）认知负担 | UI 把 Workload 藏进服务详情实例抽屉 |
