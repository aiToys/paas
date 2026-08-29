# 配置中心应用维度改造（App-Centric ConfigCenter）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 配置中心从「手工命名空间」改造为「应用维度动态配置」——开发者主路径零 namespace 心智，客户端按应用名发现，shared 保留为高级用法。

**Architecture:** `Namespace` 加 `Scope`（app/shared）+ `AppID` 双 scope 模型；应用详情经 `EnsureByApp` 懒建派生 ns 复用既有 item/publish 仓储；新增应用维度 REST（挂 application composite）+ 按应用名发现端点；前端应用详情「配置」tab 加动态配置子区，ConfigCenter 页双视图。

**Tech Stack:** Go + pgx + PostgreSQL migration（增量 `ADD COLUMN IF NOT EXISTS`）+ Vue 3 + Element Plus。

**Spec:** `docs/superpowers/specs/2026-08-29-configcenter-app-centric-design.md`

## Global Constraints

- 权限：应用维度端点用 `application:read/write`（非 governance），写操作过 AppGuard `write` 动作；shared 端点维持 `governance:read/write` 不变
- 多租户：全路径 ctx tenant 强制过滤，跨租户统一 not found 不泄漏存在性
- migration 幂等：`ADD COLUMN IF NOT EXISTS` + 存量回填 `UPDATE ... WHERE scope IS NULL OR scope=''` 置 `shared`；同时合并进 `0001_init.up.sql`（新部署路径）
- 命名：派生 ns name = `app-<appID>`（与 BaselineWorkloadName 派生风格一致）
- 响应契约：CRUD 成功 `{data:T}`（WriteData/WriteDataCreated）；按应用名发现端点保持 `{published,version,snapshot}` 裸 JSON（数据面契约，与既有 published 端点一致）
- OpenAPI：新端点全部 `reg.Operation` 登记
- 审计：应用维度 publish/rollback 记审计（action 前缀 `configcenter_`）
- 测试命令：`go test ./internal/configcenter/... ./internal/core/application/...`；构建验证 `make build`；前端 `pnpm --filter console-user build`

---

### Task 1: Namespace 双 scope 模型 + EnsureByApp（memory/pg）

**Files:**
- Modify: `internal/configcenter/model.go`
- Modify: `internal/configcenter/repository.go`
- Modify: `internal/configcenter/memory/store.go`
- Modify: `internal/configcenter/pg/store.go`
- Create: `internal/storage/pg/migrations/0036_configcenter_app_scope.up.sql`
- Create: `internal/storage/pg/migrations/0036_configcenter_app_scope.down.sql`
- Modify: `internal/storage/pg/migrations/0001_init.up.sql`（cc_namespaces 建表加 scope/app_id 列）
- Test: `internal/configcenter/memory/store_test.go`、`internal/configcenter/pg/store_test.go`

**Interfaces:**
- Produces:
  - `const ScopeApp = "app"`、`const ScopeShared = "shared"`（configcenter 包）
  - `Namespace.Scope string`、`Namespace.AppID string` 字段
  - Repository 新方法：`EnsureByApp(ctx context.Context, appID string) (Namespace, error)`——scope=app 的 ns 存在（name=`app-<appID>` 且租户内）即返回，不存在则创建（Scope=ScopeApp, AppID=appID, Name=`app-<appID>`）
  - Repository 新方法：`FindAppNamespace(ctx context.Context, appID string) (Namespace, bool, error)`——查 scope=app 且 app_id 匹配的 ns（不创建）
  - `CreateNamespace` 语义：请求体带 `AppID` 非空时强制 `Scope=ScopeApp` 且 Name 忽略请求值改用 `app-<appID>`（防伪造 shared 占名）；AppID 空时强制 `Scope=ScopeShared`
- Consumes: 无（首个任务）

- [ ] **Step 1: 写失败测试（memory）**

在 `internal/configcenter/memory/store_test.go` 追加：

