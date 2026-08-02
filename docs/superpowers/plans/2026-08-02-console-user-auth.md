# console-user 生产级登录会话 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 console-user 补齐生产级身份会话（httpOnly cookie + 限流 + 审计 + 强密码 + 安全 headers），废弃 localStorage 明文 API Key 裸奔模式。

**Architecture:** 后端已就绪 JWT 双通道（`internal/core/auth` + `gateway.BearerAuth`），本计划在其上增 cookie 通道（三通道）+ 安全加固（L1+L2）；console-user 前端改 `credentials:include` 不碰 token + 登录页/守卫/401 自动 refresh。API Key 体系保留供程序化调用。

**Tech Stack:** Go（net/http，零新增依赖）+ Vue 3 + Element Plus + Vite。bcrypt 密码、HMAC-SHA256 JWT（均已实现）。

## Global Constraints

- **主语言 Go**，零新增外部依赖（限流/cookie/headers 全用标准库）。
- **注释语言**：中文，与代码库现有注释一致。
- **License**：所有依赖 Apache 2.0 兼容；本计划不引新依赖。
- **多租户隔离**：cookie/JWT 注入的 tenant ctx 与 API Key 通道同源，下游 handler 零感知来源。
- **migration**：已合并为单一 `0001_init`（commit a620456）；auth 不加新表（复用 `users` + `audit_logs`，限流走内存）。
- **cookie Secure**：默认 `false`（适配当前 HTTP 部署），`PAAS_COOKIE_SECURE=true` 切 HTTPS 模式。
- **演示门控**：所有 seed 演示账号受 `PAAS_DISABLE_DEMO_SEED=true` 控制。
- **不引 git 操作**：每个 task 末尾的 commit 由执行者按本项目惯例（main 直接提交）执行；未经用户确认不 push。
- **测试命令**：后端单测 `go test ./internal/core/auth/... ./internal/core/gateway/... -run <Test> -v`；全量 `make test`（内存后端零依赖）；前端无强制单测框架，手测脚本在各 task 给出。

---

## File Structure

**后端（Go）：**
- Modify `cmd/core/seed.go` — 补 3 租户密码账号
- Modify `cmd/core/main.go` — resolveJWTSecret 强制 + cookieSecure env + auth.NewHandler 传参 + 安全 headers 中间件挂载
- Create `internal/core/auth/cookie.go` — cookie 签发/清除辅助
- Modify `internal/core/auth/handler.go` — Login/Refresh 设 cookie；Logout 清 cookie；Refresh 从 cookie 读；接入限流 + 审计
- Modify `internal/core/auth/jwt.go` 或 `handler.go` — `NewHandler` 加 `cookieSecure bool` 字段
- Create `internal/core/auth/ratelimit.go` — per-IP + per-username 内存限流器
- Create `internal/core/auth/password.go` — 强密码校验（若 `CheckPassword`/`HashPassword` 已在某文件，就近放）
- Modify `internal/core/identity/`（User 校验路径或 handler 层）— 改密码时调强密码校验
- Modify `internal/core/gateway/bearer.go` — 加 cookie 通道（三通道）
- Create `cmd/core/middleware.go`（或并入现有 mux 装配处）— 安全 headers 中间件
- Test: `internal/core/auth/*_test.go`、`internal/core/gateway/bearer_test.go`（若不存在则建）

**前端（console-user）：**
- Modify `frontend/console-user/src/api.ts` — `credentials:include`，移除 localStorage Key，401 自动 refresh
- Create `frontend/console-user/src/views/Login.vue` — 登录页
- Modify `frontend/console-user/src/router.ts` — `/login` 路由 + `beforeEach` 守卫
- Create `frontend/console-user/src/stores/session.ts` — pinia session store（缓存 me + 登录/退出）
- Modify `frontend/console-user/src/App.vue`（或顶栏组件）— 用户名/退出/演示快切，移除 API Key 切换器

**部署：**
- Modify `deploy/charts/paas/values.yaml` + `core-deployment.yaml` — `auth.jwtSecret` + `auth.cookieSecure` env 注入

---

## Task 1: seed 补租户密码账号

**Files:**
- Modify: `cmd/core/seed.go`（`seedIdentity` 的 `!demoDisabled` 分支）

**Interfaces:**
- Produces: identity.User 行 `u-acme-admin`(acme-admin/123456/t-acme/tenant-admin) / `u-acme-dev`(acme-dev/123456/t-acme/developer) / `u-globex-admin`(globex-admin/123456/t-globex/tenant-admin)，与现有 3 API Key 的 UserID 对齐。

- [ ] **Step 1: 写失败测试**

`cmd/core/seed_test.go`（若不存在新建；若已存在追加）：

```go
package main

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/core/identity"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
)

func TestSeedIdentity_TenantPasswordAccounts(t *testing.T) {
	idb := idmemory.NewStore()
	seedIdentity(idb, "")

	cases := []struct{ name, tenant, wantRole string }{
		{"acme-admin", "t-acme", "tenant-admin"},
		{"acme-dev", "t-acme", "developer"},
		{"globex-admin", "t-globex", "tenant-admin"},
	}
	for _, c := range cases {
		u, err := idb.GetUserByName(context.Background(), c.name)
		if err != nil {
			t.Fatalf("seed 未建用户 %s: %v", c.name, err)
		}
		if u.TenantID != c.tenant {
			t.Errorf("%s 租户错: got %s want %s", c.name, u.TenantID, c.tenant)
		}
		// 校验密码可验过（seed 用 123456）
		if !checkPasswordForTest(u.PasswordHash, "123456") {
			t.Errorf("%s 密码哈希不可验", c.name)
		}
		// 角色含期望
		if !containsRole(u.Roles, c.wantRole) {
			t.Errorf("%s 角色缺 %s: %v", c.name, c.wantRole, u.Roles)
		}
	}
	_ = identity.StatusActive // 引用包
}

func containsRole(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}
// checkPasswordForTest 复用 auth.CheckPassword（在 auth 包）；测试内薄封装避免循环引用：
// 直接调 auth.CheckPassword(u.PasswordHash, "123456")，见 Step 3 实现后。
```

