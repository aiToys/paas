# console-admin 身份对接 core 实施计划

> **For agentic workers:** 执行此计划用 superpowers:subagent-driven-development 或 superpowers:executing-plans。任务用 `- [ ]` 跟踪。

**Goal:** 让 console-admin 用用户名密码登录真实 core（JWT），打通登录→profile→菜单闭环。

**Architecture:** core 加密码+JWT（bcrypt+HMAC 自实现），BearerAuth 双通道中间件（JWT/APIKey 共存），5 个 `/api/auth/*`+`/api/system/menus` 端点对齐 admin JwtAuthProvider；admin 侧改 MSW 开关+vite proxy+拦截器适配 core `{data}` 格式。core 响应契约不动。

**Tech Stack:** Go stdlib + golang.org/x/crypto/bcrypt；Vue3+axios（admin 已有）。

## Global Constraints

- 主语言 Go；Apache 2.0；CI 禁 GPL/AGPL。
- core 响应契约 `{"data":T}`/`{"error":msg}` 不动。
- 多租户隔离由 Core 统一治理。
- 注释中文；不执行 git commit/分支。
- env 开关空值=现状不变：`PAAS_JWT_SECRET` 空→随机生成+警告。
- 设计依据：`docs/superpowers/specs/2026-07-30-console-admin-auth-integration-design.md`。

---

## 后端

### Task 1: identity 模型扩展 + Repository + migration

**Files:**
- Modify: `internal/core/identity/model.go`（User 加 Email/PasswordHash/Status）
- Modify: `internal/core/identity/repository.go`（加 GetUserByName/GetUser）
- Modify: `internal/core/identity/memory/store.go`（实现两方法）
- Modify: `internal/core/identity/pg/store.go`（实现两方法 + scan 新列）
- Create: `internal/storage/pg/migrations/0012_identity_auth.up.sql` + `.down.sql`
- Modify: `internal/storage/pg/migrate.go`（embed 已含 migrations/，自动生效）
- Test: `internal/core/identity/memory/store_test.go`（补 GetUserByName）

**Interfaces:**
- Produces: `User{...,Email,PasswordHash,Status}`；`Repository.GetUserByName(ctx,name)` / `GetUser(ctx,tenantID,userID)`

- [ ] 改 model.go User 加三字段（Email string / PasswordHash string / Status string，常量 `StatusActive="active"`/`StatusDisabled="disabled"`）
- [ ] repository.go 接口加 `GetUserByName(ctx context.Context, name string) (*User, error)` + `GetUser(ctx, tenantID, userID string) (*User, error)`
- [ ] memory/store.go：users map 已存全量，按 name/userID 查（RLock）
- [ ] pg/store.go：`GetUserByName` `SELECT ... FROM users WHERE name=$1`（全局唯一）；`GetUser` `WHERE tenant_id=$1 AND id=$2`；scan 加三列；现有 `CreateUser`/`UsersByTenant` INSERT/SELECT 同步加列
- [ ] migration 0012 up：`ALTER TABLE users ADD COLUMN email text DEFAULT '' / password_hash text DEFAULT '' / status text NOT NULL DEFAULT 'active'` + `CREATE UNIQUE INDEX idx_users_name ON users(name)`
- [ ] 测试：memory GetUserByName 命中/未命中
- [ ] `go test ./internal/core/identity/... -race` 绿

### Task 2: auth 包（JWT + 密码哈希）

**Files:**
- Create: `internal/core/auth/jwt.go` + `jwt_test.go`
- Create: `internal/core/auth/password.go` + `password_test.go`

**Interfaces:**
- Produces: `auth.Claims{Sub,Tenant,Roles,Typ,Exp,Iat}`；`Sign(claims,secret)(string,error)`；`Parse(token,secret)(*Claims,error)`；`HashPassword(plain)(string,error)`；`CheckPassword(hash,plain)bool`
- Consumes: `golang.org/x/crypto/bcrypt`（先 `go get`）

