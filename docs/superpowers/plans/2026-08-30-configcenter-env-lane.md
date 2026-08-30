# 配置中心环境隔离 + 泳道覆盖 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 配置中心补环境维度（app×env 独立配置/版本/闸门）与泳道 key 级覆盖（merge 发现），还掉对标业界的最后两块板。

**Architecture:** Namespace 加 EnvID（app scope 按 (app,env) 懒建）+ 新实体 LaneOverride（无版本链）+ 发现端点服务端两层 merge（version+overrideHash 双指纹）。生产写全接 prod:write 横切。

**Tech Stack:** Go + PostgreSQL migration + Vue 3（console-user）

**Spec:** `docs/superpowers/specs/2026-08-30-configcenter-env-lane-design.md`

## Global Constraints

- 多租户隔离：所有新查询强制 tenant 过滤，跨租户统一 not found 不泄漏。
- 生产闸门：目标 env type=prod 的写操作需 `prod:write`（EnvTypeResolver 依赖倒置，解析失败 fail-closed 按生产）。
- 向后兼容：不带 env/lane 参数的发现调用行为与升级前完全一致；存量 ns（env_id=''）继续可发现。
- sentinel 错误模式（errors.go），禁中文文本匹配。
- 审计：publish/rollback/lane 覆盖写操作记审计（`configcenter_*` 前缀，复用 AuditFunc）。
- `{data:T}` 响应契约；发现端点保持裸 JSON `{published,version,snapshot[,overrideHash]}`。
- 注释中文，与代码库一致。

---

### Task 1: 数据模型 + 存储层（EnvID + LaneOverride 双实现）

**Files:**
- Modify: `internal/configcenter/model.go`（Namespace.EnvID 字段）
- Modify: `internal/configcenter/repository.go`（EnsureByAppEnv 签名 + LaneOverrideStore 接口）
- Modify: `internal/configcenter/errors.go`（ErrLaneOverrideNotFound 如需）
- Modify: `internal/configcenter/memory/store.go`、`internal/configcenter/pg/store.go`
- Create: `internal/storage/pg/migrations/0038_configcenter_env_lane.{up,down}.sql`
- Test: `internal/configcenter/memory/store_test.go`、`internal/configcenter/pg/store_test.go`（integration）

**Interfaces:**
- Produces: `EnsureByAppEnv(ctx, appID, envID) (Namespace, error)`（替代 EnsureByApp，保留旧签名作 env="" 转发或直接改调用方）；`FindAppNamespaceEnv(ctx, appID, envID) (Namespace, bool, error)`；`UpsertLaneOverride/DeleteLaneOverride/ListLaneOverrides(ctx, appID, envID, laneID)`。
- migration：`cc_namespaces + env_id TEXT NOT NULL DEFAULT ''` + 唯一索引 `(tenant_id, app_id, env_id) WHERE app_id != ''`；新表 `cc_lane_overrides`（唯一约束五元组）。

**Steps:**

- [ ] 1.1 写失败测试：memory `TestEnsureByAppEnvCreatesPerEnv`（同 app 两 env 两 ns 独立）、`TestLaneOverrideUpsertDelete`、pg integration 同款 + `TestEnvFallbackDiscovery`（env 精确→'' 回退数据就位）
- [ ] 1.2 migration 0038（幂等 ADD COLUMN IF NOT EXISTS + CREATE TABLE IF NOT EXISTS，与 0001 合并 schema 同步）
- [ ] 1.3 model/repository 接口 + memory 实现（锁 + 深拷）
- [ ] 1.4 pg 实现（参数化 + tenant 过滤 + 唯一冲突映射 sentinel）
- [ ] 1.5 测试全绿（memory + `make test-pg` integration）+ commit

### Task 2: 发现端点两层 merge + REST 环境参数 + 生产闸门

