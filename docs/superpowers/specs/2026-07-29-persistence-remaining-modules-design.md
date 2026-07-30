# 持久化切片设计：剩余 9 模块增量迁移至 PostgreSQL

**日期**：2026-07-29
**状态**：待评审
**关联**：`2026-07-29-persistence-postgres-design.md`（基建已落地：pgx + golang-migrate + embed SQL + `//go:build integration` + docker-compose PG）、`2026-07-27-platform-modules-blueprint.md`

## 背景与动机

`identity` + `application` 已迁 PostgreSQL（上一切片，基建已就绪）。剩余 9 个模块仍为进程内内存实现，重启即丢。本切片把这 9 个模块按同一套已验证模式一次性迁完，使平台持久化层整体达成生产可用，为后续真实 K8s 数据面纳管铺路（部署记录/镜像记录需持久化）。

**observability 不迁**：纯惰性 mock（Metrics/Logs/Traces/Alerts 运行时生成、不持久化），接真实 Prometheus/Loki/Tempo 才合理，本期保持内存。

本切片**不动业务模型、不改 API 契约、不动 handler/路由/鉴权/横切（生产安全/配额拦截）**，只在每个 Repository 接口下新增 PG 实现，`cmd/core` 按 `PAAS_DB_URL` 切换后端，PG 实现对上层透明。

## 范围

**迁 PG**：environment / appconfig / dataservice / workload / devops / governance / configcenter / billing / security。

**保持内存**：observability（接真实后端时再迁）。

**明确不做（YAGNI）**：行级安全（RLS）、跨模块 DB 外键、读写分离、ClickHouse 时序库、Secret 字段级加密（pgcrypto/Vault/KMS 留后续）、连接池精细调优。

## 已定决策（本期拍板）

| 决策点 | 选型 | 理由 |
|--------|------|------|
| 多值字段存储 | **全 JSONB** | map/slice 整列存 JSONB，与内存行为 1:1，无 JOIN，schema 最简，读写一次完成。本期无按元素查询需求。 |
| 交付组织 | **一个 spec + 一个 plan** | 模式同构，统一设计；plan 内按依赖顺序连续执行（workload 先于 devops），不分独立 cycle。 |
| Secret 存储 | **明文存 PG** | 与内存现状一致：DB 明文、List/Get/Create 返回掩码、Resolve 仅平台级可读明文。字段级加密留后续。Scope 平台级特例（TenantID 可空）在 schema 层用 partial unique index 处理。 |

## 架构与模式复用

复用已验证的 identity/application 迁移模式（`internal/core/{identity,application}/pg/store.go`）：

```
cmd/core/persistence.go   # buildIdentityAndApps → 扩为 buildAllStores（全模块统一选择后端 + seed）
internal/storage/pg/      # 基建（已存在）
  helpers.go              # 新增：抽出 errAlreadyExists / isUniqueViolation / tenantOrErr / rowScanner
  migrations/
    0003_environment.{up,down}.sql
    0004_appconfig.{up,down}.sql
    0005_dataservice.{up,down}.sql
    0006_workload.{up,down}.sql
    0007_devops.{up,down}.sql
    0008_governance.{up,down}.sql
    0009_configcenter.{up,down}.sql
    0010_billing.{up,down}.sql
    0011_security.{up,down}.sql
internal/environment/pg/store.go      # 各模块 Repository 的 PG 实现（9 个）
internal/appconfig/pg/store.go
internal/dataservice/pg/store.go
internal/workload/pg/store.go
internal/devops/pg/store.go
internal/governance/pg/store.go
internal/configcenter/pg/store.go
internal/billing/pg/store.go
internal/security/pg/store.go
```

**共享辅助提取（DRY）**：identity/application 的 pg 包各自重复定义了 `errAlreadyExists`/`isUniqueViolation`/`tenantOrErr`/`rowScanner`，迁 9 模块会重复 11 次。本期顺带抽到 `internal/storage/pg/helpers.go` 导出，各模块 pg 包引用；identity/application 的 pg 包同步改为引用（消除重复，行为不变）。这是"改到哪顺手改哪"的针对性改进，不做无关重构。

**切换点**：`PAAS_DB_URL` 非空 → 全模块走 PG；为空 → 全模块走内存（与现状一致）。handler/路由/鉴权/横切全不变。

**多租户隔离**：全部沿用 `tenantOrErr(ctx)` + 显式 `WHERE tenant_id=$1`，`Create` 以 ctx 租户为准忽略请求体，缺失即拒（fail-closed），跨租户 not found 不泄漏。与内存 1:1。

