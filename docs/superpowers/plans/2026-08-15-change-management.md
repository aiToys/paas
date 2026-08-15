# 变更管理（Change / IntegrationBatch）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地变更管理三实体（Change/IntegrationBatch/复用 Release）：平台建/引 git 分支、多变更合成临时集成分支跑 CI（泳道隔离）、整批批准上线（merge 回 main + CD run）。

**Architecture:** 新包 `internal/devops/change/`（model + Repository + memory + pg + handler + 编排 service），Gitea client 扩 3 个分支 API；批次触发 CI/CD 复用 pipeline 包的 `triggerRunInternal` 等价路径（change 包内组 run，branch=集成分支/main）；批次终态由 GET 时惰性检查 run 推进（无后台 goroutine）。前端 console-user 应用详情加「变更」tab（变更列表 + 批次抽屉）。

**Tech Stack:** Go + controller-runtime 无关（纯控制面）+ PostgreSQL（migration 0027）+ Vue 3 + Element Plus。

**Spec:** `docs/superpowers/specs/2026-08-15-change-management-design.md`

## Global Constraints

- 权限复用 `pipeline:read` / `pipeline:write`（spec §10）；approve 上线额外要求 `prod:write`。
- 多租户：Repository 全方法 ctx tenant 强制过滤，跨租户 not found 不泄漏（spec §7）。
- 响应契约统一 `{data:T}`/`{error:msg}`（httputil.WriteData/WriteServiceError）；创建用 WriteDataCreated。
- 审计全写操作（action 前缀 `change_`/`batch_`，经 AuditRecorder 依赖倒置注入）。
- 仅 internal（集群 Gitea）CodeRepo 支持变更管理（spec §8）。
- 集成分支命名 `integration/<YYYYMMDD>-<seq>`；变更分支命名 `<type>/<slug>`。
- OpenAPI：全部端点 `reg.Operation` 登记（spec §4 全部 12 操作）。
- 注释语言与代码库一致（中文）。
- 不执行 git commit 以外的分支操作；每 task 结束 commit。

---

### Task 1: Gitea client 分支 API（CreateBranch/GetBranch/DeleteBranch）

**Files:**
- Modify: `internal/devops/gitea/client.go`
- Test: `internal/devops/gitea/client_test.go`

**Interfaces:**
- Produces（Task 5 桥接消费）:
  - `func (c *Client) CreateBranch(ctx context.Context, owner, repo, branch, from string) error`
  - `func (c *Client) GetBranch(ctx context.Context, owner, repo, branch string) (Branch, error)`，`Branch struct{ Name string; CommitSHA string }`
  - `func (c *Client) DeleteBranch(ctx context.Context, owner, repo, branch string) error`
  - 新 sentinel：`ErrBranchExists = errors.New("分支已存在")`、`ErrBranchNotFound = errors.New("分支不存在")`

- [ ] **Step 1: 写失败测试（httptest 模拟 Gitea，参照 client_test.go 既有 fake server 模式）**

在 `client_test.go` 追加：

```go
// fakeGitea 分支 API 模拟：记录调用 + 可注入状态码。
// 既有测试若已有 fake server helper，复用其模式；否则新建。
func TestBranchAPIs(t *testing.T) {
	// CreateBranch 成功（201）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/branches"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/branches/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "feat/x", "commit": map[string]any{"id": "abc123"},
			})
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/branches/"):
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "paas-bot", "pw")

	if err := c.CreateBranch(context.Background(), "paas-bot", "app-1", "feat/x", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	b, err := c.GetBranch(context.Background(), "paas-bot", "app-1", "feat/x")
	if err != nil || b.Name != "feat/x" || b.CommitSHA != "abc123" {
		t.Fatalf("GetBranch: %+v err=%v", b, err)
	}
	if err := c.DeleteBranch(context.Background(), "paas-bot", "app-1", "feat/x"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
}

func TestBranchAPIErrors(t *testing.T) {
	// CreateBranch 422 -> ErrBranchExists；GetBranch 404 -> ErrBranchNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "paas-bot", "pw")

	if err := c.CreateBranch(context.Background(), "o", "r", "b", "main"); err != ErrBranchExists {
		t.Fatalf("CreateBranch 422 期望 ErrBranchExists, got %v", err)
	}
	if _, err := c.GetBranch(context.Background(), "o", "r", "b"); err != ErrBranchNotFound {
		t.Fatalf("GetBranch 404 期望 ErrBranchNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/gitea/ -run TestBranch -v`
Expected: FAIL（方法未定义，编译错误）

- [ ] **Step 3: 实现**

`client.go` 追加（sentinel 块加两个错误；方法用独立 `doBranch` 处理分支 API 的状态码语义，不复用 doJSON 的 409->ErrRepoExists）：

```go
// Branch 分支最小子集。
type Branch struct {
	Name      string `json:"name"`
	CommitSHA string `json:"-"` // 从 commit.id 提取
}

// CreateBranch 从 from 分支/commit 创建新分支（POST /repos/{o}/{r}/branches）。
// 422（分支已存在）-> ErrBranchNotFound 的对偶 ErrBranchExists。
func (c *Client) CreateBranch(ctx context.Context, owner, repo, branch, from string) error {
	body := map[string]any{"new_branch_name": branch, "old_branch_name": from, "old_ref_name": from}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/branches", pathEscape(owner), pathEscape(repo))
	return c.doBranch(ctx, http.MethodPost, p, body)
}

// GetBranch 查询分支（不存在返 ErrBranchNotFound）。
func (c *Client) GetBranch(ctx context.Context, owner, repo, branch string) (Branch, error) {
	var out struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/branches/%s", pathEscape(owner), pathEscape(repo), pathEscape(branch))
	if err := c.doBranchJSON(ctx, http.MethodGet, p, nil, &out); err != nil {
		return Branch{}, err
	}
	return Branch{Name: out.Name, CommitSHA: out.Commit.ID}, nil
}

// DeleteBranch 删除分支（集成分支重建用）。
func (c *Client) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/branches/%s", pathEscape(owner), pathEscape(repo), pathEscape(branch))
	return c.doBranch(ctx, http.MethodDelete, p, nil)
}
```

