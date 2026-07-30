# 数据服务资源切片设计

> 状态：已定稿 / 蓝图优先级 #8（资源中心补齐）
> 日期：2026-07-27
> 范围：用一个通用「数据服务」领域 + `kind` 区分，一套代码覆盖 6 种数据服务（DB/缓存/MQ/对象存储/向量库/搜索引擎），消除资源中心全部「即将」标记。

## 1. 定位与边界

资源中心 = 可绑定 Add-on 的数据服务。6 种结构同构（CRUD + spec + 状态 + 环境），抽象为单一领域 `internal/dataservice/`，用 `Kind` 区分类型（DRY）。本期独立资源 CRUD；与应用的绑定操作（Add-on 绑定/解绑）留后续（复用既有 application binding 机制）。

- 租户私有，复用 API Key 三元组 + `pkg/tenant` 隔离。
- **接 `prod:write`**：数据服务绑定物理环境（EnvID），生产创建/删除需 admin（与 workload/应用配置一致，EnvTypeResolver 依赖倒置）。
- 权限 `dataservice:read/write`（admin/dev 写、viewer 读）。

## 2. 领域模型

```
DataService {
  ID, TenantID, Kind, Name(租户内唯一), Spec map[string]string,
  Status(creating|running|stopped), EnvID, AppID(可选预留), CreatedAt, UpdatedAt
}
```

6 个 Kind + `KindMeta`（标签 / 图标 / spec 字段定义 / 默认 spec），导出供前端对齐：

| Kind | 标签 | spec 字段 |
|------|------|-----------|
| `db` | 数据库 | engine(postgres\|mysql), version, size_gb |
| `cache` | 缓存 | mode(standalone\|cluster), maxmemory_mb |
| `mq` | 消息队列 | engine(kafka\|rabbitmq\|rocketmq), partitions |
| `storage` | 对象存储 | bucket, redundancy(standard\|ia) |
| `vector` | 向量数据库 | engine(milvus\|qdrant), dimension |
| `search` | 搜索引擎 | engine(elasticsearch\|opensearch), shards |

`SpecField{Key, Label, Type(text|select), Options}` 描述字段表单类型。

## 3. 核心流程

- **创建**：`Create` 即 `running`（KISS，无 goroutine 异步流转；与 observability 惰性一致，测试可控）。Spec 按字段校验非空。
- **启停**：`Update` 可改 Status（running↔stopped）+ Spec。
- **删除**：级联无依赖（独立资源）。

## 4. REST API

```
GET  /api/dataservices?kind=           列表（按 kind 过滤，空=全部）
POST /api/dataservices                 创建（body 含 kind/spec/envId）
GET  /api/dataservices/meta            返回 KindMeta 列表（前端表单）
GET  /api/dataservices/{id}            详情
PUT  /api/dataservices/{id}            更新 spec/status（prod 需 prod:write）
DELETE /api/dataservices/{id}          删除（prod 需 prod:write）
```

## 5. 前端

`DataServices.vue` 共用组件（route `props.kind`），6 路由 `/resources/{db,cache,mq,storage,vector,search}` 复用：按 kind 标签/图标 + 列表 + 创建弹窗（按 KindMeta 动态渲染 spec 字段）+ 启停/删除。侧栏去掉全部 6 个「即将」标记。

## 6. 横切继承

- 租户隔离：Repository 全方法 ctx 强制过滤。
- 生产安全：写操作（Create/Update/Delete）经 EnvTypeResolver 校验 prod:write（developer 生产只读）。
- 删除走 `useDangerConfirm`（生产输入名称确认），生产视觉强隔离自动生效。

## 7. 不做（YAGNI）

- 应用 Add-on 绑定/解绑（复用 application binding，后续切片）。
- 真实引擎接入（纳管 PG/Redis/K8s Operator）→ 后续。
- 异步创建流转（creating→running goroutine）/备份/监控/扩容 → 后续。
- 跨 kind 的差异化高级能力（如 DB 的只读副本、MQ 的消费组）→ 后续按需。
