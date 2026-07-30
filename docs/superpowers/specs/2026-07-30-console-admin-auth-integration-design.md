# console-admin 身份对接 core（密码登录 + JWT）设计

> 把 fork 的 vue-admin 基座（当前用 MSW mock、测试号 admin/123456）对接到 PaaS core 后端，
> 让管理员能用用户名密码登录真实的 core，session 复用 core identity 的租户/角色体系。
> 本切片是 P0（管理后台打通）的起手式，解锁后续 P0-2（identity admin CRUD）与 P0-3（平台运维页）。

## 目标

1. core 新增密码登录基础设施：User 扩展 Email/PasswordHash/Status + bcrypt + JWT 签发/校验 + 5 个 `/api/auth/*`、`/api/system/menus` 端点 + seed admin/123456。
2. console-admin 关闭 MSW、指向 core、适配 core 响应格式，登录链路打通（登录 → 拉 profile → 拉菜单 → 进入首页）。
3. JWT 中间件与现有 APIKeyAuth **并列**，注入同一套 ctx（`tenant.WithTenant` / `WithRoles` / `WithUserID`），下游所有业务 handler 零改动。
4. core 响应契约 `{data:T}`/`{error:msg}` **不动**（保护 console-user 与 OpenAPI），格式适配只在 admin 侧拦截器做。

## 非目标（YAGNI，留后续）

- identity 管理 CRUD（tenants/users/roles/api-keys 的 List/Create/Update/Delete）→ **P0-2**。
- console-admin 的 PaaS 业务页（租户管理/全租户计费/审计）→ **P0-3**。
- 第三方登录（OAuth/SSO）、MFA、密码找回、密码轮转策略。
- JWT 黑名单 / 服务端会话存储（本期 JWT 无状态，登出仅前端清 token，refresh 滚动签发）。
- 菜单的后端动态化（按角色权限过滤）→ 初期静态菜单，P0-3 再做权限感知菜单。
- 跨租户 super_admin 平台级管理（admin 用户本期归属 t-acme，跨租户留 P0-2）。

## 现状（事实，带路径）

### core（后端）

- `internal/core/identity/model.go:15-21`：`User{ID/TenantID/Name/IsAdmin/Roles[]string}`，**无 password/email/status**。
- `internal/core/identity/repository.go:7-18`：Repository 仅 6 方法（CreateTenant/GetTenant/CreateUser/UsersByTenant/CreateAPIKey/LookupAPIKey），**无管理 CRUD、无 GetUserByName**。
- `cmd/core/main.go:serveHTTP`：identity **0 HTTP 路由**；仅 `gateway.APIKeyAuth(stores.Identity)`（main.go:152）作中间件 + seed。
- `internal/core/gateway/auth.go:14-34`：APIKeyAuth 解析 Bearer → LookupAPIKey → 注入 ctx（tenant/roles/userID）。
- 响应格式：每 handler 各自 `json.NewEncoder(w).Encode(map{...})`，成功 `{"data":T}`、失败 `{"error":msg}`，**无统一响应函数**。
- PG 表 `migrations/0001_identity.up.sql:9-15`：`users(id/tenant_id/name/is_admin/created_at)`，无 password_hash。
- **无任何 JWT/bcrypt/password/login 代码。**

### console-admin（前端基座）

- 登录：`modules/auth/views/Login.vue:121-141` → `authService.login` → `JwtAuthProvider.login`（`lib/auth/JwtAuthProvider.ts:11-13`）→ `POST /api/auth/sessions`（当前被 MSW 拦截到 mock）。
- JwtAuthProvider 期望端点（`JwtAuthProvider.ts:5-9`，**URL 已是 RESTful 约定**）：
  - `POST /api/auth/sessions` `{username,password}` → `{accessToken,refreshToken?,expiresIn?}`
  - `DELETE /api/auth/sessions` → void
  - `POST /api/auth/tokens/refresh` `{refreshToken}` → `{accessToken,...}`
  - `GET /api/auth/users/me` → `UserProfile{id,username,nickname?,avatar?,roles[],permissions[]}`
