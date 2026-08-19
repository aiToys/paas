# 服务模型 Phase 1 实施计划：Service 实体 + Workload.ServiceID + 存量回填 + 服务 tab

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 Service 一等实体（声明式服务定义），Workload 挂 ServiceID，存量数据幂等回填，应用详情新增「服务」tab 作为新主线。

**Architecture:** 新模块 `internal/service/`（领域 + Repository + handler，模式克隆 `internal/environment/`）。Workload 加 `ServiceID`（nullable），存量回填直接消费 Workload 已有的 `Service` 字段（多服务模型 2026-08-12 已落地），命名反推仅作兜底。前端纯聚合（Phase 1 不改流水线）。

**Tech Stack:** Go + pgx（memory/pg 双实现）、Vue 3 + Element Plus。

**Spec:** `docs/superpowers/specs/2026-08-19-service-model-design.md`（Phase 1 = spec 第 4 节）

## Global Constraints

- 注释语言与代码库一致（中文）。
- Repository 全方法从 ctx 取租户强制过滤；`Create` 以 ctx 为准忽略请求体 TenantID；跨租户 not found 不泄漏。
- 响应契约：成功 `{data:T}`（`httputil.WriteData/WriteDataCreated`），删除 ack 统一 `WriteData`；错误 `WriteServiceError`。
- 写操作登记 OpenAPI Operation（`reg.Operation`，composite 路径 spec-only）。
- 权限 `service:read/write` 并入 `BuiltinRoles()`（admin/dev 读写，viewer 只读）；Service 声明不接 prod:write（门禁留部署环节）。
- PG 全参数化；migration 幂等（`ADD COLUMN IF NOT EXISTS`）；并行测试 `-p 1`（integration 门控 `//go:build integration`）。
- 写操作记审计（`AuditRecorder` 接口依赖倒置，参照 identity 模式，action 前缀 `service_`）。
- 不执行 git 分支操作；每任务一 commit。

---

### Task 1: Service 领域模型 + memory Repository

**Files:**
- Create: `internal/service/model.go`
- Create: `internal/service/repository.go`
- Create: `internal/service/memory/store.go`
- Test: `internal/service/memory/store_test.go`

**Interfaces:**
- Produces: `service.Service`（见下）、`service.Repository` 接口（`List/Get/Create/Update/Delete/Count`，ctx 首参）、`memory.NewStore()`、sentinel `ErrNotFound/ErrExists/ErrInvalid`、`Validate()`、常量 `TypeWeb/TypeBackend/TypeAgent/TypeStatic/TypeCron`。

- [ ] **Step 1: 写失败测试**

```go
package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/service"
	"github.com/aitoys/paas/pkg/tenant"
)

func ctxOf(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

func TestCreateGetRoundTrip(t *testing.T) {
	s := NewStore()
	in := service.Service{ID: "svc-1", AppID: "app-1", Name: "bff", Type: service.TypeBackend, Port: 8080, RepoPath: "services/bff"}
	if err := s.Create(ctxOf("t-acme"), in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctxOf("t-acme"), "app-1", "svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "bff" || got.Type != service.TypeBackend {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	_ = s.Create(ctxOf("t-acme"), service.Service{ID: "svc-1", AppID: "app-1", Name: "bff", Type: service.TypeBackend})
	if _, err := s.Get(ctxOf("t-globex"), "app-1", "svc-1"); err != service.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestNameUniquePerApp(t *testing.T) {
	s := NewStore()
	_ = s.Create(ctxOf("t-acme"), service.Service{ID: "svc-1", AppID: "app-1", Name: "bff", Type: service.TypeBackend})
	err := s.Create(ctxOf("t-acme"), service.Service{ID: "svc-2", AppID: "app-1", Name: "bff", Type: service.TypeWeb})
	if err != service.ErrExists {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestValidateRejectsBadType(t *testing.T) {
	err := service.Service{ID: "s", AppID: "a", Name: "x", Type: "nope"}.Validate()
	if err == nil {
		t.Fatal("want error for invalid type")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/service/memory/ -v`
Expected: FAIL（包不存在/类型未定义）

- [ ] **Step 3: 实现模型与 store**

`internal/service/model.go`：