`doBranch`/`doBranchJSON` 与 `doMerge` 同构：200/201/204 成功；404→`ErrBranchNotFound`；422→`ErrBranchExists`（POST）；401/403→`ErrUnauthorized`；网络错→`ErrGiteaUnavailable` 包装。GetBranch 用 `doBranchJSON`（需解码）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/gitea/ -v`
Expected: PASS（全包）

- [ ] **Step 5: Commit**

```bash
git add internal/devops/gitea/
git commit -m "feat(gitea): 分支 API（CreateBranch/GetBranch/DeleteBranch）"
```

---

### Task 2: change 包 model + Repository 接口 + memory 实现 + 状态机

**Files:**
- Create: `internal/devops/change/model.go`
- Create: `internal/devops/change/repository.go`
- Create: `internal/devops/change/store_memory.go`
- Test: `internal/devops/change/model_test.go`

**Interfaces:**
- Produces（Task 3/4/5 消费）:

```go
type Change struct {
	ID, TenantID, AppID, RepoID, Title, Type, Branch string
	BranchCreated bool
	BaseBranch    string
	Status        string // open|integrated|tested|released|reverted|abandoned
	BatchID       string
	ConflictWith  string // integrate 冲突时记前一个变更 ID
	CreatedBy     string
	CreatedAt, UpdatedAt time.Time
}
type IntegrationBatch struct {
	ID, TenantID, AppID, RepoID, Title, Branch, Status string
	ChangeIDs  []string // 有序
	PipelineID, RunID string
	ReleaseIDs []string
	CreatedBy  string
	CreatedAt, FinishedAt time.Time
}
// 状态常量
ChangeOpen/ChangeIntegrated/ChangeTested/ChangeReleased/ChangeReverted/ChangeAbandoned
BatchCollecting/BatchBuilding/BatchConflict/BatchTesting/BatchTested/BatchReleasing/BatchReleased/BatchFailed/BatchAbandoned
// Repository（单接口，Store 聚合）
type Repository interface {
	ListChanges(ctx, appID, status string) ([]Change, error)
	GetChange(ctx, id string) (Change, error)
	CreateChange(ctx, c Change) (Change, error)
	UpdateChange(ctx, c Change) (Change, error)
	ListBatches(ctx, appID, status string) ([]IntegrationBatch, error)
	GetBatch(ctx, id string) (IntegrationBatch, error)
	CreateBatch(ctx, b IntegrationBatch) (IntegrationBatch, error)
	UpdateBatch(ctx, b IntegrationBatch) (IntegrationBatch, error)
}
// sentinel: ErrChangeNotFound/ErrChangeExists/ErrBatchNotFound/ErrBatchExists/ErrNoTenant
```

- [ ] **Step 1: 写失败测试**

`model_test.go`：

```go
package change

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func TestChangeValidate(t *testing.T) {
	c := Change{Title: "用户导出", Type: ChangeFeat, Branch: "feat/user-export", RepoID: "repo-1", AppID: "app-1"}
	if err := c.Validate(); err != nil {
		t.Fatalf("合法变更不应报错: %v", err)
	}
	c.Type = "perf" // 非法类型
	if err := c.Validate(); err == nil {
		t.Fatal("type=perf 应被拒（仅 feat|hotfix）")
	}
	c2 := Change{Type: ChangeHotfix, Branch: ""} // 无分支且未引用
	if err := c2.Validate(); err == nil {
		t.Fatal("branch 空应被拒")
	}
}

func TestMemoryCRUD(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtx()
	created, err := s.CreateChange(ctx, Change{AppID: "app-1", RepoID: "r1", Title: "t", Type: ChangeFeat, Branch: "feat/a"})
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if created.Status != ChangeOpen {
		t.Fatalf("初始状态应 open, got %s", created.Status)
	}
	got, _ := s.GetChange(ctx, created.ID)
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 以 ctx 为准, got %s", got.TenantID)
	}
	list, _ := s.ListChanges(ctx, "app-1", "")
	if len(list) != 1 {
		t.Fatalf("应 1 条, got %d", len(list))
	}
	filtered, _ := s.ListChanges(ctx, "app-1", ChangeOpen)
	if len(filtered) != 1 {
		t.Fatalf("状态过滤应命中")
	}
	// 跨租户 not found
	gctx := tenant.WithTenant(context.Background(), "t-globex")
	if _, err := s.GetChange(gctx, created.ID); err != ErrChangeNotFound {
		t.Fatalf("跨租户应 ErrChangeNotFound, got %v", err)
	}
	// 缺租户拒
	if _, err := s.CreateChange(context.Background(), Change{AppID: "a", Branch: "b", Type: ChangeFeat}); err != ErrNoTenant {
		t.Fatalf("缺租户应 ErrNoTenant, got %v", err)
	}
	// 分支租户内唯一（同 repo）
	if _, err := s.CreateChange(ctx, Change{AppID: "app-1", RepoID: "r1", Title: "dup", Type: ChangeFeat, Branch: "feat/a"}); err != ErrChangeExists {
		t.Fatalf("同分支应 ErrChangeExists, got %v", err)
	}
}

func TestBatchCRUD(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtx()
	b, err := s.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "r1", Title: "8月集成", Branch: "integration/20260815-1"})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if b.Status != BatchCollecting {
		t.Fatalf("初始 collecting, got %s", b.Status)
	}
	b.Status = BatchTesting
	b.ChangeIDs = []string{"chg-1", "chg-2"}
	upd, err := s.UpdateBatch(ctx, b)
	if err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	got, _ := s.GetBatch(ctx, upd.ID)
	if len(got.ChangeIDs) != 2 || got.ChangeIDs[0] != "chg-1" {
		t.Fatalf("ChangeIDs 有序持久化失败: %v", got.ChangeIDs)
	}
	if _, err := s.GetBatch(context.Background(), upd.ID); err != ErrNoTenant {
		t.Fatalf("缺租户应拒, got %v", err)
	}
}

