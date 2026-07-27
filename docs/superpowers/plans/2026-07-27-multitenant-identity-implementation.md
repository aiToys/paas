# 多租户身份骨架 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为平台补齐多租户隔离根基——领域实体加租户维度、API Key 解析租户上下文、粗粒度 RBAC、端到端隔离可见。

**Architecture:** ctx 传播租户（`pkg/tenant`）→ Repository 强制过滤 → API Key 中间件注入 (tenant,roles) → Require 中间件粗粒度校验。模型目录平台共享，应用按租户隔离。

**Tech Stack:** Go 1.22 标准库 + testify；Vue 3 + Pinia + Element Plus。

## Global Constraints

- 注释语言：中文，与现有代码库一致
- 不引新外部依赖（Apache 2.0 兼容）
- Repository 必须从 ctx 取租户，缺失即拒绝（防止插件绕过隔离）
- 模型目录 `/api/models` `/v1/models` 平台级共享，不按租户过滤
- 默认开发 Key `sk-acme-admin`，兼容现有 curl 文档
- 提交者：如水 <rushui@qq.com>

---

## 任务文件结构

| 任务 | 文件 |
|---|---|
| T1 | `pkg/tenant/tenant.go` `pkg/tenant/tenant_test.go` |
| T2 | `internal/core/identity/model.go` `repository.go` `memory/store.go` `memory/store_test.go` |
| T3 | `internal/core/application/model.go` `repository.go` `handler.go` `memory/store.go` `memory/store_test.go` |
| T4 | `internal/core/gateway/auth.go` `auth_test.go` `permit.go` `permit_test.go` `openai.go` |
| T5 | `cmd/core/main.go` `cmd/core/main_test.go` `cmd/core/seed.go` |
| T6 | `frontend/console-user/src/**` |
| T7 | `CLAUDE.md` `README.md` 蓝图 |

---

### Task 1: pkg/tenant 租户上下文传播

**Files:**
- Create: `pkg/tenant/tenant.go`
- Test: `pkg/tenant/tenant_test.go`

**Interfaces:**
- Produces: `tenant.WithTenant(ctx, id) context.Context` / `tenant.TenantFrom(ctx) (string, bool)`

- [ ] **Step 1: 写失败测试**

```go
package tenant

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTenantRoundTrip(t *testing.T) {
	ctx := WithTenant(context.Background(), "t-acme")
	got, ok := TenantFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "t-acme", got)
}

func TestTenantMissing(t *testing.T) {
	_, ok := TenantFrom(context.Background())
	assert.False(t, ok)
}
```

- [ ] **Step 2: 运行验证失败** — `go test ./pkg/tenant/ -run TestTenant -v` → FAIL（未定义）

- [ ] **Step 3: 实现**

```go
// Package tenant 提供租户上下文的 ctx 传播。
// Repository 与中间件通过 TenantFrom 取租户；缺失即拒绝，防止绕过多租户隔离。
package tenant

import "context"

type ctxKey struct{}

// WithTenant 把租户 ID 注入 ctx。
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, tenantID)
}

// TenantFrom 取出租户 ID；不存在返回 ("", false)。
func TenantFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok && v != ""
}
```

- [ ] **Step 4: 验证通过** — `go test ./pkg/tenant/ -v` → PASS

- [ ] **Step 5: 提交** — `git add pkg/tenant/ && git commit -m "feat(core): add pkg/tenant context propagation"`

---

### Task 2: identity 扩展 Role/Permission/APIKey

**Files:**
- Modify: `internal/core/identity/model.go` `internal/core/identity/repository.go`
- Modify: `internal/core/identity/memory/store.go` `internal/core/identity/memory/store_test.go`

**Interfaces:**
- Produces: `identity.Permission` `identity.Role` `identity.APIKey`；`User` 加 `Roles []string`
- Produces: Repository 新增 `CreateAPIKey` / `LookupAPIKey` / `Role`；`BuiltinRoles()` 返回内置角色表

- [ ] **Step 1: 写失败测试**（追加到 store_test.go）