> 注：`checkPasswordForTest` 在 Step 3 实现后改为直接调 `auth.CheckPassword`（已存在 `internal/core/auth`）。测试先写、先红。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./cmd/core/ -run TestSeedIdentity_TenantPasswordAccounts -v
```
Expected: FAIL（用户不存在 / GetUserByName 报错）。

- [ ] **Step 3: 实现 — seed.go 补 3 账号**

在 `seedIdentity` 的 `if !demoDisabled {` 块内，紧跟现有 admin/123456 建号逻辑之后，加（结构与 admin 一致：幂等 GetUserByName 判空 + bcrypt）：

```go
// 租户密码登录账号（与 3 演示 API Key 的 UserID 对齐，供 console-user 登录）。
for _, tu := range []struct {
	id, name, tenant, role string
}{
	{"u-acme-admin", "acme-admin", "t-acme", "tenant-admin"},
	{"u-acme-dev", "acme-dev", "t-acme", "developer"},
	{"u-globex-admin", "globex-admin", "t-globex", "tenant-admin"},
} {
	if _, err := idb.GetUserByName(ctx, tu.name); err != nil {
		hash, hErr := auth.HashPassword("123456")
		if hErr != nil {
			log.Printf("[seed] hash %s 密码失败: %v", tu.name, hErr)
			continue
		}
		if err := idb.CreateUser(ctx, identity.User{
			ID: tu.id, TenantID: tu.tenant, Name: tu.name,
			PasswordHash: hash, Roles: []string{tu.role}, Status: identity.StatusActive,
		}); err != nil {
			log.Printf("[seed] %v", err)
		}
	}
}
```

把测试里的 `checkPasswordForTest` 改为直接调 `auth.CheckPassword(u.PasswordHash, "123456")`，删除占位辅助。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./cmd/core/ -run TestSeedIdentity_TenantPasswordAccounts -v
```
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add cmd/core/seed.go cmd/core/seed_test.go
git commit -m "feat(seed): 补 console-user 租户密码登录账号（acme-admin/dev/globex-admin）"
```

---

## Task 2: JWT secret 生产强制配置

**Files:**
- Modify: `cmd/core/main.go`（`resolveJWTSecret` 函数）

**Interfaces:**
- Produces: 空 `PAAS_JWT_SECRET` + 非 dev → `log.Fatal`。

- [ ] **Step 1: 写失败测试**

`cmd/core/main_test.go`（追加）：

```go
package main

import (
	"os"
	"strings"
	"testing"
)

// 捕获 log.Fatal 不可行（os.Exit），故测可调用的判定函数。
func TestResolveJWTSecret_ProductionRejectsEmpty(t *testing.T) {
	os.Unsetenv("PAAS_JWT_SECRET")
	os.Unsetenv("PAAS_DEV")

	_, err := resolveJWTSecretOrErr()
	if err == nil {
		t.Fatal("生产模式空 secret 应报错")
	}
	if !strings.Contains(err.Error(), "PAAS_JWT_SECRET") {
		t.Fatalf("错误文案应含 PAAS_JWT_SECRET，got: %v", err)
	}
}

func TestResolveJWTSecret_DevAllowsRandom(t *testing.T) {
	os.Unsetenv("PAAS_JWT_SECRET")
	os.Setenv("PAAS_DEV", "true")
	defer os.Unsetenv("PAAS_DEV")

	s, err := resolveJWTSecretOrErr()
	if err != nil {
		t.Fatalf("dev 模式空 secret 应允许随机，got err: %v", err)
	}
	if s == "" {
		t.Fatal("dev 模式应生成非空 secret")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./cmd/core/ -run TestResolveJWTSecret -v
```
Expected: FAIL（`resolveJWTSecretOrErr` 未定义）。

- [ ] **Step 3: 实现 — 拆分为可测函数**

`cmd/core/main.go` 改造 `resolveJWTSecret`：

```go
// resolveJWTSecretOrErr 返回 JWT secret 与可能的配置错误（可测）。
// 生产（PAAS_DEV 未设）空 secret → 报错；dev 空则随机生成。
func resolveJWTSecretOrErr() (string, error) {
	if s := os.Getenv("PAAS_JWT_SECRET"); s != "" {
		return s, nil
	}
	if envEnabled("PAAS_DEV") {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("生成 JWT secret 失败: %w", err)
		}
		log.Printf("[auth] PAAS_JWT_SECRET 未配置，dev 模式随机生成（生产请配置）")
		return base64.StdEncoding.EncodeToString(b), nil
	}
	return "", fmt.Errorf("PAAS_JWT_SECRET 未配置：生产环境必须显式设置（≥32 字节随机串）")
}

// resolveJWTSecret 保留原签名：Fatal 包装（main 启动路径用）。
func resolveJWTSecret() string {
	s, err := resolveJWTSecretOrErr()
	if err != nil {
		log.Fatalf("[auth] %v", err)
	}
	return s
}
```

main.go 顶部 import 补 `"crypto/rand"`、`"encoding/base64"`（若未有）。`envEnabled` 已存在（persistence.go）。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./cmd/core/ -run TestResolveJWTSecret -v
```
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add cmd/core/main.go cmd/core/main_test.go
git commit -m "feat(auth): PAAS_JWT_SECRET 生产强制配置（空+非dev 拒启）"
```

---

## Task 3: httpOnly cookie 签发/清除辅助

**Files:**
- Create: `internal/core/auth/cookie.go`
- Modify: `internal/core/auth/handler.go`（Handler 加 `cookieSecure bool`，`NewHandler` 加参）

**Interfaces:**
- Produces: `setSessionCookies(w, access, refresh string, secure bool)`、`clearSessionCookies(w, secure bool)`、`refreshFromCookie(r) (string, error)`；`NewHandler(idb, secret string, cookieSecure bool)`。
- Consumes: `Sign`/`ParseType`（已有）。

- [ ] **Step 1: 写失败测试**

`internal/core/auth/cookie_test.go`：

```go
package auth

import (
	"net/http/httptest"
	"testing"
)

func TestSetSessionCookies_Attributes(t *testing.T) {
	rec := httptest.NewRecorder()
	setSessionCookies(rec, "access.jwt", "refresh.jwt", false)

	a := findCookie(rec.Result().Cookies(), "paas_access")
	if a == nil {
		t.Fatal("缺 paas_access cookie")
	}
	if !a.HttpOnly {
		t.Error("paas_access 必须 HttpOnly")
	}
	if a.Secure {
		t.Error("secure=false 时 Secure 应为 false")
	}
	if a.SameSite != http.SameSiteLaxMode {
		t.Error("应 SameSite=Lax")
	}
	if a.Path != "/" {
		t.Error("paas_access Path 应为 /")
	}

	rf := findCookie(rec.Result().Cookies(), "paas_refresh")
	if rf == nil || rf.Path != "/api/auth" {
		t.Error("paas_refresh Path 应限定 /api/auth")
	}
}

func TestClearSessionCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	clearSessionCookies(rec, false)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge != -1 && c.Value != "" {
			t.Errorf("cookie %s 未过期清除", c.Name)
		}
	}
}

func findCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./internal/core/auth/ -run TestSetSessionCookies -v
```
Expected: FAIL（函数未定义）。

- [ ] **Step 3: 实现 cookie.go**

```go
package auth

import (
	"errors"
	"net/http"
)

const (
	accessCookieName  = "paas_access"
	refreshCookieName = "paas_refresh"
)

// setSessionCookies 签发 access + refresh 两个 httpOnly cookie。
// access: Path=/（所有 /api/* 携带）；refresh: Path=/api/auth（收窄暴露面）。
// secure 由 PAAS_COOKIE_SECURE 控制（HTTP 部署需 false 否则浏览器拒收）。
func setSessionCookies(w http.ResponseWriter, access, refresh string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: accessCookieName, Value: access, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(AccessTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: refresh, Path: "/api/auth",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(RefreshTTL.Seconds()),
	})
}

// clearSessionCookies 设过期清除两个 cookie（登出用）。
func clearSessionCookies(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: accessCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: refreshCookieName, Path: "/api/auth", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

// refreshFromCookie 从请求读 refresh cookie；无则返错误。
func refreshFromCookie(r *http.Request) (string, error) {
	c, err := r.Cookie(refreshCookieName)
	if err != nil || c.Value == "" {
		return "", errors.New("missing refresh cookie")
	}
	return c.Value, nil
}
```

`handler.go`：Handler 加字段 `cookieSecure bool`，`NewHandler` 改签名加参：

```go
type Handler struct {
	idb          identity.Repository
	secret       string
	cookieSecure bool
}

func NewHandler(idb identity.Repository, secret string, cookieSecure bool) *Handler {
	return &Handler{idb: idb, secret: secret, cookieSecure: cookieSecure}
}
```

（Login/Refresh/Logout 在 Task 5 接入 cookie 调用。）

同步改 `cmd/core/main.go` 调用点：`authPkg.NewHandler(stores.Identity, jwtSecret, envEnabled("PAAS_COOKIE_SECURE"))`（注意 cookieSecure 默认 false，env PAAS_COOKIE_SECURE=true 才 true）。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/core/auth/ -run TestSetSessionCookies -v
go build ./...
```
Expected: PASS + build exit 0。

- [ ] **Step 5: Commit**

```bash
git add internal/core/auth/cookie.go internal/core/auth/cookie_test.go internal/core/auth/handler.go cmd/core/main.go
git commit -m "feat(auth): httpOnly cookie 签发/清除辅助（Path 分离 access/refresh）"
```

---

## Task 4: BearerAuth 升级三通道（cookie 优先）

**Files:**
- Modify: `internal/core/gateway/bearer.go`

**Interfaces:**
- Produces: BearerAuth 三通道优先级 cookie access > Bearer JWT > Bearer APIKey。
- Consumes: `auth.ParseType`（access cookie 解析）。

- [ ] **Step 1: 写失败测试**

`internal/core/gateway/bearer_test.go`（若不存在新建）：

```go
package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/core/auth"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
)

// 三通道优先级：cookie access 优先于 header。
func TestBearerAuth_CookieTakesPrecedence(t *testing.T) {
	secret := "test-secret"
	// 签一个 cookie 用的 access token（租户 t-cookie）
	tok, _ := auth.Sign(auth.Claims{Sub: "u1", Tenant: "t-cookie", Roles: []string{"tenant-admin"}, Typ: auth.TokenAccess, Exp: 9999999999}, secret)

	idb := idmemory.NewStore()
	called := false
	h := BearerAuth(idb, secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if tid, _ := tenantFromCtxForTest(r); tid != "t-cookie" {
			t.Errorf("cookie 通道未生效，tenant got %s want t-cookie", tid)
		}
	}))

	// 同时带 cookie 与一个错误 API Key header：cookie 应优先
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: "paas_access", Value: tok})
	req.Header.Set("Authorization", "Bearer invalid-api-key")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("handler 未被调用")
	}
}
```

> `tenantFromCtxForTest` 用 `tenant.TenantFrom(r.Context())`（在测试内直接调真实包函数，import `pkg/tenant`）。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./internal/core/gateway/ -run TestBearerAuth_Cookie -v
```
Expected: FAIL（当前 BearerAuth 不读 cookie，会走 API Key 通道解析 invalid-api-key 失败 401，handler 不被调用）。

- [ ] **Step 3: 实现 — bearer.go 加 cookie 通道**

在 `BearerAuth` 内层，`tok, err := auth.BearerToken(r)` 之前插入 cookie 优先逻辑（cookie 命中则不走 header）：

```go
return func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// 通道 1：access cookie（浏览器会话）
		if c, err := r.Cookie("paas_access"); err == nil && c.Value != "" {
			claims, err := auth.ParseType(c.Value, jwtSecret, auth.TokenAccess)
			if err == nil {
				ctx = tenant.WithTenant(ctx, claims.Tenant)
				ctx = WithRoles(ctx, claims.Roles)
				ctx = WithUserID(ctx, claims.Sub)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// cookie 有但无效 → 直接 401（不降级到 header，防混淆）
			httputil.WriteError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		// 通道 2/3：Authorization header（JWT 或 API Key）
		tok, err := auth.BearerToken(r)
		if err != nil {
			httputil.WriteError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		// ...（保留现有 strings.Contains(tok, ".") JWT/APIKey 分支不变）
```

（现有 header 分支代码原样保留，仅在外层包了 cookie 优先判断。）

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/core/gateway/ -run TestBearerAuth -v
go test ./internal/core/gateway/... 
```
Expected: PASS（含原有双通道测试不回归）。

- [ ] **Step 5: Commit**

```bash
git add internal/core/gateway/bearer.go internal/core/gateway/bearer_test.go
git commit -m "feat(gateway): BearerAuth 三通道（cookie access 优先于 JWT/APIKey header）"
```

---

## Task 5: Login/Refresh/Logout 接入 cookie

**Files:**
- Modify: `internal/core/auth/handler.go`

**Interfaces:**
- Consumes: Task 3 的 `setSessionCookies`/`clearSessionCookies`/`refreshFromCookie`。

- [ ] **Step 1: 写失败测试**

`internal/core/auth/handler_test.go`（追加；用内存 idb + 预建用户）：

```go
func TestLogin_SetsCookies(t *testing.T) {
	idb := seedTestIDB(t) // 预建 acme-admin/123456，辅助
	h := NewHandler(idb, "secret", false)

	body := strings.NewReader(`{"username":"acme-admin","password":"123456"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/sessions", body)
	h.Login(rec, req)

	if rec.Code != 200 {
		t.Fatalf("登录失败 code=%d body=%s", rec.Code, rec.Body.String())
	}
	cs := rec.Result().Cookies()
	if findCookie(cs, "paas_access") == nil || findCookie(cs, "paas_refresh") == nil {
		t.Fatal("登录成功应下发 access+refresh cookie")
	}
}

func TestRefresh_ReadsCookie(t *testing.T) {
	idb := seedTestIDB(t)
	h := NewHandler(idb, "secret", false)
	// 先签一个 refresh token
	rt, _ := Sign(Claims{Sub: "u-acme-admin", Tenant: "t-acme", Roles: []string{"tenant-admin"}, Typ: TokenRefresh, Exp: 9999999999}, "secret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/tokens/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "paas_refresh", Value: rt})
	h.Refresh(rec, req)

	if rec.Code != 200 {
		t.Fatalf("refresh 失败 code=%d", rec.Code)
	}
	if findCookie(rec.Result().Cookies(), "paas_access") == nil {
		t.Error("refresh 应重发 access cookie")
	}
}

func TestLogout_ClearsCookies(t *testing.T) {
	h := NewHandler(seedTestIDB(t), "secret", false)
	rec := httptest.NewRecorder()
	h.Logout(rec, httptest.NewRequest("DELETE", "/api/auth/sessions", nil))
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge >= 0 {
			t.Errorf("cookie %s 未清除", c.Name)
		}
	}
}
```

`seedTestIDB` 辅助：用 `idmemory.NewStore()` + `CreateUser`（密码 `HashPassword("123456")`）建 acme-admin。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./internal/core/auth/ -run "TestLogin_SetsCookies|TestRefresh_ReadsCookie|TestLogout_ClearsCookies" -v
```
Expected: FAIL（Login 当前不设 cookie；Refresh 当前从 body 读）。

- [ ] **Step 3: 实现 — handler.go 三处改**

**Login**：成功签发后，在 `writeAuthData(w, res)` 前加：
```go
setSessionCookies(w, res.AccessToken, res.RefreshToken, h.cookieSecure)
```

**Refresh**：开头改为 cookie 优先 + body 兼容：
```go
// 优先读 refresh cookie；退化读 body（兼容 SDK 显式调用）
refreshToken, _ := refreshFromCookie(r)
if refreshToken == "" {
	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
		refreshToken = req.RefreshToken
	}
}
if refreshToken == "" {
	writeAuthErr(w, http.StatusUnauthorized, "missing refresh token")
	return
}
c, err := ParseType(refreshToken, h.secret, TokenRefresh)
// ...（后续 GetUser + issueTokens + writeAuthData 不变，issueTokens 后加 setSessionCookies）
```
成功后同样 `setSessionCookies(w, res.AccessToken, res.RefreshToken, h.cookieSecure)`。

**Logout**：加 `clearSessionCookies(w, h.cookieSecure)` 在 `writeAuthData` 前。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/core/auth/ -v
```
Expected: PASS（全 auth 包测试）。

- [ ] **Step 5: Commit**

```bash
git add internal/core/auth/handler.go internal/core/auth/handler_test.go
git commit -m "feat(auth): Login/Refresh/Logout 接入 httpOnly cookie（refresh cookie 优先）"
```

---

## Task 6: 登录限流（per-IP + per-username）

**Files:**
- Create: `internal/core/auth/ratelimit.go`
- Modify: `internal/core/auth/handler.go`（Login 调限流）+ Handler 加 `limiter *loginLimiter` 字段

**Interfaces:**
- Produces: `newLoginLimiter()`、`(*loginLimiter).allow(ip, username string) (ok bool, retryAfter time.Duration)`。
- 规则：每维度失败 5 次/5min → 锁 15min；成功不计数。

- [ ] **Step 1: 写失败测试**

`internal/core/auth/ratelimit_test.go`：

```go
package auth

import (
	"time"
	"testing"
)

func TestLoginLimiter_LocksAfter5Fails(t *testing.T) {
	l := newLoginLimiter()
	// nowFn 注入可控时间（限流器内部用可替换的 clock）
	clock := newFakeClock()
	l.clock = clock

	ip, user := "1.2.3.4", "acme-admin"
	for i := 0; i < 5; i++ {
		l.recordFailure(ip, user)
	}
	// 第 5 次失败后应锁定
	if ok, _ := l.allow(ip, user); ok {
		t.Error("5 次失败后应锁定")
	}
}

func TestLoginLimiter_SuccessResetsCount(t *testing.T) {
	l := newLoginLimiter()
	l.clock = newFakeClock()
	ip, user := "1.2.3.4", "acme-admin"
	l.recordFailure(ip, user)
	l.recordSuccess(ip, user) // 成功清零
	if ok, _ := l.allow(ip, user); !ok {
		t.Error("成功后应放行")
	}
	_ = time.Now
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./internal/core/auth/ -run TestLoginLimiter -v
```
Expected: FAIL（未实现）。

- [ ] **Step 3: 实现 ratelimit.go**

```go
package auth

import (
	"sync"
	"time"
)

const (
	loginMaxFails = 5
	loginWindow   = 5 * time.Minute
	loginLockout  = 15 * time.Minute
)

// clock 抽象便于测试；生产用真实 time。
type clock interface{ now() time.Time }
type realClock struct{}
func (realClock) now() time.Time { return time.Now() }

type fakeClock struct{ t time.Time }
func (f *fakeClock) now() time.Time { return f.t }
func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1000000, 0)} }

type failEntry struct {
	count   int
	firstAt time.Time
	lockedUntil time.Time
}

type loginLimiter struct {
	mu    sync.Mutex
	clock clock
	fails map[string]*failEntry // key = ip 或 username
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{clock: realClock{}, fails: map[string]*failEntry{}}
}

// allow 检查是否允许尝试（未锁）。不消费计数。
func (l *loginLimiter) allow(ip, username string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.now()
	for _, k := range []string{ipKey(ip), userKey(username)} {
		if e, ok := l.fails[k]; ok && now.Before(e.lockedUntil) {
			return false, e.lockedUntil.Sub(now)
		}
	}
	return true, 0
}

// recordFailure 失败计数 + 触发锁定。窗口外重置。
func (l *loginLimiter) recordFailure(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.now()
	for _, k := range []string{ipKey(ip), userKey(username)} {
		e := l.fails[k]
		if e == nil || now.Sub(e.firstAt) > loginWindow {
			e = &failEntry{firstAt: now}
			l.fails[k] = e
		}
		e.count++
		if e.count >= loginMaxFails {
			e.lockedUntil = now.Add(loginLockout)
		}
	}
}

// recordSuccess 成功清零。
func (l *loginLimiter) recordSuccess(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ipKey(ip))
	delete(l.fails, userKey(username))
}

func ipKey(ip string) string      { return "ip:" + ip }
func userKey(u string) string     { return "user:" + u }
```

`handler.go`：Handler 加 `limiter *loginLimiter`（`NewHandler` 内 `h.limiter = newLoginLimiter()`）。`Login` 开头加：

```go
ip := clientIP(r)
if ok, retry := h.limiter.allow(ip, req.Username); !ok {
	writeAuthErr(w, http.StatusTooManyRequests, fmt.Sprintf("登录尝试过多，请 %d 秒后再试", int(retry.Seconds())+1))
	return
}
```

`clientIP(r)`：取 `X-Forwarded-For` 首段，退化 `RemoteAddr`。密码校验：
- 成功后 `h.limiter.recordSuccess(ip, req.Username)`；
- 失败（密码错/用户不存在）`h.limiter.recordFailure(ip, req.Username)`（在返 401 前）。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/core/auth/ -v
```
Expected: PASS。

- [ ] **Step 5: 补 handler 限流集成测试 + Commit**

追加 `TestLogin_RateLimitedAfter5Fails`（连续 5 次 123456-错密码后第 6 次返 429）。

```bash
git add internal/core/auth/ratelimit.go internal/core/auth/ratelimit_test.go internal/core/auth/handler.go internal/core/auth/handler_test.go
git commit -m "feat(auth): 登录限流 per-IP+per-username（5次/5min 锁 15min）"
```

---

## Task 7: 登录审计（AuditLog）

**Files:**
- Modify: `internal/core/auth/handler.go`（Login 成功/失败、Logout 记审计）+ Handler 加 `audit security.AuditStore` 字段（依赖倒置，可选注入）

**Interfaces:**
- Consumes: `security.RecordAudit(ctx, log)`；Handler `NewHandler` 增 `audit` 入参（cmd/core 注入 `stores.Security`）。
- 注：security.AuditLog 的 action 字段为自由文本，直接用 "login"/"login_failed"/"logout"；resource_type 用 "session"。

- [ ] **Step 1: 写失败测试**

`handler_test.go` 追加（用内存 security store 断言）：

```go
func TestLogin_RecordsAudit(t *testing.T) {
	idb := seedTestIDB(t)
	auditor := secmemory.NewStore() // internal/security/memory
	h := NewHandlerWithAudit(idb, "secret", false, auditor)

	// 成功登录
	postLogin(h, "acme-admin", "123456")
	logs, _ := auditor.ListAuditLogs(context.Background(), "", "")
	if !containsAction(logs, "login") {
		t.Error("成功登录应记 login 审计")
	}

	// 失败登录
	postLogin(h, "acme-admin", "wrong")
	logs, _ = auditor.ListAuditLogs(context.Background(), "", "")
	if !containsAction(logs, "login_failed") {
		t.Error("失败登录应记 login_failed 审计")
	}
}
```

- [ ] **Step 2: 跑测试确认失败** → `NewHandlerWithAudit` 未定义。

- [ ] **Step 3: 实现**

`handler.go`：
- Handler 加字段 `audit AuditRecorder`（定义接口避免 auth→security 反向依赖）：
```go
type AuditRecorder interface {
	RecordAudit(ctx context.Context, log security.AuditLog) error
}
```
> 若 `auth` 不能 import `security`（循环），把 `AuditLog` 参数改为基本类型（actor/action/detail string），由 cmd/core 桥接组装。**优先用基本类型避免循环**：
```go
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, detail string) error
}
```
- `NewHandler(idb, secret, cookieSecure, audit AuditRecorder)`（或保留旧签名 + `WithAudit` option）。**推荐 option 模式**避免破坏调用方：保留 `NewHandler(idb, secret, cookieSecure)`，加 `func (h *Handler) WithAudit(a AuditRecorder) *Handler`。cmd/core 链式调用注入桥接。

- Login 成功后：`if h.audit != nil { h.audit.Record(r.Context(), u.TenantID, u.ID, "login", fmt.Sprintf("ip=%s ua=%s", ip, r.UserAgent())) }`
- Login 失败（密码错/用户不存在）：`h.audit.Record(ctx, "", req.Username, "login_failed", "ip="+ip)`（tenantID 未知时空）。
- Logout：`h.audit.Record(ctx, tenantFromCtx, userID, "logout", "")`。

cmd/core 桥接（`authAuditAdapter` 实现 `auth.AuditRecorder`，转调 `stores.Security.RecordAudit`）。

- [ ] **Step 4: 跑测试通过** → `go test ./internal/core/auth/ -v` PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/core/auth/handler.go internal/core/auth/handler_test.go cmd/core/*.go
git commit -m "feat(auth): 登录/登出/失败记 security.AuditLog（ip+ua）"
```

---

## Task 8: 强密码策略

**Files:**
- Create: `internal/core/auth/password_policy.go`
- Modify: identity 用户创建/改密码 handler（admin 后台），调强密码校验

**Interfaces:**
- Produces: `ValidatePassword(s string) error`（≥8 + 含字母 + 含数字）。

- [ ] **Step 1: 写失败测试**

`password_policy_test.go`：

```go
package auth

import "testing"

func TestValidatePassword(t *testing.T) {
	bad := []string{"", "123", "abcdefg", "onlyletters", "12345678", "短密码1"}
	for _, p := range bad {
		if err := ValidatePassword(p); err == nil {
			t.Errorf("弱密码应拒: %q", p)
		}
	}
	if err := ValidatePassword("Aa123456"); err != nil {
		t.Errorf("强密码应过: %v", err)
	}
}
```

- [ ] **Step 2: 跑失败** → 未定义。
- [ ] **Step 3: 实现**

```go
package auth

import "errors"

var ErrWeakPassword = errors.New("密码至少 8 位且需同时包含字母和数字")

// ValidatePassword 强密码策略：≥8 + 含字母 + 含数字。
// seed demo 账号（123456）由 seed 直接 bcrypt 写入，不经此校验（demo 门控）。
func ValidatePassword(s string) error {
	if len(s) < 8 {
		return ErrWeakPassword
	}
	hasLetter, hasDigit := false, false
	for _, c := range s {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasLetter = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return ErrWeakPassword
	}
	return nil
}
```

接入：console-admin 后台 CreateUser/改密码 handler 写前调 `auth.ValidatePassword`，弱则 400。**定位**：找 `internal/core/identity` 或 admin handler 里处理用户密码写入的入口，加校验（grep `PasswordHash` 写入点）。

- [ ] **Step 4: 跑测试通过** → PASS。
- [ ] **Step 5: Commit**

```bash
git add internal/core/auth/password_policy.go internal/core/auth/password_policy_test.go <admin 用户 handler 路径>
git commit -m "feat(auth): 强密码策略（≥8+字母+数字），admin 开通用户时强制"
```

---

## Task 9: 安全 headers 中间件

**Files:**
- Create: `cmd/core/security_headers.go`（或并入 mux 装配）
- Modify: `cmd/core/main.go`（中间件挂到 mux 最外层）

**Interfaces:**
- Produces: `securityHeadersMiddleware(next) http.Handler`，注入 CSP / X-Frame-Options / X-Content-Type-Options / Referrer-Policy；HSTS 仅 HTTPS。

- [ ] **Step 1: 写失败测试**

`security_headers_test.go`：

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	hdr := rec.Header()
	if hdr.Get("X-Frame-Options") != "DENY" { t.Error("缺 X-Frame-Options:DENY") }
	if hdr.Get("X-Content-Type-Options") != "nosniff" { t.Error("缺 nosniff") }
	if hdr.Get("Referrer-Policy") == "" { t.Error("缺 Referrer-Policy") }
	if hdr.Get("Content-Security-Policy") == "" { t.Error("缺 CSP") }
}

func TestSecurityHeaders_HSTSOnlyOnHTTPS(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https") // ingress TLS 后
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HTTPS 应发 HSTS")
	}
}
```

- [ ] **Step 2: 跑失败** → 未定义。
- [ ] **Step 3: 实现 security_headers.go**

```go
package main

import "net/http"

// securityHeadersMiddleware 注入安全响应头。
// HSTS 仅在 HTTPS（X-Forwarded-Proto=https，ingress TLS 后）下发，HTTP 下不发（浏览器也忽略）。
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// CSP：同源 + Scalar CDN（/docs 用）；生产可收紧
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
```

`main.go` 把它挂到 mux 最外层（在 recoveryMiddleware/otelhttp 同层，最外或次外）。具体位置：现有 `srv.Handler = otelhttp.NewHandler(recoveryMiddleware(mux), ...)` 改为 `...securityHeadersMiddleware(recoveryMiddleware(mux))...`（或与 recovery 同级链式）。

- [ ] **Step 4: 跑测试通过** → PASS。
- [ ] **Step 5: Commit**

```bash
git add cmd/core/security_headers.go cmd/core/security_headers_test.go cmd/core/main.go
git commit -m "feat(core): 安全响应头中间件（CSP/HSTS/X-Frame/nosniff）"
```

---

## Task 10: 前端 api.ts 改 cookie 模式

**Files:**
- Modify: `frontend/console-user/src/api.ts`

**Interfaces:**
- Produces: `fetchAuth`/`fetchJSON` 用 `credentials:'include'`，移除 localStorage Key 与 `auth.key`、`setApiKey`、`PRESET_KEYS` 直读；401 自动 refresh + 重试一次。

- [ ] **Step 1: 实现改造**（前端无单测，手测在 Task 12）

新 `api.ts` 核心结构：

```ts
// 会话走 httpOnly cookie（后端 Set-Cookie），前端不存不读 token。
// 401 → 自动 refresh（cookie 自带 refresh token）→ 重试原请求一次 → 仍 401 跳 /login。

let refreshing: Promise<boolean> | null = null
function refreshSession(): Promise<boolean> {
  if (refreshing) return refreshing
  refreshing = fetch('/api/auth/tokens/refresh', { method: 'POST', credentials: 'include' })
    .then((r) => r.ok)
    .finally(() => { refreshing = null })
  return refreshing
}

export async function fetchAuth(path: string, opts: RequestInit = {}): Promise<Response> {
  const headers = new Headers(opts.headers)
  if (opts.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const resp = await fetch(path, { ...opts, headers, credentials: 'include' })
  if (resp.status === 401 && !path.includes('/api/auth/')) {
    const ok = await refreshSession()
    if (ok) return fetch(path, { ...opts, headers, credentials: 'include' }) // 重试一次
    window.dispatchEvent(new CustomEvent('paas:session-expired'))
  } else if (resp.status === 429) {
    ElMessage.warning('请求过多，请稍后再试')
  }
  return resp
}
```

- 移除 `STORAGE_KEY` / `DEFAULT_KEY` / `loadKey` / `auth.key` / `setApiKey` / `currentPreset`。
- 顶栏切换器改造（Task 11）改用 session store 登录，不再用 `setApiKey`。
- `fetchJSON` 保持（内部调 fetchAuth，解 `{data:T}`）。

- [ ] **Step 2: 类型检查 + 构建**

```bash
cd frontend/console-user && pnpm build
```
Expected: 无 TS 错（若有引用旧 `auth.key`/`setApiKey` 的文件，Task 11 一并改）。

- [ ] **Step 3: Commit**

```bash
git add frontend/console-user/src/api.ts
git commit -m "refactor(console-user): api.ts 改 cookie 会话（credentials:include + 401 自动 refresh）"
```

---

## Task 11: Login.vue + 路由守卫 + session store + 顶栏

**Files:**
- Create: `frontend/console-user/src/stores/session.ts`
- Create: `frontend/console-user/src/views/Login.vue`
- Modify: `frontend/console-user/src/router.ts`
- Modify: 顶栏组件（`App.vue` 或 layout）

**Interfaces:**
- Produces: `/login` 路由、`beforeEach` 守卫（ping `/api/auth/users/me` 判登录态）、session store（profile + login/logout）、顶栏（用户名/退出/演示快切）。

- [ ] **Step 1: session store**

`stores/session.ts`：

```ts
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchJSON, fetchAuth } from '@/api'

export interface UserProfile { id: string; username: string; roles: string[]; permissions: string[] }

export const useSessionStore = defineStore('session', () => {
  const profile = ref<UserProfile | null>(null)
  const loading = ref(false)

  async function loadProfile(): Promise<boolean> {
    try {
      profile.value = await fetchJSON<UserProfile>('/api/auth/users/me')
      return true
    } catch {
      profile.value = null
      return false
    }
  }

  async function login(username: string, password: string): Promise<void> {
    const resp = await fetchAuth('/api/auth/sessions', {
      method: 'POST', body: JSON.stringify({ username, password }),
    })
    if (!resp.ok) throw new Error((await resp.json().catch(() => ({}))).error || '登录失败')
    await loadProfile()
  }

  async function logout(): Promise<void> {
    await fetchAuth('/api/auth/sessions', { method: 'DELETE' })
    profile.value = null
  }

  // 演示账号快切（dev/demo）：本质是预设账号登录
  const DEMO_ACCOUNTS = [
    { username: 'acme-admin', password: '123456', label: 'Acme · 管理员' },
    { username: 'acme-dev', password: '123456', label: 'Acme · 开发者' },
    { username: 'globex-admin', password: '123456', label: 'Globex · 管理员' },
  ]

  return { profile, loading, loadProfile, login, logout, DEMO_ACCOUNTS }
})
```

- [ ] **Step 2: Login.vue**（参考 `console-admin/.../auth/views/Login.vue` 风格，但用 session store）

核心：用户名/密码表单 → `sessionStore.login(...)` → 成功跳 `route.query.redirect || '/applications'`；dev 预填 `acme-admin/123456`（`import.meta.env.DEV`）；失败 ElMessage 后端文案；含演示快切按钮（点 → login(DEMO_ACCOUNTS[i])）。

- [ ] **Step 3: 路由守卫**

`router.ts`：

```ts
import { useSessionStore } from '@/stores/session'

// 加 /login 路由（白名单）
{ path: '/login', name: 'login', component: () => import('@/views/Login.vue'), meta: { public: true } }

router.beforeEach(async (to) => {
  const session = useSessionStore()
  if (to.meta.public) return true
  if (!session.profile) {
    const ok = await session.loadProfile()
    if (!ok) return { path: '/login', query: { redirect: to.fullPath } }
  }
  return true
})
```

- [ ] **Step 4: 顶栏改造**

`App.vue`（或顶栏组件）：
- 展示 `session.profile.username` + 退出按钮（`session.logout()` → router.push('/login')）。
- 演示快切下拉（点 → `session.login(demo.username, demo.password)` → reload/跳首页）。**移除原 PRESET_KEYS 切换器**。
- 监听 `paas:session-expired` 事件 → 跳 /login。

- [ ] **Step 5: 构建验证**

```bash
cd frontend/console-user && pnpm build
```
Expected: 构建成功无 TS 错。

- [ ] **Step 6: Commit**

```bash
git add frontend/console-user/src/stores/session.ts frontend/console-user/src/views/Login.vue frontend/console-user/src/router.ts frontend/console-user/src/App.vue
git commit -m "feat(console-user): 登录页 + 路由守卫 + session store + 顶栏（用户名/退出/演示快切）"
```

---

## Task 12: Helm 配置 + 端到端验证

**Files:**
- Modify: `deploy/charts/paas/values.yaml` + `templates/core-deployment.yaml`

**Interfaces:**
- Produces: `auth.jwtSecret` + `auth.cookieSecure` values → core env 注入。

- [ ] **Step 1: values + deployment**

`values.yaml` 加：
```yaml
auth:
  jwtSecret: ""          # 生产必填（≥32 字节随机串）；空 + 非 dev 拒启
  cookieSecure: false    # 当前 HTTP 部署 false；配 TLS 后 true
```

`core-deployment.yaml` env 段加：
```yaml
- name: PAAS_JWT_SECRET
  value: {{ .Values.auth.jwtSecret | quote }}
- name: PAAS_COOKIE_SECURE
  value: {{ .Values.auth.cookieSecure | quote }}
```

- [ ] **Step 2: 部署 + 端到端验证**

```bash
./scripts/deploy-k8s.sh   # 或 helm upgrade --set auth.jwtSecret=$(openssl rand -hex 32)
```

验证清单（浏览器 + curl）：
- [ ] 访问 `/console/applications` 未登录 → 跳 `/console/login`
- [ ] `acme-admin/123456` 登录成功 → 跳 applications，顶栏显示 `acme-admin`
- [ ] `/api/applications` 返 t-acme 数据（cookie 携带）
- [ ] 演示快切到 `globex-admin` → `/api/applications` 返 t-globex 数据
- [ ] 退出 → cookie 清除，访问 `/api/applications` 返 401
- [ ] curl 错密码 5 次 → 第 6 次返 429
- [ ] 响应头含 `X-Frame-Options:DENY` / `X-Content-Type-Options:nosniff`
- [ ] API Key 通道仍可用：`curl -H "Authorization: Bearer sk-acme-admin" /api/applications` 返 200

- [ ] **Step 3: Commit**

```bash
git add deploy/charts/paas/values.yaml deploy/charts/paas/templates/core-deployment.yaml
git commit -m "feat(chart): auth.jwtSecret + cookieSecure env 注入"
```

---

## Self-Review 结论

- **Spec 覆盖**：L1（cookie/TLS延后/限流/demo门控/secret强制）+ L2（审计/强密码/headers）全部有对应 task。TLS 按用户确认延后（部署章节标注，cookieSecure 默认 false 适配 HTTP）。
- **类型一致**：`NewHandler(idb, secret, cookieSecure)` 在 Task 3 定义，Task 5/6/7 使用一致；`AuditRecorder` 接口在 Task 7 定义为基本类型避免循环；cookie 名 `paas_access`/`paas_refresh` 全链路一致（后端 cookie.go + bearer.go + 前端不直读）。
- **无占位**：每 task 含实际测试代码 + 实现代码；前端 task 给出核心结构（无单测框架，构建+手测验证）。
- **范围**：单一特性（登录会话），12 task 各自可独立测试+commit，不跨子系统。

## 执行选择

Plan complete and saved to `docs/superpowers/plans/2026-08-02-console-user-auth.md`. Two execution options:

1. **Subagent-Driven（推荐）** — 每 task 派新鲜 subagent，任务间 review，迭代快。
2. **Inline Execution** — 本会话内逐 task 执行，批量 + checkpoint。

Which approach?
