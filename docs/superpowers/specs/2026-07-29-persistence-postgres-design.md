# 持久化切片设计：identity + application 增量迁移至 PostgreSQL

**日期**：2026-07-29
**状态**：待评审
**关联**：`2026-07-26-maas-platform-foundation-design.md`（存储红线：元数据走 PostgreSQL）、`2026-07-27-platform-modules-blueprint.md`

## 背景与动机

当前 11 个 Repository 接口（~84 方法）全部为进程内内存实现，重启即丢。foundational spec 的存储红线明确要求「Core 元数据走 PostgreSQL」。这是距开源「生产可用」标准最大的架构性缺口。

本切片**不动业务模型、不改 API 契约**，只在 Repository 接口下新增 PostgreSQL 实现，验证持久化路径打通；其余 9 模块保持内存实现，后续按模块渐进迁移。

## 范围

**迁 PG（本切片）**：`identity`、`application`——鉴权与应用主线是平台承重墙，迁移收益最高、回归覆盖最全。

**保持内存（本切片）**：workload / environment / devops / appconfig / governance / configcenter / observability / security / billing / dataservice。靠配置开关无感切换，后续切片复用同一套基建逐个迁移。

**明确不做（YAGNI）**：行级安全（RLS）、读写分离、分库分表、ClickHouse 时序计量库、连接池精细调优、多区容灾。本期显式 `WHERE tenant_id` 过滤（与内存实现 1:1 对齐），RLS 留后续。

## 技术选型（license 全部 Apache 2.0 兼容）

| 领域 | 选型 | license |
|------|------|---------|
| 驱动 | `github.com/jackc/pgx/v5`（pgxpool 连接池） | MIT |
| 迁移 | `github.com/golang-migrate/migrate/v4`（embed SQL，启动时自动 up） | MIT |
| 测试 | 构建标签 `integration` 门控 + docker-compose 提供的 PG | — |

CI 红线：禁止 GPL/AGPL。`pgx` 与 `golang-migrate` 均 MIT，合规。

## 架构

```
cmd/core/main.go
  ├─ 读 PAAS_DB_URL：空 → 内存后端（dev/echo，零依赖，与现状一致）
  └─ 非空 → internal/storage/pg.Open(url)
            ├─ pgxpool 连接 + ping
            ├─ golang-migrate 跑 embedded migrations（startup auto-up）
            └─ 构造 identity.PGStore / application.PGStore 注入 handler
               （其余 9 模块仍用 memory.NewStore()）

internal/storage/pg/            # DB 基建（连接池 + 迁移），模块无关
  ├─ pg.go                      # Open(url) → *DB（封装 *pgxpool.Pool）
  ├─ migrate.go                 # RunMigrations(db) embed.FS 自动 up
  └─ migrations/
      ├─ 0001_identity.up.sql
      ├─ 0001_identity.down.sql
      ├─ 0002_application.up.sql
      └─ 0002_application.down.sql

internal/core/identity/pg/store.go       # identity.Repository 的 PG 实现
internal/core/application/pg/store.go    # application.Repository 的 PG 实现
```

**切换点**：`main.go` 内部按 `os.Getenv("PAAS_DB_URL")` 选择 store 构造方式。handler、路由、鉴权、配额拦截全部不变——Repository 接口是边界，PG 实现对上层透明。

## 数据模型（schema）

### identity

```sql
-- 0001_identity.up.sql
CREATE TABLE tenants (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    is_admin   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_users_tenant ON users(tenant_id);

-- 用户角色多值：一行一角色（Roles []string）。
CREATE TABLE user_roles (
    user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      TEXT NOT NULL,
    PRIMARY KEY (user_id, role)
);

CREATE TABLE api_keys (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key        TEXT NOT NULL UNIQUE,   -- bearer，登录态索引
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_apikeys_tenant ON api_keys(tenant_id);
CREATE INDEX idx_apikeys_key ON api_keys(key);

-- api_key 角色多值（与 user_roles 同构，APIKey 自带 Roles，鉴权时用 Key 上的角色）。
CREATE TABLE api_key_roles (
    api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    PRIMARY KEY (api_key_id, role)
);
```