```go
// Package service 是服务实体（用户声明的服务定义）。
// 应用 → 服务 → 环境：服务是用户心智的一等实体，部署（Workload）是服务 × 环境 × 泳道的实例化。
package service

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// 服务类型。web=有对外域名的前端入口服务；backend=后端 API；
// agent=AI Agent（注入模型/工具配置）；static=静态站点（StaticSite，不产 Workload）；cron=定时任务。
const (
	TypeWeb     = "web"
	TypeBackend = "backend"
	TypeAgent   = "agent"
	TypeStatic  = "static"
	TypeCron    = "cron"
)

var validTypes = map[string]bool{TypeWeb: true, TypeBackend: true, TypeAgent: true, TypeStatic: true, TypeCron: true}

// Sentinel 错误。
var (
	ErrNotFound = errors.New("service not found")
	ErrExists   = errors.New("service already exists")
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`) // DNS-1035（作 K8s 资源名前缀）

// Service 是应用内一个服务的声明式定义（不含运行态）。
type Service struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID    string `json:"appId"`
	Name     string `json:"name"`              // 应用内唯一，DNS-1035
	Type     string `json:"type"`              // web/backend/agent/static/cron
	RepoID   string `json:"repoId,omitempty"`  // 关联 CodeRepo（static 可空）
	RepoPath string `json:"repoPath,omitempty"` // 仓库内路径（monorepo 多服务）
	Port     int    `json:"port,omitempty"`     // web/backend/agent 对外端口（0=不建 Service）
	Replicas int    `json:"replicas,omitempty"` // 期望副本（部署默认值）
	// BuildArgs 是多服务构建参数（如 SERVICE=bff），部署/构建时注入流水线。
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	// Env 是服务级环境变量（部署时与 appconfig 合并注入）。
	Env map[string]string `json:"env,omitempty"`
	// ModelRef 是 agent 类型绑定的模型 ID。
	ModelRef string `json:"modelRef,omitempty"`
	// Tools 是 agent 类型的 MCP 工具名列表。
	Tools    []string  `json:"tools,omitempty"`
	// Schedule 是 cron 类型的 cron 表达式。
	Schedule  string    `json:"schedule,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Validate 校验服务字段：type/name/appId 必填且合法；cron 须有 schedule；static 不需要 Port。
func (s Service) Validate() error {
	if !validTypes[s.Type] {
		return fmt.Errorf("%w: type", ErrInvalid)
	}
	if s.AppID == "" {
		return fmt.Errorf("%w: appId", ErrInvalid)
	}
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("%w: name 须为小写字母数字连字符（DNS-1035）", ErrInvalid)
	}
	if s.Type == TypeCron && s.Schedule == "" {
		return fmt.Errorf("%w: cron 类型须填 schedule", ErrInvalid)
	}
	return nil
}
```

补 `ErrInvalid = errors.New("invalid service")`。`repository.go` 定义接口：

```go
// Repository 是服务实体的持久化接口（租户隔离，跨租户 not found）。
type Repository interface {
	List(ctx context.Context, appID string) ([]Service, error)
	Get(ctx context.Context, appID, id string) (Service, error)
	Create(ctx context.Context, s Service) error
	Update(ctx context.Context, s Service) error
	Delete(ctx context.Context, appID, id string) error
	// GetOrCreateByName 供存量回填：按 (app, name) 取，无则建（幂等）。
	GetOrCreateByName(ctx context.Context, appID, name, typ string, fill func(*Service)) (Service, error)
}
```

`memory/store.go`：`sync.Mutex` + `map[string]Service`（key=tenant|app|id）+ `map[string]string`（key=tenant|app|name → id 唯一索引）；List/Get/Create/Update/Delete 全方法 `TenantOrErr` 风格从 ctx 取租户；写前 `Validate()`；`GetOrCreateByName` 加锁内查名索引，miss 时生成 `svc-<nanoid 风格 id>`（用现有 ID 生成模式，查 `internal/environment/memory` 同款）+ `fill` 回调填充 Port 等字段 + Create。返回深拷贝（map/slice 复制）。

- [ ] **Step 4: 跑测试通过**

Run: `go test ./internal/service/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/service/
git commit -m "feat(service): Service 一等实体——领域模型 + memory Repository（租户隔离/应用内名唯一/GetOrCreateByName 回填口）"
```

---

### Task 2: Workload 加 ServiceID + PG migration + pg store