**跨模块 DB 外键**：不建。模块间通过 ctx/接口解耦（devops 依赖 workload.Repository 接口而非表），DB 层强约束会引入迁移顺序耦合与级联删除风险。沿用 identity 的 `api_keys.user_id` 不加 FK 的既有决策（松耦合，应用层校验）。

## 数据模型（schema）

### 0003 environment

```sql
CREATE TABLE environments (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,          -- prod|test
    cluster    TEXT NOT NULL DEFAULT '',
    "desc"     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_env_tenant ON environments(tenant_id);
```

`EnvType(ctx, id)` 方法：`SELECT type FROM environments WHERE id=$1 AND tenant_id=$2`（tid 从 ctx 取，供 EnvTypeResolver，prod:write 横切）。

### 0004 appconfig

```sql
CREATE TABLE app_configs (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    env_id     TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    type       TEXT NOT NULL,          -- env|secret
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, app_id, env_id, key)
);
CREATE INDEX idx_appconfig_lookup ON app_configs(tenant_id, app_id, env_id);
```

`Upsert` 用 `INSERT ... ON CONFLICT (tenant_id, app_id, env_id, key) DO UPDATE SET value=, type=, updated_at=`，返回掩码副本（secret 掩码，与内存一致）。

### 0005 dataservice

```sql
CREATE TABLE data_services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    kind       TEXT NOT NULL,          -- db|cache|mq|storage|vector|search
    name       TEXT NOT NULL,
    spec       JSONB NOT NULL DEFAULT '{}',  -- map[string]string
    status     TEXT NOT NULL,          -- creating|running|stopped
    env_id     TEXT NOT NULL DEFAULT '',
    app_id     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_ds_tenant_kind ON data_services(tenant_id, kind);
```

`spec` JSONB 读写：`pgtype` 或手工 `json.Marshal`/`Unmarshal` 到 `map[string]string`。

### 0006 workload

```sql
CREATE TABLE workloads (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    env_id     TEXT NOT NULL DEFAULT '',
    lane_id    TEXT NOT NULL DEFAULT 'default',
    type       TEXT NOT NULL,          -- service|job|cronjob
    name       TEXT NOT NULL,
    image      TEXT NOT NULL DEFAULT '',
    image_ref  TEXT NOT NULL DEFAULT '',
    replicas   INTEGER NOT NULL DEFAULT 0,
    ready      INTEGER NOT NULL DEFAULT 0,
    status     TEXT NOT NULL DEFAULT '',
    schedule   TEXT NOT NULL DEFAULT '',
    command    TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_wl_lookup ON workloads(tenant_id, env_id, app_id, type);
```

`List(ctx, envID, appID, wtype)` 按非空过滤项动态拼 WHERE（与内存一致）。`UpdateImage` / `Update(replicas, status)` 部分列更新。

### 0007 devops（4 表）

```sql
CREATE TABLE code_repos (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    app_id        TEXT NOT NULL,
    git_url       TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    dockerfile    TEXT NOT NULL DEFAULT '',
    build_context TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_repos_app ON code_repos(tenant_id, app_id);

CREATE TABLE build_runs (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    app_id      TEXT NOT NULL,
    repo_id     TEXT NOT NULL,
    trigger     TEXT NOT NULL,
    commit      TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,          -- pending|running|success|failed
    image_id    TEXT NOT NULL DEFAULT '',
    log         TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX idx_builds_app ON build_runs(tenant_id, app_id);

CREATE TABLE images (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    app_id       TEXT NOT NULL,
    registry     TEXT NOT NULL,
    tag          TEXT NOT NULL,
    digest       TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT '',
    branch       TEXT NOT NULL DEFAULT '',
    build_run_id TEXT NOT NULL DEFAULT '',
    built_at     TIMESTAMPTZ NOT NULL,
    status       TEXT NOT NULL DEFAULT 'ready'
);
CREATE INDEX idx_images_app ON images(tenant_id, app_id);

CREATE TABLE releases (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL,
    app_id            TEXT NOT NULL,
    env_id            TEXT NOT NULL,
    image_id          TEXT NOT NULL,
    image_digest      TEXT NOT NULL DEFAULT '',
    strategy          TEXT NOT NULL DEFAULT 'rolling',
    status            TEXT NOT NULL,
    workload_id       TEXT NOT NULL DEFAULT '',
    previous_image_id TEXT NOT NULL DEFAULT '',
    is_rollback       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL,
    created_by        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_releases_app ON releases(tenant_id, app_id);
```

