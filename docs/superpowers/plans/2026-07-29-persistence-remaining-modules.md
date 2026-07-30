# 持久化：剩余 9 模块迁移 PostgreSQL Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 environment / appconfig / dataservice / workload / devops / governance / configcenter / billing / security 九个模块的 Repository 从进程内内存实现迁移到 PostgreSQL，使平台持久化层整体达成生产可用（observability 保持内存）。

**Architecture:** 复用已落地的 identity/application 迁移模式——每个 Repository 接口下新增 PG 实现（`internal/<mod>/pg/store.go`），迁移 SQL 进 `internal/storage/pg/migrations/0003..0011`，`cmd/core` 按 `PAAS_DB_URL` 在「全内存」与「全 PG」间切换，PG 实现对 handler/路由/鉴权/横切透明。多值字段全 JSONB；不建跨模块 DB 外键；显式 `WHERE tenant_id` 多租户隔离。

**Tech Stack:** Go 1.23 + `github.com/jackc/pgx/v5`（pgxpool）+ `github.com/golang-migrate/migrate/v4`（embed SQL）+ `//go:build integration` 集成测试。基建已就绪，无新增第三方依赖。

## Global Constraints

- **license**：全部依赖须 Apache 2.0 兼容，禁止 GPL/AGPL。pgx/golang-migrate 均 MIT，本期不引新依赖。
- **多租户隔离**：所有查询显式 `WHERE tenant_id=$1`（从 `tenant.TenantFrom(ctx)` 取），缺失即拒（fail-closed）；`Create` 以 ctx 租户为准、忽略请求体 `TenantID`；跨租户访问统一 not found（不泄漏存在性）。与内存实现 1:1。
- **不改 API 契约 / 不动 handler / 不动横切**：Repository 接口签名不变；生产安全（`prod:write` + EnvTypeResolver）、配额拦截（QuotaCheck→CheckAndInc）、审计记录在 PG 后端下行为不变。
- **多值字段全 JSONB**：`map[string]string` / `[]string` / `[]BillItem` 整列 JSONB，空值统一存 `{}` / `[]`，读时 nil JSONB 归一化为零值。
- **不建跨模块 DB 外键**：模块间通过 ctx/接口解耦，DB 层不加 FK、不做级联删除（除模块内级联，如 governance.DeleteService 清 instances/routes/breakers 用事务）。
- **Secret 明文存 PG**：DB 明文、API 返回掩码 `••••••`、Resolve 仅平台级可读明文。字段级加密留后续。
- **测试门控**：PG 实现测试一律 `//go:build integration`，需 `PAAS_TEST_PG_URL`；默认 `make test` 保持零依赖全绿；`make test-pg` 跑集成套件。
- **错误语义对齐**：主键/唯一冲突映射为与内存一致的「已存在」错误（复用 `pg.ErrAlreadyExists`）；`ErrNoRows` 映射为 not found。
- **commit 规则**：未经用户明确要求不执行 git commit/分支；本 plan 的 commit 步骤仅在用户授权执行时进行，否则跳过 commit step 只做实现+测试。
- **注释语言**：与代码库一致，中文注释。

**参考**：spec `docs/superpowers/specs/2026-07-29-persistence-remaining-modules-design.md`。模式样板：`internal/core/identity/pg/store.go`、`internal/core/application/pg/store.go`（已落地）。

---

## 文件结构

| 路径 | 职责 |
|------|------|
| `internal/storage/pg/helpers.go`（新建） | 抽出 `ErrAlreadyExists` / `IsUniqueViolation` / `TenantOrErr` / `RowScanner`，全模块 pg 包共用 |
| `internal/storage/pg/migrations/0003..0011_*.up/down.sql`（新建 18 文件） | 9 模块 schema |
| `internal/{environment,appconfig,dataservice,workload,devops,governance,configcenter,billing,security}/pg/store.go`（新建） | 各模块 Repository 的 PG 实现 |
| `internal/{...}/pg/store_test.go`（新建 9 文件） | 各模块 `//go:build integration` 集成测试 |
| `internal/{...}/memory/store.go`（修改） | 导出 `Seed<X>()` 供 PG seed 复用（DRY） |
| `internal/core/{identity,application}/pg/store.go`（修改） | 改引用 `storage/pg/helpers.go`，消除重复 |
| `cmd/core/persistence.go`（修改） | `buildIdentityAndApps` → `buildAllStores`（Stores 聚合 + 全模块 seed） |
| `cmd/core/main.go`（修改） | `run`/`serveHTTP` 从 Stores 取各 store 注入 |
| `Makefile`（修改） | `test-pg` 路径扩为全模块 pg 包；`resetSchema` DROP 列表扩展 |
| `internal/core/application/pg/store_test.go`（修改） | `resetSchema` DROP 列表扩展到全 11 模块表 |

---

## Task 0: 抽出 storage/pg/helpers.go 共享辅助

**Files:**
- Create: `internal/storage/pg/helpers.go`
- Modify: `internal/core/identity/pg/store.go`（删除本地 `errAlreadyExists`/`isUniqueViolation`，改引用）
- Modify: `internal/core/application/pg/store.go`（删除本地 `errAlreadyExists`/`isUniqueViolation`/`tenantOrErr`/`rowScanner`，改引用）

**Interfaces:**
- Produces（后续所有模块 pg 包依赖）:
  ```go
  // storage/pg/helpers.go
  var ErrAlreadyExists = errors.New("已存在")
  func IsUniqueViolation(err error) bool          // pgconn.PgError code 23505
  func TenantOrErr(ctx context.Context) (string, error)  // tenant.TenantFrom，缺失返 error
  type RowScanner interface{ Scan(dest ...any) error }
  func FormatExists(what string) error            // fmt.Errorf("%s%w", what, ErrAlreadyExists)
  ```

- [ ] **Step 1: 写 helpers.go**