func TestDeepCopyIsolation(t *testing.T) {
	// 读返回深拷贝：改返回值不影响 store（race 防御，与全仓 memory 模式一致）
	s := NewMemoryStore()
	ctx := acmeCtx()
	b, _ := s.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "r1", Title: "t", Branch: "integration/x"})
	got, _ := s.GetBatch(ctx, b.ID)
	got.ChangeIDs = append(got.ChangeIDs, "chg-hack")
	again, _ := s.GetBatch(ctx, b.ID)
	if len(again.ChangeIDs) != 0 {
		t.Fatal("ChangeIDs 应深拷贝隔离")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/change/ -v`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现 model.go + repository.go + store_memory.go**

`model.go`：实体 + 状态常量 + `ChangeFeat="feat"`/`ChangeHotfix="hotfix"` + `Validate()`（title/type/branch 非空、type ∈ {feat,hotfix}；BranchCreated=false 时仅要求 branch 非空）+ `ValidateBatch()`（title/branch 非空）。

`repository.go`：Repository 接口 + sentinel 错误（Task 2 Interfaces 块原文）。

`store_memory.go`：`NewMemoryStore()` 返回 `*MemoryStore`（实现 Repository），`sync.Mutex` + map[string]Change/map[string]IntegrationBatch；Create 生成 ID（`chg-`/`batch-` + randHex 风格用 `crypto/rand` 8 字节 hex，参照 pipeline store_memory）；Create 补 TenantID（ctx 为准忽略请求体）+ 时间戳；读全深拷贝（Change 值类型 + Batch.ChangeIDs/ReleaseIDs 切片拷贝）；同 (tenant, repo) 分支唯一（遍历查重）；缺租户返 ErrNoTenant。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/change/ -race -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/devops/change/
git commit -m "feat(change): Change/IntegrationBatch 领域模型 + memory 实现"
```

---

### Task 3: pg store + migration 0027

**Files:**
- Create: `internal/devops/change/pg/store.go`
- Create: `internal/storage/pg/migrations/0027_change_management.up.sql`
- Create: `internal/storage/pg/migrations/0027_change_management.down.sql`
- Modify: `internal/storage/pg/migrations/0001_init.up.sql`（新部署合并 schema）
- Test: `internal/devops/change/pg/store_test.go`（`//go:build integration`）

**Interfaces:**
- Consumes: Task 2 的 Repository 接口与实体（签名不变，pg 实现）
- Produces: `func NewStore(db *pg.DB) *Store`（实现 change.Repository）；`func (s *Store) ChangesCount(ctx) (int64, error)`（seed 判空用）

migration SQL（0027.up，幂等与既有风格一致）：

```sql
CREATE TABLE IF NOT EXISTS changes (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    title TEXT NOT NULL,
    type TEXT NOT NULL,
    branch TEXT NOT NULL,
    branch_created BOOLEAN NOT NULL DEFAULT FALSE,
    base_branch TEXT NOT NULL DEFAULT 'main',
    status TEXT NOT NULL,
    batch_id TEXT NOT NULL DEFAULT '',
    conflict_with TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_changes_tenant_repo_branch ON changes(tenant_id, repo_id, branch);
CREATE TABLE IF NOT EXISTS integration_batches (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    title TEXT NOT NULL,
    branch TEXT NOT NULL,
    status TEXT NOT NULL,
    change_ids JSONB NOT NULL DEFAULT '[]',
    pipeline_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    release_ids JSONB NOT NULL DEFAULT '[]',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_batches_tenant_branch ON integration_batches(tenant_id, branch);
```

down：`DROP TABLE IF EXISTS changes, integration_batches;`。0001_init.up.sql 追加同款两表（IF NOT EXISTS，幂等安全）。

- [ ] **Step 1: 写失败测试**

`pg/store_test.go`（参照 `internal/devops/pg/store_test.go` 的 newTestDB/resetSchema 模式；resetSchema 的 DROP 列表需含 `changes, integration_batches`）：

```go
//go:build integration

package pg

import (
	"context"
	"os"
	"testing"

	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/devops/change"
	"github.com/aitoys/paas/pkg/tenant"
)

func newTestDB(t *testing.T) *pg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过")
	}
	ctx := context.Background()
	db, err := pg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := pg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

func resetSchema(t *testing.T, db *pg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS changes, integration_batches CASCADE; DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func TestPGChangeRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	c, err := s.CreateChange(ctx, change.Change{AppID: "app-1", RepoID: "r1", Title: "导出", Type: change.ChangeFeat, Branch: "feat/export", BranchCreated: true, BaseBranch: "main"})
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	got, err := s.GetChange(ctx, c.ID)
	if err != nil || got.Title != "导出" || !got.BranchCreated || got.Status != change.ChangeOpen {
		t.Fatalf("往返不一致: %+v err=%v", got, err)
	}
	// Update：入批 + 状态推进
	got.Status = change.ChangeIntegrated
	got.BatchID = "batch-1"
	got.ConflictWith = "chg-prev"
	if _, err := s.UpdateChange(ctx, got); err != nil {
		t.Fatalf("UpdateChange: %v", err)
	}
	again, _ := s.GetChange(ctx, c.ID)
	if again.Status != change.ChangeIntegrated || again.BatchID != "batch-1" || again.ConflictWith != "chg-prev" {
		t.Fatalf("更新往返失败: %+v", again)
	}
	// 分支唯一
	if _, err := s.CreateChange(ctx, change.Change{AppID: "app-1", RepoID: "r1", Title: "dup", Type: change.ChangeFeat, Branch: "feat/export"}); err != change.ErrChangeExists {
		t.Fatalf("分支唯一应 ErrChangeExists, got %v", err)
	}
	// 跨租户
	gctx := tenant.WithTenant(context.Background(), "t-globex")
	if _, err := s.GetChange(gctx, c.ID); err != change.ErrChangeNotFound {
		t.Fatalf("跨租户 not found, got %v", err)
	}
}

func TestPGBatchRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	b, err := s.CreateBatch(ctx, change.IntegrationBatch{AppID: "app-1", RepoID: "r1", Title: "集成", Branch: "integration/20260815-1"})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	b.Status = change.BatchReleased
	b.ChangeIDs = []string{"chg-1", "chg-2", "chg-3"}
	b.ReleaseIDs = []string{"rel-1"}
	if _, err := s.UpdateBatch(ctx, b); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	got, _ := s.GetBatch(ctx, b.ID)
	if len(got.ChangeIDs) != 3 || got.ChangeIDs[2] != "chg-3" || len(got.ReleaseIDs) != 1 || got.Status != change.BatchReleased {
		t.Fatalf("JSONB 有序往返失败: %+v", got)
	}
	// 状态过滤
	list, _ := s.ListBatches(ctx, "app-1", change.BatchReleased)
	if len(list) != 1 {
		t.Fatalf("状态过滤应 1 条, got %d", len(list))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `PAAS_TEST_PG_URL=postgres://paas:paas@localhost:5432/paas_test go test ./internal/devops/change/pg/ -v`（DSN 以本地 docker compose postgres 为准）
Expected: FAIL（NewStore 未定义）

- [ ] **Step 3: 实现 pg store**

参照 `internal/devops/pipeline/store_pg.go` 模式：`changeCols` 常量（列序与 SQL 严格对齐）+ scanChange/scanBatch + Create/Get/Update/List；`change_ids`/`release_ids` JSONB 用 `json.Marshal/Unmarshal`（nil 安全：读空返 nil slice）；`Update` 带 `WHERE id=$1 AND tenant_id=$2`（0 行返 ErrChangeNotFound/ErrBatchNotFound）；分支唯一捕获 `pgconn.PgError` SQLState `23505` → `ErrChangeExists`/`ErrBatchExists`（参照 helpers.IsUniqueViolation）；List 按 created_at 倒序 + 可选 status 过滤 + `WHERE tenant_id=$1`。另在 `cmd/core/persistence.go` 装配点不动的原则下，本 task 只交付 store + `ChangesCount`。

- [ ] **Step 4: 跑测试确认通过**

Run: 同 Step 2
Expected: PASS；再跑 `make test` 确认无破坏（migration 0027 对现有集成测试包无影响——resetSchema 各包 DROP 自己的表）。

- [ ] **Step 5: Commit**

```bash
git add internal/devops/change/pg/ internal/storage/pg/migrations/
git commit -m "feat(change): PostgreSQL 持久化 + migration 0027"
```

---

### Task 4: 编排 service（integrate/approve/release/惰性推进）

**Files:**
- Create: `internal/devops/change/service.go`
- Create: `internal/devops/change/service_test.go`

**Interfaces:**
- Consumes: Task 2 Repository；Task 1 Gitea 分支 API 经依赖倒置接口：

```go
// GiteaBrancher 变更编排对 git 后端的最小依赖（cmd/core 桥接 gitea.Client）。
type GiteaBrancher interface {
	CreateBranch(ctx context.Context, owner, repo, branch, from string) error
	GetBranch(ctx context.Context, owner, repo, branch string) (gitea.Branch, error)
	DeleteBranch(ctx context.Context, owner, repo, branch string) error
	Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error)
}
// RunTrigger 批次触发流水线 run（cmd/core 桥接 pipeline 包组 run 路径）。
// branch 即 run.branch；返 runID。
type RunTrigger interface {
	TriggerAppRun(ctx context.Context, appID, pipelineID, branch string) (runID string, err error)
}
// RunReader 惰性终态推进（读 run 状态）。
type RunReader interface {
	GetRunStatus(ctx context.Context, runID string) (status string, err error)
}
```

- Produces（Task 5 handler 消费）:

```go
type Service struct { /* repo Repository; gitea GiteaBrancher; runs RunTrigger; readRuns RunReader; repoResolver func(ctx, appID) (owner, repoName, repoID string, err error) */ }
func NewService(repo Repository, opts ...ServiceOpt) *Service
func WithGitea(g GiteaBrancher) ServiceOpt
func WithRunTrigger(rt RunTrigger) ServiceOpt
func WithRunReader(rr RunReader) ServiceOpt
func WithRepoLookup(f func(ctx context.Context, appID string) (owner, repo, repoID string, error)) ServiceOpt
// 核心方法（handler 调；错误均为 sentinel 或包装中文消息）
func (s *Service) CreateChangeWithBranch(ctx, appID string, in ChangeInput) (Change, error)  // 建分支或校验已有
func (s *Service) AbandonChange(ctx, id string) (Change, error)                               // open→abandoned；integrated 拒绝（先移出批次）
func (s *Service) AddChangeToBatch(ctx, batchID, changeID string) (IntegrationBatch, error)   // 仅 collecting + change.status=open
func (s *Service) RemoveChangeFromBatch(ctx, batchID, changeID string) (IntegrationBatch, error) // collecting/conflict/failed
func (s *Service) Integrate(ctx, batchID string) (IntegrationBatch, error)                    // 编排见下
func (s *Service) Approve(ctx, batchID string) (IntegrationBatch, error)                      // tested→releasing（prod:write 由 handler 校验）
func (s *Service) Release(ctx, batchID string) (IntegrationBatch, error)                      // merge main + CD run
func (s *Service) SyncBatchStatus(ctx, batchID string) (IntegrationBatch, error)              // 惰性推进（GET 详情时调）
type ChangeInput struct { Title, Type, Branch, BaseBranch string; CreateBranch bool }
```

新 sentinel：`ErrBatchState`（状态机非法转移）、`ErrMergeConflictBatch`（含冲突信息：`type BatchConflictError struct{ BatchID, FailedChangeID, PrevChangeID string }`，实现 error 接口）、`ErrGiteaNotConfigured`、`ErrNoCIPipeline`。

**Integrate 编排（spec §5.2/5.6）**：
1. GetBatch 校验 status ∈ {collecting, conflict, failed} 且 len(ChangeIDs)>0
2. 集成分支重建：`DeleteBranch(batch.Branch)`（ErrBranchNotFound 忽略）→ `CreateBranch(batch.Branch, "main")`
3. for change in ChangeIDs（有序）：`Merge(head=change.Branch, base=batch.Branch, mode="merge")`；ErrMergeConflict → 批次 BatchConflict + 该 change.ConflictWith=前一变更 ID + 返回 `*BatchConflictError`
4. 全成功 → 触发 CI run：pipeline 取 app 第一条 kind=ci（`s.findPipeline(ctx, appID, "ci")` 经 RunTrigger 扩展接口 `ListAppPipelines(ctx, appID) ([]PipelineInfo{ID,Kind})` 或 repoResolver 时注入——**采用 RunTrigger 上加 `FindPipeline(ctx, appID, kind string) (string, error)`**）；branch=batch.Branch；记 RunID
5. 批次 BatchTesting + 批内 change 全部 ChangeIntegrated（清 ConflictWith）

**SyncBatchStatus（spec §5.5）**：
- testing + RunID 非空 → GetRunStatus：succeeded→BatchTested + change→ChangeTested；failed→BatchFailed + change 回 ChangeOpen（BatchID 清空，可重新入批）；aborted→BatchFailed 同款
- releasing + RunID 非空 → succeeded→BatchReleased + change→ChangeReleased + FinishedAt（ReleaseIDs 查询经 RunTrigger 的 `ReleasesOfRun(ctx, runID) ([]string)`——cmd/core 桥接 devops ListReleases 过滤 SourceRunID）；failed→停 releasing（可重试 Release）

**Release 编排（spec §5.3）**：状态 releasing；逐个 `Merge(head=change.Branch, base="main")`，冲突→批次回 BatchTested + `*BatchConflictError`（failedChangeID 标 ConflictWith）；全成功 → CD run（kind=cd pipeline，branch=main）+ 记 RunID（覆盖 CI 的 RunID 字段，批次只留当前活跃 run）。幂等：已合并分支再 merge 会 409 冲突——Gitea 对 no-diff PR 返回 409，捕获后视为已合并继续（`isAlreadyMerged(err)`：Gitea 返回体含 "nothing to merge" 不易判，简化：Release 前置检查 `GetBranch(change.Branch)` 的 CommitSHA 已在 main 历史则跳过——**MVP 简化：直接尝试 merge，ErrMergeConflict 时若 change.Status==ChangeTested 之前的批次已 merge 过则跳过**。实施采用：Release 失败重试时用户先在 git 解决；计划锁定为「merge 冲突即停 + 报冲突变更，重试由用户解决后重发起」，不做已合并检测——Gitea no-diff merge 409 文案实测再定，代码中留 `//nolint` 注释标记此已知边界）。

- [ ] **Step 1: 写失败测试（fake GiteaBrancher + fake RunTrigger/RunReader，覆盖编排全路径）**

`service_test.go`（核心用例，fake 记录调用序列）：

```go
package change

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/devops/gitea"
)

// fakeBrancher 记录调用序列 + 可注入 merge 冲突。
type fakeBrancher struct {
	created   []string // branch 名
	deleted   []string
	merges    [][2]string // {head, base}
	mergeErrs map[string]error // head 分支名 -> 错误（注入冲突）
}
func (f *fakeBrancher) CreateBranch(ctx context.Context, owner, repo, branch, from string) error {
	f.created = append(f.created, branch)
	return nil
}
func (f *fakeBrancher) GetBranch(ctx context.Context, owner, repo, branch string) (gitea.Branch, error) {
	return gitea.Branch{Name: branch, CommitSHA: "sha-" + branch}, nil
}
func (f *fakeBrancher) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	f.deleted = append(f.deleted, branch)
	return nil
}
func (f *fakeBrancher) Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error) {
	f.merges = append(f.merges, [2]string{head, base})
	if err := f.mergeErrs[head]; err != nil {
		return "", err
	}
	return "sha-" + head, nil
}

type fakeRuns struct {
	triggered []string // branch
	status    string
	releases  []string
	pipeline  string // FindPipeline 返回
}
func (f *fakeRuns) TriggerAppRun(ctx context.Context, appID, pid, branch string) (string, error) {
	f.triggered = append(f.triggered, branch)
	return "run-" + branch, nil
}
func (f *fakeRuns) GetRunStatus(ctx context.Context, runID string) (string, error) { return f.status, nil }
func (f *fakeRuns) FindPipeline(ctx context.Context, appID, kind string) (string, error) {
	return f.pipeline, nil
}
func (f *fakeRuns) ReleasesOfRun(ctx context.Context, runID string) ([]string, error) {
	return f.releases, nil
}

func newTestService(t *testing.T) (*Service, *MemoryStore, *fakeBrancher, *fakeRuns) {
	store := NewMemoryStore()
	g, runs := &fakeBrancher{}, &fakeRuns{pipeline: "pipe-ci", status: "succeeded"}
	s := NewService(store, WithGitea(g), WithRunTrigger(runs), WithRunReader(runs),
		WithRepoLookup(func(ctx context.Context, appID string) (string, string, string, error) {
			return "paas-bot", "app-1", "repo-1", nil
		}))
	return s, store, g, runs
}
```

用例（每个独立 t.Run，均用 acmeCtx）：

```go
func TestCreateChangeBranchAndExisting(t *testing.T) {
	s, _, g, _ := newTestService(t)
	ctx := acmeCtx()
	// 平台建分支
	c, err := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "导出", Type: ChangeFeat, Branch: "feat/export", CreateBranch: true})
	if err != nil { t.Fatalf("建分支创建: %v", err) }
	if !c.BranchCreated || len(g.created) != 1 { t.Fatalf("应调 Gitea CreateBranch") }
	// 引用已有分支（不建）
	c2, err := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "修复", Type: ChangeHotfix, Branch: "hotfix/login", CreateBranch: false})
	if err != nil { t.Fatal(err) }
	if c2.BranchCreated || len(g.created) != 1 { t.Fatalf("引用已有分支不应再建") }
}

func TestIntegrateHappyPath(t *testing.T) {
	s, store, g, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a", CreateBranch: false})
	c2, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "b", Type: ChangeFeat, Branch: "feat/b", CreateBranch: false})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "集成", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	b, _ = s.AddChangeToBatch(ctx, b.ID, c2.ID)

	got, err := s.Integrate(ctx, b.ID)
	if err != nil { t.Fatalf("Integrate: %v", err) }
	if got.Status != BatchTesting { t.Fatalf("应 testing, got %s", got.Status) }
	// merge 顺序 = ChangeIDs 顺序，base 均为集成分支
	if len(g.merges) != 2 || g.merges[0][0] != "feat/a" || g.merges[0][1] != "integration/x" || g.merges[1][0] != "feat/b" {
		t.Fatalf("merge 序列不符: %v", g.merges)
	}
	// 集成分支重建（先删后建）
	if len(g.deleted) != 1 || g.deleted[0] != "integration/x" { t.Fatalf("应重建集成分支: %v", g.deleted) }
	// CI run 以集成分支触发
	if len(runs.triggered) != 1 || runs.triggered[0] != "integration/x" { t.Fatalf("CI run branch 应=集成分支: %v", runs.triggered) }
	// change → integrated
	ch1, _ := store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeIntegrated || ch1.BatchID != b.ID { t.Fatalf("change 应 integrated: %+v", ch1) }
}

func TestIntegrateConflict(t *testing.T) {
	s, store, g, _ := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	c2, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "b", Type: ChangeFeat, Branch: "feat/b"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.AddChangeToBatch(ctx, b.ID, c2.ID)
	g.mergeErrs = map[string]error{"feat/b": gitea.ErrMergeConflict}

	_, err := s.Integrate(ctx, b.ID)
	var ce *BatchConflictError
	if !errors.As(err, &ce) || ce.FailedChangeID != c2.ID || ce.PrevChangeID != c1.ID {
		t.Fatalf("应返 BatchConflictError(c2 冲突 c1), got %v", err)
	}
	got, _ := store.GetBatch(ctx, b.ID)
	if got.Status != BatchConflict { t.Fatalf("批次应 conflict, got %s", got.Status) }
	ch2, _ := store.GetChange(ctx, c2.ID)
	if ch2.ConflictWith != c1.ID { t.Fatalf("change.ConflictWith 应=c1: %+v", ch2) }
	// conflict 状态可移出变更（spec 状态机）
	if _, err := s.RemoveChangeFromBatch(ctx, b.ID, c2.ID); err != nil {
		t.Fatalf("conflict 批次移出变更应允许: %v", err)
	}
}

func TestSyncBatchStatusAdvances(t *testing.T) {
	s, store, _, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.Integrate(ctx, b.ID)

	// CI succeeded → tested
	runs.status = "succeeded"
	got, err := s.SyncBatchStatus(ctx, b.ID)
	if err != nil || got.Status != BatchTested { t.Fatalf("应 tested: %+v err=%v", got, err) }
	ch1, _ := store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeTested { t.Fatalf("change 应 tested: %s", ch1.Status) }

	// approve → releasing
	got, err = s.Approve(ctx, b.ID)
	if err != nil || got.Status != BatchReleasing { t.Fatalf("approve: %+v err=%v", got, err) }

	// CD succeeded → released + change released + ReleaseIDs 回填
	runs.status = "succeeded"
	runs.releases = []string{"rel-1", "rel-2"}
	got, err = s.SyncBatchStatus(ctx, b.ID)
	if err != nil || got.Status != BatchReleased { t.Fatalf("应 released: %+v err=%v", got, err) }
	if len(got.ReleaseIDs) != 2 { t.Fatalf("ReleaseIDs 应回填: %v", got.ReleaseIDs) }
	ch1, _ = store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeReleased { t.Fatalf("change 应 released: %s", ch1.Status) }
}

func TestSyncBatchStatusCIFailedReopens(t *testing.T) {
	s, store, _, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.Integrate(ctx, b.ID)

	runs.status = "failed"
	got, _ := s.SyncBatchStatus(ctx, b.ID)
	if got.Status != BatchFailed { t.Fatalf("应 failed: %s", got.Status) }
	ch1, _ := store.GetChange(ctx, c1.ID)
	if ch1.Status != ChangeOpen || ch1.BatchID != "" { t.Fatalf("change 应回 open 且出批: %+v", ch1) }
	// failed 后可重新 integrate（状态机循环）
	if _, err := s.AddChangeToBatch(ctx, b.ID, c1.ID); err != nil {
		t.Fatalf("failed 批次重新收变更应允许: %v", err)
	}
}

func TestReleaseMergesToMainAndTriggersCD(t *testing.T) {
	s, store, g, runs := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	_, _ = s.Integrate(ctx, b.ID)
	runs.status = "succeeded"
	_, _ = s.SyncBatchStatus(ctx, b.ID)
	_, _ = s.Approve(ctx, b.ID)

	got, err := s.Release(ctx, b.ID)
	if err != nil { t.Fatalf("Release: %v", err) }
	// merge 到 main
	if len(g.merges) < 2 || g.merges[len(g.merges)-1][0] != "feat/a" || g.merges[len(g.merges)-1][1] != "main" {
		t.Fatalf("应有 merge 到 main: %v", g.merges)
	}
	// CD run 以 main 触发
	if runs.triggered[len(runs.triggered)-1] != "main" { t.Fatalf("CD run branch 应=main: %v", runs.triggered) }
	if got.Status != BatchReleasing { t.Fatalf("应 releasing（等 CD 终态）: %s", got.Status) }
}

func TestStateMachineGuards(t *testing.T) {
	s, store, _, _ := newTestService(t)
	ctx := acmeCtx()
	c1, _ := s.CreateChangeWithBranch(ctx, "app-1", ChangeInput{Title: "a", Type: ChangeFeat, Branch: "feat/a"})
	b, _ := store.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "repo-1", Title: "x", Branch: "integration/x"})
	// 空批次 integrate 拒
	if _, err := s.Integrate(ctx, b.ID); err == nil { t.Fatal("空批次 integrate 应拒") }
	// collecting 才能 approve
	if _, err := s.Approve(ctx, b.ID); err == nil { t.Fatal("非 tested 批次 approve 应拒") }
	// 已入批变更不能 abandon
	_, _ = s.AddChangeToBatch(ctx, b.ID, c1.ID)
	if _, err := s.AbandonChange(ctx, c1.ID); err == nil { t.Fatal("integrated 变更 abandon 应拒（先移出批次）") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/change/ -run TestIntegrate -v`
Expected: FAIL（Service 未定义）

- [ ] **Step 3: 实现 service.go**

按本 task Interfaces 块 + 编排说明实现。要点：gitea nil 时返 `ErrGiteaNotConfigured`（降级不 panic）；所有状态转移走 store.Get→改→Update（memory 锁内安全；pg 依赖状态机守卫先行检查，竞态窗口可接受——变更管理低频操作）；`BatchConflictError` 结构体含 `BatchID/FailedChangeID/PrevChangeID` 三字段 + `Error() string` 返回中文「变更 X 与 Y 冲突」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/change/ -race -v`
Expected: PASS（全包含 Task 2 用例）

- [ ] **Step 5: Commit**

```bash
git add internal/devops/change/
git commit -m "feat(change): 集成/上线编排 service + 状态机惰性推进"
```

---

### Task 5: Handler（REST + 权限 + 审计 + OpenAPI）+ cmd/core 装配

**Files:**
- Create: `internal/devops/change/handler.go`
- Test: `internal/devops/change/handler_test.go`
- Modify: `cmd/core/main.go`（composite 加 `changes`/`batches` 分发 + handler 构造 + OpenAPI Operation 登记）
- Modify: `cmd/core/persistence.go`（Stores 加 `Change change.Repository`，内存/PG 两路径装配）
- Create: `cmd/core/change_adapters.go`（giteaBrancherBridge/runTriggerBridge：桥接 gitea.Client + pipeline 包）

**Interfaces:**
- Consumes: Task 4 Service 全部方法；pipeline 包 `Store`/`Engine`（runTriggerBridge 内组 run：复用 `pipeline.Handler` 无法直接调私有 triggerRunInternal，**改为 runTriggerBridge 自行组 run**：GetPipeline→GetTemplate→ResolveStages→CreateRun→engine.Start，与 triggerRunInternal 同构，约 40 行）；devops `ListReleases`（ReleasesOfRun 过滤 SourceRunID）。

  RunTrigger 完整桥接接口（Task 4 已定 + 本 task 实现）：

  ```go
  // runTriggerBridge 实现 change.RunTrigger + change.RunReader（cmd/core/change_adapters.go）。
  type runTriggerBridge struct {
    pipes   pipeline.Repository
    runs    pipeline.RunRepository
    templates pipeline.TemplateRepository
    resolver pipeline.ParamResolver
    repos   pipeline.RepoResolver
    engine  *pipeline.Engine
    rels    devops.ReleaseRepository // ListReleases 过滤 SourceRunID
  }
  func (b *runTriggerBridge) TriggerAppRun(ctx, appID, pid, branch) (string, error)
  func (b *runTriggerBridge) GetRunStatus(ctx, runID) (string, error)
  func (b *runTriggerBridge) FindPipeline(ctx, appID, kind) (string, error)  // ListPipelines 找 kind 匹配
  func (b *runTriggerBridge) ReleasesOfRun(ctx, runID) ([]string, error)
  ```

- Produces:
  - `change.NewHandler(svc *Service, opts ...HandlerOpt) *Handler`，HandlerOpt：`WithAuthorize(func(r,perm) bool)`、`WithAudit(AuditRecorder)`、`WithActorFn(func(r) string)`、`WithProdWrite(func(r) bool)`
  - Handler.ServeHTTP 路径分发（composite 已剥 `/api/applications/{id}/` 前缀，收 `changes/...`/`batches/...` 子路径，参照 pipeline handler serveAppPipelines 模式）

REST（spec §4）：

```
GET/POST  changes[?status=]          GET/DELETE changes/{cid}
GET/POST  batches[?status=]          GET/DELETE batches/{bid}
POST/DELETE batches/{bid}/changes[{/cid}]
POST      batches/{bid}/integrate | approve | release
```

approve 前 `WithProdWrite` 校验（不足 403「生产上线需要 prod:write 权限」）。全写操作 recordAudit（action：`change_create`/`change_abandon`/`batch_create`/`batch_abandon`/`batch_integrate`/`batch_approve`/`batch_release`/`batch_add_change`/`batch_remove_change`）。GET batch 详情先调 `svc.SyncBatchStatus`（惰性推进）。`*BatchConflictError` → 409 + 响应体含冲突变更（`{data: batch, error: "变更 X 与 Y 合并冲突"}` 用 WriteError(409, msg)）。

- [ ] **Step 1: 写失败测试**

`handler_test.go`（httptest，fake Authorize=true / fake Service 用 Task 4 真实 Service + memory store + fakes——端到端 HTTP 层）：

```go
// 核心用例（HTTP 层，Service 用真实实现 + Task 4 fakes）：
// TestHandlerChangeLifecycle: POST /api/applications/app-1/changes 建变更（201+data.id）
//   → GET 列表（200 data 数组）→ DELETE（204）
// TestHandlerBatchFlow: 建 2 变更 → POST batches → POST batches/{id}/changes ×2
//   → POST integrate（200，data.status=testing）→（fake run succeeded）GET batches/{id}（data.status=tested，惰性推进生效）
//   → POST approve（无 prod:write → 403；配 prod:write → 200 releasing）→ POST release（200）
// TestHandlerConflict409: fake merge 冲突 → integrate 返 409 + error 含「冲突」
// TestHandlerUnauthorized: Authorize=false → 403
// TestHandlerAudit: fake AuditRecorder 收到 batch_integrate 记录
```

（实现者按上述用例名展开完整 Go 测试代码，模式照抄 `pipeline/handler_test.go` 既有结构：构造 Handler + httptest.NewRequest + ServeHTTP + 断言状态码/JSON 解包。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/change/ -run TestHandler -v`
Expected: FAIL

- [ ] **Step 3: 实现 handler.go**

参照 `pipeline/handler.go`：路径 parse（`strings.Split(strings.Trim(path,"/"),"/")` 分发）+ `h.allow(w,r,perm)` 权限 + `h.audit(...)` + body decode。权限：读 `pipeline:read`、写 `pipeline:write`、approve 额外 prod:write。

- [ ] **Step 4: cmd/core 装配**

`persistence.go`：Stores 加 `Change change.Repository`；内存路径 `change.NewMemoryStore()`、PG 路径 `changepg.NewStore(db)`。
`change_adapters.go`：`giteaBrancherBridge{client *gitea.Client}`（四方法直传）+ `runTriggerBridge`（Interfaces 块签名；TriggerAppRun 组 run 与 pipeline triggerRunInternal 同构——HasActiveRun 检查 + ResolveStages + CreateRun + engine.Start）。
`main.go`：composite switch 加：

```go
case "changes", "batches":
    changeHandler.ServeHTTP(w, r)
    return
```

构造：`changeHandler := change.NewHandler(changeSvc, change.WithAuthorize(...gateway.RequestAllowed), change.WithAudit(&identityAuditAdapter{store: stores.Security}), change.WithActorFn(func(r) string { return gateway.UserIDFrom(r.Context()) }), change.WithProdWrite(func(r) bool { return gateway.RequestAllowed(r, "prod:write") }))`；`changeSvc := change.NewService(stores.Change, change.WithGitea(&giteaBrancherBridge{client: giteaClient}), change.WithRunTrigger(runTriggerBridgeInst), change.WithRunReader(runTriggerBridgeInst), change.WithRepoLookup(changeRepoLookup(stores.DevOpsRepos)))`（lookup：ListRepos 找 Source==internal → owner=CodeRepo.GiteaOwner/repo=GiteaRepo/repoID）。

OpenAPI 登记（`reg.Operation`，12 条，参照 pipelines 既有条目风格）：

```go
reg.Operation("GET", "/api/applications/{id}/changes", apiroute.Tags("变更"), apiroute.Summary("变更列表"), apiroute.Perm("pipeline:read"), apiroute.WithResp([]change.Change{}))
reg.Operation("POST", "/api/applications/{id}/changes", ... Perm("pipeline:write"), WithReqBody(change.Change{}), WithResp(change.Change{}))
reg.Operation("GET", "/api/applications/{id}/changes/{cid}", ... Perm("pipeline:read"), WithResp(change.Change{}))
reg.Operation("DELETE", "/api/applications/{id}/changes/{cid}", ... Perm("pipeline:write"))
// batches 同款 8 条（list/create/get/delete/changes POST/changes/{cid} DELETE/integrate/approve/release——9 条）
```

- [ ] **Step 5: 跑全量测试 + 构建确认**

Run: `go test ./... && go build ./cmd/core`
Expected: PASS + 构建成功

- [ ] **Step 6: Commit**

```bash
git add internal/devops/change/ cmd/core/
git commit -m "feat(change): REST handler + cmd/core 装配 + OpenAPI 登记"
```

---

### Task 6: 前端「变更」tab（AppChanges.vue + 批次抽屉 + api）

**Files:**
- Create: `frontend/console-user/src/api/change.ts`
- Create: `frontend/console-user/src/views/app-tabs/AppChanges.vue`
- Modify: `frontend/console-user/src/views/ApplicationDetail.vue`（tab 注册「变更」，分组「DevOps」内，代码仓库之前）
- Modify: `frontend/console-user/src/views/DevOps.vue`（运行记录行 branch 以 `integration/` 开头显「集成」tag）

**Interfaces:**
- Consumes: Task 5 REST 端点（fetchAuth + `{data:T}` 解包，模式照抄 `src/api/pipeline.ts`）
- Produces: `listChanges(appId, status?)` / `createChange(appId, body)` / `abandonChange(appId, id)` / `listBatches(appId, status?)` / `createBatch(appId, body)` / `getBatch(appId, id)` / `abandonBatch(appId, id)` / `addChangeToBatch` / `removeChangeFromBatch` / `integrateBatch` / `approveBatch` / `releaseBatch` + TS 类型 `Change`/`IntegrationBatch`

`api/change.ts`（完整实现）：

```ts
import { fetchAuth } from './'

export interface Change {
  id: string; appId: string; repoId: string; title: string; type: 'feat' | 'hotfix'
  branch: string; branchCreated: boolean; baseBranch: string
  status: 'open' | 'integrated' | 'tested' | 'released' | 'reverted' | 'abandoned'
  batchId: string; conflictWith: string; createdAt: string
}
export interface IntegrationBatch {
  id: string; appId: string; title: string; branch: string
  status: 'collecting' | 'building' | 'conflict' | 'testing' | 'tested' | 'releasing' | 'released' | 'failed' | 'abandoned'
  changeIds: string[]; pipelineId: string; runId: string; releaseIds: string[]; createdAt: string
}
export interface ChangeInput { title: string; type: 'feat' | 'hotfix'; branch: string; baseBranch?: string; createBranch: boolean }

const unwrap = async <T>(resp: Response): Promise<T> => {
  const j = await resp.json()
  if (!resp.ok) throw new Error(j?.error || `HTTP ${resp.status}`)
  return j?.data ?? j as T
}

export const listChanges = (appId: string, status = '') =>
  fetchAuth(`/api/applications/${appId}/changes${status ? `?status=${status}` : ''}`).then(r => unwrap<Change[]>(r))
export const createChange = (appId: string, body: ChangeInput) =>
  fetchAuth(`/api/applications/${appId}/changes`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).then(r => unwrap<Change>(r))
export const abandonChange = (appId: string, id: string) =>
  fetchAuth(`/api/applications/${appId}/changes/${id}`, { method: 'DELETE' })
export const listBatches = (appId: string, status = '') =>
  fetchAuth(`/api/applications/${appId}/batches${status ? `?status=${status}` : ''}`).then(r => unwrap<IntegrationBatch[]>(r))
export const getBatch = (appId: string, id: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${id}`).then(r => unwrap<IntegrationBatch>(r))
export const createBatch = (appId: string, body: { title: string }) =>
  fetchAuth(`/api/applications/${appId}/batches`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).then(r => unwrap<IntegrationBatch>(r))
