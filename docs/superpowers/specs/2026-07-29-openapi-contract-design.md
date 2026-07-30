# OpenAPI 契约切片设计：route registry 单一真源 + 前端 TS 生成

**日期**：2026-07-29
**状态**：待评审
**关联**：CLAUDE.md「API 契约：OpenAPI 自动生成前端 TS 类型（Plan 4 起接入）」

## 背景与动机

当前 43 个后端路由的元数据（路径/方法/权限/入参出参）散落在 `cmd/core/main.go` 的 `mux.Handle` 与各 handler 内部；前端有 49 个手写 `interface` 散落各 view + `api.ts`，与后端结构靠人工同步，易漂移。

本切片建一个 **route registry 作单一真源**：路由声明一次，同时驱动（1）Go 1.22 method-scoped mux 注册、（2）OpenAPI 3.0 spec 生成；前端用 `openapi-typescript` 从 `/openapi.json` 生成 TS 类型，闭合"前后端类型一致"。

## 范围

**做**：
- `internal/apiroute` Registry（own mux + 记录元数据 + 生成 spec）
- 手写 Go 类型 → JSON Schema reflector（零依赖）
- `{data: T}` / `{error: string}` 响应包裹约定建模
- 权限 → Bearer 安全方案 + per-endpoint scope
- `/openapi.json` 端点
- 前端 `openapi-typescript` 生成 `types.gen.ts` + `pnpm gen:api` 脚本
- 43 路由机械迁移到 registry

**不做（YAGNI）**：交互式 `/docs` UI（swagger-ui/redoc 静态资源托管）、请求校验中间件、自动 mock server、存量 49 interface 一次性重写（新代码用生成类型，存量渐进迁移）。

## 技术选型

| 领域 | 选型 | license |
|------|------|---------|
| Schema 反射 | 手写 reflector（`reflect` + `encoding/json` tag） | — 零依赖 |
| TS 生成 | `openapi-typescript`（前端 devDep） | Apache 2.0 |

后端**零新增依赖**（reflector 手写）。前端仅加一个 devDep。

## 架构

```
cmd/core/main.go serveHTTP
  └─ reg := apiroute.New(mux, Info{Title:"PaaS Platform", Version:"1.0"})
     ├─ reg.Register("GET","/api/applications", appHandler, Summary, Perm, Response)   # mux + spec
     ├─ reg.Register("POST","/api/applications", appHandler, ...)
     ├─ reg.Operation("GET","/api/applications/{id}", Summary, Perm, Response)         # spec only（composite 子操作）
     └─ ...
     └─ mux.Handle("/openapi.json", apiroute.ServeSpec(reg))

internal/apiroute/
  ├─ registry.go     # Registry：own mux + Register/Operation + Options + ServeSpec
  ├─ reflect.go      # Go 类型 → JSON Schema（手写，零依赖）
  └─ openapi.go      # OpenAPI 3.0 文档结构 + 序列化
```

## Registry 设计

```go
type Opt func(*Route)

type Route struct {
    Method, Path, Summary string
    Tags                  []string
    Perm                  string                 // 映射为 security scope
    RequestBody           reflect.Type           // nil = 无请求体
    Response              reflect.Type           // 响应载荷类型（不含 {data:} 包裹）
}

type Registry struct {
    mux    *http.ServeMux
    info   Info
    routes []Route
    schemas map[string]*Schema   // 命名类型 → component schema（$ref 去重）
}

// Register 既注册 mux（Go 1.22 method-scoped）又记录 spec 元数据。
// 用于"一个 handler 对应一个端点"的普通路由。
func (r *Registry) Register(method, path string, h http.Handler, opts ...Opt) {
    r.mux.Handle(method+" "+path, h)   // Go 1.22："GET /api/applications"
    r.record(method, path, opts)
}

// Operation 仅记录 spec 元数据，不注册 mux。
// 用于 composite 路由：mux 注册是粗粒度 subtree，内部派发多个逻辑操作；
// 每个逻辑操作用 Operation 登记，spec 才完整，mux 不重复注册。
func (r *Registry) Operation(method, path string, opts ...Opt) { r.record(method, path, opts) }
```

Options：`Summary(string)` / `Tags(...string)` / `Perm(string)` / `RequestBody(T)` / `Response(T)`（后两者用泛型 `reflect.TypeOf`）。

### Composite 路由处理（关键）

当前 `/api/applications/` 注册一个 subtree handler（composite），内部按子段派发到 workloads/repos/bindings 等多个操作。**mux 注册保持原样不动**（直接 `mux.Handle`，不经 registry），registry 只负责把每个逻辑操作登记进 spec：

```go
// mux：粗粒度 subtree 保留原注册（不经 registry，避免空 method 非法 pattern）
mux.Handle("/api/applications", auth(composite))
mux.Handle("/api/applications/", auth(composite))
// spec：每个逻辑操作用 Operation 登记（不注册 mux），文档才完整
reg.Operation("GET", "/api/applications/{id}", Perm("application:read"), Response(application.Application{}))
reg.Operation("POST", "/api/applications/{id}/bindings", Perm("binding:write"), RequestBody(...), Response(...))
reg.Operation("DELETE", "/api/applications/{id}/bindings/{type}/{name}", Perm("binding:write"), ...)
reg.Operation("GET", "/api/applications/{id}/workloads", Perm("workload:read"), Response([]workload.Workload{}))
// ...
```

非 composite 的路由（如 `/api/environments`、`/api/billing/quota`）用 `Register`，一行搞定 mux + spec。

## 手写 Reflector