```go
// Package pg 共享辅助：错误映射、租户解析、行扫描抽象。
// 各业务模块的 pg 子包引用，避免 11 处重复定义（DRY）。
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitoys/paas/pkg/tenant"
)

// ErrAlreadyExists 表示主键/唯一键冲突，映射为与内存实现一致的「已存在」错误。
var ErrAlreadyExists = errors.New("已存在")

// IsUniqueViolation 判断 PG 唯一约束冲突（主键或 UNIQUE，SQLSTATE 23505）。
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// FormatExists 把实体名拼进「已存在」错误，如 FormatExists("应用") → "应用已存在"。
func FormatExists(what string) error { return fmt.Errorf("%s%w", what, ErrAlreadyExists) }

// TenantOrErr 从 ctx 取租户 ID；缺失返错误（fail-closed，与内存实现一致）。
func TenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", errors.New("missing tenant context")
	}
	return tid, nil
}

// RowScanner 抽象 pgx QueryRow 与 Row 的 Scan 来源，供 scan 辅助函数复用。
type RowScanner interface {
	Scan(dest ...any) error
}
```

- [ ] **Step 2: identity/pg/store.go 改引用**

删除 `errAlreadyExists`、`isUniqueViolation` 本地定义；全局替换：
- `errAlreadyExists` → `storagepg.ErrAlreadyExists`
- `isUniqueViolation(err)` → `storagepg.IsUniqueViolation(err)`
- `fmt.Errorf("租户%w: %s", errAlreadyExists, t.ID)` 等 → `storagepg.FormatExists("租户")`（注意：identity 现有错误消息带 ID 后缀，保留原有消息格式即可，仅把 `errAlreadyExists` 换成 `storagepg.ErrAlreadyExists`，不动 %w 之外的文本）

保持 import `storagepg "github.com/aitoys/paas/internal/storage/pg"`。

- [ ] **Step 3: application/pg/store.go 改引用**

删除 `errAlreadyExists`、`isUniqueViolation`、`tenantOrErr`、`rowScanner` 本地定义；替换为 `storagepg.X`。`scanApp` 的参数类型 `rowScanner` → `storagepg.RowScanner`。

- [ ] **Step 4: 验证未回归**

Run: `make test && make lint && make vet`
Expected: 全绿，0 issues。identity/application 行为不变。

- [ ] **Step 5: Commit**（仅在用户授权时）

```bash
git add internal/storage/pg/helpers.go internal/core/identity/pg/store.go internal/core/application/pg/store.go
git commit -m "refactor: 抽出 storage/pg 共享辅助，消除 identity/application pg 重复"
```

---

## Task 1: environment 迁移 PG（黄金模板）

后续模块复用本任务建立的模式：迁移 up/down + Store struct + NewStore + 各方法 + 集成测试。

**Files:**
- Create: `internal/storage/pg/migrations/0003_environment.up.sql`
- Create: `internal/storage/pg/migrations/0003_environment.down.sql`
- Create: `internal/environment/pg/store.go`
- Create: `internal/environment/pg/store_test.go`
- Modify: `internal/environment/memory/store.go`（导出 `SeedEnvs()`）

**领域结构（spec §0003）**：`Environment{ID, TenantID, Name, Type(prod|test), Cluster, Desc, CreatedAt}`。Repository 方法：
```
List(ctx) ([]Environment, error)
Get(ctx, id) (Environment, error)
Create(ctx, e) error
Delete(ctx, id) error
EnvType(ctx, id) (string, error)
EnvsCount(ctx) (int, error)   // 供 seed 判空（PG 版新增，内存版无需）
```

**Interfaces:**
- Consumes: `storagepg.DB`、`storagepg.{ErrAlreadyExists,IsUniqueViolation,TenantOrErr,RowScanner}`、`environment.Repository`
- Produces: `environmentpg.NewStore(db *storagepg.DB) *Store`、`(*Store).EnvsCount(ctx) (int, error)`

- [ ] **Step 1: 写迁移 up.sql**

```sql
-- 0003_environment.up.sql
CREATE TABLE environments (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    cluster    TEXT NOT NULL DEFAULT '',
    "desc"     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_env_tenant ON environments(tenant_id);
```

- [ ] **Step 2: 写迁移 down.sql**

```sql
-- 0003_environment.down.sql
DROP TABLE IF EXISTS environments CASCADE;
```

- [ ] **Step 3: 导出 memory seed**

在 `internal/environment/memory/store.go` 把现有内联 seed（小写 `seed()` 或 NewStore 内联）改为导出函数，返回 seed 切片（与 `application.SeedApps()` 同构）：

```go
// SeedEnvs 返回预置环境数据（PG seed 复用，DRY：内存/PG 同一真源）。
func SeedEnvs() []environment.Environment { return seed() }  // 若现无 seed() 则把内联数据提到此返回
```

- [ ] **Step 4: 写 store.go**

```go
// Package pg 提供 environment.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 过滤（与内存 1:1）；Create 以 ctx 租户为准忽略请求体。
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/core/environment"  // 注：按实际 import 路径，可能为 internal/environment
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct{ db *storagepg.DB }

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

const envCols = `id, tenant_id, name, type, cluster, "desc", created_at`

func scanEnv(r storagepg.RowScanner, e *environment.Environment) error {
	return r.Scan(&e.ID, &e.TenantID, &e.Name, &e.Type, &e.Cluster, &e.Desc, &e.CreatedAt)
}

func (s *Store) List(ctx context.Context) ([]environment.Environment, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx, `SELECT `+envCols+` FROM environments WHERE tenant_id=$1 ORDER BY id`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []environment.Environment
	for rows.Next() {
		var e environment.Environment
		if err = scanEnv(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (environment.Environment, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return environment.Environment{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `SELECT `+envCols+` FROM environments WHERE id=$1 AND tenant_id=$2`, id, tid)
	var e environment.Environment
	if err = scanEnv(row, &e); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return environment.Environment{}, fmt.Errorf("环境不存在: %s", id)
		}
		return environment.Environment{}, err
	}
	return e, nil
}

func (s *Store) Create(ctx context.Context, e environment.Environment) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	e.TenantID = tid
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO environments (`+envCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.ID, e.TenantID, e.Name, e.Type, e.Cluster, e.Desc, e.CreatedAt)
	if storagepg.IsUniqueViolation(err) {
		return storagepg.FormatExists("环境")
	}
	return err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM environments WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("环境不存在: %s", id)
	}
	return nil
}