```go
func TestEnsureByAppIdempotent(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	n1, err := s.EnsureByApp(ctx, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if n1.Scope != configcenter.ScopeApp || n1.AppID != "app-1" || n1.Name != "app-app-1" {
		t.Fatalf("scope/appID/name 错误: %+v", n1)
	}
	n2, err := s.EnsureByApp(ctx, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if n2.ID != n1.ID {
		t.Fatalf("幂等失败: %s vs %s", n1.ID, n2.ID)
	}
	// 跨租户隔离：t-globex 看不到 t-acme 的 ns，各自独立
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	n3, err := s.EnsureByApp(ctxB, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if n3.ID == n1.ID {
		t.Fatal("跨租户泄漏：两租户拿到同一 ns")
	}
}

func TestFindAppNamespace(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if _, ok, _ := s.FindAppNamespace(ctx, "app-1"); ok {
		t.Fatal("未创建时不应找到")
	}
	s.EnsureByApp(ctx, "app-1")
	ns, ok, err := s.FindAppNamespace(ctx, "app-1")
	if err != nil || !ok || ns.AppID != "app-1" {
		t.Fatalf("创建后应找到: ok=%v err=%v", ok, err)
	}
	// 手工 shared ns 不被 FindAppNamespace 命中
	s.CreateNamespace(ctx, configcenter.Namespace{Name: "manual-ns"})
	if _, ok, _ := s.FindAppNamespace(ctx, "app-1"); !ok {
		t.Fatal("app ns 仍应找到（shared 不干扰）")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/configcenter/memory/ -run TestEnsureByApp -v`
Expected: FAIL（`s.EnsureByApp undefined`）

- [ ] **Step 3: 实现模型 + memory**

`model.go` Namespace 加字段与常量：

```go
// Namespace scope：app（应用派生，EnsureByApp 懒建）| shared（跨应用共享，治理方手工建）。
const (
	ScopeApp    = "app"
	ScopeShared = "shared"
)

type Namespace struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"`
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`              // app | shared（存量迁移为 shared）
	AppID     string    `json:"appId,omitempty"`    // scope=app 时归属应用
	ServiceID string    `json:"serviceId,omitempty"`
	Desc      string    `json:"desc,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}
```

`repository.go` NamespaceStore 接口追加两方法：

```go
	// EnsureByApp 懒建（或返回既有的）应用派生命名空间（scope=app，name=app-<appID>）。幂等。
	EnsureByApp(ctx context.Context, appID string) (Namespace, error)
	// FindAppNamespace 查应用派生命名空间（不创建）。无返回 false。
	FindAppNamespace(ctx context.Context, appID string) (Namespace, bool, error)
```

memory/store.go 实现（持写锁）：

```go
func (s *Store) EnsureByApp(ctx context.Context, appID string) (configcenter.Namespace, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.namespaces {
		if n.TenantID == tid && n.Scope == configcenter.ScopeApp && n.AppID == appID {
			return n, nil
		}
	}
	s.nsSeq++
	n := configcenter.Namespace{
		ID: fmt.Sprintf("ns-%d", s.nsSeq), TenantID: tid,
		Name: "app-" + appID, Scope: configcenter.ScopeApp, AppID: appID,
		UpdatedAt: time.Now(),
	}
	s.namespaces[n.ID] = n
	return n, nil
}