**关键**：`ReleaseRepository.CreateRelease` / `RollbackRelease` 的编排逻辑**调用注入的 `workload.Repository` 接口**（List 找/建基线 Workload → UpdateImage → 记回滚指针），不直接读写 `workloads` 表。因此 devops 的 PG 实现对 workload 的存储后端**完全透明**——workload 是 PG 还是内存，devops PG 都照常工作。Store 构造签名保持 `devopspg.NewStore(db, wlRepo workload.Repository)`，与内存版 `memory.NewStore(wlRepo)` 同构。

BuildRun mock CI runner（goroutine 异步流转 pending→running→success 并产出 Image）：PG 版保留同样的 goroutine 编排，流转用 `UpdateBuildRun`（新增方法，或在 Store 内部直接 `pool.Exec` 更新 status/image_id/finished_at）。

### 0008 governance（4 表）

```sql
CREATE TABLE gov_services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    app_id     TEXT NOT NULL DEFAULT '',
    env_id     TEXT NOT NULL,
    protocol   TEXT NOT NULL,
    port       INTEGER NOT NULL,
    "desc"     TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_svc_tenant_env ON gov_services(tenant_id, env_id, app_id);

CREATE TABLE gov_instances (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    service_id TEXT NOT NULL,
    addr       TEXT NOT NULL,
    status     TEXT NOT NULL,
    lane_id    TEXT NOT NULL DEFAULT 'default',
    meta       JSONB NOT NULL DEFAULT '{}',   -- map[string]string
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_inst_service ON gov_instances(service_id);

CREATE TABLE gov_routes (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    service_id TEXT NOT NULL DEFAULT '',
    methods    JSONB NOT NULL DEFAULT '[]',   -- []string (GET|POST|PUT|DELETE|ANY)
    strip_path BOOLEAN NOT NULL DEFAULT FALSE,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_routes_service ON gov_routes(service_id);

CREATE TABLE gov_breakers (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    name        TEXT NOT NULL,
    service_id  TEXT NOT NULL DEFAULT '',
    strategy    TEXT NOT NULL,
    threshold   INTEGER NOT NULL,
    min_requests INTEGER NOT NULL,
    window_secs INTEGER NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at  TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_breakers_service ON gov_breakers(service_id);
```

**不持久化字段**：`CircuitBreaker.State` / `WindowStats`——运行时由 `EvaluateBreaker` 即时推导，handler 返回前填充，迁移时**不建列**。`DeleteService` 级联清 instances/routes/breakers（事务内 DELETE）。

### 0009 configcenter（3 表）

```sql
CREATE TABLE cc_namespaces (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    "desc"     TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);

CREATE TABLE cc_items (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    key          TEXT NOT NULL,
    value        TEXT NOT NULL,
    type         TEXT NOT NULL DEFAULT 'text',  -- text|json|yaml
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (namespace_id, key)
);
CREATE INDEX idx_ccitems_ns ON cc_items(namespace_id);

CREATE TABLE cc_publishes (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    version      INTEGER NOT NULL,
    snapshot     JSONB NOT NULL DEFAULT '{}',   -- map[string]string，不可变
    status       TEXT NOT NULL,                 -- active|rolled-back
    created_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (namespace_id, version)
);
CREATE INDEX idx_ccpub_ns ON cc_publishes(namespace_id);
```

`CreatePublish` 在事务内：`SELECT COALESCE(MAX(version),0)+1` → 快照当前全部 item → INSERT active → 旧 active 转 rolled-back。`RollbackPublish`：激活历史 rolled-back 为 active（事务内 status 翻转）。

### 0010 billing（3 表）

```sql
CREATE TABLE billing_quotas (
    tenant_id  TEXT PRIMARY KEY,
    limits     JSONB NOT NULL DEFAULT '{}',   -- map[string]int，-1=无限
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE billing_usages (
    tenant_id  TEXT PRIMARY KEY,
    counts     JSONB NOT NULL DEFAULT '{}',   -- map[string]int
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE billing_records (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    period     TEXT NOT NULL,                 -- YYYY-MM
    items      JSONB NOT NULL DEFAULT '[]',   -- []BillItem
    total      DOUBLE PRECISION NOT NULL DEFAULT 0,
    status     TEXT NOT NULL,                 -- unpaid|paid
    created_at TIMESTAMPTZ NOT NULL,
    paid_at    TIMESTAMPTZ,
    UNIQUE (tenant_id, period)
);
```

**`CheckAndInc` 原子性**（配额横切拦截的核心，必须并发安全）：