// EnvType 返回环境类型（供 EnvTypeResolver，prod:write 横切）。
func (s *Store) EnvType(ctx context.Context, id string) (string, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return "", err
	}
	var t string
	err = s.db.Pool().QueryRow(ctx, `SELECT type FROM environments WHERE id=$1 AND tenant_id=$2`, id, tid).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("环境不存在: %s", id)
	}
	return t, err
}

// EnvsCount 供 seed 判空（表空才灌，幂等）。
func (s *Store) EnvsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM environments`).Scan(&n)
	return n, err
}
```

> **import 路径校验**：implementer 先 `grep -rn "package environment" internal/` 确认实际路径（`internal/environment` 还是 `internal/core/environment`），按实际填写。

- [ ] **Step 5: 写 store_test.go（integration）**

```go
//go:build integration
package pg

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/..."  // environment 包
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestDB / acmeCtx / globexCtx / noTenantCtx / resetSchema 沿用 application/pg 的样板；
// resetSchema 的 DROP 列表须含 environments（Task 10 统一扩展，本任务可先本地补 environments）。

func sampleEnv(id string) environment.Environment {
	return environment.Environment{ID: id, Name: id, Type: "test", Cluster: "test-bj", Desc: "d", CreatedAt: time.Now()}
}

func TestEnvCreateListGet(t *testing.T) {
	db := newTestDB(t); s := NewStore(db); ctx := acmeCtx()
	if err := s.Create(ctx, sampleEnv("env-1")); err != nil { t.Fatalf("Create: %v", err) }
	got, err := s.Get(ctx, "env-1")
	if err != nil { t.Fatalf("Get: %v", err) }
	if got.Type != "test" { t.Fatalf("Type=test, got %s", got.Type) }
	if list, _ := s.List(ctx); len(list) != 1 { t.Fatalf("应 1 条, got %d", len(list)) }
}

func TestEnvMissingTenantRejected(t *testing.T) {
	db := newTestDB(t); s := NewStore(db)
	if err := s.Create(noTenantCtx(), sampleEnv("env-x")); err == nil { t.Fatal("缺失租户应拒绝") }
}

func TestEnvTenantIsolation(t *testing.T) {
	db := newTestDB(t); s := NewStore(db)
	_ = s.Create(acmeCtx(), sampleEnv("env-acme"))
	if _, err := s.Get(globexCtx(), "env-acme"); err == nil { t.Fatal("跨租户应 not found") }
	if list, _ := s.List(globexCtx()); len(list) != 0 { t.Fatalf("globex 应 0 条") }
}

func TestEnvTypeResolver(t *testing.T) {
	db := newTestDB(t); s := NewStore(db); ctx := acmeCtx()
	_ = s.Create(ctx, environment.Environment{ID: "p", Name: "p", Type: "prod", Cluster: "x", CreatedAt: time.Now()})
	if t, _ := s.EnvType(ctx, "p"); t != "prod" { t.Fatalf("EnvType=prod, got %s", t) }
}

func _ = context.Background // 占位避免未用 import（实际按 lint 调整）
```

> 测试中 `environment`、`time`、`context` 的 import 按实际使用调整，勿留未用 import（golangci 会报）。

- [ ] **Step 6: 跑集成测试**

Run: `PAAS_TEST_PG_URL=postgres://paas:paas-dev@localhost:5432/paas?sslmode=disable go test -tags=integration ./internal/environment/pg/ -v -count=1`
Expected: 全 PASS。

- [ ] **Step 7: Commit**（仅授权时）

```bash
git add internal/storage/pg/migrations/0003_environment.* internal/environment/pg/ internal/environment/memory/store.go
git commit -m "feat: environment 模块迁移 PostgreSQL"
```

---

## Task 2: appconfig 迁移 PG

**Files:** `migrations/0004_appconfig.{up,down}.sql`、`internal/appconfig/pg/store.go`、`internal/appconfig/pg/store_test.go`、改 `internal/appconfig/memory/store.go` 导出 `SeedConfigs()`。

**领域**：`ConfigItem{ID, TenantID, AppID, EnvID, Key, Value, Type(env|secret), UpdatedAt}`。唯一键 `(tenant_id, app_id, env_id, key)`。
**Repository**：
```
List(ctx, appID, envID) ([]ConfigItem, error)   // secret 值掩码
Upsert(ctx, item) (ConfigItem, error)           // 掩码返回
Delete(ctx, id) error
```

- [ ] **Step 1: up.sql** —— spec §0004 原文照写（表名 `app_configs`，UNIQUE(tenant_id, app_id, env_id, key)，索引 idx_appconfig_lookup）。
- [ ] **Step 2: down.sql** —— `DROP TABLE IF EXISTS app_configs CASCADE;`
- [ ] **Step 3: 导出 memory.SeedConfigs()**
- [ ] **Step 4: store.go** —— 要点：
  - `List`：`SELECT ... WHERE tenant_id=$1`，appID/envID 非空时追加 `AND app_id=$N AND env_id=$N`；返回前对 `Type==TypeSecret` 的项调 `Masked()`（与内存一致，掩码不泄漏）。
  - `Upsert`：`INSERT ... ON CONFLICT (tenant_id, app_id, env_id, key) DO UPDATE SET value=EXCLUDED.value, type=EXCLUDED.type, updated_at=EXCLUDED.updated_at`，以 ctx 租户写 tenant_id；返回掩码副本。
  - `Delete`：`DELETE ... WHERE id=$1 AND tenant_id=$2`，RowsAffected==0 → not found。
  - 掩码常量 `appconfig.SecretMask="••••••"` 复用领域包；`Masked()` 复用。