`internal/apiroute/reflect.go`：`func schemaOf(t reflect.Type, reg *Registry) *Schema`。命名类型登记进 `components/schemas` 并用 `$ref` 引用（去重、支持自引用/共享）。

| Go 类型 | JSON Schema |
|---------|-------------|
| struct | `type:object` + properties（读 `json` tag 名；`omitempty` → 非 required；`json:"-"` 跳过；匿名字段 inline） |
| `*T` / `[]T` | unwrap 指针；slice → `array` + items |
| `map[string]V` | `object` + additionalProperties |
| string / int* / float* / bool | string / integer / number / boolean |
| `time.Time` | string（format: date-time） |
| `interface{}`/`any` | 无类型约束 |

幂等：同命名类型只登记一次。复杂边界（递归类型、嵌套指针）覆盖常见用例，罕见分支按需补（YAGNI）。

## 响应包裹约定

后端统一 `{"data": <T>}` / `{"error": "msg"}`。`Response(T)` 选项把载荷 schema **inline 包装**为：

```json
{"type":"object","properties":{"data":<schemaOf(T)>,"error":{"type":"string"}}}
```

inline 包装（不注册命名 component）避免泛型实例化产生丑陋类型名。list 端点 `T = []Entity`，详情 `T = Entity`。错误响应统一 `{"error": string}`，所有端点共享一份（4xx/5xx 复用）。

## 权限 → 安全方案

```yaml
components:
  securitySchemes:
    BearerAPIKey:
      type: http
      scheme: bearer
      description: "Authorization: Bearer <api-key>"
paths:
  /api/applications:
    get:
      security:
        - BearerAPIKey: [application:read]   # perm 映射为 scope
```

每端点的 `Perm(...)` → 该端点 required scope，文档直显"需 application:read"，呼应 RBAC。`/livez` 与 `/openapi.json` 无 security。

## 端点

`GET /openapi.json`：返回完整 OpenAPI 3.0 文档（`Content-Type: application/json`）。无鉴权（公开契约）。由 `apiroute.ServeSpec(reg)` 提供，`r.JSON()` 序列化。

## 前端 TS 生成

```jsonc
// frontend/console-user/package.json
"scripts": {
  "gen:api": "openapi-typescript http://localhost:8080/openapi.json -o src/api/types.gen.ts"
}
```

`openapi-typescript`（Apache 2.0）加为 devDep。流程：后端启动 → `pnpm gen:api` → 生成 `src/api/types.gen.ts`（含 paths + components/schemas）。

**存量迁移策略**：不一次性重写 49 个手写 interface（大爆炸式重构，高风险）。新代码 + 改动处用生成类型；手写 interface 在自然接触时替换。`fetchAuth` 可加泛型 `fetchAuth<T>(path): Promise<T>` 消费生成类型。

## 迁移计划（43 路由）

`cmd/core/main.go` 的 `serveHTTP` 机械替换：

1. 把 `mux := http.NewServeMux()` 改为 `reg := apiroute.New(mux, Info{...})`（registry own 同一个 mux）。
2. 普通路由：`mux.Handle("/api/environments", auth(envHandler))` → `reg.Register("GET","/api/environments", auth(envHandler), Summary(...), Perm("environment:read"), Response(...))`，按方法拆成 GET/POST 各一行。
3. Composite 路由：保留 `mux.Handle("/api/applications/", auth(composite))`（或经 `reg.Register("", path, h)`），逻辑子操作用 `reg.Operation(...)` 逐一登记。
4. 末尾 `mux.Handle("/openapi.json", apiroute.ServeSpec(reg))`。

迁移以**模块为单位**推进（application → workload → environment → ... → billing/dataservice），每模块：登记路由 + 跑 handler 测试确认不破坏。

## 测试策略

| 层 | 测试 |
|----|------|
| Reflector | 表驱动：struct/slice/map/指针/time.Time/嵌套/命名类型 $ref 去重 |
| Registry | 注册后 spec 含正确 method/path/security；Operation 不注册 mux；`{data:}` 包裹正确 |
| 端到端 | 启动 core → `GET /openapi.json` 返回合法 OpenAPI 3.0；关键路径含正确 scope |
| 路由不破坏 | 现有 handler 测试 + 手工 curl（CLAUDE.md 的 e2e 示例）全过 |

不新增"路由可达性"自动化（mux 注册由 Go 1.22 保证；spec 正确性由 registry 单测覆盖）。

## 验收标准

1. `GET /openapi.json` 返回合法 OpenAPI 3.0 文档，含全部 43 逻辑操作的 method/path/security/schema。
2. 前端 `pnpm gen:api` 生成 `types.gen.ts`，含 paths 与 schemas。
3. 现有所有 handler 测试绿；`golangci-lint` 0 issues；`go vet` 干净。
4. 后端零新增依赖（reflector 手写）；前端仅加 `openapi-typescript` devDep。
5. e2e：`curl /api/applications`（带 Bearer）行为与迁移前一致；`/livez` 正常。

## 风险与对策

- **method-scoped mux 迁移破坏现有路由**：以模块为单位小步替换，每步跑 handler 测试；composite 路由的 mux 注册保持粗粒度 subtree 不变，降低风险。
- **reflector 边界遗漏**：表驱动测试覆盖常见型；遇未覆盖型补测，不臆造。
- **TS 生成需后端运行**：`gen:api` 文档说明需先 `make run`；未来可加 `make openapi` 一键生成。
- **spec 与实际漂移**：registry 是单一真源，路由只能经 `Register/Operation` 声明，杜绝第二个声明处。
