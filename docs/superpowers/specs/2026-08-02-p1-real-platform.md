# P1 生产可用：应用去假 + 租户开通 + 模型管理

> 2026-08-02 · 承接 P0 登录会话，解决「看起来假」的三个核心问题。

## 背景

P0 登录会话已落地（httpOnly cookie + 限流 + 审计 + 强密码 + 安全 headers）。用户反馈「看到的还是很假」聚焦三点：

1. **应用列表是 seed 演示数据**（智能客服/推荐服务/数据导入/实验沙盒/智能体平台 5 条硬编码），非用户真实应用。
2. **模型硬编码无管理后台**：`internal/maas/catalog.go` 纯函数返回 `[]*provider.Model`，MaaSPlugin.Init 注册到 Gateway；仅 `GET /api/models` 只读，无 CRUD，admin 无模型管理页面。
3. **租户无法开通**：identity 后端 CRUD 已齐全（`/api/tenants|users|api-keys`，adminGuard super_admin），但 console-admin **无租户管理页面**，且 `system/user/api.ts` 创建用户硬编码 `tenantId:'t-acme'`（不支持跨租户开通）。

## 目标（生产可用标准）

- `seed.demo=false` 时**零假数据**（应用列表空，用户自建）。
- 租户可经 admin 后台**自助开通**（建租户 -> 建该租户管理员 -> 用户登录 console-user 自建应用/Key）。
- 模型可经 admin 后台**全生命周期管理**（Model/Channel/Credential CRUD，catalog DB 驱动）。
- 权限隔离：super_admin（平台级，跨租户）vs tenant-admin（本租户）。
- 危险操作确认 + 审计。

## 分阶段交付

| 阶段 | 范围 | 工作量 | 状态 |
|------|------|--------|------|
| **P1.1**（本里程碑） | 应用去假 + 租户开通 | 中（后端已有，补前端+门控+保护） | 进行中 |
| **P1.2**（下一里程碑） | 模型管理（Model/Channel/Credential CRUD + admin 页面） | 大（后端 Repository+migration+handler+plugin 改造 + 前端页面） | 待启 |

## P1.1 设计

### 1. 应用去假（seed.demo 门控）

与演示凭证门控（`PAAS_DISABLE_DEMO_SEED`，chart `seed.demo=false`）**同源**，避免两套门控：

- **内存路径**：`internal/core/application/memory/store.go` `NewStore()` 检查 env，`true` 跳过 `seed()` 灌空 store。
- **PG 路径**：`cmd/core/persistence.go` `seedApplications()` 检查 env，`true` 跳过灌入。
- 生产 `seed.demo=false` -> 空应用列表，用户经 console-user 自建真实应用。
- dev 保留 5 演示应用（零配置可演示）。
- **不删 `SeedApps()` 函数**：保留作 dev seed 真源（DRY，与演示凭证同模式）。

### 2. 租户开通

后端 CRUD 已齐，核心是补前端 + 安全保护：

**后端**：
- `CreateTenant`（id+name 必填，super_admin）-- 已有。
- `CreateUser` 已支持跨租户（super_admin 指定 `tenantID`，普通 tenant-admin 强制本租户 + 不可创建超管）-- 已有。
- **补 `DeleteTenant` 非空保护**：删租户前检查租户下有用户则拒绝（409 引导先清用户），防孤儿数据。memory + pg 两路径。跨 store 业务数据（应用/工作负载/数据服务）级联清留后（删租户低频高危，先防主要孤儿）。

**前端（console-admin）**：
- **菜单**：`internal/core/auth/menus.go` `staticMenus()` system 下加「租户管理」项（`/system/tenant` -> `system/tenant/views/List`）。
- **system/tenant 模块**：`api.ts`（对接 `/api/tenants`）+ `List.vue`（列表/创建/删除）+ `FormDrawer.vue`（创建表单）。删除走危险确认。
- **system/user 选租户**：`api.ts` 去硬编码 `tenantId:'t-acme'`，`UserFormDrawer.vue` 加租户下拉（super_admin 可选全部租户，普通 admin 锁定本租户）。

**开通流程**（admin 操作手册）：
1. admin 登录 console-admin -> 租户管理 -> 新建租户（id+name）。
2. 用户管理 -> 新建用户（选刚建的租户 + tenant-admin 角色 + 密码）。
3. 该用户登录 console-user -> 自建应用 / 生成 API Key（或 admin 代建 API Key）。

### 3. console-user 空应用状态

去假后生产环境应用列表为空，补空状态引导（无应用时「新建应用」CTA），避免空白页。

## P1.2 概要（模型管理，下一里程碑详化）

- **后端**：`internal/maas/` 加 Model/Channel Repository（memory + pg）+ migration（`maas_models`/`maas_channels` 表）+ handler CRUD（`/api/admin/models`、`/api/admin/channels`）+ MaaSPlugin.Init 改从 Store 加载（保留 catalog seed 作 demo，seed.demo 门控）。
- **凭证**：复用 `security.Secret`（平台级），Channel.CredentialRef 引用；模型管理 UI 选/绑凭证。airouter 平台级凭证仍 env 注入 seed（白嫖容灾链路）。
- **前端**：console-admin 新建模型管理模块（模型列表 / 通道配置 / 凭证绑定），平台级（super_admin），不按租户过滤。
- **OpenAPI**：新增端点登记 route registry。

## 不做项（YAGNI，明确留后）

- 跨 store 级联删租户业务数据（应用/工作负载/数据服务/账单）-- 删租户低频高危，P1.1 只防用户孤儿。
- 租户自助公开注册（生产走 admin 开通，非公开注册页）。
- 租户配额初始化向导（billing 已有默认配额，`GetQuota` 不存在返默认）。
- 租户级模型目录（模型是平台级共享，CLAUDE.md 已定）。
- 租户禁用/冻结（软删）-- 留后。

## 验收标准（P1.1）

- `PAAS_DISABLE_DEMO_SEED=true` 启动，`GET /api/applications`（sk-acme-admin）返空列表。
- admin 登录 console-admin -> 租户管理可见，可建/删租户；删有用户的租户 409。
- admin 建用户时可选租户（非仅 t-acme）。
- console-user 空应用列表显示空状态引导。
- `go test ./...` 绿 + `golangci-lint` 0 + `pnpm build` 绿。
- helm `seed.demo=false` 生效（应用 seed 不灌）。