- [ ] **Step 5: store_test.go** —— 测：CRUD、租户隔离、Upsert 同 key 更新（非新增）、secret 掩码（List/Upsert 返回值含 `••••••` 非原值）、缺失租户拒绝。
- [ ] **Step 6: 跑测试** —— `go test -tags=integration ./internal/appconfig/pg/ -v`，PASS。
- [ ] **Step 7: Commit**（仅授权时）。

---

## Task 3: dataservice 迁移 PG（JSONB）

**Files:** `migrations/0005_dataservice.{up,down}.sql`、`internal/dataservice/pg/store.go`、`store_test.go`、导出 `SeedDataServices()`。

**领域**：`DataService{ID, TenantID, Kind(db|cache|mq|storage|vector|search), Name, Spec map[string]string, Status(creating|running|stopped), EnvID, AppID, CreatedAt, UpdatedAt}`。租户内 Name 唯一。
**Repository**：`List(ctx, kind)`、`Get(ctx, id)`、`Create(ctx, d) (DataService, error)`（status 空补 running）、`Update(ctx, d)`、`Delete(ctx, id)`。

- [ ] **Step 1: up.sql** —— spec §0005 原文（`spec JSONB NOT NULL DEFAULT '{}'`，UNIQUE(tenant_id, name)，索引 idx_ds_tenant_kind）。
- [ ] **Step 2: down.sql** —— DROP data_services。
- [ ] **Step 3: 导出 memory.SeedDataServices()**
- [ ] **Step 4: store.go** —— 要点：
  - Spec JSONB 读写：`json.Marshal(d.Spec)` → `[]byte` 入库；读出 `[]byte` → `json.Unmarshal` 到 `map[string]string`，nil/`null` → 空 map（非 nil，避免 nil map 写入 panic）。
  - 用辅助 `func marshalSpec(m map[string]string) ([]byte, error)`、`func unmarshalSpec(b []byte) map[string]string`（nil 安全）。
  - `Create`：status 空补 `"running"`（与内存一致）；唯一冲突 → `FormatExists("数据服务")`。
  - `Update`：仅改 spec/status（与内存一致）。
  - `List`：kind 非空追加 `AND kind=$N`。
- [ ] **Step 5: store_test.go** —— 测：Spec map JSONB 往返（含多键、空 map、含中文值）、Create 补 running、唯一名冲突、租户隔离。
- [ ] **Step 6: 跑测试** —— PASS。
- [ ] **Step 7: Commit**（仅授权时）。

---

## Task 4: workload 迁移 PG

**Files:** `migrations/0006_workload.{up,down}.sql`、`internal/workload/pg/store.go`、`store_test.go`、导出 `SeedWorkloads()`。

**领域**：`Workload{ID, TenantID, AppID, EnvID, LaneID, Type(service|job|cronjob), Name, Image, ImageRef, Replicas int, Ready int, Status, Schedule, Command, CreatedAt}`。
**Repository**：`List(ctx, envID, appID, wtype)`、`Get(ctx, id)`、`Create(ctx, w)`、`Update(ctx, id, replicas, status)`、`UpdateImage(ctx, id, image, imageRef)`、`Delete(ctx, id)`、`WorkloadsCount(ctx) (int, error)`（seed 判空用，PG 新增）。

- [ ] **Step 1: up.sql** —— spec §0006 原文（含 lane_id DEFAULT 'default'、replicas/ready INTEGER、索引 idx_wl_lookup）。
- [ ] **Step 2: down.sql** —— DROP workloads。
- [ ] **Step 3: 导出 memory.SeedWorkloads()**
- [ ] **Step 4: store.go** —— 要点：
  - `List`：动态拼 WHERE——固定 `tenant_id=$1`，envID/appID/wtype 非空各追加 `AND col=$N`（与内存过滤语义一致）；参数按顺序累加。
  - `Update`：`UPDATE workloads SET replicas=$3, status=$4 WHERE id=$1 AND tenant_id=$2 RETURNING <cols>`；ErrNoRows → not found。
  - `UpdateImage`：同上，改 `image, image_ref`。
  - `Create`：以 ctx 租户写；冲突 → `FormatExists("工作负载")`。
- [ ] **Step 5: store_test.go** —— 测：CRUD、List 多过滤组合（envID+type）、Update/UpdateImage 返回值正确、缺失租户拒绝、租户隔离、WorkloadsCount。
- [ ] **Step 6: 跑测试** —— PASS。
- [ ] **Step 7: Commit**（仅授权时）。

> **依赖顺序**：workload 必须先于 devops（Task 5 注入 workload.Repository）。本任务先于 Task 5。

---

## Task 5: devops 迁移 PG（4 表，注入 workload.Repository）

**Files:** `migrations/0007_devops.{up,down}.sql`、`internal/devops/pg/store.go`、`store_test.go`、导出 `SeedRepos()/SeedBuildRuns()/SeedImages()/SeedReleases()`（或合并 `SeedDevOps()` 返回 4 切片）。

**领域（4 实体）**：`CodeRepo`、`BuildRun`、`Image`、`Release`（字段见 spec §0007）。
**Repository（4 子接口）**：见盘点/spec。`CreateRelease(input)` / `RollbackRelease(releaseID)` 编排调注入的 `wlRepo workload.Repository`。

