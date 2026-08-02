# console-user 生产级登录会话设计

> 日期：2026-08-02
> 范围：P0 生产可用第一步——给用户控制台（console-user）补齐生产级身份与会话能力，废弃"localStorage 明文 API Key + 默认管理员"的裸奔模式。
> 交付层级：L1（红线）+ L2（加固），L3（企业级 MFA/SSO）明确延后。

## 1. 背景与目标

### 问题现状
console-user 当前无任何登录与会话机制：
- 无 `/login` 页、无路由守卫（`router.ts` 无 `beforeEach`、无 Login.vue）。
- `api.ts` 把 API Key 明文存 `localStorage`（`paas.apiKey`），默认值 `sk-acme-admin`。
- 打开 `paas.k8s.dd/console/` 即自动以 Acme **管理员**身份进入；`sk-acme-admin` 是开源仓公开的演示 Key。

后果：任何能访问该 URL 的人默认是 Acme 管理员，能查看全部应用、删除生产工作负载。**这层不补，再多真实 Pod 也是裸奔**，不构成生产可用平台。

### 目标
让 console-user 走"用户名 + 密码 → httpOnly cookie 会话"的标准 Web 登录模型，达到生产可用（L1+L2），同时保留 API Key 体系供程序化调用（`/dp/`、SDK、curl）。

### 非目标（延后到 L3）
- MFA、OIDC/SSO 集成、主动 session 撤销黑名单、密码重置流程、账号锁定策略、refresh token rotation。
- 多副本下的限流/会话状态共享（需 Redis）。

## 2. 现状（可复用资产）

后端身份能力**已就绪**，本期主要补 cookie 通道 + 限流 + 审计 + 前端登录页：

| 资产 | 位置 | 说明 |
|------|------|------|
| JWT 签发/校验 | `internal/core/auth/jwt.go` | HMAC-SHA256 零依赖标准库；Claims{Sub,Tenant,Roles,Typ,Exp,Iat}；AccessTTL=15min、RefreshTTL=7d |
| auth handler | `internal/core/auth/handler.go` | Login/Refresh/Logout/Me/Menus 已实现 |
| 双通道鉴权 | `internal/core/gateway/bearer.go` | `BearerAuth` 已支持 JWT（含 `.`）+ API Key（无 `.`），注入同一套 ctx（tenant/roles/userID） |
| 路由挂载 | `cmd/core/main.go` | `/api/auth/sessions`、`/api/auth/tokens/refresh`、`/api/auth/sessions`(DELETE)、`/api/auth/users/me`、`/api/system/menus` 已注册 |
| JWT secret | `cmd/core/main.go resolveJWTSecret` | `PAAS_JWT_SECRET` 配置；**现状空则随机生成（重启失效），本期改为生产强制** |

### 现状关键约束
- 可登录密码账号**仅 `admin/123456`**（super_admin，IsAdmin，跨租户平台超管）。
- 3 个演示 API Key（`sk-acme-admin`/`sk-globex-admin`/`sk-acme-dev`）关联的 userID（`u-acme-admin`/`u-globex-admin`/`u-acme-dev`）**无对应 identity.User 行**，仅是 Key 的 userID 字段。
- console-admin（后台）已有完整密码登录 + JWT；console-user（用户实际使用）没有。

## 3. 设计

### 3.1 后端：httpOnly cookie 会话

**cookie 签发**（`auth/handler.go` 的 Login/Refresh 成功后）：

| cookie | 属性 | 用途 |
|--------|------|------|
| `paas_access` | HttpOnly; Secure(可配); SameSite=Lax; Path=/; MaxAge=900 | 所有 `/api/*` 请求携带，15min |
| `paas_refresh` | HttpOnly; Secure(可配); SameSite=Lax; Path=/api/auth; MaxAge=604800 | 仅刷新端点携带，收窄暴露面，7d |