func (s *Store) FindAppNamespace(ctx context.Context, appID string) (configcenter.Namespace, bool, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.namespaces {
		if n.TenantID == tid && n.Scope == configcenter.ScopeApp && n.AppID == appID {
			return n, true, nil
		}
	}
	return configcenter.Namespace{}, false, nil
}
```

`CreateNamespace` 语义修正（memory + pg 两处）：AppID 非空 → Scope=ScopeApp + Name=`app-<AppID>`；AppID 空 → Scope=ScopeShared。同步在 `Validate` 后追加：

```go
if n.AppID != "" {
	n.Scope = configcenter.ScopeApp
	n.Name = "app-" + n.AppID
} else {
	n.Scope = configcenter.ScopeShared
}
```

- [ ] **Step 4: 跑 memory 测试通过**

Run: `go test ./internal/configcenter/memory/ -v`
Expected: PASS（含既有测试——CreateNamespace 无 AppID 路径行为兼容）

- [ ] **Step 5: migration + pg 实现**

`0036_configcenter_app_scope.up.sql`：

```sql
ALTER TABLE cc_namespaces ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'shared';
ALTER TABLE cc_namespaces ADD COLUMN IF NOT EXISTS app_id TEXT NOT NULL DEFAULT '';
UPDATE cc_namespaces SET scope='shared' WHERE scope IS NULL OR scope='';
CREATE INDEX IF NOT EXISTS idx_cc_namespaces_tenant_app ON cc_namespaces(tenant_id, app_id) WHERE app_id != '';
```

`0036_configcenter_app_scope.down.sql`：

```sql
DROP INDEX IF EXISTS idx_cc_namespaces_tenant_app;
ALTER TABLE cc_namespaces DROP COLUMN IF EXISTS app_id;
ALTER TABLE cc_namespaces DROP COLUMN IF EXISTS scope;
```

`0001_init.up.sql` 的 `cc_namespaces` 建表语句同步加 `scope TEXT NOT NULL DEFAULT 'shared'` 与 `app_id TEXT NOT NULL DEFAULT ''` 两列（新部署路径含列，0036 的 IF NOT EXISTS 自动跳过）。

pg/store.go：`nsCols` 改 `id, tenant_id, name, scope, app_id, service_id, "desc", updated_at`；`scanNS`（或等价 scan 函数）与 CreateNamespace INSERT 同步加两列；实现两新方法：

```go
func (s *Store) EnsureByApp(ctx context.Context, appID string) (configcenter.Namespace, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	// 先查后插 + 唯一约束兜底（tenant_id+app_id partial index 之外，靠 name 租户内唯一约束）
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+nsCols+` FROM cc_namespaces WHERE tenant_id=$1 AND scope='app' AND app_id=$2`, tid, appID)
	var n configcenter.Namespace
	if err := scanNS(row, &n); err == nil {
		return n, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return configcenter.Namespace{}, err
	}
	n = configcenter.Namespace{Name: "app-" + appID, Scope: configcenter.ScopeApp, AppID: appID}
	return s.CreateNamespace(ctx, n) // CreateNamespace 内含 scope 强制逻辑
}

func (s *Store) FindAppNamespace(ctx context.Context, appID string) (configcenter.Namespace, bool, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, false, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+nsCols+` FROM cc_namespaces WHERE tenant_id=$1 AND scope='app' AND app_id=$2`, tid, appID)
	var n configcenter.Namespace
	if err := scanNS(row, &n); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.Namespace{}, false, nil
		}
		return configcenter.Namespace{}, false, err
	}
	return n, true, nil
}
```

注意：`cc_namespaces` 若无 `(tenant_id, name)` 唯一约束，EnsureByApp 并发竞态可能重复建——0036 up.sql 追加 `CREATE UNIQUE INDEX IF NOT EXISTS uq_cc_ns_tenant_name ON cc_namespaces(tenant_id, name);`，CreateNamespace 捕获 23505 时先查既有的 app ns 返回（幂等兜底）。**先 grep 确认 0001 里既有约束，已存在则不加。**

- [ ] **Step 6: pg 集成测试**

在 `internal/configcenter/pg/store_test.go` 追加（`//go:build integration` 已是包级约定）：

```go
func TestEnsureByAppRoundTrip(t *testing.T) {
	s := newTestStore(t) // 复用既有测试构造 helper（resetSchema + NewStore）
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	n1, err := s.EnsureByApp(ctx, "app-e2e")
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := s.EnsureByApp(ctx, "app-e2e")
	if n1.ID != n2.ID {
		t.Fatal("幂等失败")
	}
	got, ok, _ := s.FindAppNamespace(ctx, "app-e2e")
	if !ok || got.Scope != "app" || got.AppID != "app-e2e" {
		t.Fatalf("FindAppNamespace 错误: %+v ok=%v", got, ok)
	}
}
```

Run: `PAAS_TEST_PG_URL=postgres://paas:paas-dev@127.0.0.1:5432/paas go test -tags integration ./internal/configcenter/pg/ -run TestEnsureByApp -v`
Expected: PASS（无 PG 环境时跳过，memory 测试已覆盖逻辑）

- [ ] **Step 7: 全量回归 + 提交**

Run: `go test ./internal/configcenter/... && go build ./...`
Expected: PASS

```bash
git add internal/configcenter/ internal/storage/pg/migrations/
git commit -m "feat(configcenter): Namespace 双 scope 模型 + EnsureByApp 懒建应用派生命名空间"
```

---

### Task 2: 应用维度 REST + AppGuard + 级联删

**Files:**
- Create: `internal/configcenter/app_handler.go`
- Create: `internal/configcenter/app_handler_test.go`
- Modify: `cmd/core/main.go`（composite 加 `dynamic-configs` 分发 + 装配）
- Modify: `cmd/core/app_cascade_deleter.go`（级联删 app ns）
- Test: `internal/configcenter/app_handler_test.go`