```go
func (s *Store) CheckAndInc(ctx context.Context, resource string, delta int) error {
    tid, _ := tenantOrErr(ctx)
    tx, err := s.db.Pool().Begin(ctx); if err != nil { return err }
    defer func() { _ = tx.Rollback(ctx) }()
    // 行锁：同租户并发 Create 串行化
    var countsJSON []byte
    err = tx.QueryRow(ctx,
        `SELECT counts FROM billing_usages WHERE tenant_id=$1 FOR UPDATE`, tid).Scan(&countsJSON)
    // 解析 counts，取 counts[resource]，查 limits[resource]
    // limit>0 且 counts[resource]+delta>limit → return ErrQuotaExceeded（不写）
    // 否则 counts[resource]+=delta → UPDATE counts=, updated_at=
    return tx.Commit(ctx)
}
```

`FOR UPDATE` 行锁保证「检查+递增」原子（与内存版 `sync.Mutex` 语义一致）。`GetQuota` 不存在返回默认配额（非错误）；`GenerateBill(period)` 同 period unpaid 覆盖（`ON CONFLICT (tenant_id, period) DO UPDATE` 或先删后插）。

### 0011 security（2 表）

```sql
CREATE TABLE secrets (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT,                          -- 平台级 NULL
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,                 -- secret|certificate
    scope      TEXT NOT NULL,                 -- tenant|platform
    value      TEXT NOT NULL,                 -- 明文（YAGNI，字段级加密留后续）
    "desc"     TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL
);
-- 平台级：scope='platform' 时 name 全局唯一；租户级：(tenant_id, name) 唯一
CREATE UNIQUE INDEX uniq_secret_platform ON secrets(name) WHERE scope='platform';
CREATE UNIQUE INDEX uniq_secret_tenant   ON secrets(tenant_id, name) WHERE scope='tenant';
CREATE INDEX idx_secrets_tenant ON secrets(tenant_id) WHERE scope='tenant';

CREATE TABLE audit_logs (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    actor         TEXT NOT NULL,
    action        TEXT NOT NULL,              -- create|update|delete
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    detail        TEXT NOT NULL DEFAULT '',
    at            TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_audit_tenant_time ON audit_logs(tenant_id, at DESC);
```

**Scope 平台级特例**（与内存版 `Scope` 字段对齐）：
- `ListSecrets`：`WHERE (scope='tenant' AND tenant_id=$1) OR scope='platform'`（全租户可见平台级，与内存一致）。
- `Resolve(id)`：仅 `scope='platform'` 返明文；租户级返 not found（供第三方供应商通道）。
- `Create/Delete`：handler 层校验 platform 仅 tenant-admin 可写（不变）；DB 层 unique 由两个 partial index 保证。

## main.go 接线

`cmd/core/persistence.go` 的 `buildIdentityAndApps` 扩展为 `buildAllStores`，返回全部 10 个 store（identity + application + 9 模块）+ `closeFn`：

```go
// PAAS_DB_URL 非空：全模块走 PG（迁移 + SeedIfEmpty）；为空：全模块走内存。
func buildAllStores(ctx context.Context) (*Stores, func(), error) {
    if dsn := os.Getenv("PAAS_DB_URL"); dsn != "" {
        db, err := storagepg.Open(ctx, dsn); if err != nil { return nil, nil, err }
        if err := storagepg.RunMigrations(ctx, db); err != nil {
            db.Close(); return nil, nil, err }
        idb := identitypg.NewStore(db); appRepo := applicationpg.NewStore(db)
        envRepo := environmentpg.NewStore(db)
        appCfg := appconfigpg.NewStore(db)
        dsRepo := dataservicepg.NewStore(db)
        wlRepo := workloadpg.NewStore(db)
        // devops PG 注入 workload 仓储（接口透明）
        devopsRepo := devopspg.NewStore(db, wlRepo)
        govRepo := governancepg.NewStore(db)
        ccRepo := configcenterpg.NewStore(db)
        billRepo := billingpg.NewStore(db)
        secRepo := securitypg.NewStore(db)
        stores := &Stores{ /* 全部 */ }
        seedPGIfEmpty(ctx, stores)   // 各模块表空才 seed，幂等
        return stores, db.Close, nil
    }
    // 内存路径：全模块 NewStore()（各自内联 seed，保持现状）
    ...
}
```

`Stores` 聚合结构体收口，`run()` 与 `serveHTTP` 从中取各 store 注入 handler。横切注入不变：`workload.Handler.QuotaCheck` 桥接 `billingStore.CheckAndInc`；各 handler 的 `EnvTypeResolver` 桥接 `envRepo.EnvType`。