**Files:**
- Modify: `internal/workload/model.go`（Workload 结构体加 ServiceID 字段）
- Modify: `internal/workload/pg/store.go`（列读写加 service_id）
- Modify: `internal/workload/memory/store.go`（如存整结构体则自动继承，确认即可）
- Create: `internal/storage/pg/migrations/0029_service_entity.up.sql` / `.down.sql`
- Create: `internal/service/pg/store.go`
- Test: `internal/service/pg/store_test.go`（integration 门控）

**Interfaces:**
- Consumes: Task 1 的 `service.Repository`/`Service`。
- Produces: `Workload.ServiceID string json:"serviceId,omitempty"`；`service/pg.NewStore(pool)`；`SeedServices` 无需（回填在 cmd/core 做）。

- [ ] **Step 1: Workload 模型加字段**

`internal/workload/model.go` Workload 结构体 `Service` 字段后加：

```go
	// ServiceID 关联 service 实体（Phase 1 回填/新部署写入；空=未关联，向后兼容）。
	ServiceID string `json:"serviceId,omitempty"`
```

`internal/workload/pg/store.go`：INSERT 列、SELECT 列（`chCols`/`wlCols` 同款常量）、Scan 目标全加 `service_id`——**列错位 panic 是最易踩坑，逐处核对**。memory store 存整结构体自动继承，跑既有测试确认。

- [ ] **Step 2: migration**

`0029_service_entity.up.sql`：

```sql
-- 服务实体（应用→服务→环境 三层心智，spec 2026-08-19 Phase 1）
CREATE TABLE IF NOT EXISTS services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    repo_id    TEXT NOT NULL DEFAULT '',
    repo_path  TEXT NOT NULL DEFAULT '',
    port       INTEGER NOT NULL DEFAULT 0,
    replicas   INTEGER NOT NULL DEFAULT 0,
    build_args JSONB,
    env        JSONB,
    model_ref  TEXT NOT NULL DEFAULT '',
    tools      JSONB,
    schedule   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_services_name ON services(tenant_id, app_id, name);
ALTER TABLE workloads ADD COLUMN IF NOT EXISTS service_id TEXT NOT NULL DEFAULT '';
```

`.down.sql`：`DROP TABLE IF EXISTS services; ALTER TABLE workloads DROP COLUMN IF EXISTS service_id;`

- [ ] **Step 3: 写 pg store 测试（integration）**

```go
//go:build integration

package pg

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/service"
	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestServiceRoundTrip(t *testing.T) {
	pool := pg.TestPool(t) // 复用既有测试基建（看 identity/pg/store_test.go 同款 helper 名）
	s := NewStore(pool)
	ctx := tenant.WithTenant(context.Background(), "t-svc")
	in := service.Service{ID: "svc-pg-1", AppID: "app-1", Name: "chatbot", Type: service.TypeAgent,
		ModelRef: "glm-5.2", Tools: []string{"product"}, BuildArgs: map[string]string{"SERVICE": "chatbot"}, Port: 8080}
	if err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "app-1", "svc-pg-1")
	if err != nil || got.ModelRef != "glm-5.2" || got.Tools[0] != "product" || got.BuildArgs["SERVICE"] != "chatbot" {
		t.Fatalf("round trip: %+v err=%v", got, err)
	}
	// GetOrCreateByName 幂等
	a, _ := s.GetOrCreateByName(ctx, "app-1", "chatbot", service.TypeAgent, nil)
	b, _ := s.GetOrCreateByName(ctx, "app-1", "chatbot", service.TypeAgent, nil)
	if a.ID != b.ID {
		t.Fatalf("GetOrCreateByName not idempotent: %s vs %s", a.ID, b.ID)
	}
}
```

（若 `pg.TestPool` 不存在，查 `internal/identity/pg/store_test.go` 的 resetSchema/pool 构造模式并复用同款。）

- [ ] **Step 4: 实现 pg store**

克隆 `internal/environment/pg/store.go` 模式：全参数化 SQL、`WHERE tenant_id=$1 AND app_id=$2`、JSONB 用 `marshalStrMap`/`json.Marshal` 同款 helper、Tools 用 JSONB 数组。

- [ ] **Step 5: 跑测试**

Run: `PAAS_TEST_PG_URL=... go test -tags integration ./internal/service/pg/ ./internal/workload/pg/ -v -p 1`
Expected: PASS（含 workload 既有测试不回归——service_id 列加入后）

- [ ] **Step 6: Commit**

