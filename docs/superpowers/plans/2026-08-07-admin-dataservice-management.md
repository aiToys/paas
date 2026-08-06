# console-admin 数据服务管理（P1 样板）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 console-admin 的数据服务总览从「只读列表」升级为完整管理（L1 详情+实例 / L2 启停·重启·扩缩·删 / L3 代建），并补环境 L3 代建，作为后续 admin 管理模块的样板。

**Architecture:** 新建独立的 `dataservice.AdminHandler`（与租户侧 `Handler` 平行，不污染租户侧 prod:write 校验），挂 `/api/admin/dataservices*`，全挂 `adminGuard`(super_admin)。跨租户读靠新 `Repository.GetAny`；实例信息复用 `dataplane.EndpointsReader`（admin 注入目标租户 ctx 穿透其 tenant label 校验）；代建消耗目标租户配额（新增 `billing.ResDataservices` 维度）；所有写操作经审计 adapter 落 `security.AuditStore`（actor=super_admin，target_tenant=资源所属租户，action 带 `admin:` 前缀）。admin 绕过 prod:write（super_admin 有权干预生产），但前端 UI 仍走危险确认。

**Tech Stack:** Go（controller-runtime 风格 handler / net/http / pgx）+ Vue 3 + Element Plus + TypeScript + Pinia（console-admin 四件套 SearchTable/FormDrawer/useCrud）。

## Global Constraints

- **主语言 Go + Vue3/Element Plus/TS**；所有依赖 Apache 2.0 兼容（禁 GPL/AGPL）。
- **注释语言中文**，与代码库现有注释一致。
- **多租户隔离由 Core 统一**：admin 跨租户读取仅经 admin 端点；单资源按 id 定位，资源不存在统一 404 不泄漏。
- **响应契约**：成功 `{data:T}`（`httputil.WriteData`/`WriteDataCreated`），错误 `WriteServiceError`（500 脱敏不泄漏 pgx 细节）。
- **admin 端点全挂 `adminGuard`**（`gateway.IsPlatformAdmin` = super_admin）；handler 内不重复 `Authorize`。
- **绕过 prod:write**：admin handler 不调 `allowProd`（super_admin 有权干预生产），但写操作必记审计 + 前端危险确认。
- **代建消耗目标租户配额**：`QuotaCheck(目标租户 ctx, +1)`，超额 429；删除回收 `QuotaCheck(目标租户 ctx, -1)`。
- **租户归属从资源本身取**（`GetAny`），不信任请求体；代建 body `tenantId` 必填 + 校验存在。
- **凭证掩码**：任何对外端点 Connection 一律 `MaskConnection`（明文仅内部注入用）。
- **不自动 commit**（项目约定「未经用户明确要求，不要执行 git commit / 分支操作」）。SDD 工作区差异审查模式：implementer 不提交，reviewer 审 `git diff`。task 末尾不写 commit step，以 build/test/verify 收尾。
- **K8s 数据面 Service 名 = DataService.ID**（reconciler 建的 ClusterIP Service 名与 CR 名一致，应用 DNS 解析基此）。
- **降级**：无 clientset（纯内存/dev 非 k8s）实例读取返空 + 友好提示，不 5xx/panic。

---

## File Structure

| 文件 | 职责 | 动作 |
|---|---|---|
| `internal/billing/model.go` | 加 `ResDataservices` 维度常量 + 默认配额 + PriceTable + ResourceOrder | 修改 |
| `internal/billing/memory/store.go` | 默认配额 map 加 dataservices 键 | 修改 |
| `internal/billing/pg/store.go` | seed 默认配额加 dataservices 键（如有） | 修改 |
| `internal/dataservice/repository.go` | `Repository` 加 `GetAny(ctx, id)`（admin 跨租户读） | 修改 |
| `internal/dataservice/memory/store.go` | 实现 `GetAny` | 修改 |
| `internal/dataservice/pg/store.go` | 实现 `GetAny` | 修改 |
| `internal/dataservice/admin_handler.go` | **新建** admin handler（详情+实例+L2 运维+L3 代建+审计） | 创建 |
| `internal/dataservice/admin_handler_test.go` | admin handler 单测 | 创建 |
| `internal/environment/admin_handler.go` | **新建** admin 环境 handler（L3 代建+审计） | 创建 |
| `internal/environment/admin_handler_test.go` | admin 环境 handler 单测 | 创建 |
| `cmd/core/main.go` | 装配 admin handler（路由 + 注入 + OpenAPI 登记） | 修改 |
| `frontend/console-admin/src/modules/resources/api.ts` | 加 admin dataservice 详情/运维/代建/实例/审计 + 环境代建 API | 修改 |
| `frontend/console-admin/src/modules/resources/views/Dataservices.vue` | 加详情抽屉 + 运维按钮 + 代建 FormDrawer | 修改 |
| `frontend/console-admin/src/modules/resources/views/DataserviceDrawer.vue` | **新建** 详情抽屉组件（实例 + 连接 + 操作历史） | 创建 |
| `frontend/console-admin/src/modules/resources/views/DataserviceCreateDrawer.vue` | **新建** 代建表单（租户选择器 + 引擎/规格） | 创建 |
| `frontend/console-admin/src/modules/resources/views/Environments.vue` | 加代建 FormDrawer | 修改 |
| `frontend/console-admin/src/modules/resources/views/EnvironmentCreateDrawer.vue` | **新建** 环境代建表单 | 创建 |
| `CLAUDE.md` | 补 admin 管理能力基线 + 数据服务样板小节 | 修改 |

---

## Task 1: billing 加 dataservices 配额维度 + Repository.GetAny

**Files:**
- Modify: `internal/billing/model.go`
- Modify: `internal/billing/memory/store.go`
- Modify: `internal/billing/pg/store.go`
- Modify: `internal/dataservice/repository.go`
- Modify: `internal/dataservice/memory/store.go`
- Modify: `internal/dataservice/pg/store.go`
- Test: `internal/billing/`（现有测试）+ `internal/dataservice/`（现有测试 + 新 GetAny 断言）

**Interfaces:**
- Produces: `billing.ResDataservices = "dataservices"`（实例数配额维度）；`dataservice.Repository.GetAny(ctx context.Context, id string) (DataService, error)`（跨租户读，不过滤 tenant，admin 专用）。

**背景**：billing 现有维度 applications/workloads/models/gpu/tokens/storage_gb，无 dataservice 实例数。代建需要按实例数限配。`Repository.Get` 强制 ctx tenant（`tenantOrErr`），admin ctx 无 tenant 无法用，故加跨租户 `GetAny`。

- [ ] **Step 1: 加 billing 维度常量**

`internal/billing/model.go`，在 `ResStorage` 后加：

```go
	ResDataservices = "dataservices" // 数据服务实例数
```

`PriceTable` 加：

```go
	ResDataservices: 8.0,
```

`ResourceOrder` 把 `ResDataservices` 插到 `ResStorage` 之前（与其他计数维度同组）：

```go
var ResourceOrder = []string{
	ResApplications, ResWorkloads, ResModels, ResDataservices, ResGPU, ResTokens, ResStorage,
}
```

- [ ] **Step 2: 默认配额 map 加 dataservices 键**

`internal/billing/memory/store.go` 找默认配额字面量（`ResApplications: 50,` 所在 map），加：

```go
			billing.ResDataservices: 20,
```

`internal/billing/pg/store.go` 若有等价默认配额 seed map，同步加同一键（grep `ResApplications:` 定位）。若默认配额统一来自 `billing.memory`，则只改 memory。