> 内存路径 `devopsmemory.NewStore(wlRepo)` 的 wlRepo 在内存路径下是 `workloadmemory.NewStore()`——需保证内存路径下 devops 拿到的 workload store 与注入到 workload handler 的是同一实例（现状已如此，保持）。

## Seed 幂等

每模块 PG `SeedIfEmpty` 检查各自主表是否为空（`SELECT COUNT(*)`），空才灌。复用内存版 seed 数据：各 `internal/<mod>/memory/store.go` 导出 `Seed<X>() []<Entity>`（DRY，PG/内存同一真源），PG 路径按每条数据自身 `TenantID` 建 ctx 灌入（`Create` 以 ctx 租户为准）。已灌数据不重复写。与 `application.SeedApps()` 复用模式一致。

跨租户 seed（security 平台级 Secret）：`TenantID=""` + `Scope="platform"`，灌入时 ctx 租户无要求（platform 不按租户过滤）。

## 测试策略

| 层 | 策略 | 默认 `make test` 是否运行 |
|----|------|--------------------------|
| 内存实现 | 现有单测，不变 | 是 |
| PG 实现 | `//go:build integration` 门控，每模块 `pg/store_test.go`，需 `PAAS_TEST_PG_URL` | **否**（`make test-pg` 专属） |
| 共享契约（可选增强） | 抽取 Repository 接口的表驱动契约套件，内存/PG 各跑一遍 | 内存版是；PG 版 integration |

**默认 `make test` 保持零外部依赖、CI 全绿**。`make test-pg` 启 docker-compose PG 跑集成套件（扩展为覆盖 9 模块 pg 包路径）。

每模块 `pg/store_test.go` 覆盖：CRUD 正确性、租户隔离（跨租户 not found）、`Create` 以 ctx 为准忽略请求体、多值字段 JSONB 往返、billing `CheckAndInc` 并发原子性（`go test -race` 验证）、security 平台级 Scope 特例。

**Makefile 扩展**：`test-pg` 目标的 `go test` 路径从两个 pg 包扩为 `./internal/.../pg/...`（或 `./...` 配合 `-tags=integration`，未设 `PAAS_TEST_PG_URL` 的测试 `t.Skip`）。

## 验收标准

1. 不设 `PAAS_DB_URL`：行为与当前**完全一致**（全内存），所有现有测试绿。
2. 设 `PAAS_DB_URL`：core 启动跑迁移（0003–0011）→ seed（首次）→ 9 模块走 PG；重启后数据持久。
3. 多租户隔离：全模块跨租户访问 not found 不泄漏（与内存一致）。
4. 横切不变：workload Create 配额拦截（429）、生产写 `prod:write`（EnvTypeResolver 桥接 envRepo）、security 审计自动记录，PG 后端下全部生效。
5. `make test-pg` 集成套件覆盖 9 模块，契约与内存一致；`go test -race` 下 billing `CheckAndInc` 无竞态。
6. `docker compose up` 一键可用，重启 core 数据不丢。
7. `golangci-lint run ./...` 0 issues；无新增第三方依赖（基建已就绪）；observability 保持内存未动。
8. `internal/storage/pg/helpers.go` 抽出后，identity/application pg 包改引用，重复消除且行为不变。

## 风险与对策

- **devops 编排跨 store**：Release 编排调 workload.Repository 接口，devops PG 与 workload 后端透明。事务边界限于 devops 自身表；workload 侧 UpdateImage 失败由编排逻辑回滚（与内存版语义一致，不在 DB 层做跨 store 事务）。
- **billing 并发**：`CheckAndInc` 用 `FOR UPDATE` 行锁串行化同租户，`-race` 验证。无行（首次）时 `INSERT ... ON CONFLICT DO NOTHING` 预占行再 `FOR UPDATE`。
- **security 平台级**：两个 partial unique index 互斥覆盖，避免 tenant 级误占全局 name。`ListSecrets` 的 `OR scope='platform'` 走 `idx_secrets_tenant` + 平台扫描（平台级数据量小，可接受）。
- **JSONB 字段往返**：空 map/nil 统一存 `{}`/`[]`（DEFAULT 兜底），读时 nil JSONB → 空 map/slice（与内存零值一致），避免 nil 解引用。
- **迁移失败**：`RunMigrations` 错误 fail-fast，进程退出，不进入半启动态（既有行为）。
- **共享 helper 重构**：抽出后 identity/application 单测全跑一遍（`make test`），确保行为无回归。
