# 配置中心应用维度改造（App-Centric ConfigCenter）

日期：2026-08-29
状态：待审阅

## 1. 背景与问题

现行配置中心是 2017 年 Apollo 式模型：手工创建 Namespace + 可选关联 Service（弱关联）+ 客户端按 nsID 拉取。问题：

- **基础设施概念泄漏给开发者**：用户要先理解「命名空间」才能配一条 KV，与应用维度脱节
- **应用零接入路径**：paas-shop dogfooding 至今未接入配置中心——集成模型不好用的实证
- **不符合云原生标准**：现代配置归属跟工作负载/应用走（K8s ConfigMap 同 namespace），不存在「先建 ns 再关联」动作

对照现代分层（已覆盖项）：appconfig≈ConfigMap 静态层 ✅、GitOps ✅、Secret 分离+加密 ✅。本改造补齐**应用维度的动态运行时配置层**（feature flag/热更新 KV），保留版本/发布/回滚既有能力。

## 2. 目标与非目标

**目标**：
1. 开发者主路径 = 应用详情直接管动态配置，零 namespace 心智
2. 客户端按**应用名**发现配置（不暴露 nsID）
3. 保留版本/发布/回滚/审计（既有强项不动）
4. 跨应用共享配置保留为平台级高级用法（治理方场景：第三方对接参数/全局开关）
5. paas-shop 真实接入验证（dogfooding 闭环）

**非目标（YAGNI）**：
- 长连接 watch/推送（维持客户端轮询 version 比对）
- 灰度下发（先 1 实例再全量）——留后续
- OpenFeature 标准 SDK——留后续
- 迁移掉手工 namespace 的存量数据（兼容保留）

## 3. 核心设计

### 3.1 Namespace 双 scope（数据模型）

`Namespace` 加两字段（migration 增量，`ADD COLUMN IF NOT EXISTS`）：

```go
Scope string `json:"scope"` // "app"（应用派生，默认）| "shared"（跨应用共享，治理方手工建）
AppID string `json:"appId,omitempty"` // scope=app 时归属应用
```

- **scope=app**：由应用详情页首次写配置时 `EnsureByApp(ctx, appID)` 懒建——name 规则 `app-<appID>`（与 BaselineWorkloadName 等既有派生命名风格一致），幂等（存在即返回）。用户永远不手工建它。
- **scope=shared**：现行为完全保留（手工创建、可选关联 ServiceID）。入口收进「平台能力 → 配置中心」的高级区，列表页默认按应用分组视图。
- 存量数据迁移：UPDATE 无 scope 行置 `scope='shared'`（存量全是手工建的）。

### 3.2 应用维度 REST（开发者主路径）

挂在既有 application composite 下（与 configs/static 并列）：

```
GET    /api/applications/{id}/dynamic-configs            列 draft 项（自动 EnsureByApp）
POST   /api/applications/{id}/dynamic-configs            upsert 项
DELETE /api/applications/{id}/dynamic-configs/{itemId}   删项
POST   /api/applications/{id}/dynamic-configs/publish    发布
GET    /api/applications/{id}/dynamic-configs/publishes  发布历史
GET    /api/applications/{id}/dynamic-configs/published  当前生效
```

- 复用 configcenter.Repository（handler 加 `serveAppDynamicConfigs` 分发，内部经 `EnsureByApp` 拿 ns 再走既有 item/publish 逻辑）——**零新仓储方法除 EnsureByApp 外**
- 权限：`application:read/write`（应用资产归应用权限域，不再要求 governance:write；AppGuard 联动：受限应用写动态配置需 `write` 动作）
- 删除应用级联：`EnsureByApp` 的 ns 挂 AppID，appCascadeDeleter 级联删（与 workload/appconfig 同款）

### 3.3 客户端发现（按应用名）

```
GET /api/configcenter/apps/{appName}/published
```

- appName 租户内唯一（Application.Name 已保证）；服务端 appName→appID→ns（scope=app）→active 快照
- 鉴权：API Key（租户身份即解析域），数据面服务无 cookie 场景
- 响应 shape 与既有 published 端点一致：`{published,version,snapshot}`（去掉 publishId——客户端不关心）
- 既有 `/api/configcenter/namespaces/{id}/published` 保留（shared 用）

### 3.4 前端

- **应用详情「配置」tab** 加「动态配置」子区（与静态 env/secret 并列）：draft KV 表 + 发布按钮 + 版本历史 + 回滚 + 「当前生效」视图。发布走 `confirmDangerous`（生产环境）。
- **ConfigCenter.vue 改造**：默认视图改为「按应用分组」（左侧应用列表 + 右侧该应用配置，等价于应用详情入口的聚合视图）；「共享配置」折叠区保留手工 namespace CRUD（治理方场景）。
- console-user 应用详情 tab 分组「资源（资源绑定/配置/用量）」不变，动态配置并入「配置」tab。

### 3.5 paas-shop 接入（dogfooding 验收标准）

chatbot 服务增加动态配置消费：启动拉取 + 60s 轮询 `/api/configcenter/apps/paas-shop-chatbot/published`（经平台 API Key），管理「欢迎语/推荐 topK」等运行时参数，在平台上改配置 → 不重启生效。examples 仓库独立改动，不进 Platform Core。

## 4. 横切

- **多租户**：EnsureByApp / 发现端点全路径 ctx tenant 过滤（与既有 Repository 一致）
- **审计**：应用维度 publish/rollback 记审计（configcenter handler 注入 AuditRecorder，action 前缀 `configcenter_`）
- **OpenAPI**：新端点全部 Operation 登记；Perm 标注 application:read/write
- **测试**：EnsureByApp 幂等/跨租户隔离/scope 迁移；发现端点 appName 解析与 404 不泄漏；handler 权限矩阵

## 5. 实施切片（单 plan，5 任务量级）

1. model + migration + EnsureByApp（memory/pg）
2. 应用维度 REST + 权限 + 级联删
3. 按应用名发现端点
4. 前端（应用详情动态配置 + ConfigCenter 双视图）
5. paas-shop 接入 + e2e

## 6. 留后续

- 长连接 watch、灰度下发、OpenFeature SDK
- shared namespace 应用侧引用（应用配置 import 共享段，Nacos common.yml 模式）
- 配置变更 webhook 通知