```bash
git add internal/workload/ internal/service/pg/ internal/storage/pg/migrations/0029*
git commit -m "feat(service): PG 持久化——services 表 + workloads.service_id 列（migration 0029）+ pg store"
```

---

### Task 3: REST handler + 权限 + 审计 + composite 装配

**Files:**
- Create: `internal/service/handler.go`
- Modify: `cmd/core/main.go`（装配 svcStore/svcHandler + composite case + BuiltinRoles + OpenAPI Operation）
- Modify: `internal/core/identity/model.go`（BuiltinRoles 加 service:read/write）
- Test: `internal/service/handler_test.go`

**Interfaces:**
- Consumes: Task 1/2 的 Repository；`httputil.WriteData/WriteDataCreated/WriteServiceError`；`gateway.Require`。
- Produces: `service.NewHandler(repo, opts...)`；路由 `/api/applications/{id}/services`（GET/POST）+ `/{sid}`（GET/PUT/DELETE）。

- [ ] **Step 1: 写失败测试**（克隆 `internal/environment/handler_test.go` 模式：httptest + memory store + 断言 `{data:T}` 解包、404 跨租户、400 校验、201 创建）

覆盖：列表/创建（201+data）/详情/更新/删除（ack data）/无效 type 400 / 跨租户 404。

- [ ] **Step 2: 实现 handler**

路径解析用 `lastID(r)` 同款（取路径末段）。写操作成功后记审计（`AuditRecorder` 接口：`Record(ctx, tenantID, actor, action, resourceType, resourceID, detail)`，action：`service_create/service_update/service_delete`，resourceType `service`）。`WithAudit`/`WithActor` 选项，模式照抄 `internal/core/identity` 的 handler。

- [ ] **Step 3: 装配**

`cmd/core/main.go`：
- `persistence.go` `buildAllStores` 加 service store（memory + pg 两路径，聚合进 `Stores`）。
- composite 加 `case "services": svcHandler.ServeHTTP(w, r); return`。
- OpenAPI：`reg.Operation("GET", "/api/applications/{id}/services", apiroute.Info{Perm: "service:read", ...})` 等 5 操作（composite spec-only，照 pipelines 装配处同款）。
- `identity.BuiltinRoles()`：admin/dev 加 `service:read/write`，viewer 加 `service:read`。
- 审计桥接复用既有 `identityAuditAdapter`。

- [ ] **Step 4: 全量测试**

Run: `make test && PAAS_TEST_PG_URL=... make test-pg`
Expected: 全绿

- [ ] **Step 5: Commit**

```bash
git add internal/service/ cmd/core/ internal/core/identity/
git commit -m "feat(service): REST /api/applications/{id}/services CRUD + service:read/write 权限 + 审计 + composite 装配"
```

---

### Task 4: 存量回填（幂等 seed）

**Files:**
- Create: `cmd/core/service_backfill.go`
- Test: `cmd/core/service_backfill_test.go`

**Interfaces:**
- Consumes: `service.Repository.GetOrCreateByName`、`workload.Repository`（需加 `ListAll` 风格遍历？——**不加**：回填按租户走 `List(ctx, "", appID, "", "")`（appID 空返全部，查 workload.Repository.List 是否支持 appID 过滤空环境；若现有签名不支持跨 app，则用既有 admin ListAll 或按 app 维度循环，选侵入最小的）。
- Produces: `backfillServices(ctx, svcRepo service.Repository, wlRepo workload.Repository, tenants []string) error`。

- [ ] **Step 1: 写失败测试**（memory store）

```go
// 场景：两租户各一 app，workload 带 Service="bff"（多服务）与 Service=""（单服务老数据）。
// 期望：bff → GetOrCreate 出 Service{name:bff,type:backend}；空 Service → {name:"main",type:backend}；
// 二次调用幂等（不重复建）；workload.ServiceID 被回填（Update）。
func TestBackfillIdempotent(t *testing.T) { ... }
```

（断言：跑两遍后 services 数量不变；wl.ServiceID 非空且指向正确服务。）

- [ ] **Step 2: 实现回填**