- login/refresh 响应**同时**设 cookie 与返回 body（`AuthResult` 字段不变）——向后兼容 console-admin 现有 `authService`（它从 body 读 token 存 storage）。**console-user 前端只消费 cookie，不读 response body 的 token**，因此 console-user 侧 XSS 无法拿到 token（httpOnly cookie 不可被 JS 读）。
- 两个前端两种模式共存，后端一处实现：console-admin 保持现状（body token + localStorage，其风险窗口本期不处理，后续统一改 cookie）；console-user 走纯 cookie。这是渐进式收敛，不阻塞本期落地。
- `Secure` 由 env `PAAS_COOKIE_SECURE` 控制（默认 `false`，适配当前 HTTP 部署）；未来 ingress 配 TLS 后设 `true`。
- core 同域 serve 前端（`/console/*` 与 `/api/*` 同源 `paas.k8s.dd`），cookie 同源，无 CORS / 跨域 credentials 问题。

**BearerAuth 升级三通道**（`gateway/bearer.go`），按优先级：
1. cookie `paas_access`（有则解析 JWT access）
2. `Authorization: Bearer <JWT>`（含 `.`，admin 后台/显式 Bearer 调用）
3. `Authorization: Bearer <APIKey>`（无 `.`，程序化调用）

任一成功即注入 `(tenant, roles, userID)` ctx，下游 handler 零感知来源；全部失败 401。API Key 通道**保留不变**，`/dp/`、paas-registry SDK、curl 调用不受影响。

**refresh 端点改读 cookie**：`POST /api/auth/tokens/refresh` 从 `paas_refresh` cookie 读 refresh token，不再从请求体读（请求体字段保留兼容，cookie 优先）。

**登出**：`DELETE /api/auth/sessions` 设两个 cookie 过期（MaxAge<0）。

### 3.2 后端：安全加固（L1+L2）

**JWT secret 强制配置**（`cmd/core/main.go resolveJWTSecret`）：
- `PAAS_JWT_SECRET` 非空 → 用之。
- 空 + 非 dev 模式 → **拒绝启动**（log fatal）。
- 空 + dev 模式（`PAAS_DEV=true` 或等效）→ 允许随机生成并告警（保持本地零配置启动）。

**登录限流**（新增 `internal/core/auth/ratelimit.go`，`auth/handler.go` Login 调用）：
- 内存令牌桶，双维度：per-IP + per-username。
- 失败 5 次/5min → 锁该维度 15min，锁内拒绝并返 429。
- 锁与计数存内存 `map`（单 core 实例；多副本需 Redis，延后）。
- IP 取 `X-Forwarded-For` 首段（ingress 注入），退化 `RemoteAddr`。

**登录审计**（`auth/handler.go` Login/Logout，复用 `security.RecordAudit`）：
- 成功：`action=login`，actor=userID，detail=`{ip,ua}`。
- 失败：`action=login_failed`，actor=请求 username（不存在也记），detail=`{ip,reason}`。
- 登出：`action=logout`。
- ip/ua 从请求头提取。

**强密码策略**（`identity` 校验，`CreateUser`/改密码路径）：
- 规则：长度 ≥ 8 + 至少一个字母 + 至少一个数字。
- seed demo 账号豁免（由 `PAAS_DISABLE_DEMO_SEED` 门控，生产关闭后无弱密码账号）。

**安全 headers 中间件**（新增 `cmd/core` 全局 middleware，包 mux 最外层之一）：
- `Content-Security-Policy`：默认 `default-src 'self'`；`/docs` 的 Scalar CDN 白名单（与现有 Scalar 文档一致）。
- `Strict-Transport-Security`：`max-age=31536000`——仅 HTTPS 部署发送（浏览器仅在 HTTPS 尊重 HSTS）；当前 HTTP 部署跳过，配 TLS 后启用。
- `X-Frame-Options: DENY`。
- `X-Content-Type-Options: nosniff`。
- `Referrer-Policy: strict-origin-when-cross-origin`。