**Interfaces:**
- Consumes: Task 1 的 `EnsureByApp(ctx, appID)`、`FindAppNamespace(ctx, appID)`、`ScopeApp`
- Consumes: 既有 `application.AppGuard.Allow(r *http.Request, appID, action string) bool`（action 用 `"write"`）、`gateway.RequestAllowed(r, perm)` 风格鉴权（参考 appconfig handler 的注入方式）
- Produces:
  - `type AppHandler struct` + `NewAppHandler(repo Repository) *AppHandler` + `WithAuthorize(fn)` + `WithGuard(g *application.AppGuard)` + `WithAudit(fn)` 选项
  - `AppHandler.ServeHTTP(w, r)`——处理 `/api/applications/{id}/dynamic-configs[...]`（路径前缀匹配后按剩余段分发）
  - 级联：`appCascadeDeleter` 加 `cc` 字段（`configcenter.Repository`），删应用时查 FindAppNamespace 存在则 DeleteNamespace

- [ ] **Step 1: 写失败测试**

`internal/configcenter/app_handler_test.go`（用 httptest，参考既有 `handler_test.go` 的 fake authorize 模式）：

```go
func TestAppDynamicConfigsCRUDAndPublish(t *testing.T) {
	repo := mem.NewStore()
	h := NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// POST upsert（自动 EnsureByApp）
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"greeting","value":"hello","type":"text"}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("upsert: %d %s", rec.Code, rec.Body.String())
	}
	// GET 列表
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "greeting") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	// publish
	req = httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs/publish", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	// published 当前生效
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs/published", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"published":true`) || !strings.Contains(rec.Body.String(), "greeting") {
		t.Fatalf("published: %s", rec.Body.String())
	}
}

func TestAppDynamicConfigsGuardDenied(t *testing.T) {
	repo := mem.NewStore()
	h := NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return perm == "application:read" } // 读放行写拒绝
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("写应 403: %d", rec.Code)
	}
}

func TestAppDynamicConfigsCrossTenantNotFound(t *testing.T) {
	repo := mem.NewStore()
	h := NewAppHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("POST", "/api/applications/app-1/dynamic-configs", strings.NewReader(`{"key":"k","value":"v"}`))
	req = req.WithContext(ctxA)
	h.ServeHTTP(httptest.NewRecorder(), req)
	// t-globex 访问同名 app-1：EnsureByApp 建的是自己租户的 ns，互不可见（不泄漏）
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	req = httptest.NewRequest("GET", "/api/applications/app-1/dynamic-configs", nil)
	req = req.WithContext(ctxB)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "k") {
		t.Fatal("跨租户泄漏")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/configcenter/ -run TestAppDynamicConfigs -v`
Expected: FAIL（`NewAppHandler undefined`）

- [ ] **Step 3: 实现 AppHandler**

`internal/configcenter/app_handler.go`：

```go
// Package configcenter 应用维度动态配置 handler（scope=app 主路径）。
//
// 路由（挂 application composite 的 dynamic-configs 分发）：
//   GET    /api/applications/{id}/dynamic-configs            列 draft 项（自动 EnsureByApp）
//   POST   /api/applications/{id}/dynamic-configs            upsert 项
//   DELETE /api/applications/{id}/dynamic-configs/{itemId}   删项
//   POST   /api/applications/{id}/dynamic-configs/publish    发布
//   GET    /api/applications/{id}/dynamic-configs/publishes  发布历史
//   GET    /api/applications/{id}/dynamic-configs/published  当前生效
//
// 权限 application:read/write（应用资产归应用权限域）；受限应用写需 AppGuard write 动作。
type AppHandler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
	Guard     GuardAdapter // 可空：受限应用 enforcement
	Audit     AuditFunc    // 可空：publish/rollback 审计
}

// GuardAdapter 应用级权限判定（依赖倒置，避免 configcenter→application import）。
type GuardAdapter interface {
	Allow(r *http.Request, appID, action string) bool
}

