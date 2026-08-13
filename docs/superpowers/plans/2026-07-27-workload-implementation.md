# 工作负载切片 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans（inline）或 subagent-driven。

**Goal:** 应用挂载工作负载（Service/Job/CronJob），跨应用视图按租户可见，CRUD 端到端。

**Architecture:** `internal/workload/` 领域 + Repository（复用 pkg/tenant 隔离）+ handler；挂 application 子路由 + 跨应用 `/api/workloads`；前端 `Workloads.vue` 按 type tab + 应用详情分组。

**Tech Stack:** Go 1.22 标准库 + testify；Vue 3 + Element Plus。

## Global Constraints

- 注释中文，与代码库一致
- Repository 从 ctx 取租户强制过滤，缺失/跨租户即 not found
- 工作负载归属应用，不进 ResourceCount（不是绑定资源）
- 不引新外部依赖；提交者 The PaaS Authors
- 默认 Key sk-acme-admin

---

### Task 1: workload 领域模型 + Repository 接口

**Files:** Create `internal/workload/model.go` `internal/workload/repository.go`

- 常量：TypeService/TypeJob/TypeCronJob；StatusRunning/Deploying/Failed/Succeeded/Pending
- `Workload` 结构（见 spec）
- `Validate() error`：type 合法、name/image 非空、cronjob 须 schedule
- `Repository` 接口（见 spec）

- [ ] 写 model.go + repository.go
- [ ] `go build ./internal/workload/`

---

### Task 2: 内存实现 + seed + 测试

**Files:** Create `internal/workload/memory/store.go` `internal/workload/memory/store_test.go`

- Store: `map[id]Workload`，全部方法从 ctx 取租户
- List(appID, wtype)：按 tenant + 可选 appID + 可选 type 过滤，ID 排序
- Get/Update/Delete：跨租户 not found
- Create：Validate + 写 tenantID（ctx）
- seed：5 条（见 spec 表）
- 测试：隔离、跨租户拒绝、Validate、CRUD

- [ ] TDD 写测试 → 实现 → `go test ./internal/workload/... -race`

---

### Task 3: handler + 权限常量

**Files:** Create `internal/workload/handler.go` `internal/workload/handler_test.go`

- 常量 `PermWorkloadRead/Write`
- `Handler{repo, Authorize}`（复用 application 的注入模式）
- 路由：`/api/applications/{id}/workloads`（GET 列表/POST 创建）、`/api/workloads`（GET 跨应用?type=）、`/api/workloads/{id}`（PUT/DELETE）
- ServeHTTP 用 Go 1.22 ServeMux 风格的子路由细分
- 测试：stubRepo + Authorize 注入

- [ ] TDD → `go test ./internal/workload -race`

---

### Task 4: identity 权限 + cmd/core 装配

**Files:** Modify `internal/core/identity/model.go`（BuiltinRoles 加 workload 权限）、`cmd/core/main.go`、`cmd/core/main_test.go`（如需）、Create `cmd/core/workload_seed.go`（或并入）

- BuiltinRoles：admin/developer 加 workload:read/write；viewer 加 workload:read
- serveHTTP 注入 workload handler + Authorize + Require 包裹（模型/推理用 Require；workload 用方法级 Authorize）
- 路由 mount
- 验证：curl 跨租户隔离 + CRUD

- [ ] `go build && go test ./... -race`
- [ ] curl 冒烟（acme vs globex service 列表；扩缩容）

---

### Task 5: 前端 Workloads.vue + router + 应用详情分组

**Files:** Create `frontend/console-user/src/views/Workloads.vue`；Modify `router.ts`（三页指向 Workloads.vue + props type）、`ApplicationDetail.vue`（工作负载分组）、`api.ts`（如需）

- Workloads.vue：props type；三 tab 切换路由；接 `/api/workloads?type=`；行：名称/应用/镜像/副本/状态/调度；扩缩容（el-input-number + PUT）+ 删除
- 应用详情：fetch `/api/applications/{id}/workloads` 渲染分组
- 监听 paas:key-changed 重载

- [ ] `pnpm --dir frontend build`
- [ ] Playwright：三 tab 真实数据 + 切租户隔离

---

### Task 6: 文档 + 全量验证

- CLAUDE.md：垂直切片加「工作负载」；完成度 28%→35%
- README：curl 工作负载示例
- 蓝图：工作负载由 ❌ 改 ✅
- `make lint && make test && gofmt -l .`；前端三套 build；Playwright

## Self-Review

- 覆盖：领域(T1)/隔离+seed(T2)/API+权限(T3/T4)/前端(T5)/文档(T6) ✓
- 类型一致：Workload 字段、Repository 签名、Perm 常量贯穿 ✓
- 无占位符 ✓
