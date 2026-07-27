# 多租户身份骨架（RBAC + 租户隔离端到端）设计

> 切片目标：为平台补齐「多租户隔离根基」——给所有领域实体加租户维度、API Key 解析租户上下文、粗粒度 RBAC 权限校验、控制台端到端隔离可见。
> CLAUDE.md 约束：「多租户隔离由 Core 统一治理（DB 访问层强制 tenant 过滤），插件不得绕过」。本切片把该约束从「写在未来」落到「现在强制」。

## 范围

**做：**
- `identity` 补 `Role`/`Permission`/`APIKey` 模型 + 内存仓储
- `application` 领域加 `TenantID`；Repository 强制按租户过滤
- `pkg/tenant`：ctx 传播租户上下文
- Gateway 鉴权进化：API Key → `(tenantID, userID, roles)` → 注入 ctx
- `Require(perm)` 粗粒度权限中间件
- 两租户 seed + 三演示 Key
- console-user：API Key 登录态、应用列表按租户隔离、401/403 处理

**不做（YAGNI）：**
- PostgreSQL 持久化（仍是内存实现，但 Repository 接口已为 PG 行级安全铺好）
- 实例级 ABAC、组织树、工作空间
- OAuth/SSO、JWT 签发（本期 API Key 唯一凭证）
- 模型目录按租户隔离（平台级共享，符合行业惯例）

## 权限模型（粗粒度 RBAC）

权限标识统一为 `<resource>:<action>`。内置角色：

| 角色 | 权限集 |
|---|---|
| `tenant-admin` | 通行所有资源（含 `tenant:admin`） |
| `developer` | `application:read` `application:write` `binding:write` `model:infer` `model:read` |
| `viewer` | `application:read` `model:read` |

校验规则：`tenant-admin` 通行；否则角色权限集需包含目标权限。

## 领域模型

### identity 扩展

```go
// 角色与权限（内置，起步期固定）
type Permission string // 形如 "application:read"

type Role struct {
    Name        string
    Permissions []Permission
}

// APIKey 是 (租户, 用户, 角色) 三元组的凭证。
type APIKey struct {
    ID        string
    TenantID  string
    UserID    string
    Roles     []string // 角色名
    Key       string   // 明文 bearer，内存态
    CreatedAt time.Time
}
```

`User` 加 `Roles []string` 字段。`Tenant` 不变。

`identity.Repository` 扩展：
```go
CreateAPIKey(ctx, APIKey) error
LookupAPIKey(ctx, key string) (APIKey, error)  // 鉴权入口
Role(ctx, name string) (Role, error)           // 取内置角色定义
```

### application 加租户维度

```go
type Application struct { ...; TenantID string }
```

Repository 全部方法从 ctx 取 `tenantID` 并强制过滤。

### pkg/tenant（ctx 传播）

```go
func WithTenant(ctx, tenantID string) context.Context
func TenantFrom(ctx) (string, bool)  // 无则 false
```

Repository 实现内部 `TenantFrom(ctx)`，缺失或空 → 拒绝（防止漏传）。

## 鉴权层进化

`gateway.APIKeyAuth` 从单一全局 key 升级为依赖 `identity.Repository`：

```
Authorization: Bearer <key>
  → identity.LookupAPIKey(key)
  → 注入 ctx(tenant, userID, roles)
  → next
```

失败：401 `invalid api key`。

`gateway.Require(perm string) func(http.Handler) http.Handler`：
- 从 ctx 取 roles；`tenant-admin` 通行
- 否则聚合各角色权限集，含 perm 放行
- 失败：403 `forbidden: missing <perm>`

路由权限映射：
- `/v1/chat/completions` → `model:infer`
- `/v1/models` `/api/models` → `model:read`
- `/api/applications` GET → `application:read`
- `/api/applications` POST → `application:write`
- 绑定/解绑 → `binding:write`

模型目录 `/api/models` `/v1/models` **平台级共享**，不按租户过滤（目录是平台公共资产；租户私有的是应用及其绑定）。

## seed（隔离演示）

两租户：
- `Acme`（`t-acme`）：智能客服、推荐服务
- `Globex`（`t-globex`）：数据导入、智能体平台

三个演示 Key：
- `sk-acme-admin` — Acme tenant-admin（默认开发 key，兼容现有 curl 文档）
- `sk-globex-admin` — Globex tenant-admin
- `sk-acme-dev` — Acme developer（验证权限差异）

`PAAS_API_KEY` 环境变量仍可覆盖默认 Key。

## 前端

- console-user 新增 API Key 登录态：顶栏 tenant-chip 点击 → 输入/选择 Key → 存 `localStorage` → 全局请求注入 `Authorization: Bearer`
- 应用列表按当前 Key 的租户数据呈现
- 401 → 提示重新输入 Key；403 → 提示权限不足
- 默认预填 `sk-acme-admin`，开箱可演示

## 验收

- 隔离：`sk-acme-admin` 调 `/api/applications` 只见 Acme 两个应用；`sk-globex-admin` 只见 Globex 两个；跨租户 `GET /api/applications/{id}` 返回 404（不泄漏存在性）
- 权限：`sk-acme-dev` 能读写应用、能 infer；无 `tenant:admin`（无对应路由时按权限集判定）
- 模型目录：两租户 Key 都能看 `/api/models` 全集（平台共享）
- 前端：换 Key → 应用列表切换；Playwright 验证隔离可见
- `go test -race` 全绿；新增单测覆盖 Repository 跨租户拒绝、权限中间件放行/拒绝、API Key 解析注入

## 架构约束（不变）

- 业务领域逻辑不进 Core：RBAC/隔离属 Core 治理职责，OK
- 插件不得绕过租户隔离：Repository 强制 ctx 取租户，缺失即拒
- Apache 2.0：无新外部依赖（纯标准库 + 现有 testify）