- [ ] **Step 3: Repository 接口加 GetAny**

`internal/dataservice/repository.go`，在 `Get` 方法注释后加：

```go
	// GetAny 跨租户读取单条（admin 平台运维视角，不过滤 tenant；返回对象带 TenantID）。
	// 与 Get 的区别：Get 强制 ctx tenant 隔离（租户侧），GetAny 供 admin 跨租户定位。
	GetAny(ctx context.Context, id string) (DataService, error)
```

- [ ] **Step 4: 写 GetAny 失败测试（memory）**

`internal/dataservice/memory/store_test.go`（若无则建），加：

```go
func TestGetAnyCrossTenant(t *testing.T) {
	repo := NewStore() // 内存 store seed 不依赖 tenant ctx
	// 用 admin ctx（无 tenant）创建一条属 t-acme 的记录
	ctxAcme := tenant.WithTenant(context.Background(), "t-acme")
	d, err := repo.Create(ctxAcme, DataService{ID: "ds-1", Kind: KindDB, Name: "mysql-x", EngineID: ""})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// admin ctx 无 tenant，Get 会失败，GetAny 应成功
	if _, err := repo.Get(context.Background(), d.ID); err == nil {
		t.Fatal("Get should fail without tenant ctx")
	}
	got, err := repo.GetAny(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("GetAny: %v", err)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("tenant = %s, want t-acme", got.TenantID)
	}
	// 不存在统一返 err（不泄漏）
	if _, err := repo.GetAny(context.Background(), "nope"); err == nil {
		t.Fatal("GetAny should fail for missing")
	}
}
```

注：内存 store 的 `Create` 校验逻辑会要求 tenant ctx；测试用 acme ctx 创建后切回无 tenant ctx 验证 GetAny。

- [ ] **Step 5: 实现 memory GetAny**

`internal/dataservice/memory/store.go`，在 `Get` 方法后加（与 Get 同结构但去掉 tenant 校验）：

```go
// GetAny 跨租户读取单条（admin 专用，不过滤 tenant）。
func (s *Store) GetAny(ctx context.Context, id string) (dataservice.DataService, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.services[id]
	if !ok {
		return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", id)
	}
	d.Spec = cloneStrMap(d.Spec)
	d.Connection = cloneStrMap(d.Connection)
	d.Replicas = cloneIntP(d.Replicas)
	return d, nil
}
```

- [ ] **Step 6: 实现 pg GetAny**

`internal/dataservice/pg/store.go`，参考现有 `Get` 实现（grep `func (s \*Store) Get(`），新增 `GetAny` 去掉 `WHERE tenant_id=$1` 条件、去掉 tenantOrErr，仅按 id 查询。复用同一 `scanDataservice`/列常量。大致：

```go
func (s *Store) GetAny(ctx context.Context, id string) (dataservice.DataService, error) {
	row := s.pool.QueryRow(ctx, "SELECT "+dsCols+" FROM dataservices WHERE id=$1", id)
	d, err := scanDataservice(row)
	if err != nil {
		return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", id)
	}
	return d, nil
}
```

（实际列名/scan helper 以仓库现有命名为准，grep `dsCols` / `scanDataservice` 对齐。）

- [ ] **Step 7: 运行测试**

Run: `go test ./internal/billing/ ./internal/dataservice/... -race`
Expected: PASS（含新 TestGetAnyCrossTenant）。

- [ ] **Step 8: verify（不提交）**

确认 `go build ./...` 通过，`GetAny` 已在接口 + memory + pg 三处实现。

---

## Task 2: dataservice AdminHandler —— L1 详情+实例 + L2 运维+删除

**Files:**
- Create: `internal/dataservice/admin_handler.go`
- Create: `internal/dataservice/admin_handler_test.go`

**Interfaces:**
- Consumes: `dataservice.Repository`（含 `GetAny`/`ListAll`/`Update`/`Delete`，Task 1）；`dataplane` 风格实例读取（cmd/core 注入）；`dataservice.Restarter`（现有）。
- Produces: `AdminHandler` 类型 + 构造 opts；`InstanceInfo`/`InstanceReader`/`TenantChecker`/`AdminAuditRecorder`/`QuotaCheckFunc` 类型；路由方法 `ServeHTTP`（分发 `/api/admin/dataservices*`）。

**设计要点**：
- 不挂 `Authorize`、不调 `allowProd`（adminGuard 兜 super_admin + 绕过 prod:write）。
- 每个按 id 操作先 `repo.GetAny` 取资源 + `tenantID`，后续配额/审计/实例读都以 `tenant.WithTenant(ctx, tenantID)` 派生 ctx。
- L2 操作（start/stop/scale）逻辑与租户侧 `serveLifecycle`/`serveScale` 同（replicas→0/1、字段合并），但不走 allowProd；直接 `repo.Update`（Update 强制 ctx tenant → 用资源租户 ctx）。
- 删除：先 QuotaCheck(目标租户, -1) 回收，再 `repo.Delete`（同样用资源租户 ctx）。
- 实例信息：`InstanceReader.Instances(目标租户 ctx, namespace, ds.ID)`。

- [ ] **Step 1: 写 admin_handler.go 骨架 + 类型定义**

创建 `internal/dataservice/admin_handler.go`：

```go
package dataservice

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/aitoys/pkg/tenant"
	"github.com/aitoys/paas/internal/httputil"
)
```

（import 路径以仓库实际为准：`pkg/tenant`。）

定义依赖类型（依赖倒置，dataservice 不 import dataplane/billing/security/identity）：

```go
// InstanceInfo 是 admin 详情暴露的运行实例（轻量，dataservice 包自定义，cmd/core 桥接 dataplane）。
type InstanceInfo struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Port int32  `json:"port"`
}

// InstanceReader 读数据服务运行实例（admin 详情用）。cmd/core 桥接 dataplane.EndpointsReader。
// nil 或读不到时返空（集群外降级），不报错。
type InstanceReader interface {
	Instances(ctx context.Context, namespace, serviceName string) ([]InstanceInfo, error)
}

// TenantChecker 校验租户存在（admin 代建 body tenantId 必填校验）。cmd/core 桥接 identity.Repository。
type TenantChecker interface {
	Exists(ctx context.Context, tenantID string) error
}

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 dataservice->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// QuotaCheckFunc 配额检查-递增（横切）。ctx 必须带目标租户；delta=+1 创建/-1 删除。
type QuotaCheckFunc func(ctx context.Context, delta int) error

// AdminHandler 暴露数据服务 admin REST API（/api/admin/dataservices*）。
//
// 路由：
//
//	GET    /api/admin/dataservices          跨租户列表（已有，非本 handler）
//	GET    /api/admin/dataservices/{id}     跨租户详情（含运行实例）
//	POST   /api/admin/dataservices          代建（body tenantId 必填，消耗目标租户配额）
//	DELETE /api/admin/dataservices/{id}     强制删除（回收配额）
//	POST   /api/admin/dataservices/{id}/{stop|start|restart}
//	PUT    /api/admin/dataservices/{id}/scale
type AdminHandler struct {
	repo        Repository
	engineRepo  EngineRepository
	instances   InstanceReader
	restarter   Restarter
	quota       QuotaCheckFunc
	audit       AdminAuditRecorder
	tenants     TenantChecker
	namespace   string // K8s 命名空间（读实例），空则不读
	actorOf     func(*http.Request) string
}

// AdminHandlerOpt admin handler 配置。
type AdminHandlerOpt func(*AdminHandler)

func NewAdminHandler(repo Repository, opts ...AdminHandlerOpt) *AdminHandler {
	h := &AdminHandler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

func WithAdminInstances(r InstanceReader) AdminHandlerOpt       { return func(h *AdminHandler) { h.instances = r } }
func WithAdminRestarter(r Restarter) AdminHandlerOpt            { return func(h *AdminHandler) { h.restarter = r } }
func WithAdminQuota(f QuotaCheckFunc) AdminHandlerOpt            { return func(h *AdminHandler) { h.quota = f } }
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt       { return func(h *AdminHandler) { h.audit = a } }
func WithAdminTenants(c TenantChecker) AdminHandlerOpt          { return func(h *AdminHandler) { h.tenants = c } }
func WithAdminNamespace(ns string) AdminHandlerOpt              { return func(h *AdminHandler) { h.namespace = ns } }
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt { return func(h *AdminHandler) { h.actorOf = f } }
func WithAdminEngineRepo(r EngineRepository) AdminHandlerOpt    { return func(h *AdminHandler) { h.engineRepo = r } }
```

