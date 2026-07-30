# 服务治理切片设计：服务注册与发现

> 蓝图优先级 #4「服务治理（含配置中心）」的首个子切片。治理四件套（注册发现 / 配置中心 / API 网关 / 熔断）中的**基石**：没有注册发现，微服务的消费-提供解耦无从谈起。
>
> 本切片只做**注册中心（控制面管理 + 发现 API）**，配置中心 / API 网关 / 熔断各自后续切片。YAGNI：不在开源起步期引入真实数据面 SDK 接入（参考 zeus，留后续）。

## 定位

服务治理属「平台能力（横切）」维度——所有应用共享，不归属单一应用（即便服务定义挂靠某应用）。与「资源中心（数据服务 Add-on）」「工作负载」正交。

服务治理页挂在 console-user 侧栏「平台能力 → 服务治理」，独立菜单（非应用详情子页），体现其横切定位。

## 范围（本切片）

### 实体

```
Service（服务定义）
  ID, TenantID, Name（租户内唯一服务名）, AppID（归属应用，可选）, EnvID,
  Protocol（http|grpc）, Port, Desc, UpdatedAt

Instance（服务实例 = 服务的一个运行点）
  ID, TenantID, ServiceID, Addr（host:port）, Status（healthy|unhealthy）,
  LaneID（预留，基线=default；实例可带泳道标签，本期不实现路由）,
  Meta（map，扩展点）, UpdatedAt
```

### Repository（单 Store 实现，方法带前缀避免重名）

- `ListServices(ctx, envID, appID) / GetService(ctx, id) / CreateService / DeleteService`
- `ListInstances(ctx, serviceID) / RegisterInstance / DeregisterInstance / Heartbeat(ctx, id)`
- 全方法从 ctx 取租户强制过滤；跨租户访问统一 not found（不泄漏存在性）。

### mock 行为

- 实例注册即 `healthy`，无真实健康检查。
- `Heartbeat` 更新 `UpdatedAt`（消费方可据 `UpdatedAt` 判断存活，本期不做过期剔除，留后续数据面接入）。
- 不接真实数据面 SDK / Sidecar / K8s endpoints；进程内 mock。

### REST API

```
GET    /api/services?envId=&appId=        服务列表（租户隔离，按环境/应用过滤）
POST   /api/services                       注册服务（生产需 prod:write）
GET    /api/services/{id}                  服务详情（含实例列表）
DELETE /api/services/{id}                  注销服务（生产需 prod:write）
POST   /api/services/{id}/instances        注册实例
DELETE /api/services/{id}/instances/{iid}  注销实例（生产需 prod:write）
PUT    /api/instances/{iid}/heartbeat      心跳
```

### 权限

- `governance:read` / `governance:write`（并入 BuiltinRoles：admin/dev 读写，viewer 只读）。
- 生产环境注册/注销服务/实例需额外 `prod:write`（developer 生产只读）。`EnvTypeResolver` 依赖倒置，由 environment.Repository 注入。
- **横切继承**：生产安全防护（`prod:write` RBAC + `useDangerConfirm` + 视觉强隔离）自动生效——切片只关注业务逻辑。

### 多租户

服务治理是租户私有（租户内服务发现），Repository 强制 tenant 过滤；不平台级共享（与模型目录不同）。

## 前端

- 侧栏「平台能力 → 服务治理」→ `/governance`：服务列表（按顶栏 scope 当前环境过滤），注册/注销服务。
- `/governance/:id` 服务详情：实例表（addr / status / lane / 最后心跳）+ 注册/注销实例。
- 生产注销走 `useDangerConfirm`（输入名称确认）。

## 不做（YAGNI / 后续）

- 配置中心（动态配置 + 版本/灰度）—— 紧接的下个治理子切片。
- API 网关 / 熔断降级 —— 后续。
- 泳道路由（染色 + 降级 default）—— Instance.LaneID 预留，路由归服务治理后续。
- 真实数据面 SDK / Sidecar / K8s endpoints 接入 —— 参考 zeus，留后续。
- 实例过期自动剔除 —— 留数据面接入期。
