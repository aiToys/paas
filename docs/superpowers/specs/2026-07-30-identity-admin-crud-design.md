# identity 管理 CRUD API 设计

> 为 console-admin「访问控制」管理页（P0-3）提供后端：租户/用户/角色/API Key 的 CRUD。
> 承接 P0-1 登录闭环，复用 BearerAuth + tenant:admin 权限。

## 目标

1. identity Repository 扩展管理方法（List/Delete/Update）。
2. 暴露 `/api/tenants`、`/api/users`、`/api/api-keys`、`/api/roles` 端点（CRUD）。
3. 平台级特权 API（跨租户），要求 `tenant:admin`（super_admin 通行）。
4. OpenAPI 登记；内存 + PG 双实现；掩码 API Key 明文（仅创建时返回一次）。

## 非目标

- 角色定义 CRUD（角色是 `BuiltinRoles()` 固定三角色，仅只读列表）。
- 部门/字典/公告/菜单 CRUD（vue-admin 演示概念，PaaS 不需要，P0-3 砍页）。
- LDAP/OIDC 用户同步、密码轮转策略。

## 架构决策

### D1：identity 管理方法是平台级（跨租户），不套业务模块的 tenant 过滤

业务模块 Repository「ctx 取租户强制过滤」是针对租户私有资源（应用/工作负载）。identity 管理的是「租户/用户」本身——**管理租户这件事天然跨租户**。故 identity admin 方法不带 tenant 过滤，但 handler 强制 `tenant:admin`（super_admin 通行）。

### D2：端点风格对齐 PaaS（复数 /api/tenants），admin 侧适配

core 用 `/api/tenants`（复数，与 `/api/applications` 一致），不对齐 admin 的 `/api/user`（单数）。P0-3 改 admin `system/user/api.ts` 指向 core 端点。

### D3：API Key 创建返明文一次，列表/详情掩码

与 security.Secret 一致：Create 返明文 Key 一次（给用户保存），List/Get 掩码 `sk-****`。PG/memory 存明文。

## Repository 扩展（identity/repository.go）

```go
// 管理方法（平台级，跨租户，handler 强制 tenant:admin）
ListTenants(ctx) ([]Tenant, error)
DeleteTenant(ctx, id string) error
ListUsers(ctx, tenantID string) ([]User, error)         // tenantID 空则全租户
UpdateUser(ctx, u User) error                            // 改 roles/status/is_admin
DeleteUser(ctx, tenantID, userID string) error
ListAPIKeys(ctx, tenantID string) ([]APIKey, error)     // tenantID 空则全租户
DeleteAPIKey(ctx, id string) error
```

memory + pg 各实现。PG 删除带 CASCADE（user_roles/api_key_roles/api_keys FK 已 CASCADE）。

## handler（identity/handler.go，新）

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | /api/tenants | tenant:admin | 列所有租户 |
| POST | /api/tenants | tenant:admin | 建租户 |
| DELETE | /api/tenants/{id} | tenant:admin | 删租户 |
| GET | /api/users?tenantId= | tenant:admin | 列用户（可按租户过滤） |
| POST | /api/users | tenant:admin | 建用户（含密码 → bcrypt） |
| PUT | /api/users/{id} | tenant:admin | 改 roles/status（密码可选） |
| DELETE | /api/users/{id} | tenant:admin | 删用户 |
| GET | /api/api-keys?tenantId= | tenant:admin | 列 Key（掩码） |
| POST | /api/api-keys | tenant:admin | 建 Key（返明文一次） |
| DELETE | /api/api-keys/{id} | tenant:admin | 删 Key |
| GET | /api/roles | tenant:admin | 内置角色列表（只读） |

挂 BearerAuth + `gateway.Require("tenant:admin")`。Create User 时若带 password 则 `auth.HashPassword`。main.go 注册 + reg.Operation 登记。

## 验收

1. `make build` + `go vet` + `go test ./... -race` 全绿（admin handler 单测：CRUD + 跨租户 + 掩码）。
2. JWT（admin/123456）调 `/api/tenants` 返回两租户；`POST /api/users` 建用户 + 登录验证。
3. 非 admin（developer JWT）调 `/api/tenants` → 403。
4. API Key List 返掩码；Create 返明文。
5. PG 路径 CRUD 落库（CASCADE 删）。
6. OpenAPI + /docs；CHANGELOG + CLAUDE.md 同步。