// AuditFunc 审计记录（依赖倒置）。参数：ctx, tenantID, action, resourceID, detail。
type AuditFunc func(ctx context.Context, tenantID, action, resourceID, detail string)
```

`ServeHTTP`：TrimPrefix `/api/applications/` 取 parts，`parts[0]`=appID、`parts[1]`=`dynamic-configs`；写方法（POST/DELETE）先 `Authorize(r, "application:write")` + `Guard != nil && !Guard.Allow(r, appID, "write")` → 403；读方法 `Authorize(r, "application:read")`。各子操作：

- 列表/upsert/delete：`repo.EnsureByApp(ctx, appID)` 拿 nsID，然后复用与既有 handler 相同的 ListItems/UpsertItem/DeleteItem 调用（item.NamespaceID=nsID；delete 前同样校验 item 归属该 ns）
- publish：EnsureByApp + CreatePublish + Audit(`configcenter_publish`)
- publishes/published：FindAppNamespace（不创建——只读路径懒建无意义）；无 ns 返回 `{"published":false}` / 空列表

- [ ] **Step 4: 跑测试通过**

Run: `go test ./internal/configcenter/ -v`
Expected: PASS

- [ ] **Step 5: 装配 + 级联删**

`cmd/core/main.go` composite switch 加：

```go
case "dynamic-configs":
	ccAppHandler.ServeHTTP(w, r)
	return
```

装配（ccHandler 构造附近）：`ccAppHandler := configcenter.NewAppHandler(ccStore)` + `.WithAuthorize(gateway.Require(...))` 复用 ccHandler 同款 authorize 注入方式 + `.WithGuard(appGuard)`（既有 application.AppGuard 实例已存在则直接传；它满足 GuardAdapter 接口）+ `.WithAudit(...)` 桥接 identityAuditAdapter。OpenAPI：6 个 Operation 登记（Perm application:read/write）。

`app_cascade_deleter.go`：加 `cc configcenter.Repository` 字段；CascadeDelete 追加：

```go
if c.cc != nil {
	if ns, ok, err := c.cc.FindAppNamespace(ctx, appID); err == nil && ok {
		if err := c.cc.DeleteNamespace(ctx, ns.ID); err != nil {
			log.Printf("级联删应用动态配置失败（best-effort）: app=%s: %v", appID, err) //nolint:gosec // G706 误报
		}
	}
}
```

main.go 两处 appCascadeDeleter 构造点（内存/PG）同步注入 ccStore。

- [ ] **Step 6: 全量回归 + 提交**

Run: `go build ./... && go test ./internal/configcenter/... ./cmd/core/... 2>&1 | tail -5`
Expected: PASS

```bash
git add internal/configcenter/ cmd/core/
git commit -m "feat(configcenter): 应用维度动态配置 REST + AppGuard + 级联删"
```

---

### Task 3: 按应用名发现端点

**Files:**
- Modify: `internal/configcenter/handler.go`
- Modify: `cmd/core/main.go`（装配 AppLookup + 路由）
- Test: `internal/configcenter/handler_test.go`

**Interfaces:**
- Consumes: Task 1 `FindAppNamespace`；application 仓储 `List(ctx) ([]Application, error)` + `Application.ID/Name`
- Produces:
  - `handler.go` 新增 `AppLookup` 接口 + `WithAppLookup` 选项 + `serveAppPublished` 分支：

```go
// AppLookup 按应用名查应用 ID（依赖倒置，避免 configcenter→application import）。
// 实现按 ctx tenant 过滤；跨租户/不存在返 ""（统一 not found 不泄漏）。
type AppLookup interface {
	AppIDByName(ctx context.Context, appName string) (string, error)
}
```

  - 路由：`GET /api/configcenter/apps/{appName}/published`（ServeHTTP 加 `strings.HasPrefix(path, "/api/configcenter/apps/")` 分支，仅 GET，其余 405）

- [ ] **Step 1: 写失败测试**

`handler_test.go` 追加（fake AppLookup 返回固定映射）：

```go
type fakeAppLookup struct{ m map[string]string }

func (f fakeAppLookup) AppIDByName(ctx context.Context, name string) (string, error) {
	return f.m[name], nil
}