- [ ] `go get golang.org/x/crypto/bcrypt` + tidy
- [ ] jwt.go：header `{"alg":"HS256","typ":"JWT"}`；`base64url(header).base64url(payload).base64url(hmac-sha256)`；Parse 验签（hmac.Equal）+ 验 exp
- [ ] jwt_test.go：Sign→Parse 往返；篡改 payload 验签失败；过期 token 返 `ErrTokenExpired`
- [ ] password.go：HashPassword bcrypt cost=10；CheckPassword 比对
- [ ] password_test.go：Hash→Check true；错密码 false；不同 hash 不同
- [ ] `go test ./internal/core/auth/... -race` 绿

### Task 3: gateway BearerAuth 双通道中间件

**Files:**
- Create: `internal/core/gateway/bearer.go` + `bearer_test.go`
- Consumes: `identity.Repository`，`auth.Parse`（避免循环：bearer.go 只调 ctx 注入，JWT 解析在 auth handler 或 bearer.go 内联最小校验——**决策**：bearer.go import auth 包，gateway→auth 单向，auth 不 import gateway，无循环）

**Interfaces:**
- Produces: `gateway.BearerAuth(idb) http.Handler` 中间件（token 含 `.`→JWT，否则→APIKey）

- [ ] bearer.go：取 Bearer；`strings.Contains(token,".")` 分发；JWT 路径 `auth.Parse`→验 typ=access→注入 ctx（复用 `tenant.WithTenant`/`WithRoles`/`WithUserID`）；APIKey 路径走原 LookupAPIKey 逻辑（抽 `apiKeyAuth` 内部函数复用）；失败 401 `{"error":"..."}`
- [ ] 把现有 `auth.go::APIKeyAuth` 内部 lookup 逻辑抽成 `resolveAPIKey(ctx,idb,key)` 供 bearer 复用（APIKeyAuth 保持公开签名不变）
- [ ] bearer_test.go：JWT 通过、APIKey 通过、错 token 401、过期 401
- [ ] `go vet ./internal/core/gateway/...` + test 绿

### Task 4: auth handler（5 端点）

**Files:**
- Create: `internal/core/auth/handler.go` + `handler_test.go`
- Consumes: `identity.Repository`，`auth.Sign/Parse/HashPassword/CheckPassword`
- Produces: `auth.NewHandler(idb, secret) http.Handler`（composite 分发）；端点见 spec 表

- [ ] handler.go：`login`（GetUserByName→CheckPassword→Status 检查→签 access 15m + refresh 7d）；`refresh`（Parse refresh→签新对）；`logout`（返回 `{}`，无状态）；`me`（ctx 取 userID/tenant→GetUser→映射 UserProfile，IsAdmin→roles=[super_admin]/perms=[*]）；`menus`（静态菜单切片）
- [ ] handler_test.go：login 成功返 accessToken；错密码 401；disabled 401；me 映射 super_admin；menus 非空（用 httptest + fake idb）
- [ ] `go test ./internal/core/auth/... -race` 绿

### Task 5: seed + main.go 接线 + OpenAPI 登记

**Files:**
- Modify: `cmd/core/seed.go`（admin 用户 bcrypt 哈希，幂等）
- Modify: `cmd/core/main.go`（auth 改 BearerAuth；挂 auth handler；reg.Operation 登记 5 端点）
- Maybe Modify: `cmd/core/persistence.go`（seed 路径传 secret，如需）

