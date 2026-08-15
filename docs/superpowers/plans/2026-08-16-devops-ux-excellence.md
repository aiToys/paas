# DevOps UX 业界优秀标准改造 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 变更收件箱 + 值班台 + 站内通知 + Release 对账，四阶段（A补底/B收件箱/C通知/D对账）让 DevOps 体验达业界优秀标准。

**Architecture:** 后端补跨应用列表 + 通知聚合两个轻量端点；前端补五单据详情独立页 + DevOps 中心值班台/档案室 + 顶栏通知中心。详情组件唯一、双视图（中心/应用）复用同源。

**Tech Stack:** Go（change/observability handler 模式）+ Vue3 + Element Plus。

**Spec:** docs/superpowers/specs/2026-08-16-devops-ux-excellence-design.md

## Global Constraints

- 新端点 OpenAPI 登记 + `{data:T}` 契约 + camelCase json 标签
- 跨应用列表强制 ctx tenant 过滤；权限 pipeline:read（notifications 用登录态）
- 前端详情组件唯一复用；轮询 10s + onUnmounted clearInterval；不用 v-html
- 注释语言与现有代码库一致（中文）

---

### Task 1: 后端——变更/批次跨应用列表端点

**Files:**
- Modify: `internal/devops/change/handler.go`（新增 serveGlobalChanges/serveGlobalBatches）
- Modify: `cmd/core/main.go`（注册 `mux.Handle("/api/changes/", ...)` `/api/batches/`）
- Test: `internal/devops/change/handler_test.go`

**Interfaces:**
- Produces: `GET /api/changes?appId=&status=` → `{data:[Change]}`；`GET /api/batches?appId=&status=` → `{data:[IntegrationBatch]}`（tenant 内跨应用，perm pipeline:read）

- [ ] handler 加全局路由入口（`/api/changes` 无 appID 前缀，ListChanges ctx 空表示全部）+ OpenAPI 登记 + 测试（两应用数据 + 跨租户隔离 404/空）
- [ ] `go test ./internal/devops/...` 全绿 + commit

### Task 2: 后端——通知聚合端点

**Files:**
- Create: `internal/devops/change/notifications.go`（Notifications() 聚合函数）
- Modify: `internal/devops/change/handler.go` + `cmd/core/main.go`
- Test: `internal/devops/change/notifications_test.go`

**Interfaces:**
- Produces: `GET /api/notifications` → `{data:{items:[{id,type,severity,title,appId,targetType,targetId,at}]}}`；type ∈ batch_conflict|batch_testing|batch_releasing|run_failed|run_paused|change_released；severity ∈ error|warning|info

- [ ] 聚合实现（ListBatches 全量 + ListRuns 过滤状态 + ListChanges released）+ 测试
- [ ] commit

### Task 3: 前端——五单据详情独立页 + 路由

**Files:**
- Create: `frontend/console-user/src/views/ChangeDetail.vue`
- Create: `frontend/console-user/src/views/BatchDetail.vue`
- Create: `frontend/console-user/src/views/BuildDetail.vue`
- Create: `frontend/console-user/src/views/ReleaseDetail.vue`
- Modify: `frontend/console-user/src/router.ts`（4 路由 `/devops/changes/:id` 等）
- Modify: `frontend/console-user/src/api/change.ts`（补 getChange）

**Interfaces:**
- Consumes: Task 1 端点；既有 `/api/buildruns/{id}` `/api/releases`（列表过滤）
- Produces: 四个详情页组件，均含「返回 + 应用归属 + 状态 + 链路导航」

- [ ] 四详情页（ChangeDetail 含 B 阶段收件箱五段结构骨架；BatchDetail el-steps + 关联 run/变更 chips；BuildDetail 日志 monospace + 关联镜像；ReleaseDetail 信息 + 回滚 + 关联 workload 占位）
- [ ] `pnpm build` 通过 + commit

### Task 4: 前端——DevOps 中心值班台 + 档案室

**Files:**
- Modify: `frontend/console-user/src/views/DevOps.vue`（重构：默认「值班台」tab + 六单据 tab）

**Interfaces:**
- Consumes: Task 1/2 端点 + 既有 runs/buildruns/releases/images

- [ ] 值班台（三列：失败待处理/等审批/运行中，聚合 notifications + runs，点击跳详情）
- [ ] 六 tab 档案室（运行/变更/批次/构建/镜像/发布，行点详情跳独立页）
- [ ] build + commit

### Task 5: 前端——变更收件箱（B 阶段核心）

**Files:**
- Modify: `frontend/console-user/src/views/ChangeDetail.vue`（五段聚合完善）
- Modify: `frontend/console-user/src/views/app-tabs/AppChanges.vue`（列表状态列 + 内联操作 + 点行跳详情）

**Interfaces:**
- Consumes: 既有 changes/batches/pipelineruns/gitea commits API（`/api/applications/{id}/repositories/{rid}/commits`）

- [ ] 变更详情五段（代码/批次/测试/发布/时间线）+ commits 拉取 + 关联跳转
- [ ] AppChanges 列表「卡在哪」列 + 内联按钮（入批/去集成/去审批）
- [ ] build + commit

### Task 6: 前端——通知中心（C 阶段）

**Files:**
- Create: `frontend/console-user/src/components/NotificationBell.vue`
- Modify: `frontend/console-user/src/App.vue`（顶栏挂铃铛）

- [ ] 铃铛 + 未读红点（localStorage `paas:notif-read` 记已读 id）+ 下拉列表（分组 by severity）+ 点击跳转 + 30s 轮询
- [ ] build + commit

### Task 7: 前端——Release 运行态对账（D 阶段）+ 部署验证

**Files:**
- Modify: `frontend/console-user/src/views/ReleaseDetail.vue`（运行态卡片）

- [ ] Release 详情按 workloadId 拉 `/api/workloads/{id}` 展示副本/镜像/状态
- [ ] 全量 `go test ./...` + 三套 `pnpm build` + `./scripts/deploy-k8s.sh` + e2e（新端点 200 + 前端页面可达）
- [ ] commit + CLAUDE.md 章节更新