> `identity.User` 当前 seed 不创建独立 user 记录（seed 只建 tenant + api_key）。为忠实持久化领域模型，`users` 表仍建出；`UsersByTenant` 从该表读。PG seed 与内存 seed 对齐：仅 seed tenants + api_keys（+ api_key_roles），不 seed users。

### application

```sql
-- 0002_application.up.sql
CREATE TABLE applications (
    id        TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    name      TEXT NOT NULL,
    initial   TEXT NOT NULL DEFAULT '',
    env       TEXT NOT NULL DEFAULT '',
    status    TEXT NOT NULL DEFAULT '',
    gradient  TEXT NOT NULL DEFAULT '',
    "desc"    TEXT NOT NULL DEFAULT '',
    replicas  TEXT NOT NULL DEFAULT '',
    rps       TEXT NOT NULL DEFAULT '',
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_apps_tenant ON applications(tenant_id);

-- 绑定项（真源）；ResourceCount 由 Bindings 派生，读时 Recount。
CREATE TABLE application_bindings (
    app_id   TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    ord      INTEGER NOT NULL,           -- 保持插入顺序，列表稳定
    type     TEXT NOT NULL,
    name     TEXT NOT NULL,
    note     TEXT NOT NULL DEFAULT '',
    UNIQUE (app_id, type, name)
);
CREATE INDEX idx_bindings_app ON application_bindings(app_id);
```

> `Resources`（计数）不入库，读时 `Recount()` 重算——与内存实现一致，避免计数与绑定漂移。`bindings` 用 `ord` 列保序。

## 多租户隔离

显式 `WHERE tenant_id = $1`，从 `pkg/tenant.TenantFrom(ctx)` 取。每个查询都带租户条件：

```go
func (s *Store) List(ctx context.Context) ([]application.Application, error) {
    tid, ok := tenant.TenantFrom(ctx)
    if !ok {
        return nil, errors.New("missing tenant context")
    }
    rows, err := s.pool.Query(ctx,
        `SELECT id,name,initial,env,status,gradient,"desc",replicas,rps
         FROM applications WHERE tenant_id=$1 ORDER BY id`, tid)
    // ...
}
```

`Create` 以 ctx 租户为准、忽略请求体 `TenantID`（与内存实现一致，防越权写）。缺失租户上下文即拒（fail-closed，与内存一致）。模型目录（`/api/models`）不经这两个 Repository，不受影响。

## main.go 接线

```go
func run(ctx context.Context, plugins []plugin.Plugin, gw *gateway.Gateway, meter *gateway.Meter) error {
    idb, appRepo, err := buildIdentityAndApps(ctx)  // 选择后端 + seed 全在这
    if err != nil { return err }
    // ...其余不变：secStore / billingStore / deps / serveHTTP（serveHTTP 改为收 appRepo 参数）
}

// buildIdentityAndApps 按 PAAS_DB_URL 选择后端，并负责 seed（统一入口，避免重复灌）：
//   有 DB_URL → PG（迁移 + SeedIfEmpty）；无 → 内存（NewStore + seedIdentity / 内联 seed）。
func buildIdentityAndApps(ctx context.Context) (identity.Repository, application.Repository, error) {
    if dsn := os.Getenv("PAAS_DB_URL"); dsn != "" {
        db, err := pg.Open(ctx, dsn)            // pgxpool + ping
        if err != nil { return nil, nil, err }
        if err := pg.RunMigrations(ctx, db); err != nil { return nil, nil, err }
        idb := identitypg.NewStore(db)
        appRepo := applicationpg.NewStore(db)
        seedAll(ctx, idb, appRepo, resolveAPIKey(), true /*ifEmpty 仅表空时灌 */)
        return idb, appRepo, nil
    }
    idb := idmemory.NewStore()
    seedAll(ctx, idb, appmemory.NewStore(), resolveAPIKey(), false /*内存：NewStore 已含应用 seed，identity 走 seedIdentity*/)
    return idb, appmemory.NewStore(), nil
}
```