- [ ] **Step 1: up.sql** —— spec §0007 原文 4 表（code_repos / build_runs / images / releases），各带 tenant_id + 索引。down 反向 DROP 4 表。
- [ ] **Step 2: down.sql** —— DROP releases, images, build_runs, code_repos。
- [ ] **Step 3: 导出 memory seed**（4 切片）
- [ ] **Step 4: store.go** —— 要点：
  - `NewStore(db *storagepg.DB, wlRepo workload.Repository) *Store`——与内存版 `memory.NewStore(wlRepo)` 同构，**对 workload 存储后端完全透明**。
  - CodeRepo/BuildRun/Image 的 CRUD：标准模式（参照 Task 1）。
  - `CreateRelease`：编排逻辑**复制内存版**（取 image → `wlRepo.List` 找/建基线 service Workload → `wlRepo.Create` / `wlRepo.UpdateImage` → 记 `previous_image_id` → INSERT release）。事务仅覆盖 releases 表写入；workload 侧操作经接口，失败按内存版同款回滚语义（不在 DB 层做跨 store 事务）。
  - `RollbackRelease`：`wlRepo.UpdateImage` 回退 + 原 release 标 `rolled-back` + 新建 `is_rollback=true` release（事务覆盖 releases 表）。
  - BuildRun mock CI runner：`CreateBuildRun` 后启 goroutine 异步流转（与内存版同款定时器/步进），流转用 `UPDATE build_runs SET status=$2, image_id=$3, finished_at=$4 WHERE id=$1`，并 `INSERT images`。goroutine 持 `*Store` 引用 + ctx；进程退出由 Go runtime 回收（与内存版一致，不持久化 runner 状态）。
  - `<X>Count` 判空方法（4 个，供 seed）。
- [ ] **Step 5: store_test.go** —— 测：4 实体 CRUD、租户隔离、CreateRelease 找到/建基线 Workload + 更新 ImageRef + 记 PreviousImageID、RollbackRelease 回退镜像 + 标记 + 新建 rollback release、编排调 wlRepo（测试用 `workloadmemory.NewStore()` 作 fake 注入，验证接口透明）。
- [ ] **Step 6: 跑测试** —— PASS。
- [ ] **Step 7: Commit**（仅授权时）。

> **注意**：devops PG store 的编排逻辑与内存版语义必须一致——逐行对照 `internal/devops/memory/store.go` 的 `CreateRelease`/`RollbackRelease`，确保 Workload 找/建/更新策略、回滚指针、status 翻转完全相同。这是最易漂移点。

---

## Task 6: governance 迁移 PG（4 表，JSONB，不持久化 state/stats）

**Files:** `migrations/0008_governance.{up,down}.sql`、`internal/governance/pg/store.go`、`store_test.go`、导出 `SeedServices()/SeedInstances()/SeedRoutes()/SeedBreakers()`（或合并）。

**领域（4 实体）**：`Service`、`Instance`(Meta map)、`Route`(Methods []string)、`CircuitBreaker`(State/Stats **不持久化**)。
**Repository**：聚合 `Repository = ServiceStore + InstanceStore + RouteStore + BreakerStore`（方法见盘点）。

- [ ] **Step 1: up.sql** —— spec §0008 原文 4 表：gov_services、gov_instances（meta JSONB）、gov_routes（methods JSONB、strip_path/enabled BOOLEAN）、gov_breakers（**不含** state/stats 列）。各 UNIQUE(tenant_id, name)（service/route/breaker）、索引。down 反向 DROP。
- [ ] **Step 2: down.sql** —— DROP gov_breakers, gov_routes, gov_instances, gov_services。
- [ ] **Step 3: 导出 memory seed**（4 切片）
- [ ] **Step 4: store.go** —— 单 Store 实现四接口（方法带前缀，与内存版一致）。要点：
  - `Instance.Meta` map → JSONB（同 dataservice.Spec 模式，nil 安全）。
  - `Route.Methods` []string → JSONB 数组（`json.Marshal`/`Unmarshal`，nil → `[]`，读出 nil → 空 slice）。
  - `CircuitBreaker`：只存配置列；读出后 `State`/`Stats` 留空，由 handler 调 `EvaluateBreaker(b, now)` 即时填充（与内存版 handler 同构）。
  - `DeleteService`：事务内 `DELETE FROM gov_services WHERE id=$1 AND tenant_id=$2` + 级联 `DELETE FROM gov_instances/routes/breakers WHERE service_id=$1`（事务保证原子）。
  - `Heartbeat`：`UPDATE gov_instances SET updated_at=$2 WHERE id=$1 AND tenant_id=$2`（仅更新时间，与内存一致）。
- [ ] **Step 5: store_test.go** —— 测：4 实体 CRUD、Instance.Meta JSONB 往返、Route.Methods JSONB 往返、Breaker 读出无 State（运行时填）、DeleteService 级联清子表、租户隔离、唯一名冲突。
- [ ] **Step 6: 跑测试** —— PASS。
- [ ] **Step 7: Commit**（仅授权时）。

---

## Task 7: configcenter 迁移 PG（3 表，JSONB snapshot，version 单调）

**Files:** `migrations/0009_configcenter.{up,down}.sql`、`internal/configcenter/pg/store.go`、`store_test.go`、导出 `SeedNamespaces()/SeedItems()/SeedPublishes()`。

**领域（3 实体）**：`Namespace`、`ConfigItem`、`Publish`(Snapshot map + Version int + Status)。
**Repository**：聚合 3 子接口（方法见盘点）。`CreatePublish`/`RollbackPublish`/`ActivePublish` 是核心。