func TestAppPublishedByName(t *testing.T) {
	repo := mem.NewStore()
	h := NewHandler(repo)
	h.Authorize = func(r *http.Request, perm string) bool { return true }
	h.WithAppLookup(fakeAppLookup{m: map[string]string{"shop": "app-1"}})
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	// 经应用维度先发布
	ns, _ := repo.EnsureByApp(ctx, "app-1")
	repo.UpsertItem(ctx, configcenter.ConfigItem{NamespaceID: ns.ID, Key: "topk", Value: "3", Type: "text"})
	repo.CreatePublish(ctx, ns.ID)
	// 按名发现
	req := httptest.NewRequest("GET", "/api/configcenter/apps/shop/published", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "topk") {
		t.Fatalf("按名发现失败: %d %s", rec.Code, rec.Body.String())
	}
	// 未知应用名：{"published":false}（不泄漏存在性）
	req = httptest.NewRequest("GET", "/api/configcenter/apps/nope/published", nil)
	req = req.WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"published":false`) {
		t.Fatalf("未知应用: %d %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/configcenter/ -run TestAppPublishedByName -v`
Expected: FAIL

- [ ] **Step 3: 实现**

handler.go：`Handler` 加 `appLookup AppLookup` 字段 + `WithAppLookup`。ServeHTTP switch 加分支（在 namespaces 前缀判断之前）：

```go
case strings.HasPrefix(path, "/api/configcenter/apps/"):
	h.serveAppPublished(w, r)
```

`serveAppPublished`：仅 GET（405）；`Authorize(r, PermConfigCenterRead)`；解析 appName（路径末段，URL 路径解码）；`appLookup.AppIDByName` 返空 → `{"published":false}`；否则 FindAppNamespace → 无 ns 或无 active → `{"published":false}`；有则 `{"published":true,"version":v,"snapshot":...}`（**不含 publishId**——与 spec 3.3 一致）。

cmd/core/main.go：定义桥接（application 仓储无按名查，用 List 遍历匹配，租户内）：

```go
type ccAppLookup struct{ apps application.Repository }

func (l ccAppLookup) AppIDByName(ctx context.Context, name string) (string, error) {
	list, err := l.apps.List(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range list {
		if a.Name == name {
			return a.ID, nil
		}
	}
	return "", nil
}
```

装配 `ccHandler.WithAppLookup(ccAppLookup{apps: appStore})`。OpenAPI 登记 1 Operation。

- [ ] **Step 4: 跑测试 + 提交**

Run: `go test ./internal/configcenter/... && go build ./...`
Expected: PASS

```bash
git add internal/configcenter/ cmd/core/
git commit -m "feat(configcenter): 按应用名发现端点 /api/configcenter/apps/{name}/published"
```

---

### Task 4: 前端（应用详情动态配置 + ConfigCenter 双视图）

**Files:**
- Create: `frontend/console-user/src/views/app-tabs/AppDynamicConfigs.vue`
- Modify: `frontend/console-user/src/views/ApplicationDetail.vue`（配置 tab 嵌入新组件）
- Modify: `frontend/console-user/src/views/ConfigCenter.vue`（双视图改造）
- Modify: `frontend/console-user/src/api/configcenter.ts`（或等价 api 文件——先 grep 确认既有文件名）

**Interfaces:**
- Consumes: Task 2/3 的 REST 端点；既有 `fetchAuth` helper；既有 `useDangerConfirm` composable
- Produces: 无后端依赖

- [ ] **Step 1: api 函数**

`api/configcenter.ts` 追加（与既有同文件风格）：

```ts
// 应用维度动态配置（scope=app，主路径）
export const fetchAppDynamicConfigs = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs`)
export const upsertAppDynamicConfig = (appId: string, body: { key: string; value: string; type?: string }) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs`, { method: 'POST', body: JSON.stringify(body) })
export const deleteAppDynamicConfig = (appId: string, itemId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/${itemId}`, { method: 'DELETE' })
export const publishAppDynamicConfigs = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/publish`, { method: 'POST' })
export const fetchAppPublishes = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/publishes`)
export const fetchAppPublished = (appId: string) =>
  fetchAuth(`/api/applications/${appId}/dynamic-configs/published`)
