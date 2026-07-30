# Task 7：configcenter 迁 PostgreSQL 实施报告

## 概览

configcenter 模块（namespace + 配置项 draft + 发布版本快照）从内存实现迁 PostgreSQL：
3 表 + JSONB Snapshot + 事务级 version 单调 + active 唯一保证。复用 governance/pg
（多实体单 Store + 事务级联）与 dataservice/pg（JSONB nil 安全）两套黄金模板。

## 新增文件

- `internal/storage/pg/migrations/0009_configcenter.up.sql`
- `internal/storage/pg/migrations/0009_configcenter.down.sql`
- `internal/configcenter/pg/store.go`
- `internal/configcenter/pg/store_test.go`（`//go:build integration`）

## 修改文件

- `internal/configcenter/memory/store.go`：seed 内联改为调 `SeedNamespaces()` /
  `SeedItems()` / `SeedPublishes()` 三个导出函数（PG/内存共用同一真源，DRY）。

## 表 schema（3 表）

| 表 | 用途 | 关键约束 |
|---|---|---|
| `cc_namespaces` | 命名空间 | `UNIQUE(tenant_id, name)` |
| `cc_items` | draft 配置项 | `UNIQUE(namespace_id, key)`；`idx_ccitems_ns` |
| `cc_publishes` | 发布版本快照 | `snapshot JSONB NOT NULL DEFAULT '{}'`、`version INTEGER`、`status TEXT`、`UNIQUE(namespace_id, version)`；`idx_ccpub_ns` |

无外键，级联清理由仓储层事务保证（与 governance/devops 同款）。

## 聚合 Store 方法

单 Store 实现 `NamespaceStore + ItemStore + PublishStore` 三子接口，编译期断言：

```go
var (
    _ configcenter.NamespaceStore = (*Store)(nil)
    _ configcenter.ItemStore      = (*Store)(nil)
    _ configcenter.PublishStore   = (*Store)(nil)
)
```

- Namespace：`ListNamespaces / GetNamespace / CreateNamespace / DeleteNamespace`
- Item：`ListItems / UpsertItem / DeleteItem`
- Publish：`ListPublishes / CreatePublish / RollbackPublish / ActivePublish / PublishNamespaceID`
- Count（seed 判空）：`NamespacesCount / ItemsCount / PublishesCount`

## CreatePublish 事务（逐行对齐 memory）

内存版 CreatePublish（`internal/configcenter/memory/store.go`）的关键步骤：
1. 校验 namespace 存在且属本租户
2. 遍历 items 组 snapshot（`snapshot[it.Key] = it.Value`）
3. 遍历 publishes 算 `maxVersion`（namespace 内）
4. 遍历 publishes 把 namespace 内所有 active 翻 rolled-back
5. 构造新 `pub`，version=maxVersion+1，status=active

PG 版等价事务（`internal/configcenter/pg/store.go` CreatePublish）：

| 步骤 | SQL | 对齐 memory |
|---|---|---|
| 校验 ns | `s.GetNamespace(ctx, namespaceID)` | 同 |
| BEGIN tx | `pool.Begin(ctx)` | — |
| 算 version | `SELECT COALESCE(MAX(version),0)+1 FROM cc_publishes WHERE namespace_id=$1 AND tenant_id=$2` | 等价 memory 遍历求 max |
| 组 snapshot | `SELECT key,value FROM cc_items WHERE namespace_id=$1 AND tenant_id=$2` → map | 等价 memory 遍历 items |
| marshal snapshot | `marshalSnapshot(map)` → JSONB 字节 | memory 直接持有 map |
| INSERT 新 active | `INSERT INTO cc_publishes (...) VALUES (...)` status=active | memory 写入 map |
| 旧 active 翻转 | `UPDATE cc_publishes SET status='rolled-back' WHERE namespace_id=$2 AND tenant_id=$3 AND status='active' AND id<>$5` | 等价 memory 遍历翻 status |
| COMMIT | `tx.Commit(ctx)` | memory 在 mu.Lock 下 |

事务保证 `MAX(version)+1` 计算与新行 INSERT 之间无并发插入；
`UNIQUE(namespace_id, version)` 兜底；先插后翻 status（`id<>新行`）避免误翻新行自身。

## RollbackPublish 事务（逐行对齐 memory）

memory 版语义：
1. 取目标行；不存在或跨租户 → not found
2. 已是 active → 报错
3. 遍历 namespace 内 active 翻 rolled-back
4. 目标翻 active