- [ ] **Step 1: up.sql** —— spec §0009 原文 3 表：cc_namespaces、cc_items、cc_publishes（snapshot JSONB、UNIQUE(namespace_id, version)）。down 反向。
- [ ] **Step 2: down.sql** —— DROP cc_publishes, cc_items, cc_namespaces。
- [ ] **Step 3: 导出 memory seed**（3 切片）
- [ ] **Step 4: store.go** —— 要点：
  - `CreatePublish(namespaceID)`：事务内 `SELECT COALESCE(MAX(version),0)+1 FROM cc_publishes WHERE namespace_id=$1` → `SELECT key,value FROM cc_items WHERE namespace_id=$1` 组装 snapshot map → `json.Marshal` → INSERT 新 active → `UPDATE cc_publishes SET status='rolled-back' WHERE namespace_id=$1 AND status='active'`（旧 active 翻转）。事务保证 version 单调 + active 唯一。
  - `RollbackPublish(publishID)`：事务内把目标 rolled-back publish 翻转为 active，当前 active 翻转为 rolled-back。
  - `ActivePublish(namespaceID)`：返回当前 active（bool=false 表示无发布）。
  - Snapshot JSONB 读写（nil 安全）。
  - `DeleteNamespace`：事务级联清 items + publishes。
- [ ] **Step 5: store_test.go** —— 测：namespace/item CRUD、CreatePublish 后 version 单调递增（连发 2 次 v1/v2）、旧 active 转 rolled-back、ActivePublish 返回最新、RollbackPublish 激活历史、Snapshot 内容正确、租户隔离、DeleteNamespace 级联。
- [ ] **Step 6: 跑测试** —— PASS。
- [ ] **Step 7: Commit**（仅授权时）。

---

## Task 8: billing 迁移 PG（3 表，JSONB，CheckAndInc 原子）

**Files:** `migrations/0010_billing.{up,down}.sql`、`internal/billing/pg/store.go`、`store_test.go`、导出 `SeedQuota()/SeedUsage()/SeedBills()`。

**领域**：`ResourceQuota`(Limits map)、`ResourceUsage`(Counts map)、`BillingRecord`(Items []BillItem, Total, Status, PaidAt *time.Time)。
**Repository**：`GetQuota`/`SetQuota`、`GetUsage`/`IncUsage`/`CheckAndInc`、`ListBills`/`GenerateBill`/`GetBill`/`PayBill`。

- [ ] **Step 1: up.sql** —— spec §0010 原文 3 表：billing_quotas（PK tenant_id, limits JSONB）、billing_usages（PK tenant_id, counts JSONB）、billing_records（items JSONB, total DOUBLE PRECISION, paid_at TIMESTAMPTZ NULL, UNIQUE(tenant_id, period)）。down 反向。
- [ ] **Step 2: down.sql** —— DROP billing_records, billing_usages, billing_quotas。
- [ ] **Step 3: 导出 memory seed**（3 切片）
- [ ] **Step 4: store.go** —— 要点：
  - `GetQuota`：无行返回 `DefaultQuota`（非错误，与内存一致）；有行解析 limits JSONB。
  - `SetQuota`：`INSERT ... ON CONFLICT (tenant_id) DO UPDATE SET limits=EXCLUDED.limits, updated_at=EXCLUDED.updated_at`。
  - `GetUsage`：无行返回空 Counts（非错误）。
  - **`CheckAndInc(ctx, resource, delta)`**（配额横切核心，必须并发安全）：
    ```go
    func (s *Store) CheckAndInc(ctx context.Context, resource string, delta int) error {
        tid, err := storagepg.TenantOrErr(ctx); if err != nil { return err }
        tx, err := s.db.Pool().Begin(ctx); if err != nil { return err }
        defer func() { _ = tx.Rollback(ctx) }()
        // 预占行（首次无 usage 行）
        _, _ = tx.Exec(ctx, `INSERT INTO billing_usages(tenant_id, counts, updated_at) VALUES ($1,'{}',now()) ON CONFLICT (tenant_id) DO NOTHING`, tid)
        var countsJSON []byte
        if err = tx.QueryRow(ctx, `SELECT counts FROM billing_usages WHERE tenant_id=$1 FOR UPDATE`, tid).Scan(&countsJSON); err != nil { return err }
        counts := unmarshalIntMap(countsJSON)
        cur := counts[resource]
        // 取 limit（quota 行可能不存在=默认配额）
        limit := s.limitFor(ctx, tx, tid, resource)  // 查 billing_quotas，无行用 DefaultQuota[resource]
        if limit > 0 && cur+delta > limit {
            return billing.ErrQuotaExceeded  // 不写，直接返回（事务回滚）
        }
        counts[resource] = cur + delta
        b, _ := json.Marshal(counts)
        _, err = tx.Exec(ctx, `UPDATE billing_usages SET counts=$2, updated_at=now() WHERE tenant_id=$1`, tid, b)
        if err != nil { return err }
        return tx.Commit(ctx)
    }
    ```
    `FOR UPDATE` 行锁串行化同租户并发（与内存版 `sync.Mutex` 语义一致）。
  - `GenerateBill(period)`：查 usage × PriceTable 逐项算 amount 求和；`INSERT ... ON CONFLICT (tenant_id, period) DO UPDATE`（覆盖同 period unpaid，与内存一致）。
  - `PayBill`：`UPDATE ... SET status='paid', paid_at=now() WHERE id=$1 AND status='unpaid'`；RowsAffected==0 → 拒绝重复支付。
  - Items JSONB 读写（[]BillItem 序列化）。
- [ ] **Step 5: store_test.go** —— 测：GetQuota 无行返默认、SetQuota 覆盖、CheckAndInc 正常递增/超限返 ErrQuotaExceeded 不写/unlimited 不拦截、**并发安全**（`for i:=0;i<50;i++ { go s.CheckAndInc(ctx,"workloads",1) }` 后 count==50 且无 race，`-race` 验证）、GenerateBill 同 period 覆盖、PayBill 状态机、重复支付拒绝、租户隔离。
- [ ] **Step 6: 跑测试** —— `go test -tags=integration -race ./internal/billing/pg/ -v`，PASS，无竞态。
- [ ] **Step 7: Commit**（仅授权时）。