**Files:**
- Modify: `internal/configcenter/handler.go`（serveAppPublished 加 env/lane query + merge + overrideHash）
- Modify: `internal/configcenter/app_handler.go`（全部操作 envId 参数 + EnvTypeResolver 注入 + allowProd + lane-overrides 三端点 + 审计）
- Modify: `cmd/core/main.go`（装配：EnvTypeResolver 桥接 envStore，同 appconfig 模式）
- Test: `internal/configcenter/app_handler_test.go`

**Interfaces:**
- Consumes: Task 1 的 EnsureByAppEnv/FindAppNamespaceEnv/LaneOverride 三方法。
- Produces: merge 逻辑纯函数 `mergeSnapshot(base map, overrides []LaneOverride) map`（可测）；`overrideHash` = FNV-1a(排序 key=value 串)，无覆盖时省略字段。
- 发现解析：env 精确 → env='' 回退 → {}；lane 同规则取覆盖。

**Steps:**

- [ ] 2.1 失败测试：`TestPublishEnvIsolation`（两 env 独立发布互不可见）、`TestProdPublishRequiresPerm`（prod env 403）、`TestLaneOverrideMerge`（覆盖/透传/hash 变化）、`TestDiscoveryBackwardCompat`（不带 env 行为不变）
- [ ] 2.2 handler/app_handler 实现（merge 纯函数独立文件 merge.go）
- [ ] 2.3 cmd/core 装配 + OpenAPI Operation 更新（envId/lane query 登记）
- [ ] 2.4 测试全绿 + commit

### Task 3: 泳道回收级联 + lane handler 联动

**Files:**
- Modify: `cmd/core/app_cascade_deleter.go` 或新 adapter（ReclaimLane 时清 cc 覆盖）
- Modify: `internal/workload/lanegc.go` 或 `internal/lane/handler.go`（注入 OverrideCleaner 依赖倒置接口）
- Test: 对应 handler_test

**Interfaces:**
- Produces: `LaneOverrideCleaner` 接口（configcenter 定义，`CleanLane(ctx, tenantID, appID, envID, laneID)`）；lane 回收路径调用（best-effort 失败日志）。注意 LaneGC 按泳道维度回收（多 app），清理按 (tenant, env, lane) 全 app 扫。

**Steps:**

- [ ] 3.1 失败测试：泳道 ReclaimLane 后覆盖被清、发现回落基线
- [ ] 3.2 实现 + 装配
- [ ] 3.3 测试全绿 + commit

### Task 4: 前端（环境贯穿 + 泳道覆盖子区）

**Files:**
- Modify: `frontend/console-user/src/api/configcenter.ts`（全部函数加 envId；lane-overrides 三函数）
- Modify: `frontend/console-user/src/views/app-tabs/AppDynamicConfigs.vue`（顶栏 env store 贯穿 + 泳道覆盖子区 + prod 发布 confirmDangerous isProd）
- Test: `pnpm --filter console-user build`

**Interfaces:**
- Consumes: Task 2 REST 契约。env 取 `stores/env.ts` 顶栏环境（与 AppConfigs.vue 同款）；泳道下拉取 `GET /api/lanes?envId=`（已有端点）。

**Steps:**

- [ ] 4.1 api.ts 扩展 + 组件改造（覆盖子区标注「即时生效，随泳道回收消失」）
- [ ] 4.2 build 通过 + commit

### Task 5: dogfooding（chatbot env/lane 拉取）+ e2e + 文档

**Files:**
- Modify: `examples/paas-shop/chatbot/dynconfig.go`（PAAS_CONFIG_ENV/PAAS_LANE_ID → query 参数；version+overrideHash 双比对）
- Modify: `examples/paas-shop/chatbot/dynconfig_test.go`
- Modify: `CLAUDE.md`（配置中心章节补环境+泳道段）

**Steps:**

- [ ] 5.1 chatbot 客户端改造 + 测试（hash 变化热替换用例）
- [ ] 5.2 k8s 部署 + e2e 验收（spec 验收 5 条：env 隔离/prod 拒/泳道覆盖 merge/回收回落/向后兼容）
- [ ] 5.3 CLAUDE.md 同步 + commit + push