```go
// backfillServices 存量回填：遍历各租户 workloads，为无 ServiceID 的负载
// 按 Workload.Service（多服务模型既有字段）GetOrCreate Service 实体并回填 ServiceID。
// 幂等：ServiceID 非空跳过；GetOrCreateByName 按名去重。启动时调用（内存/PG 两路径同源）。
func backfillServices(ctx context.Context, svcRepo service.Repository, wlRepo workload.Repository, tenantIDs []string) error {
	for _, tid := range tenantIDs {
		tctx := tenant.WithTenant(ctx, tid)
		wls, err := wlRepo.List(tctx, "", "", "", "") // envID/appID/type 空串不过滤
		if err != nil {
			continue // 租户无数据/查询失败不阻断启动（best-effort，log）
		}
		for _, wl := range wls {
			if wl.ServiceID != "" {
				continue
			}
			name := wl.Service
			if name == "" {
				name = "main" // 单服务老数据统一归 "main"
			}
			typ := service.TypeBackend
			if wl.Type == workload.TypeCronJob {
				typ = service.TypeCron
			} else if wl.Type == workload.TypeJob {
				typ = service.TypeCron // job 归 cron 类（无更好映射，UI 可改）
			}
			filled := false
			svc, err := svcRepo.GetOrCreateByName(tctx, wl.AppID, name, typ, func(s *service.Service) {
				s.Port, s.Replicas, filled = wl.Port, wl.Replicas, true
			})
			if err != nil {
				continue
			}
			wl.ServiceID = svc.ID
			_ = wlRepo.Update(tctx, wl) // best-effort
		}
	}
	return nil
}
```

注意：`List` 签名以 `internal/workload/repository.go` 实际为准（参数顺序 envID/appID/laneID/wtype），空串=不过滤；若 laneID 是必过滤维度则传 `workload.LaneDefault`。job→cron 的映射在注释里说明取舍。`filled` 变量删掉（不需要）。

- [ ] **Step 3: main.go 启动挂载**

内存路径与 PG 路径（`persistence.go` seed 后）都调 `backfillServices`，log 失败不阻断启动。租户列表：identity Repository `ListTenants`。

- [ ] **Step 4: 测试 + Commit**

Run: `go test ./cmd/core/ -run TestBackfill -v && make test`
Expected: PASS

```bash
git add cmd/core/service_backfill.go cmd/core/service_backfill_test.go cmd/core/main.go cmd/core/persistence.go
git commit -m "feat(service): 存量回填——按 Workload.Service 幂等 GetOrCreate Service 并回填 ServiceID"
```

---

### Task 5: 部署关联（Releaser 按 ServiceID 找/建 Workload）

**Files:**
- Modify: `internal/devops/release.go`（或 CreateRelease 所在文件）的「找/建基线 Workload」逻辑
- Modify: `cmd/core/pipeline_adapters.go`（Deploy 桥接处传 ServiceID）
- Test: 既有 devops 测试扩一例

**Interfaces:**
- Consumes: `workload.Repository`（List 加 laneID 维度已是现状）。
- Produces: 找/建逻辑优先 `(app, env, lane, serviceID)` 匹配；新建时从 Service 带出 Port/Replicas。

- [ ] **Step 1: 写失败测试**：创建带 ServiceID 的部署后，同 `(app,env,lane,serviceID)` 二次 deploy 复用同一 Workload（不新建）；Workload 的 Port 来自 Service 定义（注入 `ServiceLookup` 依赖倒置接口：`GetService(ctx, appID, serviceID) (service.Service, error)`，cmd/core 桥接，避免 devops→service import）。

- [ ] **Step 2: 实现**：`BaselineWorkloadName` 不变（命名链保持）；匹配条件加 ServiceID 优先、退化按现有 Service 名匹配（存量兼容）。新建 Workload 时 `ServiceID`/`Port`/`Replicas` 从 ServiceLookup 取（查不到用传入值，行为不变）。

- [ ] **Step 3: 测试 + Commit**

Run: `go test ./internal/devops/... -v`
Expected: PASS（含既有 TestReleasePreviousAndRollback 不回归）

```bash
git add internal/devops/ cmd/core/pipeline_adapters.go
git commit -m "feat(service): 部署按 ServiceID 找/建基线 Workload + 新建时从 Service 带出端口/副本"
```

---

### Task 6: 应用详情「服务」tab（前端聚合视图）

**Files:**
- Create: `frontend/console-user/src/api/service.ts`
- Create: `frontend/console-user/src/views/app-tabs/AppServices.vue`
- Modify: `frontend/console-user/src/views/ApplicationDetail.vue`（tab 注册，置顶默认）
- Modify: `frontend/console-user/src/composables/useStatus.ts`（如需 service 类型 tag 色）