- token 存储：`MemorySessionTokenStorage`（`lib/auth/TokenStorage.ts:16-43`），sessionStorage key `va:access`/`va:refresh`。
- 守卫 `lib/router/guards.ts:60-107` 4 步：白名单 → 认证 → profile bootstrap（`loadProfile`→`me()`）→ menus bootstrap（`GET /api/system/menus`）→ 权限。
- HTTP 层 `lib/http/client.ts:6`：`baseURL: VITE_API_BASE_URL ?? '/'`；**vite.config.ts 无 server.proxy**。
- 响应拦截器 `lib/http/interceptors.ts:34-74`：识别 `{code,data,msg}`，`code===0` 解包 `data`；非 2xx → ProblemDetail。**core 的 `{data:T}` 无 code 字段会被原样透传（不解包）→ 业务页拿到 `{data:T}` 而非 T，需兼容。**
- MSW 开关 `app/main.ts:58-66`：`enableMock = DEV || VITE_ENABLE_MOCK==='true'`，**DEV 永远 true → 开发期 mock 关不掉（bug）**。env.d.ts 声明的 `VITE_USE_MOCK` 与 main.ts 读的 `VITE_ENABLE_MOCK` 不一致（历史遗留）。
- permission store `app/stores/permission.ts:15`：`isSuperAdmin = roles.includes('super_admin')` 短路所有判断。
- mock 测试号 `mock/handlers/auth.ts:20-21`：admin/123456（super_admin，全权限 `['*']`）、user/123456。

## 架构决策

### D1：认证模式 = 密码 + JWT（非 API Key）

**决策**：core 新增密码登录 + JWT。**不**走「admin 填 API Key」捷径。

**理由**：开源 PaaS 控制台标配用户名密码登录；admin 基座已有完整 JwtAuthProvider/refresh/守卫/超管短路，复用比砍掉更合算；API Key 是长效 bearer、无失效/刷新，不适合浏览器交互登录。

### D2：core 端点路径对齐 admin 期望（admin 零 URL 改动）

**决策**：core 提供的端点路径 = admin JwtAuthProvider 已期望的路径（`/api/auth/sessions` 等）。admin 侧 JwtAuthProvider **不改 URL**。

**理由**：admin 前端已是成熟约定，让后端对齐前端比改前端风险低。core 现有 `/api/*` 风格也一致。

### D3：响应格式适配只在 admin 侧（core 契约不动）

**决策**：core 继续返回 `{"data":T}`/`{"error":msg}`。admin `lib/http/interceptors.ts` 加 core 格式兼容分支：响应体有 `data` 字段且无 `code` → 解包 `data`；HTTP 4xx/5xx + `{error}` → 映射 ProblemDetail。

**理由**：保护 console-user（已完整接入）+ OpenAPI 契约（55 路径/36 schema）不受影响；一处拦截器改动，admin 所有 API 复用。

### D4：JWT 自实现 HMAC-SHA256（零依赖）

**决策**：core 不引 `golang-jwt`，自写最小 JWT（`base64url(header).base64url(payload).base64url(HMAC-SHA256(secret, header.payload))`）。claims：`sub`(userID)/`tenant`/`roles[]`/`typ`(access|refresh)/`exp`/`iat`。密钥来自 `PAAS_JWT_SECRET` env（空则启动时随机生成 + 日志警告「重启失效」）。

**理由**：与 spec 2（apiroute 零依赖 reflector）同构；避免新依赖；JWT 规范简单。access 15min、refresh 7d。

### D5：密码哈希用 bcrypt（golang.org/x/crypto/bcrypt）

**决策**：User 加 `PasswordHash` 字段，bcrypt cost=10。引入 `golang.org/x/crypto/bcrypt`（BSD-3-Clause，Apache 2.0 兼容，license-check 通过）。

**理由**：bcrypt 是密码哈希业界标准，比自造安全。x/crypto 非 GPL/AGPL。

### D6：JWT 中间件与 APIKeyAuth 并列（下游零改动）

**决策**：新增 `gateway.JWTAuth(idb)` 中间件，解析 Bearer → 若是 JWT（含两段 `.`）走 JWT 校验 → 注入同一 ctx（`tenant.WithTenant`/`WithRoles`/`WithUserID`）；若是 API Key（无 `.`）走原 LookupAPIKey。用一个聚合中间件 `gateway.BearerAuth(idb)` 按 token 形态分发。

**理由**：`/v1/chat/completions` 等仍用 API Key（程序化调用），`/api/auth/*` 与 admin 用 JWT（人机），两种 token 共存。下游 handler 只认 ctx，不关心 token 来源。

### D7：admin 用户归属 t-acme + 超管角色映射

**决策**：seed 一个 admin 用户（username=admin, password=123456, TenantID=t-acme, roles=[tenant-admin], IsAdmin=true）。`/api/auth/users/me` 返回时，IsAdmin 用户 `roles=['super_admin']`、`permissions=['*']`（触发 admin 基座超管短路）；普通用户返回实际 roles + BuiltinRoles 展开的 permissions 列表。