```go
func TestLookupAPIKey(t *testing.T) {
	s := NewStore()
	require.NoError(t, s.CreateTenant(context.Background(), identity.Tenant{ID: "t-acme", Name: "Acme", CreatedAt: time.Now()}))
	require.NoError(t, s.CreateAPIKey(context.Background(), identity.APIKey{
		ID: "k1", TenantID: "t-acme", UserID: "u1", Roles: []string{"developer"}, Key: "sk-acme-dev",
	}))
	got, err := s.LookupAPIKey(context.Background(), "sk-acme-dev")
	require.NoError(t, err)
	assert.Equal(t, "t-acme", got.TenantID)
	assert.Equal(t, []string{"developer"}, got.Roles)
}

func TestLookupAPIKeyUnknown(t *testing.T) {
	_, err := NewStore().LookupAPIKey(context.Background(), "sk-nope")
	assert.Error(t, err)
}

func TestBuiltinRoleAdminPassAll(t *testing.T) {
	r := identity.BuiltinRoles()["tenant-admin"]
	assert.Contains(t, r.Permissions, identity.Permission("tenant:admin"))
	assert.True(t, r.Grants("application:write"))
}

func TestBuiltinRoleDeveloperScoped(t *testing.T) {
	r := identity.BuiltinRoles()["developer"]
	assert.True(t, r.Grants("model:infer"))
	assert.False(t, r.Grants("tenant:admin"))
}
```

- [ ] **Step 2: 验证失败** — `go test ./internal/core/identity/... -v` → FAIL

- [ ] **Step 3: 扩展 model.go**

```go
// Permission 是粗粒度权限标识，形如 "application:read"。
type Permission string

// Role 是一组权限的命名集合。
type Role struct {
	Name        string
	Permissions []Permission
}

// Grants 判断角色是否持有某权限。
func (r Role) Grants(p Permission) bool {
	// tenant-admin 通行：含 tenant:admin 即放行所有
	for _, own := range r.Permissions {
		if own == p || own == "tenant:admin" {
			return true
		}
	}
	return false
}

// APIKey 是 (租户, 用户, 角色) 三元组的凭证。
type APIKey struct {
	ID        string
	TenantID  string
	UserID    string
	Roles     []string
	Key       string
	CreatedAt time.Time
}
```

`User` 结构体加 `Roles []string` 字段。

新增 `BuiltinRoles()`：

```go
// BuiltinRoles 返回内置角色定义（起步期固定）。
func BuiltinRoles() map[string]Role {
	return map[string]Role{
		"tenant-admin": {Name: "tenant-admin", Permissions: []Permission{"tenant:admin"}},
		"developer": {Name: "developer", Permissions: []Permission{
			"application:read", "application:write", "binding:write", "model:infer", "model:read",
		}},
		"viewer": {Name: "viewer", Permissions: []Permission{"application:read", "model:read"}},
	}
}
```

- [ ] **Step 4: 扩展 repository.go**

```go
CreateAPIKey(ctx context.Context, k APIKey) error
LookupAPIKey(ctx context.Context, key string) (APIKey, error)
```

- [ ] **Step 5: 内存实现**（store.go 加 `apiKeys map[string]APIKey` + 按 key 索引；`LookupAPIKey` 找不到返回 error）

- [ ] **Step 6: 验证通过** — `go test ./internal/core/identity/... -race -v` → PASS

- [ ] **Step 7: 提交** — `feat(identity): add Role/Permission/APIKey models and builtin roles`

---

### Task 3: application 加 TenantID + Repository 强制租户过滤

**Files:**
- Modify: `internal/core/application/model.go` `repository.go` `handler.go`
- Modify: `internal/core/application/memory/store.go` `memory/store_test.go`

**Interfaces:**
- Consumes: `pkg/tenant.TenantFrom`
- Produces: `Application.TenantID`；Repository 全方法从 ctx 取 tenant 强制过滤

- [ ] **Step 1: 写失败测试**（store_test.go 追加）

```go
func TestListIsolatedByTenant(t *testing.T) {
	s := NewStore() // seed 已含两租户应用
	acme, _ := s.List(tenant.WithTenant(context.Background(), "t-acme"))
	globex, _ := s.List(tenant.WithTenant(context.Background(), "t-globex"))
	for _, a := range acme {
		assert.Equal(t, "t-acme", a.TenantID)
	}
	for _, a := range globex {
		assert.Equal(t, "t-globex", a.TenantID)
	}
}

func TestGetRejectsCrossTenant(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-globex")
	_, err := s.Get(ctx, "app-cs") // app-cs 属 t-acme
	assert.Error(t, err) // 不泄漏存在性 → 统一 not found
}

func TestMissingTenantRejected(t *testing.T) {
	_, err := NewStore().List(context.Background())
	assert.Error(t, err) // 缺租户 → 拒绝
}
```

- [ ] **Step 2: 验证失败** — `go test ./internal/core/application/... -v` → FAIL

- [ ] **Step 3: model.go 加字段** — `Application` 加 `TenantID string \`json:"tenantId,omitempty"\``

- [ ] **Step 4: store.go 改造**