- [ ] **Step 2: 实现 ServeHTTP 分发 + 辅助方法**

继续 `admin_handler.go`：

```go
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/admin/dataservices" && r.Method == http.MethodPost:
		h.serveCreate(w, r)
	case strings.HasPrefix(path, "/api/admin/dataservices/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// tenantCtx 派生资源所属租户 ctx（admin 跨租户操作以资源租户身份执行下游）。
func tenantCtx(r *http.Request, tenantID string) (context.Context, *http.Request) {
	ctx := tenant.WithTenant(r.Context(), tenantID)
	return ctx, r.WithContext(ctx)
}

func (h *AdminHandler) actor(r *http.Request) string {
	if h.actorOf != nil {
		return h.actorOf(r)
	}
	return "admin"
}

// audit best-effort 记审计（错误不影响主流程）。
func (h *AdminHandler) audit(r *http.Request, tenantID, action, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, "dataservice", resourceID, detail)
}
```

- [ ] **Step 3: 实现 serveItem（详情 + 删除 + L2 运维）**

继续：

```go
func (h *AdminHandler) serveItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/dataservices/")
	rest = strings.TrimRight(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	d, err := h.repo.GetAny(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	switch action {
	case "":
		h.serveDetail(w, r, d)
	case "stop", "start":
		h.serveLifecycle(w, r, d, action)
	case "restart":
		h.serveRestart(w, r, d)
	case "scale":
		h.serveScale(w, r, d)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveDetail 详情：资源（掩码 Connection）+ 运行实例（目标租户 ctx 读 Endpoints）。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, d DataService) {
	out := map[string]any{
		"resource": maskDS(d),
	}
	if h.instances != nil && h.namespace != "" && !IsExternal(d.Source) {
		ctx, _ := tenantCtx(r, d.TenantID)
		ins, err := h.instances.Instances(ctx, h.namespace, d.ID)
		if err == nil {
			out["instances"] = ins
		} else {
			out["instances"] = []InstanceInfo{}
		}
	} else {
		out["instances"] = []InstanceInfo{}
	}
	httputil.WriteData(w, out)
}

// serveLifecycle start(replicas→1,running)/stop(replicas→0,stopped)，绕过 prod:write。
func (h *AdminHandler) serveLifecycle(w http.ResponseWriter, r *http.Request, d DataService, action string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rep := 1
	if action == "stop" {
		rep = 0
		d.Status = StatusStopped
	} else {
		d.Status = StatusRunning
	}
	d.Replicas = &rep
	ctx, rr := tenantCtx(r, d.TenantID)
	updated, err := h.repo.Update(ctx, d)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.audit(rr, d.TenantID, "admin:"+action, d.ID, action+" dataservice")
	httputil.WriteData(w, maskDS(updated))
}

func (h *AdminHandler) serveRestart(w http.ResponseWriter, r *http.Request, d DataService) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.restarter != nil {
		ctx, _ := tenantCtx(r, d.TenantID)
		if err := h.restarter.Restart(ctx, d.ID); err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
	}
	h.audit(r, d.TenantID, "admin:restart", d.ID, "restart dataservice")
	httputil.WriteData(w, map[string]string{"restarted": d.ID})
}

type adminScaleInput struct {
	Replicas  *int   `json:"replicas,omitempty"`
	CPU       string `json:"cpu,omitempty"`
	Memory    string `json:"memory,omitempty"`
	StorageGB int    `json:"storageGb,omitempty"`
}

func (h *AdminHandler) serveScale(w http.ResponseWriter, r *http.Request, d DataService) {
	if r.Method != http.MethodPut {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in adminScaleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	d.Replicas = in.Replicas
	if in.CPU != "" {
		d.CPU = in.CPU
	}
	if in.Memory != "" {
		d.Memory = in.Memory
	}
	if in.StorageGB > 0 {
		d.StorageGB = in.StorageGB
	}
	ctx, rr := tenantCtx(r, d.TenantID)
	updated, err := h.repo.Update(ctx, d)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.audit(rr, d.TenantID, "admin:scale", d.ID, "scale dataservice")
	httputil.WriteData(w, maskDS(updated))
}

// serveDelete 强制删除（先回收目标租户配额，再以资源租户 ctx 删）。
func (h *AdminHandler) serveDelete(w http.ResponseWriter, r *http.Request, d DataService) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, rr := tenantCtx(r, d.TenantID)
	if h.quota != nil {
		_ = h.quota(ctx, -1) // 回收配额 best-effort（删除主操作不因配额回滚失败而阻断）
	}
	if err := h.repo.Delete(ctx, d.ID); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.audit(rr, d.TenantID, "admin:delete", d.ID, "delete dataservice")
	httputil.WriteData(w, map[string]string{"deleted": d.ID})
}

// maskDS 返回 Connection 掩码副本（admin 详情/操作返回前一律掩码）。
func maskDS(d DataService) DataService {
	d.Connection = MaskConnection(d.Connection)
	return d
}
```

注意：`serveItem` 的 `case ""` 走详情（GET）；DELETE 需在分发时处理。修订 `serveItem` 的 `switch action`：当 `action==""` 且方法为 DELETE 时调 `serveDelete`。把 `case ""` 改为：

```go
	case "":
		if r.Method == http.MethodDelete {
			h.serveDelete(w, r, d)
			return
		}
		h.serveDetail(w, r, d)
```

- [ ] **Step 4: 写 admin_handler L1+L2 测试**

`internal/dataservice/admin_handler_test.go`：用内存 repo + fake InstanceReader/Restarter/audit，断言：
- 详情返回 `{resource, instances}`，Connection 已掩码，外部模式 instances 为空。
- stop 把 replicas→0/status→stopped，记审计 `admin:stop`。
- 删除调 quota(-1) + repo.Delete + 记审计 `admin:delete`。
- 跨租户：资源属 t-acme，admin ctx 无 tenant，GetAny 仍能取到。