**理由**：admin 基座 `isSuperAdmin` 短路要求 `super_admin`；core 角色是 tenant-admin/developer/viewer。me 端点做映射，不改 admin permission store 逻辑。跨租户管理留 P0-2。

### D8：菜单初期静态（PaaS 业务页未建）

**决策**：`GET /api/system/menus` 返回与 admin 现有视图对齐的最小菜单（dashboard/profile/about），component 字符串指向 admin 已有的 `modules/*/views/*.vue`。system/* 演示页菜单本期不下发（页面保留但不入口），P0-3 再下发 PaaS 业务页菜单。

**理由**：admin 的 PaaS 业务页尚未创建（P0-3 才做），先保证登录后能进 dashboard 不白屏。

## 后端设计（core）

### 模型扩展（`internal/core/identity/model.go`）

```go
type User struct {
    ID           string
    TenantID     string
    Name         string // 登录用户名（租户内唯一，本期全局唯一即可）
    Email        string // 可选
    PasswordHash string // bcrypt
    IsAdmin      bool
    Roles        []string
    Status       string // active|disabled（active 才可登录）
    CreatedAt    time.Time
}
```

### Repository 扩展（`internal/core/identity/repository.go`）

新增方法（管理 CRUD 留 P0-2，本期只加登录所需）：
```go
GetUserByName(ctx, name string) (*User, error) // 登录用
GetUser(ctx, tenantID, userID string) (*User, error) // me 端点用
```

内存实现 `memory/store.go` + PG 实现 `pg/store.go` 各补这两个方法。PG `GetUserByName` 用 `WHERE name=$1`（全局唯一，本期简化）。

### migration（`internal/storage/pg/migrations/0012_identity_auth.up.sql`）

```sql
ALTER TABLE users ADD COLUMN IF NOT EXISTS email text DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash text DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active';
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_name ON users(name);
```

seed 调整：`seedIdentity` 给 admin 用户写入 bcrypt 哈希（`123456`）。

### JWT 包（`internal/core/auth/jwt.go`，新）

纯标准库（`crypto/hmac` + `crypto/sha256` + `encoding/base64` + `encoding/json`）：
```go
type Claims struct {
    Sub    string   `json:"sub"`    // userID
    Tenant string   `json:"tenant"`
    Roles  []string `json:"roles"`
    Typ    string   `json:"typ"`    // access|refresh
    Exp    int64    `json:"exp"`
    Iat    int64    `json:"iat"`
}
func Sign(claims Claims, secret string) (string, error)
func Parse(token, secret string) (*Claims, error) // 验签 + 验 exp
```

### 密码哈希（`internal/core/auth/password.go`，新）

```go
func HashPassword(plain string) (string, error)  // bcrypt cost=10
func CheckPassword(hash, plain string) bool
```

### JWT 中间件（`internal/core/gateway/jwt.go`，新 + 改 auth.go）

```go
// BearerAuth 按 token 形态分发：含 '.' 走 JWT，否则走 APIKey。
func BearerAuth(idb identity.Repository) func(http.Handler) http.Handler
```

main.go:152 把 `auth := gateway.APIKeyAuth(...)` 改为 `auth := gateway.BearerAuth(...)`。下游全不变。

### auth handler（`internal/core/auth/handler.go`，新）

5 端点，挂 `mux`（不走 BearerAuth，login 公开；me/menus/refresh/logout 挂 BearerAuth）：

| 方法 | 路径 | 鉴权 | 请求体 | 响应 |
|------|------|------|--------|------|
| POST | `/api/auth/sessions` | 公开 | `{username,password}` | `{accessToken,refreshToken,expiresIn}` |
| POST | `/api/auth/tokens/refresh` | 公开（凭 refresh） | `{refreshToken}` | `{accessToken,refreshToken,expiresIn}` |
| DELETE | `/api/auth/sessions` | BearerAuth | — | `{}`（前端清 token） |
| GET | `/api/auth/users/me` | BearerAuth | — | `UserProfile` |
| GET | `/api/system/menus` | BearerAuth | — | `MenuDTO[]` |

登录流程：`GetUserByName` → `CheckPassword` → 检查 Status=active → 签 access+refresh → 返回。

main.go 用 composite handler 分发 `/api/auth/` 与 `/api/system/menus`，全部 `reg.Operation` 登记 OpenAPI。

### seed（`cmd/core/seed.go`）

`seedIdentity` 加：admin 用户（name=admin, bcrypt(123456), t-acme, [tenant-admin], IsAdmin=true）。幂等（已存在跳过）。

## 前端设计（console-admin）

### env 统一 + MSW 开关修复（`app/main.ts:58-66`）

```ts
const enableMock = import.meta.env.VITE_ENABLE_MOCK === 'true' // 显式开启才 mock
```
- 新建 `.env.development`：`VITE_API_BASE_URL=`（空，走 vite proxy）+ `VITE_ENABLE_MOCK=false`
- `env.d.ts` 统一为 `VITE_ENABLE_MOCK`（删 `VITE_USE_MOCK` 历史字段）

### vite proxy（`vite.config.ts`）

```ts
server: { proxy: { '/api': 'http://localhost:8080', '/v1': 'http://localhost:8080' } }
```
开发期 admin:5173 的 `/api/*` 代理到 core:8080。

### 响应拦截器兼容 core 格式（`lib/http/interceptors.ts:36-60`）

改成功分支：响应体是对象且**有 `data` 字段**（无论有无 `code`）→ 若有 `code` 且 `!==0` 当错误；否则解包 `data`。core 的 `{data:T}` 命中解包。失败分支：HTTP 4xx/5xx + `{error:msg}` → ProblemDetail（`title`/`detail` 取 `error`）。

### token storage key（`lib/auth/TokenStorage.ts:11-12`）

`va:access`/`va:refresh` → `paas:access`/`paas:refresh`（品牌统一；旧 key 清理）。

### JwtAuthProvider（`lib/auth/JwtAuthProvider.ts`）

URL 已对齐 core，**不改**。仅确认 `AuthResult`/`UserProfile` 字段与 core 返回一致（一致）。

### 登录页（`modules/auth/views/Login.vue`）

默认填充 admin/123456（演示便利，仅 dev）。提示文案改 PaaS。

## 数据契约（JSON）

**POST /api/auth/sessions**
```json
// req
{"username":"admin","password":"123456"}
// resp (core {"data": T} → 拦截器解包为 T)
{"accessToken":"<jwt>","refreshToken":"<jwt>","expiresIn":900}
```

**GET /api/auth/users/me**
```json
{"id":"u-acme-admin","username":"admin","nickname":"Acme 管理员",
 "roles":["super_admin"],"permissions":["*"]}
```

**GET /api/system/menus**（初期静态，对齐 admin 视图）
```json
[{"id":"dashboard","title":"首页","icon":"HomeFilled","component":"dashboard/views/Home"},
 {"id":"profile","title":"个人中心","icon":"User","component":"profile/views/Profile"}]
```

## 验收标准

1. `make build` + `go vet` + `go test ./... -race` 全绿；新增 auth 包单测（JWT 签/验、密码哈希、login handler 成功/密码错/禁用）。
2. core 启动后：`curl -POST /api/auth/sessions -d '{"username":"admin","password":"123456"}'` 返回 accessToken；`curl -H "Authorization: Bearer <jwt>" /api/auth/users/me` 返回 super_admin profile；`curl -H "Bearer <jwt>" /api/applications` 复用 JWT 鉴权通过。
3. 错密码 401 `{error:"用户名或密码错误"}`；JWT 过期/伪造 401；API Key 调用（`sk-acme-admin`）仍正常工作（D6 双通道）。
4. console-admin `VITE_ENABLE_MOCK=false pnpm dev` → 浏览器 5173 用 admin/123456 登录成功 → 进入 dashboard、菜单正常、profile 显示。
5. PG 路径（`PAAS_DB_URL` 非空）：migration 0012 自动 up、admin 用户落库、登录走 PG。
6. OpenAPI `/openapi.json` 含 5 个新 auth 端点契约；`/docs` 可试。
7. CHANGELOG + CLAUDE.md 同步。

## 风险

- **JWT 密钥**：未配 `PAAS_JWT_SECRET` 时随机生成，重启后旧 token 全失效（可接受，生产必配）。
- **全局 username 售一**：本期 admin 用户少，全局唯一简化；P0-2 改租户内唯一 + GetUserByName 加 tenant 参数。
- **refresh 无黑名单**：refresh 滚动签发新 access，登出仅前端清 token（无状态 JWT 的已知取舍，文档注明）。
- **admin system/* 演示页**：本期保留代码但菜单不入口；P0-3 决定砍/重做。

## 依赖

- `golang.org/x/crypto/bcrypt`（BSD-3，Apache 2.0 兼容）—— 唯一新依赖，license-check 通过。
- 前端无新依赖。
