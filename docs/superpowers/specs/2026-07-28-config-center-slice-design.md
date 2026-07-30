# 服务治理切片设计：配置中心

> 治理四件套第二个子切片。与「应用配置」（工作负载级、静态、重启注入）正交：配置中心是**运行时动态**配置，跨实例共享，**版本/发布/回滚**，客户端监听热更新。
>
> 两者关系见蓝图「配置中心 vs 应用配置」。本切片让产品区分落地为两套独立实现。

## 定位

配置中心属「平台能力（横切）」，**独立于物理环境**（namespace 是逻辑隔离单元，不绑定 prod/test）。因此不接入 `EnvTypeResolver` / `prod:write`，权限统一用 `governance:read/write`；发布/回滚作为高危操作走 `useDangerConfirm` 二次确认。

侧栏「平台能力 → 配置中心」，与「服务治理（注册中心）」并列（均治理子能力）。

## 范围

### 实体

```
Namespace（命名空间 = 配置逻辑隔离单元）
  ID, TenantID, Name（租户内唯一）, Desc, UpdatedAt

ConfigItem（配置项 = namespace 下的一个键值，draft 可编辑）
  ID, TenantID, NamespaceID, Key, Value, Type（text|json|yaml）, UpdatedAt

Publish（发布 = namespace 配置的不可变版本快照）
  ID, TenantID, NamespaceID, Version（namespace 内单调递增）,
  Snapshot（map[key]value，JSON 存储）, Status（active|rolled-back）, CreatedAt
```

### 核心流程

- **编辑**：ConfigItem Upsert（同 namespace+key 更新）—— 仅改 draft，不影响已发布。
- **发布**：快照当前 namespace 全部 item -> 生成 Publish(version=N+1, status=active)，该 namespace 其他 active 标 rolled-back。**不可变**。
- **发现**（客户端拉取）：返回当前 active Publish 的 snapshot + version（客户端据 version 判断是否需刷新）。
- **回滚**：激活某个历史 rolled-back Publish 为 active，当前 active 改 rolled-back。draft item 不变（published 视角回退）。

### Repository（单 Store，方法带前缀）

- namespace：`ListNamespaces / GetNamespace / CreateNamespace / DeleteNamespace`
- item：`ListItems(nsID) / UpsertItem / DeleteItem`
- publish：`ListPublishes(nsID) / CreatePublish(nsID)（生成快照+版本） / RollbackPublish(pid) / ActivePublish(nsID)（发现）`
- 全方法租户强制过滤；跨租户 not found（不泄漏）。

### REST API

```
GET    /api/configcenter/namespaces                  命名空间列表
POST   /api/configcenter/namespaces                  创建命名空间
GET    /api/configcenter/namespaces/{id}             命名空间详情
DELETE /api/configcenter/namespaces/{id}             删除（级联清 item+publish）
GET    /api/configcenter/namespaces/{id}/items       配置项列表（draft）
POST   /api/configcenter/namespaces/{id}/items       upsert 配置项
DELETE /api/configcenter/namespaces/{id}/items/{iid} 删除配置项
POST   /api/configcenter/namespaces/{id}/publish     发布（生成版本快照）
GET    /api/configcenter/namespaces/{id}/publishes   发布历史
GET    /api/configcenter/namespaces/{id}/published   客户端发现（当前 active 快照 + version）
POST   /api/configcenter/publishes/{pid}/rollback    回滚到某历史发布
```

### 权限

- `governance:read` / `governance:write`（admin/dev 读写，viewer 只读）——复用服务治理切片已加入 BuiltinRoles 的权限，无需新增。
- 发布/回滚是高危：前端走 `useDangerConfirm`（统一二次确认，不区分环境）。
- 客户端发现接口仅需 `governance:read`。

### 多租户

namespace 租户私有，Repository 强制 tenant 过滤。

## 不做（YAGNI / 后续）

- 客户端长轮询 / WebSocket 监听（本期客户端主动拉 version 比对）。
- 灰度发布（按 IP/标签分批）——归后续。
- 配置变更审计 / diff 视图 —— 归后续。
- 推送到数据面热更新 —— 接入数据面 SDK 时实现。
- 跨集群同步。