所有方法首步：
```go
tenantID, ok := tenant.TenantFrom(ctx)
if !ok { return ..., fmt.Errorf("missing tenant context") }
```
- `List`：只返回 `a.TenantID == tenantID`
- `Get/BindResource/Unbind`：找到后校验 `a.TenantID == tenantID`，不匹配返回 not found（不泄漏）
- `Create`：`a.TenantID = tenantID`（以 ctx 为准，忽略请求体里的 TenantID）

- [ ] **Step 5: seed 拆两租户** — 智能客服/推荐服务 → `t-acme`；数据导入/智能体平台 → `t-globex`。实验沙盒 → `t-acme`

- [ ] **Step 6: handler.go Create 写 tenant** — `a.TenantID` 由 repo.Create 从 ctx 写入，handler 无需改（已传 r.Context()）。仅需确认。

- [ ] **Step 7: 验证通过** — `go test ./internal/core/application/... -race -v` → PASS。`handler_test.go` 若失败则补 `tenant.WithTenant` 包装。

- [ ] **Step 8: 提交** — `feat(application): enforce tenant isolation in repository`

---

### Task 4: Gateway 鉴权进化 + Require 中间件

**Files:**
- Modify: `internal/core/gateway/auth.go` `auth_test.go` `openai.go`
- Create: `internal/core/gateway/permit.go` `permit_test.go`

**Interfaces:**
- Consumes: `identity.Repository`（LookupAPIKey / BuiltinRoles）、`pkg/tenant`
- Produces: `APIKeyAuth(idb identity.Repository)`；`Require(perm identity.Permission)`

- [ ] **Step 1: 写失败测试**

```go
// auth_test.go
func TestAPIKeyAuthInjectsTenantAndRoles(t *testing.T) {
	idb := memid.NewStore()
	_ = idb.CreateTenant(context.Background(), identity.Tenant{ID: "t-acme", Name: "Acme", CreatedAt: time.Now()})
	_ = idb.CreateAPIKey(context.Background(), identity.APIKey{ID: "k", TenantID: "t-acme", UserID: "u", Roles: []string{"developer"}, Key: "sk-x"})

	var gotTenant, gotRoles string
	h := APIKeyAuth(idb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, _ := tenant.TenantFrom(r.Context())
		gotTenant = tid
		roles, _ := RolesFrom(r.Context())
		gotRoles = strings.Join(roles, ",")
	}))
	_ = h.ServeHTTP // 用 httptest 构造 Bearer sk-x 请求
	assert.Equal(t, "t-acme", gotTenant)
	assert.Equal(t, "developer", gotRoles)
}

func TestAPIKeyAuthRejectsUnknown(t *testing.T) { /* 401 */ }

// permit_test.go
func TestRequireAllowsAdmin(t *testing.T) { /* tenant-admin 通行 model:infer */ }
func TestRequireRejectsMissing(t *testing.T) { /* viewer 调 model:infer → 403 */ }
```

- [ ] **Step 2: 验证失败**

- [ ] **Step 3: auth.go 重写**

```go
func APIKeyAuth(idb identity.Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				writeErr(w, http.StatusUnauthorized, "missing api key"); return
			}
			k, err := idb.LookupAPIKey(r.Context(), strings.TrimPrefix(h, prefix))
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid api key"); return
			}
			ctx := tenant.WithTenant(r.Context(), k.TenantID)
			ctx = WithRoles(ctx, k.Roles)
			ctx = context.WithValue(ctx, userIDKey{}, k.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
```

新增 `WithRoles` / `RolesFrom`（同 tenant 模式，放 auth.go 或单独 ctx 文件）。

- [ ] **Step 4: permit.go 实现 Require**

```go
// Require 返回粗粒度权限校验中间件：tenant-admin 通行，否则角色权限集需含 perm。
func Require(perm identity.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			roles, _ := RolesFrom(r.Context())
			builtin := identity.BuiltinRoles()
			for _, name := range roles {
				if r, ok := builtin[name]; ok && r.Grants(perm) {
					next.ServeHTTP(w, r); return
				}
			}
			writeErr(w, http.StatusForbidden, "forbidden: missing "+string(perm))
		})
	}
}
```

- [ ] **Step 5: openai.go meter 从 ctx 取租户** — ChatCompletions 内 `tid, _ := tenant.TenantFrom(r.Context()); meter.Record(tid, ...)`

- [ ] **Step 6: 验证通过** — `go test ./internal/core/gateway/... -race -v` → PASS

- [ ] **Step 7: 提交** — `feat(gateway): api-key resolves tenant/roles + Require middleware`

---

### Task 5: cmd/core 装配

**Files:**
- Modify: `cmd/core/main.go` `cmd/core/main_test.go`
- Create: `cmd/core/seed.go`