**Seed 统一函数** `seedAll(ctx, idb, appRepo, extraKey, ifEmpty bool)`：把现有 `seedIdentity`（含 extraKey 兼容自定义部署）+ application seed 合并；`ifEmpty=true`（PG）先查 `tenants` 表是否为空才灌（幂等、重启不重复），`ifEmpty=false`（内存）保持现状语义（`appmemory.NewStore()` 已内联 seed 应用，`seedAll` 仅灌 identity）。`Create` 在 PG 上遇主键冲突返回「已存在」错误（与内存一致），seed 路径容忍该错误继续。

`serveHTTP` 改为接收 `appRepo application.Repository` 参数（当前内联 `application.NewHandler(appmemory.NewStore())`），其余 handler 接线、QuotaCheck 注入（Q2）不变。

## Seed 幂等

PG seed 放 `cmd/core/seed.go`：`SeedIfEmpty` 检查 `tenants` 表是否为空，空才灌入内存版同一批种子（两租户、三 Key、五应用）。已灌数据不重复写。与内存 `NewStore()` 内联 seed 对齐，演示体验一致。

## 测试策略

| 层 | 策略 | 默认 `make test` 是否运行 |
|----|------|--------------------------|
| 内存实现 | 现有单测，不变 | 是 |
| PG 实现 | `//go:build integration` 门控的集成测试，需 `PAAS_TEST_PG_URL` | **否**（`make test-pg` 专属） |
| Repository 契约 | 共享表驱动测试套件（同接口、两后端各跑一遍），保证内存/PG 行为一致 | 内存版是；PG 版 integration |

**默认 `go test ./...` 不依赖 PG，保持零外部依赖、CI 全绿**。`make test-pg` 启 docker-compose PG 跑集成套件。

**Makefile 目标**：
- `make test` —— 内存，不变
- `make test-pg` —— `docker compose up -d postgres` + `go test -tags=integration ./...`（设置 `PAAS_TEST_PG_URL`）
- `make migrate` —— 手动跑迁移（开发调试用，正常启动自动跑）

## docker-compose

`docker-compose.yml` 已有 core 服务，新增 `postgres` 服务 + 持久卷，core 依赖它：

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: paas
      POSTGRES_USER: paas
      POSTGRES_PASSWORD: paas-dev
    ports: ["5432:5432"]
    volumes: ["paas-pg:/var/lib/postgresql/data"]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U paas"]
      interval: 5s
  core:
    environment:
      PAAS_DB_URL: postgres://paas:paas-dev@postgres:5432/paas?sslmode=disable
    depends_on:
      postgres: { condition: service_healthy }
volumes:
  paas-pg:
```

`docker compose up` 一键起 PG + 迁移 + seed + core，持久化自动生效。

## 验收标准

1. 不设 `PAAS_DB_URL`：行为与当前**完全一致**（内存后端），所有现有测试绿。
2. 设 `PAAS_DB_URL`：core 启动跑迁移 → seed（首次）→ identity/application 走 PG；重启后数据持久。
3. 多租户隔离：`sk-acme-admin` 仅见 Acme 应用，跨租户 404 不泄漏（与内存一致）。
4. `make test-pg` 集成套件通过：identity/application 两模块在 PG 后端下契约与内存一致。
5. `docker compose up` 一键可用，重启 core 数据不丢。
6. `golangci-lint run ./...` 0 issues；新增依赖（pgx、golang-migrate）license 合规。

## 风险与对策

- **迁移失败导致启动失败**：`RunMigrations` 错误直接返回，进程退出（fail-fast），不进入半启动态。
- **连接泄漏**：`pgxpool` 统一管理；`*pgxpool.Pool` 在进程退出时 `Close()`（run 返回路径 + ctx.Done）。
- **PG 不可用**：未设 `PAAS_DB_URL` 时纯内存，PG 故障不影响 dev/echo 演示路径。
- **集成测试污染**：`make test-pg` 每次跑前 `DROP` 重建 schema（down→up），保证隔离。