---

## Task 9: security 迁移 PG（2 表，平台级 Scope 特例）

**Files:** `migrations/0011_security.{up,down}.sql`、`internal/security/pg/store.go`、`store_test.go`、导出 `SeedSecrets()/SeedAuditLogs()`。

**领域**：`Secret`(TenantID 可空, Scope tenant|platform, Value 明文)、`AuditLog`。
**Repository**：`ListSecrets`(掩码)/`GetSecret`/`CreateSecret`(掩码)/`DeleteSecret`/`Resolve`(平台级明文)、`ListAuditLogs`/`RecordAudit`。

- [ ] **Step 1: up.sql** —— spec §0011 原文 2 表：secrets（tenant_id **NULLable**、value TEXT 明文、两个 partial unique index）、audit_logs。索引 idx_audit_tenant_time。down 反向。
  ```sql
  CREATE UNIQUE INDEX uniq_secret_platform ON secrets(name) WHERE scope='platform';
  CREATE UNIQUE INDEX uniq_secret_tenant ON secrets(tenant_id, name) WHERE scope='tenant';
  CREATE INDEX idx_secrets_tenant ON secrets(tenant_id) WHERE scope='tenant';
  ```
- [ ] **Step 2: down.sql** —— DROP audit_logs, secrets。
- [ ] **Step 3: 导出 memory seed**（2 切片；平台级 Secret 的 TenantID=""、Scope="platform"）
- [ ] **Step 4: store.go** —— 要点：
  - `ListSecrets`：`WHERE (scope='tenant' AND tenant_id=$1) OR scope='platform'`（全租户可见平台级，与内存一致）；返回前全部 `Masked()`（不泄漏任何明文）。
  - `GetSecret`/`CreateSecret`：返回掩码副本（`Masked()`）。
  - `Resolve(id)`：`SELECT value, scope FROM secrets WHERE id=$1`；仅 `scope='platform'` 返明文 value；`scope='tenant'` 返 not found（供第三方通道，与内存一致）。
  - `CreateSecret`：tenant 级以 ctx 租户写 tenant_id；platform 级 tenant_id 写 NULL。唯一冲突（两 partial index 之一）→ `FormatExists("密钥")`。
  - `DeleteSecret`：tenant 级带 `AND tenant_id=$2`；platform 级仅 admin（handler 层校验，DB 层 `WHERE id=$1 AND (scope='platform' OR tenant_id=$2)`）。
  - `RecordAudit`：INSERT，Actor 从 ctx（`gateway.UserIDFrom` 或领域 Actor 字段，与内存 handler 一致）。
  - 平台级 seed 灌入：TenantID 为空时 ctx 不强制租户（或在 background ctx 灌）。
- [ ] **Step 5: store_test.go** —— 测：tenant 级 Secret CRUD + 掩码、platform 级 Secret 全租户可见、Resolve 平台级返明文/租户级 not found、平台级 name 全局唯一（两个 platform name 冲突报已存在）、tenant 与 platform 同名不冲突、审计只增、租户隔离（tenant 级跨租户 not found）。
- [ ] **Step 6: 跑测试** —— PASS。
- [ ] **Step 7: Commit**（仅授权时）。

---

## Task 10: cmd/core 接线 + resetSchema 扩展

**Files:**
- Modify: `cmd/core/persistence.go`（`buildIdentityAndApps` → `buildAllStores`，返回 `*Stores` + closeFn）
- Modify: `cmd/core/main.go`（`run`/`serveHTTP` 从 `Stores` 取各 store 注入；横切注入 QuotaCheck/EnvTypeResolver 桥接不变，指向 PG store）
- Modify: `cmd/core/seed.go`（PG seed 编排：各模块 SeedIfEmpty）
- Modify: `internal/core/application/pg/store_test.go`（`resetSchema` DROP 列表扩为全 11 模块表）

**Interfaces:**
- Consumes: Task 1–9 各 `pg.NewStore` + memory `NewStore` + memory `Seed<X>()`
- Produces: `*Stores` 聚合 + `buildAllStores(ctx) (*Stores, func(), error)`

- [ ] **Step 1: Stores 聚合 + buildAllStores**

在 `cmd/core/persistence.go` 定义 `Stores` 收口全部 store（identity/application + 9 模块），`buildAllStores` 按 `PAAS_DB_URL`：
- 非空：`storagepg.Open` → `RunMigrations` → 构造全 11 个 PG store（devops PG 注入 workload PG store）→ `seedPGIfEmpty` 各模块 → 返回 `db.Close`。
- 为空：全模块 `memory.NewStore()`（devops memory 注入 workload memory store，且与注入 workload handler 的同一实例）→ 返回 nil closeFn。

- [ ] **Step 2: seedPGIfEmpty 各模块**

每模块 `if pgStore, ok := stores.X.(*<mod>pg.Store); ok { if n,_:=pgStore.<X>Count(ctx); n==0 { seedX(ctx, stores.X) } }`。`seedX` 遍历 `memory.Seed<X>()`，按每条数据 `TenantID` 建 ctx 灌入（`Create` 以 ctx 为准）；平台级 Secret（TenantID 空）用 background ctx。

- [ ] **Step 3: main.go 改注入**

`run` 调 `buildAllStores` 取 `*Stores`；`serveHTTP` 从 Stores 取各 store 构造 handler。横切注入保持现状语义：
- `workloadHandler.QuotaCheck = func(ctx, delta) error { _, err := stores.Billing.(*billingpg.Store).CheckAndInc(...); ... }` —— 或更好：billing PG store 直接实现同接口，QuotaCheck 桥接不分内存/PG（接口已统一，直接 `stores.Billing.CheckAndInc`）。
- 各 EnvTypeResolver 桥接 `stores.Environment.EnvType`。
> **优先**：若 QuotaCheck/EnvTypeResolver 已是接口/方法注入（非具体类型断言），则 PG/memory 透明，无需特判。确认 handler 注入字段类型，优先走接口。