- [ ] seed.go：`seedIdentity` 加 admin 用户（name=admin, HashPassword("123456"), t-acme, [tenant-admin], IsAdmin=true, status=active）；内存 + PG 两路径都生效（PG 路径 seed 用 raw store 写入，见 persistence.go seedPGAllIfEmpty）
- [ ] main.go:152 `auth := gateway.APIKeyAuth` → `gateway.BearerAuth(stores.Identity)`
- [ ] main.go 挂路由：`/api/auth/sessions`（POST 公开）、`/api/auth/tokens/refresh`（POST 公开）、`/api/auth/sessions`（DELETE BearerAuth）、`/api/auth/users/me`（GET BearerAuth）、`/api/system/menus`（GET BearerAuth）；composite 分发
- [ ] reg.Operation 登记 5 端点（WithReqBody for login/refresh）
- [ ] JWT secret：`PAAS_JWT_SECRET` env，空则 `crypto/rand` 生成 32 字节 + 日志警告
- [ ] `make build` + 启动手动 curl 验证（login→me→JWT 调 /api/applications）
- [ ] `go test ./... -race` 全绿

---

## 前端

### Task 6: env 统一 + MSW 开关修复 + vite proxy

**Files:**
- Modify: `frontend/console-admin/src/app/main.ts`（enableMock 条件）
- Modify: `frontend/console-admin/src/lib/http/env.d.ts`（统一 VITE_ENABLE_MOCK）
- Modify: `frontend/console-admin/vite.config.ts`（server.proxy）
- Create: `frontend/console-admin/.env.development`

- [ ] main.ts:59 `enableMock = import.meta.env.VITE_ENABLE_MOCK === 'true'`
- [ ] env.d.ts 删 VITE_USE_MOCK，保留 VITE_API_BASE_URL + VITE_ENABLE_MOCK
- [ ] .env.development：`VITE_API_BASE_URL=` + `VITE_ENABLE_MOCK=false`
- [ ] vite.config.ts server.proxy：`/api`→`http://localhost:8080`，`/v1`→同（changeOrigin true）
- [ ] `pnpm dev` 启动无报错，请求不再被 MSW 拦

### Task 7: 拦截器适配 core 格式 + token key

**Files:**
- Modify: `frontend/console-admin/src/lib/http/interceptors.ts`
- Modify: `frontend/console-admin/src/lib/http/problem.ts`（core `{error}` → ProblemDetail）
- Modify: `frontend/console-admin/src/lib/auth/TokenStorage.ts`（key paas:*）
- Test: `frontend/console-admin/src/lib/http/interceptors.test.ts`（补 core 格式用例）

- [ ] interceptors 成功分支：对象且有 `data` 字段→无 `code` 或 `code===0` 解包 `data`
- [ ] 失败分支：body `{error:msg}`→ProblemDetail `{title:msg,detail:msg,status}`
- [ ] token key va:*→paas:*（TokenStorage 常量）
- [ ] interceptors.test.ts：core `{data:{a:1}}` 解包为 `{a:1}`；`{error:"x"}`+401→HttpError
- [ ] `pnpm test` 绿

### Task 8: 登录页默认值 + 端到端验证

**Files:**
- Modify: `frontend/console-admin/src/modules/auth/views/Login.vue`（dev 默认填 admin/123456）
- Manual: 端到端

- [ ] Login.vue dev 环境默认填 admin/123456 + 文案改 PaaS
- [ ] **端到端验收**（spec 验收标准 1-7 逐条）：
  - core 启动 + admin/123456 登录
  - me 返回 super_admin
  - JWT 调 /api/applications 通过
  - 错密码 401；API Key（sk-acme-admin）仍工作
  - admin:5173 浏览器登录成功进 dashboard
  - PG 路径验证
  - /openapi.json + /docs
- [ ] CHANGELOG + CLAUDE.md 同步

---

## 自检

- **Spec 覆盖**：8 个任务覆盖 spec 全部决策 D1-D8 + 后端 5 组件 + 前端 3 改造。
- **类型一致**：`User` 三字段在 model/repo/memory/pg/seed 全对齐；`auth.Claims`/`Sign`/`Parse` 签名一致；`BearerAuth` 与 `APIKeyAuth` 注入同一 ctx 键。
- **无占位符**：每任务有具体文件+步骤+测试。