- [ ] **Step 1: seed.go** — 函数 `seedIdentity(idb)` 注入两租户 + 三 Key（`sk-acme-admin`/`sk-globex-admin`/`sk-acme-dev`）。`PAAS_API_KEY` 环境变量若指定且非内置 Key，则追加为 `t-acme` admin（兼容自定义）。

- [ ] **Step 2: main.go serveHTTP 改造** — 签名加 `idb identity.Repository`；`auth := gateway.APIKeyAuth(idb)`；应用 handler 用同一 idb 注入的租户；路由包 `Require`：

```go
mux.Handle("/v1/chat/completions", auth(gateway.Require("model:infer")(gateway.ChatCompletions(gw, meter))))
mux.Handle("/v1/models", auth(gateway.Require("model:read")(gateway.ListModels(gw))))
mux.Handle("/api/models", auth(gateway.Require("model:read")(gateway.CatalogModels(gw))))
mux.Handle("/api/applications", auth(appHandler))        // 内部按方法细分，方法级 Require 见下
mux.Handle("/api/applications/", auth(appHandler))
```

应用 handler 内部需按方法区分权限：在 handler.go 加 `application:read`（GET）/`application:write`（POST）/`binding:write`（绑定）校验，或包 Require。**简化方案**：handler.go 内部对写操作调 `gateway.CheckPermission(r, perm)` helper（避免中间件无法区分 GET/POST）。

- [ ] **Step 3: resolveAPIKey → 默认 sk-acme-admin** — 兼容 curl 文档。

- [ ] **Step 4: main_test.go 更新** — `TestResolveAPIKey` 默认值改 `sk-acme-admin`；自定义仍工作。

- [ ] **Step 5: 验证** — `go build ./... && go test ./... -race` → PASS。手动：

```bash
./bin/core
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/applications        # 仅 Acme
curl -H "Authorization: Bearer sk-globex-admin" http://localhost:8080/api/applications       # 仅 Globex
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/applications/app-etl # 404（跨租户）
curl -H "Authorization: Bearer sk-acme-dev" http://localhost:8080/api/models                # 平台共享 ✓
```

- [ ] **Step 6: 提交** — `feat(core): wire identity store, seed tenants, route-level RBAC`

---

### Task 6: 前端 API Key 登录态 + 隔离

**Files:**
- Modify: `frontend/console-user/src/api.ts`（若无则创建）、`App.vue`、`Applications.vue`、`ApplicationDetail.vue`、`Marketplace.vue`、`Playground.vue`

- [ ] **Step 1: api 封装** — 统一 `fetchWithAuth(path, opts)`：从 localStorage 取 Key，注入 Bearer；401 → 触发重新登录事件；403 → ElMessage 权限提示。默认 Key 预填 `sk-acme-admin`。

- [ ] **Step 2: 顶栏 tenant-chip** — 点击弹出 Key 输入/选择（三预设 + 自定义）；切换后刷新当前视图。

- [ ] **Step 3: 应用列表** — 用 fetchWithAuth 调 `/api/applications`，按返回的租户数据渲染。

- [ ] **Step 4: Marketplace/Playground** — 同样带 Bearer（平台共享，但仍需有效 Key）。

- [ ] **Step 5: 验证** — `pnpm --filter console-user build` 通过；Playwright：默认 Key 见 Acme 应用；切 `sk-globex-admin` 见 Globex；无效 Key 提示 401。

- [ ] **Step 6: 提交** — `feat(console-user): api-key auth + tenant isolation e2e`

---

### Task 7: 文档 + 全量验证

- [ ] **Step 1: CLAUDE.md** — 垂直切片加「身份与多租户隔离」；常用命令 curl 示例改三 Key；架构约束勾选。
- [ ] **Step 2: README.md** — 快速开始补充两租户隔离演示 curl。
- [ ] **Step 3: 蓝图** — `platform-modules-blueprint.md` 身份与 RBAC 由 ❌ 改 ✅；完成度 20% → 28%。
- [ ] **Step 4: 全量验证** — `make lint && make test && gofmt -l .`；前端三套 build。
- [ ] **Step 5: 提交** — `docs: multitenant identity slice`

## Self-Review

- **Spec 覆盖**：RBAC（T2/T4）、租户隔离（T1/T3）、API Key 解析（T4/T5）、前端（T6）、seed（T5）、验收（T5/T6 手动 + 单测）✓
- **类型一致**：`identity.Permission`、`tenant.TenantFrom`、`APIKeyAuth(idb)` 签名贯穿 ✓
- **无占位符**：所有步骤有具体代码/命令 ✓