- [ ] **Step 4: resetSchema 扩展**

`internal/core/application/pg/store_test.go` 的 `resetSchema` DROP 列表扩为全 11 模块表 + `schema_migrations`：
```sql
DROP TABLE IF EXISTS audit_logs, secrets,
  billing_records, billing_usages, billing_quotas,
  cc_publishes, cc_items, cc_namespaces,
  gov_breakers, gov_routes, gov_instances, gov_services,
  releases, images, build_runs, code_repos,
  workloads, data_services, app_configs, environments,
  application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants CASCADE;
DROP TABLE IF EXISTS schema_migrations CASCADE;
```
> 该 DROP 列表所有模块 pg 测试共用（复制到各 store_test.go，或抽到共享 test helper——后者更 DRY，但跨包测试 helper 需放 `internal/storage/pg` 的 `internal` 测试包，可选优化，本期复制可接受）。

- [ ] **Step 5: 端到端验证（内存路径不变）**

Run: `make build && make test && make lint && make vet`
Expected: 全绿（内存后端行为不变）。

- [ ] **Step 6: 端到端验证（PG 路径）**

Run: `make test-pg`（全 11 模块 pg 包集成测试）
Expected: 全 PASS。
然后 `PAAS_DB_URL=postgres://paas:paas-dev@localhost:5432/paas?sslmode=disable ./bin/core` 启动 → `curl /api/applications`（带 sk-acme-admin）→ 重启 core → 再 curl 验证数据持久。
Expected: 重启后数据仍在；多租户隔离（sk-globex-admin 不见 acme 应用）。

- [ ] **Step 7: Commit**（仅授权时）。

---

## Task 11: Makefile test-pg 扩展 + 收尾

**Files:**
- Modify: `Makefile`（`test-pg` 的 `go test` 路径扩为全模块）
- Modify: `CHANGELOG.md`（Added 条目）
- Modify: `CLAUDE.md`（持久化小节更新：9 模块迁完，observability 仍内存）

- [ ] **Step 1: Makefile test-pg 路径**

```makefile
test-pg:
	docker compose up -d postgres
	@echo "等待 postgres 就绪…"
	@until docker compose exec -T postgres pg_isready -U paas >/dev/null 2>&1; do sleep 1; done
	PAAS_TEST_PG_URL=$(PG_DSN) go test -tags=integration -race ./internal/core/identity/pg/ ./internal/core/application/pg/ ./internal/environment/pg/ ./internal/appconfig/pg/ ./internal/dataservice/pg/ ./internal/workload/pg/ ./internal/devops/pg/ ./internal/governance/pg/ ./internal/configcenter/pg/ ./internal/billing/pg/ ./internal/security/pg/ -count=1 -v
```
> 或简化为 `./...` 配合 `-tags=integration`（未设 `PAAS_TEST_PG_URL` 的测试 `t.Skip`）。优先显式列路径（清晰）。

- [ ] **Step 2: CHANGELOG**

`## [Unreleased] ### Added` 加：
> 持久化（剩余模块）：environment/appconfig/dataservice/workload/devops/governance/configcenter/billing/security 9 模块迁 PostgreSQL（多值字段全 JSONB；billing `CheckAndInc` 行锁原子；security 平台级 partial unique index；devops Release 编排对 workload 后端透明）。observability 保持内存（接真实后端时再迁）。`storage/pg/helpers.go` 抽出共享辅助消除 11 处重复。

- [ ] **Step 3: CLAUDE.md**

持久化小节更新：identity/application → 全 10 模块（除 observability）；完成度 94% → 96%。

- [ ] **Step 4: 最终全量验证**

Run: `make build && make test && make lint && make vet && make test-pg`
Expected: 全绿。

- [ ] **Step 5: Commit**（仅授权时）。

---

## Self-Review

**1. Spec coverage**：spec 9 模块 schema（0003–0011）→ Task 1–9 各对应；helpers 抽取 → Task 0；main.go 接线/Stores/seed → Task 10；test-pg/CHANGELOG/CLAUDE → Task 11；billing CheckAndInc FOR UPDATE → Task 8；devops 编排透明 → Task 5；security 平台级 → Task 9；governance 不持久化 state/stats → Task 6；configcenter version 单调 → Task 7。✓ 全覆盖。

**2. Placeholder scan**：Task 2–9 的 store.go 用"要点"描述而非完整代码——这是有意权衡（9 模块每个完整 store 代码会让 plan 上万字；SQL 完整给出、方法签名精确、特殊点明确，配合 Task 1 黄金模板可执行）。implementer 执行 Task 2–9 时须参照 Task 1 的完整代码骨架 + 本任务要点。若执行方反馈要点不足以直接写代码，再补完整代码。✅ 可接受，但标注为执行风险点（见下）。

**3. Type consistency**：`storagepg.ErrAlreadyExists`/`IsUniqueViolation`/`TenantOrErr`/`RowScanner`/`FormatExists`（Task 0 定义）在 Task 1–9 一致引用；各 `<X>Count` 判空方法命名统一；`NewStore` 签名（devops 带 wlRepo）与内存版同构。✓

**4. 依赖顺序**：Task 0（helpers）→ Task 1（模板）→ Task 2–4（独立）→ Task 5（devops 依赖 workload Task 4）→ Task 6–9（独立）→ Task 10（接线依赖全部）→ Task 11（收尾）。✓

**执行风险**：Task 2–9 store.go 仅给要点。缓解：Task 1 是完整可运行模板；同构性高；implementer 遇歧义时参照对应 memory store（语义真源）。若用 subagent-driven 执行，每个 store 实现任务建议附带对应 `memory/store.go` 路径作为语义参考。