```

- [ ] **Step 2: AppDynamicConfigs.vue 组件**

结构（props: `appId: string`）：
- draft KV 表（key/value/type 列）+ 新增/编辑弹窗（key/value/type select text|json|yaml）+ 删除（`useDangerConfirm`）
- 「发布」按钮 → confirmDangerous（生产 scope 时输入应用名确认）→ publishAppDynamicConfigs → 刷新
- 版本历史折叠列表（version/时间/状态 tag）+ 每行「回滚」（rollback 走既有 `/api/configcenter/publishes/{pid}/rollback`，需要 nsID——从 `GET /api/applications/{id}/dynamic-configs` 不返回，改用组件内 `published` 响应无 pid；**回滚按钮经发布历史数据携带的 publishId 调既有端点**，该端点路径不含 nsID，直接可用）
- 「当前生效」卡片（version + snapshot KV 表）
- 空状态引导：「动态配置用于运行时热更新（无需重启）。添加第一项配置后发布生效。」

- [ ] **Step 3: ApplicationDetail.vue 配置 tab 嵌入**

配置 tab 在既有静态 env/secret 区域（AppConfigs 或内联）之后加分隔标题「动态配置（热更新）」+ `<AppDynamicConfigs :app-id="app.id" />`（lazy import 与其他 app-tabs 组件同款）。

- [ ] **Step 4: ConfigCenter.vue 双视图**

- 顶部 el-radio-group：「按应用」/「共享配置」
- 按应用视图：左侧应用列表（复用 `/api/applications`）+ 右侧嵌入 `<AppDynamicConfigs :app-id="selected" />`；无选中时空态
- 共享配置视图：既有 namespace CRUD 逻辑原样保留（默认折叠/次选）
- 既有 `?serviceId=` 路由参数兼容：进入时切共享视图并过滤

- [ ] **Step 5: 构建验证 + 提交**

Run: `pnpm --filter console-user build`
Expected: vue-tsc + vite build 通过

```bash
git add frontend/console-user/
git commit -m "feat(console-user): 应用详情动态配置 tab + ConfigCenter 双视图（按应用/共享）"
```

---

### Task 5: paas-shop 接入 + e2e 验证

**Files:**
- Modify: `examples/paas-shop/chatbot/`（配置消费客户端——examples 是独立 module `github.com/aitoys/paas-examples`，改动不进主仓构建）

**Interfaces:**
- Consumes: Task 3 的 `GET /api/configcenter/apps/{appName}/published`
- Produces: dogfooding 验收证据

- [ ] **Step 1: chatbot 动态配置消费**

chatbot 增加（与既有 paas-shop 结构一致的最小实现）：
- env `PAAS_CONFIG_APP`（默认 `chatbot`）+ `PAAS_API_KEY`（已有）+ `PAAS_CORE_URL`（已有则复用）
- 启动拉取 + 60s 定时轮询 `/api/configcenter/apps/<PAAS_CONFIG_APP>/published`：比对 version，变化则原子替换内存配置 map（`sync.RWMutex` 保护）
- 消费的 key：`welcome_message`（欢迎语）、`recommend_topk`（推荐数，默认 3）——注入到既有聊天回复逻辑
- 拉取失败（网络/未发布）静默降级用默认值 + 日志（不 panic——配置中心不可用不能拖死业务）

- [ ] **Step 2: 平台侧 e2e（k8s）**

1. `./scripts/deploy-k8s.sh` 部署后，console-user 应用详情 → paas-shop-chatbot（或任一应用）→ 配置 tab → 动态配置：加 `welcome_message=你好`，发布 → 「当前生效」显示 version=1
2. curl 验证：`curl -H "Authorization: Bearer <app-key>" http://paas.k8s.dd/api/configcenter/apps/<appName>/published` 返回 `{"published":true,"version":1,...}`
3. 修改 value → 再发布 → version=2；paas-shop chatbot 60s 内输出新欢迎语（不重启）
4. shared 回归：ConfigCenter 共享视图建手工 ns + 关联服务 → 既有链路（items/publish/published by nsID）全通
5. 级联回归：删除测试应用 → cc_namespaces 无残留 `app-<id>` 行
6. 跨租户：另一租户 API Key 访问同名 appName → `{"published":false}` 不泄漏

- [ ] **Step 3: 提交 + CLAUDE.md 同步**

examples 仓库独立提交（在 examples 目录内）：

```bash
cd examples && git add paas-shop/ && git commit -m "feat(shop): chatbot 接入平台配置中心（按应用名发现 + 60s 热更新）"
```

主仓 CLAUDE.md：配置中心章节追加应用维度改造说明（双 scope/新端点/paas-shop 接入）+ 留后续清单更新。

```bash
git add CLAUDE.md && git commit -m "docs: 配置中心应用维度改造章节"
```