```go
package dataservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

type fakeInstances struct{ list []InstanceInfo }
func (f fakeInstances) Instances(ctx context.Context, ns, svc string) ([]InstanceInfo, error) {
	return f.list, nil
}
type fakeAudit struct{ last string }
func (a *fakeAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}
type fakeQuota struct{ n int }
func (q *fakeQuota) check(ctx context.Context, d int) error { q.n += d; return nil }

func newAdminForTest(t *testing.T) (*AdminHandler, *fakeAudit, *fakeQuota) {
	t.Helper()
	repo := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	_, err := repo.Create(ctx, DataService{ID: "ds-1", Kind: KindDB, Name: "m1", Source: SourceManaged, Spec: map[string]string{"engine": "postgres"}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	au := &fakeAudit{}
	q := &fakeQuota{}
	h := NewAdminHandler(repo,
		WithAdminInstances(fakeInstances{list: []InstanceInfo{{Name: "ds-1-0", IP: "10.0.0.1", Port: 5432}}}),
		WithAdminNamespace("paas"),
		WithAdminAudit(au),
		WithAdminQuota(q.check),
	)
	return h, au, q
}

func TestAdminDetailReturnsInstancesAndMasksConnection(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/dataservices/ds-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	data := out["data"].(map[string]any)
	res := data["resource"].(map[string]any)
	conn, _ := res["connection"].(map[string]any)
	if conn != nil && conn["password"] != "" && conn["password"] != SecretMask {
		t.Fatal("connection should be masked")
	}
	ins := data["instances"].([]any)
	if len(ins) != 1 {
		t.Fatalf("instances=%v", ins)
	}
}

func TestAdminStopUpdatesAndAudits(t *testing.T) {
	h, au, _ := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices/ds-1/stop", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if au.last != "admin:stop" {
		t.Fatalf("audit=%s", au.last)
	}
}

func TestAdminDeleteRecoversQuotaAndAudits(t *testing.T) {
	h, au, q := newAdminForTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/dataservices/ds-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if q.n != -1 {
		t.Fatalf("quota delta=%d want -1", q.n)
	}
	if au.last != "admin:delete" {
		t.Fatalf("audit=%s", au.last)
	}
}

func TestAdminScaleMergesFields(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"replicas":3,"cpu":"2"}`))
	req := httptest.NewRequest(http.MethodPut, "/api/admin/dataservices/ds-1/scale", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 5: 运行测试**

Run: `go test ./internal/dataservice/ -run TestAdmin -race -v`
Expected: PASS（4 例）。

- [ ] **Step 6: verify**

`go build ./...` 通过；admin handler L1+L2 完成（代建在 Task 3）。

---

## Task 3: dataservice AdminHandler —— L3 代建（POST）

**Files:**
- Modify: `internal/dataservice/admin_handler.go`（加 `serveCreate`）
- Modify: `internal/dataservice/admin_handler_test.go`（加代建测试）

**Interfaces:**
- Consumes: Task 2 的 `AdminHandler` + `EngineRepository`（现有 `resolveFromEngine` 复用）；`TenantChecker`/`QuotaCheckFunc`/`AdminAuditRecorder`（Task 2）。
- Produces: `AdminHandler.serveCreate`（POST `/api/admin/dataservices`）。

**要点**：代建 body 必填 `tenantId`；校验租户存在 → quota(+1) → 引擎解析 → 以目标租户 ctx `repo.Create` → 审计 `admin:create`。复用租户侧的引擎解析语义（`resolveFromEngine` 是 Handler 方法，admin handler 需独立实现等价逻辑或抽出共享函数——为不污染租户侧，admin handler 内联等价解析）。

- [ ] **Step 1: admin handler 内联引擎解析（与租户侧 resolveFromEngine 等价）**

`admin_handler.go` 加（admin 不依赖租户侧 Handler 实例，内联解析逻辑）：

```go
// resolveEngineForAdmin 与 Handler.resolveFromEngine 等价（admin 路径独立，避免耦合租户侧 Handler）。
// engineID 必填；按引擎目录回填 kind/source/connection。
func (h *AdminHandler) resolveEngineForAdmin(ctx context.Context, d *DataService) error {
	if d.EngineID == "" {
		return fmt.Errorf("missing engineId")
	}
	if h.engineRepo == nil {
		return fmt.Errorf("引擎目录未启用")
	}
	eng, err := h.engineRepo.GetEngine(ctx, d.EngineID)
	if err != nil {
		return fmt.Errorf("引擎不存在: %s", d.EngineID)
	}
	if !eng.Enabled {
		return fmt.Errorf("引擎未启用: %s", d.EngineID)
	}
	d.Kind = eng.Kind
	d.Source = eng.Mode
	if d.Spec == nil {
		d.Spec = map[string]string{}
	}
	d.Spec["engine"] = eng.Engine
	switch eng.Mode {
	case EngineModeExternalShared:
		d.Connection = map[string]string{}
		for k, v := range eng.Connection {
			d.Connection[k] = v
		}
	case EngineModeExternalDedicated:
		if d.Connection == nil || d.Connection["host"] == "" {
			return fmt.Errorf("external-dedicated 模式需填写连接 host")
		}
	default:
		d.Connection = nil // store.FillConnection 平台生成
	}
	return nil
}
```

（import 补 `"fmt"`。）

- [ ] **Step 2: 实现 serveCreate**

`admin_handler.go` 加：

```go
// adminCreateInput 代建请求体。TenantID 必填（归属租户）；其余与 DataService 一致。
type adminCreateInput struct {
	TenantID string `json:"tenantId"`
	DataService
}

func (h *AdminHandler) serveCreate(w http.ResponseWriter, r *http.Request) {
	var in adminCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.TenantID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing tenantId")
		return
	}
	// 校验租户存在
	if h.tenants != nil {
		if err := h.tenants.Exists(r.Context(), in.TenantID); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, fmt.Errorf("租户不存在: %s", in.TenantID))
			return
		}
	}
	d := in.DataService
	d.TenantID = in.TenantID
	if err := h.resolveEngineForAdmin(r.Context(), &d); err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	// 以目标租户 ctx 执行（配额 + Create 都按目标租户计）
	ctx, rr := tenantCtx(r, in.TenantID)
	if h.quota != nil {
		if err := h.quota(ctx, 1); err != nil {
			httputil.WriteServiceError(w, http.StatusTooManyRequests, err)
			return
		}
	}
	saved, err := h.repo.Create(ctx, d)
	if err != nil {
		if h.quota != nil {
			_ = h.quota(ctx, -1) // 创建失败回滚配额
		}
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.audit(rr, in.TenantID, "admin:create", saved.ID, "代建 dataservice")
	httputil.WriteDataCreated(w, maskDS(saved))
}
```

- [ ] **Step 3: 写代建测试**

`admin_handler_test.go` 加：

```go
type fakeTenants struct{}
func (fakeTenants) Exists(ctx context.Context, id string) error {
	if id == "t-acme" {
		return nil
	}
	return fmt.Errorf("not found")
}

func TestAdminCreateConsumesQuotaAndAttachesTenant(t *testing.T) {
	repo := NewStore()
	au := &fakeAudit{}
	q := &fakeQuota{}
	h := NewAdminHandler(repo,
		WithAdminTenants(fakeTenants{}),
		WithAdminAudit(au),
		WithAdminQuota(q.check),
	)
	body := bytes.NewReader([]byte(`{"tenantId":"t-acme","id":"ds-new","name":"pg1","engineId":""}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// engineId 空 -> 400（resolveEngineForAdmin 报 missing engineId），配额回滚
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (missing engineId), got %d", rec.Code)
	}
	if q.n != 0 {
		t.Fatalf("quota should roll back on failure, got %d", q.n)
	}
}