PG 版事务：在 BEGIN 内 `SELECT ... FOR UPDATE` 等价的 `SELECT pubCols WHERE id=$1 AND tenant_id=$2`，
状态机校验同款，再 `UPDATE WHERE status='active'` 把当前 active 翻 rolled-back，再 `UPDATE` 目标为 active。
COMMIT 保证两步原子。

## Snapshot JSONB 实现

复用 dataservice.Spec / governance.Meta 同款辅助（`marshalSnapshot` / `unmarshalSnapshot`）：
- nil → `{}`（与列 DEFAULT 一致）
- 读出 nil/空/null/无效 JSON → 空 map 非 nil（避免调用方 nil map write panic）
- 单行坏数据不阻塞整个 List（容错返回空 map）

Snapshot 不可变：仅由 CreatePublish 在事务内基于当前 cc_items 组装生成，
无任何 UPDATE 语句改 snapshot 列；其他状态机翻转只改 status 列。

## version 单调 + active 唯一保证

- 单调：`COALESCE(MAX(version),0)+1` 在事务内执行，事务隔离级别下读到的 MAX
  已包含其他已提交事务；并发提交依赖 `UNIQUE(namespace_id, version)` 兜底（
  第二个事务会冲突回滚）。
- active 唯一：CreatePublish 末尾 `UPDATE ... WHERE status='active'` 翻全部旧 active；
  RollbackPublish 同款翻当前 active。同 namespace 至多 1 行 active。

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| 构建 | `go build ./...` | OK |
| 构建（含集成） | `go build -tags=integration ./...` | OK |
| 单测 | `make test` | 全部 PASS |
| lint | `make lint` | **0 issues** |
| vet | `make vet` | OK |
| 集成测试 | `PAAS_TEST_PG_URL=… go test -tags=integration ./internal/configcenter/pg/ -v -count=1` | **14/14 PASS**（2.380s） |

集成测试覆盖：
- Namespace CRUD + 租户内 Name 唯一冲突 + 跨租户同名允许
- Item CRUD + **UpsertItem 同 key 更新**（ON CONFLICT 主路径，原 id 保留）+ 校验 ns 存在属本租户
- **CreatePublish version 单调递增**（连发 v1/v2/v3）
- **旧 active → rolled-back**（namespace 内 active 仅 1 个）
- **ActivePublish 返回最新 active**（无发布返 false）
- **RollbackPublish 激活历史**（连滚 p1→p2，当前 active 拒绝再滚）
- Snapshot 多 key 内容正确 + 无 item 时空 snapshot（非 nil）
- **DeleteNamespace 级联清 items + publishes**（事务原子，不误删他 ns）
- **多租户隔离**（缺失拒、跨租户 not found 不泄漏、Globex 在 Acme ns 上 UpsertItem 被 GetNamespace 拦截）
- Count 方法（seed 判空用）

## Constraints 合规

- 显式 `WHERE tenant_id` 全方法 ✓
- Create 以 ctx 租户为准忽略请求体 ✓
- 缺失租户即拒（fail-closed）✓
- 跨租户 not found 不泄漏 ✓
- version 事务内单调 ✓
- Snapshot 不可变 ✓
- 不改接口签名 / 不动 handler ✓
- 注释中文 ✓
- 不用 FormatExists 哨兵（沿用内存领域文本）✓
- 未执行 git commit ✓

## Concerns

- **PG seed 接线暂未做**：与 Task 0-6 的进展一致，本任务只交付 store + 测试 + 导出 seed
  函数；接线到 `cmd/core/main.go`（`PAAS_DB_URL` 切换 ccHandler 用 PG Store）以及
  `cmd/core/persistence.go` 扩 seed 编排属于后续 Task（与 governance/devops 等
  未接线模块同款）。
- 跨 namespace 并发 CreatePublish 极端场景下依赖 `UNIQUE(namespace_id, version)`
  兜底——失败回滚，调用方按 5xx 重试即可。本期 mock 数据面，不会触发。
- RollbackPublish 的事务隔离级别为 pgxpool 默认（Read Committed）；当前 SELECT 目标行
  后 UPDATE 旧 active 的间隙，理论上有另一并发 Rollback 同 namespace active 的窗口会
  翻两次。本期客户端串行调用，不构成实际风险；后续可升级为 `SELECT ... FOR UPDATE`
  锁目标行（与 governance 同款未锁）。