### 3.3 seed：补租户密码账号（`cmd/core/seed.go`）

`seedIdentity` 在 `!demoDisabled` 分支内补 3 个 identity.User（与现有 admin/123456 同批，幂等：`GetUserByName` 已存在则跳过）：

| ID | Name | TenantID | 密码 | 角色 | IsAdmin |
|----|------|----------|------|------|---------|
| u-acme-admin | acme-admin | t-acme | 123456 | tenant-admin | false |
| u-acme-dev | acme-dev | t-acme | 123456 | developer | false |
| u-globex-admin | globex-admin | t-globex | 123456 | tenant-admin | false |

- userID 与现有 3 个 API Key 的 `UserID` 字段对齐（登录后 JWT 的 `Sub` 与 API Key 的 `UserID` 指向同一用户，权限语义一致）。
- `admin/123456`（super_admin）保留不变，供 console-admin 后台登录。
- 生产 `PAAS_DISABLE_DEMO_SEED=true` → 这 3 账号 + admin/123456 + sk-* Key 全部不灌，console-user 必须用 admin 后台开通的真实账号登录。

### 3.4 前端：console-user 登录改造

**`api.ts` 改造**：
- `fetchAuth` 增加 `credentials: 'include'`，**完全不再读/写 token**（移除 `localStorage` 的 `paas.apiKey`、`DEFAULT_KEY`、`setApiKey`、`auth.key`）。
- Authorization header 不再由前端注入（cookie 自动携带）。
- 401 响应：调 `POST /api/auth/tokens/refresh`（cookie 自动带 refresh）→ 成功则重试原请求一次 → 仍 401 则清状态跳 `/login?redirect=<当前路径>`。
- 并发 refresh 复用同一 promise（防多请求并发触发多次 refresh）。

**新增 `/login` 路由 + `Login.vue`**：
- 用户名/密码表单，提交 `POST /api/auth/sessions`。
- dev 预填 `acme-admin/123456`（`import.meta.env.DEV`），生产留空。
- 成功 → 跳 `route.query.redirect` 或 `/applications`。
- 登录失败显示后端文案（`用户名或密码错误` / `账号已禁用` / 限流提示）。

**路由守卫**（`router.ts` 加 `beforeEach`）：
- 会话探测复用 `GET /api/auth/users/me`（不新增端点，KISS）：首次进入或无缓存 profile 时 ping，200=已登录、401=跳 `/login?redirect=`。
- 探测结果缓存到 pinia store（避免每次路由切换都 ping）。
- `/login` 已登录访问 → 跳 `/applications`。

**顶栏改造**（`App.vue` / 布局组件）：
- 展示：当前用户名（来自 me）+ 退出按钮（`DELETE /api/auth/sessions` → 跳 /login）。
- 演示账号快切下拉（dev/demo 模式）：点 `acme-admin`/`acme-dev`/`globex-admin` → 后台 `POST /api/auth/sessions {username,password}` → 后端 Set-Cookie → 跳 `/applications`。本质是"预设账号一键登录"，每次切换都是真实登录态。
- 生产关 demo（无预设账号）后，快切器隐藏。
- **移除原"切换 API Key"交互**（PRESET_KEYS 下拉）。

**`/settings/api-keys` 保留**：用户在此管理自己的程序化 API Key（curl/SDK 用），与登录态正交。

### 3.5 部署配置

| 配置 | 值 | 说明 |
|------|-----|------|
| ingress TLS | **本期不配（用户确认先 HTTP）** | 后续补；HTTP 下 cookie 明文传输，权衡见 §5 自决点 4 |
| `PAAS_JWT_SECRET` | 随机 ≥32 字节 | 生产必配，空则拒启 |
| `PAAS_COOKIE_SECURE` | 默认 `false`（适配 HTTP） | 配 TLS 后设 `true` |
| `PAAS_DISABLE_DEMO_SEED` | `true`（chart `seed.demo=false`） | 生产关闭演示凭证 |