**Interfaces:**
- Consumes: `/api/applications/{id}/services`（Task 3）、既有 `/api/applications/{id}/workloads`。
- Produces: 服务卡片列表 + 新建服务弹窗；tab 名「服务」。

- [ ] **Step 1: api 层**

```ts
// api/service.ts
export interface ServiceEntity {
  id: string; appId: string; name: string
  type: 'web' | 'backend' | 'agent' | 'static' | 'cron'
  repoId?: string; repoPath?: string; port?: number; replicas?: number
  buildArgs?: Record<string, string>; env?: Record<string, string>
  modelRef?: string; tools?: string[]; schedule?: string
}
export const listServices = (appId: string) => fetchJSON<ServiceEntity[]>(`/api/applications/${appId}/services`)
export const createService = (appId: string, body: Partial<ServiceEntity>) =>
  fetchJSON<ServiceEntity>(`/api/applications/${appId}/services`, { method: 'POST', body: JSON.stringify(body) })
export const deleteService = (appId: string, id: string) =>
  fetchJSON<void>(`/api/applications/${appId}/services/${id}`, { method: 'DELETE' })
```

- [ ] **Step 2: AppServices.vue**：服务卡片 grid（名称/类型 tag[web 蓝|backend 绿|agent 紫|static 橙|cron 灰]/端口/副本/模型 tag）；每卡「查看实例」抽屉 = 该 ServiceID 的 Workload 列表（复用 Workloads 详情抽屉模式：副本/实例/日志按钮）；「新建服务」el-dialog（name/type select/repoId select（复用 devops api）/repoPath/port/replicas；agent 显 modelRef+tools 逗号分隔；cron 显 schedule；static 隐藏 port）；删除走 `useDangerConfirm`。空态：引导文案「应用由一个或多个服务组成，创建你的第一个服务」。

- [ ] **Step 3: ApplicationDetail.vue**：tabs 数组头部插入 `{ name: 'services', label: '服务' }` 并设为默认 active；原「部署」tab 保留（Phase 1 不删，UI 降级归 Phase 2/4 清理）。

- [ ] **Step 4: 构建验证**

Run: `cd frontend && pnpm build`
Expected: vue-tsc + vite 三套全过

- [ ] **Step 5: Commit**

```bash
git add frontend/console-user/src/
git commit -m "feat(fe): 应用详情「服务」tab 置顶——服务卡片 + 新建服务向导 + 实例抽屉聚合"
```

---

### Task 7: 端到端验证 + 部署 dev 集群

**Files:** 无新文件（验证任务）。

- [ ] **Step 1: 全量回归**

Run: `make test && make lint && cd frontend && pnpm build`
Expected: 全绿

- [ ] **Step 2: 本地起 core 冒烟**（`./bin/core`）：

```bash
# 创建服务 → 列表 → workloads 出现 serviceId（回填后）
curl -s -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"name":"bff","type":"backend","port":8080}' \
  http://localhost:8080/api/applications/app-cs/services
curl -s -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/applications/app-cs/services
curl -s -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/applications/app-cs/workloads | grep serviceId
```

- [ ] **Step 3: k8s 部署**（`./scripts/deploy-k8s.sh`，常驻授权）后 Playwright 验证：应用详情默认落在「服务」tab；paas-shop 应用回填出 bff/chatbot/product 等 4 服务卡片（Workload.Service 归位）；新建服务弹窗各类型表单显隐正确；跨租户 404。

- [ ] **Step 4: Commit（如有修补）+ 汇报**

```bash
git add -A && git commit -m "fix(service): Phase 1 e2e 修补"
```

---

## Self-Review 结论

- **Spec 覆盖**：spec 第 4 节全部要点有任务（4.1→Task 1/2/3、4.2→Task 5、4.3→Task 4、4.4→Task 6）；「OnServiceCreate 自动绑 CI 流水线」**移入 Phase 2**（流水线绑定是 Phase 2 主题，Phase 1 绑了无消费方，YAGNI）。
- **占位符**：无 TBD；Task 4 Step 2 代码中 `filled` 标注删除；List 签名标注「以实际为准」并给出查证位置。
- **类型一致**：`Service.Type` 常量、`ServiceID` 字段名、`GetOrCreateByName(appID, name, typ, fill)` 签名各任务一致。