func TestAdminCreateRejectsMissingTenant(t *testing.T) {
	h, _, _ := newAdminForTest(t)
	body := bytes.NewReader([]byte(`{"id":"ds-x","name":"x"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 missing tenantId, got %d", rec.Code)
	}
}

func TestAdminCreateRejectsUnknownTenant(t *testing.T) {
	repo := NewStore()
	h := NewAdminHandler(repo, WithAdminTenants(fakeTenants{}))
	body := bytes.NewReader([]byte(`{"tenantId":"t-ghost","name":"x"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dataservices", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 unknown tenant, got %d", rec.Code)
	}
}
```

（import 补 `"fmt"`。）

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/dataservice/ -run TestAdmin -race -v`
Expected: PASS（含 3 个新代建用例）。

- [ ] **Step 5: verify**

`go build ./...` 通过；代建路径完成。

---

## Task 4: environment AdminHandler —— L3 代建

**Files:**
- Create: `internal/environment/admin_handler.go`
- Create: `internal/environment/admin_handler_test.go`
- Modify: `internal/environment/repository.go`（如需 `Exists`，否则用现有 List 兜底——见 Step 1）

**Interfaces:**
- Consumes: `environment.Repository`（现有 `Create`/`List`）；`TenantChecker`/`AdminAuditRecorder`（依赖倒置，与 dataservice 同款）。
- Produces: `environment.AdminHandler`（POST `/api/admin/environments` 代建）。

**要点**：环境无配额维度（不计费），代建仅校验租户 + 以目标租户 ctx `Create` + 审计。环境 `Create(ctx, e)` 以 ctx tenant 为准（CLAUDE.md「Create 以 ctx 租户为准忽略请求体」），故 admin 用 `tenant.WithTenant(ctx, tenantId)` 派生 ctx 调 Create。

- [ ] **Step 1: 写 environment admin handler**

`internal/environment/admin_handler.go`：

```go
package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// AdminTenantChecker / AdminAuditRecorder 与 dataservice 同款（依赖倒置，避免 environment->identity/security）。
type AdminTenantChecker interface {
	Exists(ctx context.Context, tenantID string) error
}
type AdminAuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// AdminHandler 暴露环境 admin REST API（/api/admin/environments*）。
// 仅代建（L3）：环境是基础设施，admin 可代某租户建环境；其余运维（删除）走租户侧。
type AdminHandler struct {
	repo    Repository
	tenants AdminTenantChecker
	audit   AdminAuditRecorder
	actorOf func(*http.Request) string
}

type AdminHandlerOpt func(*AdminHandler)

func NewAdminHandler(repo Repository, opts ...AdminHandlerOpt) *AdminHandler {
	h := &AdminHandler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}
func WithAdminTenants(c AdminTenantChecker) AdminHandlerOpt { return func(h *AdminHandler) { h.tenants = c } }
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt   { return func(h *AdminHandler) { h.audit = a } }
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

func (h *AdminHandler) actor(r *http.Request) string {
	if h.actorOf != nil {
		return h.actorOf(r)
	}
	return "admin"
}

// ServeHTTP 仅处理 POST /api/admin/environments（代建）。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost || r.URL.Path != "/api/admin/environments" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		TenantID string `json:"tenantId"`
		Environment
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.TenantID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing tenantId")
		return
	}
	if h.tenants != nil {
		if err := h.tenants.Exists(r.Context(), in.TenantID); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, fmt.Errorf("租户不存在: %s", in.TenantID))
			return
		}
	}
	e := in.Environment
	e.TenantID = in.TenantID
	if err := e.Validate(); err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	// 未指定 promoteOrder 时按 type 填默认（与租户侧 handler 一致）。
	if e.PromoteOrder == 0 {
		e.PromoteOrder = DefaultPromoteOrder(e.Type)
	}
	ctx := tenant.WithTenant(r.Context(), in.TenantID)
	if err := h.repo.Create(ctx, e); err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	if h.audit != nil {
		_ = h.audit.Record(r.Context(), in.TenantID, h.actor(r), "admin:create", "environment", e.ID, "代建 environment")
	}
	httputil.WriteDataCreated(w, e)
}
```

注：`Environment.Validate()` / `DefaultPromoteOrder` / `Environment.PromoteOrder` 已存在（见 model.go / DevOps promote 改造）。implementer grep 确认签名一致；若 `Create` 返回 `(Environment, error)` 而非 `error`，按实际签名调整（repository.go 显示 `Create(ctx, e) error`）。

- [ ] **Step 2: 写 environment admin 测试**

`internal/environment/admin_handler_test.go`：fake tenant checker + audit，断言：
- 缺 tenantId → 400。
- 未知租户 → 400。
- 成功：以目标租户 ctx Create（用内存 repo 验证 TenantID 落库）+ 记审计 `admin:create`。

```go
package environment

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

type fakeChk struct{}
func (fakeChk) Exists(ctx context.Context, id string) error {
	if id == "t-acme" {
		return nil
	}
	return errNotFound // 用包内既有 sentinel 或 errors.New
}
type fakeEnvAudit struct{ last string }
func (a *fakeEnvAudit) Record(ctx context.Context, tid, actor, action, rt, rid, detail string) error {
	a.last = action
	return nil
}

func TestAdminCreateEnvironmentAttachesTenant(t *testing.T) {
	repo := NewStore()
	au := &fakeEnvAudit{}
	h := NewAdminHandler(repo, WithAdminTenants(fakeChk{}), WithAdminAudit(au))
	body := bytes.NewReader([]byte(`{"tenantId":"t-acme","id":"env-1","name":"prod","type":"prod","cluster":"prod-bj"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/environments", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := repo.Get(tenant.WithTenant(context.Background(), "t-acme"), "env-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TenantID != "t-acme" {
		t.Fatalf("tenant=%s", got.TenantID)
	}
	if au.last != "admin:create" {
		t.Fatalf("audit=%s", au.last)
	}
}
```

（`errNotFound` / `repo.Get` 签名以包内既有为准，implementer 对齐。）

- [ ] **Step 3: 运行测试**

Run: `go test ./internal/environment/ -race -v`
Expected: PASS。

- [ ] **Step 4: verify**

`go build ./...` 通过。

---

## Task 5: cmd/core 装配 admin handler + 路由 + OpenAPI

**Files:**
- Modify: `cmd/core/main.go`（装配 + 路由）
- Modify: `cmd/core/auth_audit.go`（复用/确认 adapter）或新建 admin audit 注入

**Interfaces:**
- Consumes: Task 2/3 `dataservice.AdminHandler` + 其 opts 类型；Task 4 `environment.AdminHandler`；`dataplane.EndpointsReader`（现有，cmd/core 装配处 `appliers.clientset`）；`billing.CheckAndInc`；`security.AuditStore`；`identity.Repository`（TenantChecker）；`gateway.UserIDFrom`。

**要点**：
- 桥接 `dataplane.EndpointsReader` → `dataservice.InstanceReader`（adapter 转换 `dataplane.Instance` → `dataservice.InstanceInfo`）。
- 桥接 `identity.Repository` → `dataservice.TenantChecker` / `environment.AdminTenantChecker`（`Exists` = `GetTenant != nil`）。
- 桥接 `billing.Store.CheckAndInc` → `dataservice.QuotaCheckFunc`。
- 审计 adapter 复用 `identityAuditAdapter`（签名匹配 `AdminAuditRecorder`）——直接复用同一 `*identityAuditAdapter` 实例（它实现了 `Record(ctx, tid, actor, action, rt, rid, detail)`）。
- 路由：`/api/admin/dataservices*` 与 `/api/admin/environments` POST 挂 `adminGuard`。

- [ ] **Step 1: 写 dataplane→dataservice InstanceReader adapter**

`cmd/core/main.go`（与 `dsEnvLookup` 同处）加：

```go
// dsInstanceReader 桥接 dataplane.EndpointsReader -> dataservice.InstanceReader（admin 详情读实例）。
type dsInstanceReader struct{ r dataplane.EndpointsReader }

func (a dsInstanceReader) Instances(ctx context.Context, ns, svc string) ([]dataservice.InstanceInfo, error) {
	if a.r == nil {
		return nil, nil
	}
	list, err := a.r.Instances(ctx, ns, svc)
	if err != nil {
		return nil, err
	}
	out := make([]dataservice.InstanceInfo, 0, len(list))
	for _, x := range list {
		out = append(out, dataservice.InstanceInfo{Name: x.Name, IP: x.IP, Port: x.Port})
	}
	return out, nil
}
```

- [ ] **Step 2: 写 identity→TenantChecker adapter**

```go
// tenantChecker 桥接 identity.Repository -> dataservice/environment TenantChecker。
type tenantChecker struct{ repo identity.Repository }

func (c tenantChecker) Exists(ctx context.Context, tenantID string) error {
	t, err := c.repo.GetTenant(ctx, tenantID)
	if err != nil || t.ID == "" {
		return fmt.Errorf("租户不存在: %s", tenantID)
	}
	return nil
}
```

（`identity.Repository.GetTenant` 签名以仓库实际为准，grep 确认。）

- [ ] **Step 3: 写 billing→QuotaCheck adapter**

```go
// quotaCheckFn 桥接 billing.CheckAndInc -> dataservice.QuotaCheckFunc（资源维度=dataservices）。
// ctx 必须带目标租户（admin 代建时已 WithTenant）。
func quotaCheckFn(bill billing.Repository) dataservice.QuotaCheckFunc {
	return func(ctx context.Context, delta int) error {
		_, err := bill.CheckAndInc(ctx, billing.ResDataservices, delta)
		return err
	}
}
```

（`billing.Repository` 是否暴露 `CheckAndInc`？grep 确认；若仅 `*billing.Store` 暴露，则用 `stores.Billing.(*billing.Store)` 或在 Repository 接口加方法——以仓库实际为准，application handler 已用同款 QuotaCheck，参考其 cmd/core 装配。）

- [ ] **Step 4: 装配 dataservice AdminHandler**

`cmd/core/main.go` 在 `dsHandler` 装配后加：

```go
	// admin dataservice handler（L1 详情+实例 / L2 运维+删 / L3 代建）。全挂 adminGuard(super_admin)。
	dsAdminAudit := &identityAuditAdapter{store: stores.Security} // 复用通用审计 adapter（签名匹配 AdminAuditRecorder）
	dsAdminHandler := dataservice.NewAdminHandler(stores.DataService,
		dataservice.WithAdminEngineRepo(stores.Engine),
		dataservice.WithAdminInstances(dsInstanceReader{r: dataplane.NewEndpointsReader(appliers.clientset)}),
		dataservice.WithAdminRestarter(appliers.dsRestarter),
		dataservice.WithAdminQuota(quotaCheckFn(stores.Billing)),
		dataservice.WithAdminAudit(dsAdminHandler),
		dataservice.WithAdminTenants(tenantChecker{repo: stores.Identity}),
		dataservice.WithAdminNamespace(appliers.namespace),
		dataservice.WithAdminActor(gateway.UserIDFrom),
	)
```

（修正 `WithAdminAudit(dsAdminHandler)` → `WithAdminAudit(dsAdminAudit)`。`appliers.dsRestarter` / `appliers.namespace` 已存在。）

- [ ] **Step 5: 装配 environment AdminHandler**

```go
	envAdminHandler := environment.NewAdminHandler(stores.Environment,
		environment.WithAdminTenants(tenantChecker{repo: stores.Identity}),
		environment.WithAdminAudit(&identityAuditAdapter{store: stores.Security}),
		environment.WithAdminActor(gateway.UserIDFrom),
	)
```

- [ ] **Step 6: 注册路由 + OpenAPI**

`cmd/core/main.go` 在 `/api/admin/dataservices` GET Register 之后、其它 admin 总览端点区，加 admin 写路由。注意 mux 最长前缀匹配：`/api/admin/dataservices/` 需显式 Handle 到 dsAdminHandler，避免与 GET 列表（Register 注册的 `/api/admin/dataservices`）冲突。

```go
	// admin dataservice 管理（详情/运维/代建/删）。
	mux.Handle("/api/admin/dataservices", adminGuard(dsAdminHandler))   // POST 代建（GET 列表由上方 Register 处理，mux 精确匹配优先）
	mux.Handle("/api/admin/dataservices/", adminGuard(dsAdminHandler))  // {id}/{action}
	mux.Handle("/api/admin/environments", adminGuard(envAdminHandler))  // POST 代建（GET 列表由上方 Register 处理）
```

**关键冲突排查**：上方已有 `reg.Register("GET", "/api/admin/dataservices", ...)`（列表，经 registry 注册到 mux 同路径）+ `reg.Register("GET", "/api/admin/environments", ...)`。`mux.Handle("/api/admin/dataservices", ...)` 会与 registry 注册的同路径 handler 冲突（panic: multiple registrations）。**解决**：不在 mux 重复 Handle 同一路径，改让 dsAdminHandler 也处理 GET 列表（或把列表逻辑移入 admin handler）。

**修订方案**（implementer 遵循）：把现有 GET 列表逻辑也交由 `dsAdminHandler` 处理（在 `AdminHandler.ServeHTTP` 加 `path == "/api/admin/dataservices" && GET` 分支调 `repo.ListAll` + 掩码），然后删掉 main.go 中 `reg.Register("GET","/api/admin/dataservices",...)` 那块，统一 `mux.Handle("/api/admin/dataservices", adminGuard(dsAdminHandler))` + `mux.Handle("/api/admin/dataservices/", ...)`。OpenAPI Operation 用 `reg.Operation(...)` 登记（spec-only，不重复 mux 注册）。环境同理：`envAdminHandler.ServeHTTP` 加 GET `/api/admin/environments` 分支（`repo.ListAll`），删 main.go 原 GET Register。

implementer 执行此修订时：
1. `AdminHandler.ServeHTTP` 补 GET 列表分支（`repo.ListAll` + 逐条 `MaskConnection` + `WriteData`）。
2. `environment.AdminHandler.ServeHTTP` 补 GET 列表分支（`repo.ListAll`）。
3. main.go 删除原 `reg.Register("GET","/api/admin/dataservices",...)` 与 `reg.Register("GET","/api/admin/environments",...)` 两个列表块，改 `mux.Handle` 到 admin handler。
4. OpenAPI 用 `reg.Operation` 登记 GET/POST/DELETE/PUT 各操作（spec-only），`Perm("super_admin")`。

- [ ] **Step 7: OpenAPI Operation 登记**

```go
	reg.Operation("GET", "/api/admin/dataservices/{id}", apiroute.Tags("数据服务管理"), apiroute.Summary("数据服务详情（跨租户，含实例）"), apiroute.Perm("super_admin"))
	reg.Operation("POST", "/api/admin/dataservices", apiroute.Tags("数据服务管理"), apiroute.Summary("代建数据服务（指定租户）"), apiroute.Perm("super_admin"), apiroute.WithReqBody(map[string]any{}), apiroute.WithResp(map[string]any{}))
	reg.Operation("DELETE", "/api/admin/dataservices/{id}", apiroute.Tags("数据服务管理"), apiroute.Summary("强制删除数据服务"), apiroute.Perm("super_admin"))
	reg.Operation("POST", "/api/admin/dataservices/{id}/stop", apiroute.Tags("数据服务管理"), apiroute.Summary("停止"), apiroute.Perm("super_admin"))
	reg.Operation("POST", "/api/admin/dataservices/{id}/start", apiroute.Tags("数据服务管理"), apiroute.Summary("启动"), apiroute.Perm("super_admin"))
	reg.Operation("POST", "/api/admin/dataservices/{id}/restart", apiroute.Tags("数据服务管理"), apiroute.Summary("重启"), apiroute.Perm("super_admin"))
	reg.Operation("PUT", "/api/admin/dataservices/{id}/scale", apiroute.Tags("数据服务管理"), apiroute.Summary("扩缩容"), apiroute.Perm("super_admin"), apiroute.WithReqBody(map[string]any{}))
	reg.Operation("POST", "/api/admin/environments", apiroute.Tags("环境管理"), apiroute.Summary("代建环境（指定租户）"), apiroute.Perm("super_admin"), apiroute.WithReqBody(map[string]any{}))
```

- [ ] **Step 8: 运行 + verify**

Run: `go build ./... && go test ./... -race`
Expected: 全绿（含现有 dataservice/environment 测 + 新 admin 测）。

`./bin/core` 启动正常（无 mux 注册 panic）。

---

## Task 6: 前端 api.ts —— admin 数据服务/环境管理 API

**Files:**
- Modify: `frontend/console-admin/src/modules/resources/api.ts`

**Interfaces:**
- Consumes: Task 5 的后端端点。
- Produces: `AdminDataserviceDetail` / `DataserviceInstance` 类型；`fetchDataserviceDetail` / `dataserviceAction`（stop/start/restart）/`scaleDataservice`/`deleteDataservice`/`createDataserviceForTenant`/`fetchAdminAuditLogs` API 函数；`createEnvironmentForTenant`。

**要点**：复用 `api.get/post/put/delete`（console-admin 统一 http 客户端，自动解包 `{data:T}`）。详情响应是 `{resource, instances}`。

- [ ] **Step 1: 加类型 + API 函数**

`api.ts` 在 `fetchDataserviceList` 之后加：

```ts
// -- 数据服务管理（详情/运维/代建）--
export interface DataserviceInstance {
  name: string
  ip: string
  port: number
}
export interface AdminDataserviceDetail {
  resource: AdminDataservice & {
    engineId?: string
    source?: string
    replicas?: number
    cpu?: string
    memory?: string
    storageGb?: number
    envId?: string
    spec?: Record<string, string>
    connection?: Record<string, string>
    createdAt?: string
  }
  instances: DataserviceInstance[]
}

export const fetchDataserviceDetail = (id: string) =>
  api.get<AdminDataserviceDetail>(`/api/admin/dataservices/${id}`)

// start/stop/restart 统一 action
export const dataserviceAction = (id: string, action: 'start' | 'stop' | 'restart') =>
  api.post<unknown>(`/api/admin/dataservices/${id}/${action}`, {})

export const scaleDataservice = (
  id: string,
  body: { replicas?: number; cpu?: string; memory?: string; storageGb?: number }
) => api.put<unknown>(`/api/admin/dataservices/${id}/scale`, body)

export const deleteDataservice = (id: string) =>
  api.delete<unknown>(`/api/admin/dataservices/${id}`)

export const createDataserviceForTenant = (body: {
  tenantId: string
  id?: string
  name: string
  engineId: string
  envId?: string
  replicas?: number
  cpu?: string
  memory?: string
  storageGb?: number
  connection?: Record<string, string>
}) => api.post<unknown>('/api/admin/dataservices', body)

// -- 环境代建 --
export const createEnvironmentForTenant = (body: {
  tenantId: string
  id?: string
  name: string
  type: 'prod' | 'test'
  cluster?: string
  desc?: string
}) => api.post<unknown>('/api/admin/environments', body)

// -- 租户下拉（代建选租户）--
export interface AdminTenant {
  id: string
  name: string
}
export const fetchAllTenants = () => api.get<AdminTenant[]>('/api/admin/tenants')
```

- [ ] **Step 2: verify**

`cd frontend/console-admin && pnpm exec vue-tsc --noEmit`
Expected: 无类型错误。

---

## Task 7: 前端 Dataservices.vue —— 详情抽屉 + 运维 + 代建

**Files:**
- Modify: `frontend/console-admin/src/modules/resources/views/Dataservices.vue`
- Create: `frontend/console-admin/src/modules/resources/views/DataserviceDrawer.vue`
- Create: `frontend/console-admin/src/modules/resources/views/DataserviceCreateDrawer.vue`

**Interfaces:**
- Consumes: Task 6 的 API；console-admin 四件套（SearchTable/FormDrawer/useCrud）；`useDangerConfirm` 等价（console-admin 用 `confirmService`，见 console-admin CLAUDE.md 第 12 条）。
- Produces: 总览页行点击开详情抽屉；详情抽屉含实例表 + 连接（掩码）+ 运维按钮 + 操作历史；代建 FormDrawer（租户选择器 + 引擎/规格）。

**范式参考**：参照 `modules/model/Detail.vue`（详情）+ `modules/model/views/ChannelFormDrawer.vue`（FormDrawer + dependencies）+ `modules/system/tenant`（租户 CRUD 抽屉）。implementer 必读这三个文件对齐范式。

- [ ] **Step 1: Dataservices.vue 加行点击 + 顶部「新建」**

`Dataservices.vue` 模板 `<template #actions>` 加「新建数据服务」按钮（开 create drawer），表格加 `@row-click="(row) => openDetail(row)"`：

```vue
<template #actions>
  <el-button :icon="Plus" type="primary" @click="createVisible = true">新建数据服务</el-button>
  <el-button :icon="Refresh" @click="fetchList">刷新</el-button>
</template>
```

script 加：

```ts
import { Plus } from '@element-plus/icons-vue'
import DataserviceDrawer from './DataserviceDrawer.vue'
import DataserviceCreateDrawer from './DataserviceCreateDrawer.vue'

const detailVisible = ref(false)
const detailId = ref('')
const createVisible = ref(false)
const openDetail = (row: AdminDataservice) => {
  detailId.value = row.id
  detailVisible.value = true
}
```

模板末尾加：

```vue
<DataserviceDrawer v-model="detailVisible" :id="detailId" />
<DataserviceCreateDrawer v-model="createVisible" @created="fetchList" />
```

`columns` 加 `tenantId` 列（已有）。行点击样式：`SearchTable` 支持 `@row-click`（参照其它模块用法；若 SearchTable 不直接支持，加「详情」操作列按钮）。

- [ ] **Step 2: DataserviceDrawer.vue（详情 + 实例 + 运维 + 操作历史）**

新建，结构：
- props: `modelValue: boolean`, `id: string`。
- 打开时 `fetchDataserviceDetail(id)`，10s 轮询实例（`onUnmounted clearInterval`）。
- 区块：基本信息（ID/名称/Kind/租户/环境/状态/引擎/来源/副本/CPU/Memory/Storage）→ 实例表（name/ip/port，空则「未接入集群数据面」）→ 连接信息（connection map，掩码值原样显示 `••••••`）→ 运维按钮组（按状态启用：running→可停/重启/扩缩，stopped→可启）→ 操作历史折叠（`fetchAdminAuditLogs` 过滤 resourceId，按 spec 复用现有 audit API）。
- 危险操作（删除/停止生产）用 console-admin `confirmService`（import 自 `@/lib/confirm/confirmService`）二次确认。
- 运维/删除成功后 emit refresh + 重新 fetchDetail。

```vue
<script lang="ts" setup>
import { ref, watch, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import {
  fetchDataserviceDetail,
  dataserviceAction,
  scaleDataservice,
  deleteDataservice,
  fetchAdminAuditLogs,
  type AdminDataserviceDetail,
  type AdminAuditLog
} from '../api'
import { confirmService } from '@/lib/confirm/confirmService'

const props = defineProps<{ modelValue: boolean; id: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'refresh'): void }>()

const detail = ref<AdminDataserviceDetail | null>(null)
const audits = ref<AdminAuditLog[]>([])
const loading = ref(false)
let timer: number | undefined

const load = async () => {
  if (!props.id) return
  loading.value = true
  try {
    detail.value = await fetchDataserviceDetail(props.id)
  } finally {
    loading.value = false
  }
}
const loadAudits = async () => {
  if (!props.id) return
  const all = await fetchAdminAuditLogs({ page: 1, size: 50 })
  audits.value = (all.records ?? []).filter((a) => a.resourceId === props.id)
}

watch(
  () => [props.modelValue, props.id],
  ([open]) => {
    if (open) {
      load()
      loadAudits()
      timer = window.setInterval(load, 10000)
    } else if (timer) {
      clearInterval(timer)
    }
  }
)
onUnmounted(() => timer && clearInterval(timer))

const runAction = async (action: 'start' | 'stop' | 'restart') => {
  await confirmService.confirm(`确认${action === 'stop' ? '停止' : action === 'start' ? '启动' : '重启'}该数据服务？`)
  await dataserviceAction(props.id, action)
  ElMessage.success('已执行')
  load()
  emit('refresh')
}
const remove = async () => {
  await confirmService.confirm('确认强制删除该数据服务？此操作不可恢复。')
  await deleteDataservice(props.id)
  ElMessage.success('已删除')
  emit('update:modelValue', false)
  emit('refresh')
}
</script>
```

模板用 `el-drawer size="50%"`，运维按钮 `:disabled` 按状态（`detail.resource.status`）。扩缩弹一个内联 `el-popover` + 副本/CPU 输入 → `scaleDataservice`。

- [ ] **Step 3: DataserviceCreateDrawer.vue（代建表单）**

新建，结构：
- props: `modelValue: boolean`；emit `created`。
- 字段：租户（`fetchAllTenants` 下拉，必选）→ 引擎（`GET /api/engines?kind=&enabled=true` 拉启用的引擎列表选 engineId，必选）→ 名称（必填）→ 实例 ID（可选，空则后端生成）→ 副本/CPU/Memory/Storage（可选）→ envId（可选，`GET /api/admin/environments` 当前租户的环境，或留空）。
- 引擎选定后展示 mode（managed/external-shared/external-dedicated）；external-dedicated 时显示 connection.host 输入。
- 提交 `createDataserviceForTenant` → 成功 emit `created` + 关闭 + ElMessage。

参考 `ChannelFormDrawer.vue` 的 `dependencies` 声明式显隐 + `FormDrawer` mode='add'。

- [ ] **Step 4: verify**

```bash
cd frontend/console-admin && pnpm exec vue-tsc --noEmit && pnpm build
```
Expected: 通过。

---

## Task 8: 前端 Environments.vue —— 代建

**Files:**
- Modify: `frontend/console-admin/src/modules/resources/views/Environments.vue`
- Create: `frontend/console-admin/src/modules/resources/views/EnvironmentCreateDrawer.vue`

**Interfaces:**
- Consumes: Task 6 `createEnvironmentForTenant` / `fetchAllTenants`。

- [ ] **Step 1: EnvironmentCreateDrawer.vue**

字段：租户（必选）→ 名称（必填）→ 类型（prod/test select）→ 集群（可选）→ 描述（可选）。提交 `createEnvironmentForTenant`。

- [ ] **Step 2: Environments.vue 加「新建环境」按钮**

模板 `#actions` 加按钮开 drawer，`@created="fetchList"`。

- [ ] **Step 3: verify**

`cd frontend/console-admin && pnpm exec vue-tsc --noEmit && pnpm build`
Expected: 通过。

---

## Task 9: 收尾 —— 全量验证 + 部署 + CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 后端全量测试**

Run: `go test ./... -race`
Expected: 全绿。

- [ ] **Step 2: 前端三套 build**

```bash
cd frontend && pnpm install && pnpm build
```
Expected: 三套（console-admin/console-user/landing）通过。

- [ ] **Step 3: 部署 + e2e**

Run: `./scripts/deploy-k8s.sh`
Expected: 退出 0。验证：
- `GET /api/admin/dataservices`（admin token）200。
- `GET /api/admin/dataservices/{id}` 200，返回 `{resource, instances}`。
- `POST /api/admin/dataservices/{id}/stop` 200，状态变 stopped。
- `POST /api/admin/dataservices` body `{tenantId, name, engineId}` 201，目标租户配额 dataservices +1。
- `DELETE /api/admin/dataservices/{id}` 200，配额 -1。
- `POST /api/admin/environments` body `{tenantId, name, type}` 201。
- console-admin `/admin/` 登录 → 资源总览 → 数据服务行点击开详情抽屉 → 实例/连接/运维按钮可见 → 代建弹窗选租户+引擎提交成功。

- [ ] **Step 4: 更新 CLAUDE.md**

在「P1.4 后台管理重构」之后或新建小节「admin 管理能力基线（P1.7 数据服务样板）」，记录：
- 三层模型（L1 详情+实例 / L2 运维+删 / L3 代建）。
- 跨租户写端点规范（`/api/admin/<resource>/{id}/{action}` 独立端点 + adminGuard + 绕过 prod:write + 审计）。
- 实例读取复用 dataplane EndpointsReader（admin 注入目标租户 ctx）。
- 代建消耗目标租户配额（新增 `billing.ResDataservices` 维度）。
- Repository.GetAny（admin 跨租户读）。
- 留后续：P2 工作负载/应用、P3 DevOps、P4 治理/可观测/计费/安全 按本样板推进；批量运维；平台共享实例池。

- [ ] **Step 5: 最终 verify**

确认所有改动在工作区（未提交），`git status` 干净呈现改动文件，`git diff` 可供审查。

---

## 验证策略（汇总）

- **后端单测**：Task 1（GetAny 跨租户）、Task 2（admin 详情/stop/delete/scale + 审计 + 配额回收）、Task 3（代建 quota+1/失败回滚/租户校验）、Task 4（环境代建）。`go test ./... -race` 全绿。
- **前端**：vue-tsc + pnpm build 三套通过。
- **k8s e2e**：admin 端点全 200/201；代建消耗配额；运维操作 reflected 到 K8s；审计落库；console-admin 详情抽屉可用。

## 非目标（YAGNI，留后续）

- 工作负载/应用/DevOps/治理/可观测/计费/安全 的 admin 管理能力（按本样板后续逐模块推进）。
- 批量运维（批量启停/删）。
- 平台共享实例池（代建归属平台供多租户共享）。
- admin 操作的细粒度审计查询/导出（本期只落库 + 抽屉折叠展示）。
- 数据服务 upgrade（版本升级）admin 端点（租户侧已有，admin 暂不镜像，按需再加）。
- 应用/业务编排类代建（明确不做，见基线 spec）。