Helm `deploy/charts/paas/values.yaml` 增 `auth.jwtSecret` + `auth.cookieSecure` 字段，core-deployment 注入对应 env；ingress 模板支持 TLS 配置。

## 4. 安全清单（落实情况）

| 项 | 层级 | 本期落实 |
|----|------|---------|
| token 不进 JS 可读存储 | L1 | httpOnly cookie |
| TLS/HTTPS | L1 | **延后（用户确认先 HTTP）**：HTTP 下 cookie 网络明文，配 TLS 后闭环 |
| 登录限流 | L1 | per-IP+per-user 内存令牌桶 |
| 演示凭证门控 | L1 | `PAAS_DISABLE_DEMO_SEED` |
| JWT secret 强制配置 | L1 | 空则拒启（非 dev） |
| 登录审计 | L2 | security.AuditLog |
| 强密码策略 | L2 | CreateUser 校验 |
| 安全 headers | L2 | CSP/HSTS/X-Frame/X-Content-Type |
| refresh token rotation | L3 | **延后**（见 §5） |
| MFA / OIDC·SSO / 主动撤销 | L3 | 延后 |

## 5. 自决点与理由

1. **refresh token rotation 不做**：httpOnly cookie 使 JS 读不到 refresh token，被窃风险已大幅降低；rotation 需服务端存储 jti/黑名单，引入有状态。本期靠 httpOnly + 短 access TTL(15min) + refresh 走 DB 校验用户状态（Refresh handler 已 `GetUser` 校验 active）即达生产标准。延后到 L3。
2. **会话探测复用 `/api/auth/users/me`**：不新增端点，守卫 ping me 判登录态，结果缓存到 pinia store 避免重复请求。
3. **限流用内存而非 Redis**：单 core 实例部署够用；多副本场景上 Redis 延后（与限流同构的其他内存状态如 observability 一致）。
4. **ingress 本期不配 TLS（用户确认）**：当前 HTTP 部署，cookie（含 access/refresh token）在网络明文传输，存在被中间人嗅探风险。代码层安全设计（httpOnly cookie / 限流 / 审计 / 强密码 / 安全 headers 除 HSTS）本期全部落地；TLS 作为部署层加固后续补——配证书后将 `PAAS_COOKIE_SECURE=true` + 启用 HSTS 即闭环。这是用户明确接受的阶段性权衡，不阻塞代码层落地。

## 6. 测试策略

- **后端单测**：
  - cookie 签发属性（HttpOnly/Secure/SameSite/Path/MaxAge）正确。
  - BearerAuth 三通道优先级（cookie > JWT header > APIKey header；全失败 401）。
  - 限流：5 次失败后第 6 次返 429；锁定窗口过后恢复。
  - refresh 从 cookie 读（无 cookie 401）。
  - 强密码策略（弱密码拒、demo 豁免）。
  - secret 空 + 非 dev → 启动失败。
- **前端**：守卫未登录跳 /login + redirect 回跳；401 自动 refresh 重试；refresh 失败跳 /login。
- **端到端**（集群）：浏览器登录 acme-admin → cookie 下发 → /api/applications 返 t-acme 数据 → 切 globex-admin → 返 t-globex 数据；退出后 cookie 清除、访问 /api 返 401。

## 7. 范围边界（本期不做）

- L3 企业级：MFA、OIDC/SSO、主动 session 撤销黑名单、密码重置、账号锁定、refresh rotation。
- 多副本共享限流/会话状态（Redis）。
- 租户/用户真实开通 CRUD（admin 后台，P1 范围；本期仅 seed 演示账号保证可登录）。
- API Key 体系本身的改造（保留现状，仅前端不再用作登录态）。
- ingress TLS（本期不配，用户确认先 HTTP；权衡见 §5 自决点 4）。