export const abandonBatch = (appId: string, id: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${id}`, { method: 'DELETE' })
export const addChangeToBatch = (appId: string, bid: string, changeId: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/changes`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ changeId }) })
export const removeChangeFromBatch = (appId: string, bid: string, cid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/changes/${cid}`, { method: 'DELETE' })
export const integrateBatch = (appId: string, bid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/integrate`, { method: 'POST' }).then(r => unwrap<IntegrationBatch>(r))
export const approveBatch = (appId: string, bid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/approve`, { method: 'POST' }).then(r => unwrap<IntegrationBatch>(r))
export const releaseBatch = (appId: string, bid: string) =>
  fetchAuth(`/api/applications/${appId}/batches/${bid}/release`, { method: 'POST' }).then(r => unwrap<IntegrationBatch>(r))
```

`AppChanges.vue` 要点（完整组件，参照 `AppPipelines.vue` 结构）：
- **变更列表**（el-table）：标题 / 类型 tag（feat=success、hotfix=danger）/ 分支（monospace + 复制按钮 navigator.clipboard）/ 状态 tag（open=info、integrated/tested=warning、released=success、abandoned=info）/ 所属批次（可点开批次抽屉）/ 放弃按钮（open 状态显，ElMessageBox.confirm）。
- **创建变更弹窗**（el-dialog）：标题 input + 类型 radio（feat/hotfix）+ 「平台创建分支 / 引用已有分支」radio + 分支名 input（placeholder `feat/user-export`）+ 基分支 input（默认 main）。成功后 ElMessageBox 展示 clone 命令 `git fetch origin && git checkout <branch>`（复制按钮）。
- **批次 section**（下半区）：批次列表 el-table（标题/状态 tag 九态映射/集成分支 monospace/变更数/时间）+ 「创建批次」弹窗（标题 + open 变更多选 el-checkbox-group）。
- **批次详情抽屉**（el-drawer）：状态步骤条（el-steps：collecting→testing→tested→released，conflict/failed 显红色异常态）+ 变更 chips（可移出，collecting/conflict/failed 时显 × ）+ 操作按钮按状态显隐：integrate（collecting/conflict/failed）、approve（tested，走 `useDangerConfirm` 的 confirmDangerous(title, {isProd:true})——生产语义二次确认）、release（releasing 时显「重试上线」）+ 关联 run 链接（runId 非空时 `<router-link :to="'/devops/runs/'+batch.runId">查看运行</router-link>`）。
- 轮询：批次 testing/releasing 时 10s 轮询 getBatch（silent，onUnmounted clearInterval）。
- 状态 tag 映射表（中文）：collecting=收集中、building=合并中、conflict=合并冲突、testing=集成测试中、tested=测试通过、releasing=上线中、released=已上线、failed=测试失败、abandoned=已放弃。

`DevOps.vue` 运行记录行：`<el-tag v-if="row.branch?.startsWith('integration/')" size="small" type="warning">集成</el-tag>` 插在 branch 列。

- [ ] **Step 1: 实现 api/change.ts**（上方完整代码）
- [ ] **Step 2: 实现 AppChanges.vue + tab 注册**

ApplicationDetail.vue tab 分组「DevOps」加 `<el-tab-pane label="变更" name="changes"><AppChanges :app-id="app.id" /></el-tab-pane>`（import + components 注册，位置在「代码仓库」之前；props 模式照抄 AppPipelines）。

- [ ] **Step 3: 构建验证**

Run: `cd frontend && pnpm build`
Expected: vue-tsc + vite 全过（三套中 console-user 构建）

- [ ] **Step 4: Commit**

```bash
git add frontend/console-user/src/
git commit -m "feat(frontend): 应用详情「变更」tab（变更 CRUD + 集成批次抽屉）"
```

---

### Task 7: e2e 验证 + 部署

**Files:**
- Modify: `CLAUDE.md`（「垂直切片」追加变更管理章节，按既有切片文档风格 ~20 行）

**Interfaces:**
- Consumes: 全部前序 task 交付

- [ ] **Step 1: 全量测试**

Run: `make test && cd frontend && pnpm build`
Expected: 全绿

- [ ] **Step 2: 部署 dev 集群**（[[k8s-always-latest]] 常设授权）

Run: `./scripts/deploy-k8s.sh`
Expected: 镜像构建 + push + helm upgrade + rollout 成功

- [ ] **Step 3: e2e 验证（curl，sk-acme-admin）**

```bash
# 1. 建变更（平台建分支）
curl -s -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"title":"e2e变更","type":"feat","branch":"feat/e2e-test","createBranch":true}' \
  http://paas.k8s.dd/api/applications/paas-shop/changes
# 2. 建批次 + 加入变更 + 触发集成
curl -s -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"title":"e2e批次"}' http://paas.k8s.dd/api/applications/paas-shop/batches
curl -s -X POST ... /batches/{bid}/changes -d '{"changeId":"<cid>"}'
curl -s -X POST ... /batches/{bid}/integrate   # 期望 200，data.status=testing
# 3. 轮询批次详情（惰性推进到 tested/failed）
curl -s ... /batches/{bid}    # run 终态后 status 推进
# 4. 前端目检：应用详情「变更」tab + 批次抽屉 + 运行跳转
```

Expected: 全链路 200 + 状态按状态机推进 + 前端 tab 可操作（Playwright 目检批次抽屉渲染）

- [ ] **Step 4: CLAUDE.md 文档 + Commit**

CLAUDE.md「垂直切片」追加「变更管理（Change/IntegrationBatch）」章节：三实体模型 / 状态机 / REST 端点 / 编排（integrate/approve/release）/ 惰性推进 / YAGNI 边界（跨应用批次等 6 项）。

```bash
git add CLAUDE.md
git commit -m "docs(claude): 变更管理切片文档"
```
