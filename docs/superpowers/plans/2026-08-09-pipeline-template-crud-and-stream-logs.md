# 流水线完善：模板 CRUD + 构建实时日志 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development。Steps 用 checkbox `- [ ]`。

**Goal:** 补两个流水线产品 gap：(B) admin 后台 CRUD 公共流水线模板；(A) 构建中实时 Pod 日志（SSE follow），让构建排障等同 GitHub Actions。

**Architecture:** B = TemplateRepository 补 Update/Delete + PipelineTemplate.Builtin 字段（拒改删）+ admin handler + console-admin 管理页（表单化 stage 编辑）。A = clientset 经 BuildLogStreamer 接口（依赖倒置）注入 devops handler，GET /api/buildruns/{id}/logs/stream SSE follow（k8s Pod logs），前端 EventSource 消费。

**Tech Stack:** Go（controller-runtime/k8s clientset + SSE text/event-stream）+ Vue 3（Element Plus）。

**Spec:** `docs/superpowers/specs/2026-08-09-pipeline-template-crud-and-stream-logs-design.md`

## Global Constraints
- Go 主语言；中文注释；多租户隔离（BuildRun.TenantID 校验、模板 admin 越权）；main 分支 commit。
- 依赖倒置：devops handler 不直接持有 k8s clientset，经接口注入（与 StatusReader/EnvTypeResolver 同款）。
- 集群外（无 clientset）降级返 503/空，不 panic。
- builtin 模板（tpl-ci/tpl-cd）拒改删（保护新应用默认 binding）。

---

## Phase B：模板 CRUD（admin 后台）

### Task B1: PipelineTemplate.Builtin 字段 + Repository Update/Delete

**Files:**
- Modify: `internal/devops/pipeline/model.go`（PipelineTemplate 加 Builtin + ErrTemplateBuiltin sentinel）
- Modify: `internal/devops/pipeline/repository.go`（TemplateRepository 加 UpdateTemplate/DeleteTemplate）
- Modify: `internal/devops/pipeline/memory/store.go`（实现 + Builtin 字段读写）
- Modify: `internal/devops/pipeline/pg/store.go`（实现 + Builtin 列读写）
- Modify: `internal/storage/pg/migrations/0023_pipeline_template_builtin.up.sql`（新建：ADD COLUMN builtin BOOL DEFAULT false + 回填 tpl-ci/tpl-cd）
- Modify: `internal/devops/pipeline/templates.go`（BuiltinTemplates 返回的模板标 Builtin=true）

**Interfaces:**
- Produces: `TemplateRepository.UpdateTemplate(ctx, t) (PipelineTemplate, error)`、`DeleteTemplate(ctx, id) error`；`PipelineTemplate.Builtin bool`；`ErrTemplateBuiltin = errors.New("builtin template cannot be modified")`。

**Steps:**
- [ ] model.go：PipelineTemplate 加 `Builtin bool \`json:"builtin,omitempty"\``；加 `ErrTemplateBuiltin` sentinel。
- [ ] repository.go：TemplateRepository 加 `UpdateTemplate(ctx, t) (PipelineTemplate, error)` + `DeleteTemplate(ctx, id) error`。
- [ ] memory/store.go：UpdateTemplate（Get→存在校验+本租户/builtin 拒→覆盖 Stages/Name/Kind/Params 存）+ DeleteTemplate（Get→builtin 拒→delete）。模板列表读返深拷贝。
- [ ] pg/store.go：UpdateTemplate（UPDATE WHERE id AND (tenant_id=ctx OR builtin)... 实际 admin 路径 ctx 可能无 tenant，由 handler 校验）+ DeleteTemplate；templateCols/Scan/Insert 加 builtin 列。
- [ ] migration 0023：`ALTER TABLE pipeline_templates ADD COLUMN IF NOT EXISTS builtin BOOL NOT NULL DEFAULT false;` + `UPDATE pipeline_templates SET builtin=true WHERE id IN ('tpl-ci','tpl-cd');`
- [ ] templates.go：BuiltinTemplates() 返回值标 `Builtin: true`；seed 时 ensure 写入 builtin=true（migration 回填兜底）。
- [ ] 测：memory/pg Update/Delete 成功 + builtin 拒（ErrTemplateBuiltin）+ 不存在 ErrTemplateNotFound。

### Task B2: handler admin CRUD 端点 + Builtin 保护

**Files:**
- Modify: `internal/devops/pipeline/handler.go`（serveAdminTemplates CRUD）
- Modify: `cmd/core/main.go`（/api/admin/pipeline-templates 路由 + adminGuard + OpenAPI）

**Interfaces:**
- `GET /api/admin/pipeline-templates`（super_admin，列表含 builtin tag）
- `POST /api/admin/pipeline-templates`（创建自定义，builtin=false）
- `PUT /api/admin/pipeline-templates/{id}`（更新，builtin 拒 409）
- `DELETE /api/admin/pipeline-templates/{id}`（删除，builtin 拒 409）

**Steps:**
- [ ] handler.go：serveAdminTemplates 分发（GET list / POST create / PUT update / DELETE），adminGuard（super_admin，handler 内 `IsPlatformAdmin` 判定，与 identity/security admin 同款）；create 强制 Builtin=false（防伪造 builtin）；update/delete 走 Repository，ErrTemplateBuiltin 映射 409。
- [ ] toHTTPStatus 加 ErrTemplateBuiltin→409。
- [ ] cmd/core：mux 注册 `/api/admin/pipeline-templates`（composite）；OpenAPI 登记 4 操作（Perm super_admin）。
- [ ] 测：handler CRUD + builtin 拒 + 非 super_admin 403。

### Task B3: console-admin 模板管理页

**Files:**
- Create: `frontend/console-admin/src/modules/pipeline-template/api.ts`
- Create: `frontend/console-admin/src/modules/pipeline-template/List.vue`
- Create: `frontend/console-admin/src/modules/pipeline-template/TemplateFormDrawer.vue`
- Modify: console-admin 菜单（auth/menus.go staticMenus + mock menu 对齐）

**Steps:**
- [ ] api.ts：CRUD + 类型（PipelineTemplate/StageDef，含 builtin）；fetch 走 console-admin http client。
- [ ] List.vue：SearchTable + useCrud 假分页；列 id/name/kind/builtin tag/stage 数；创建/编辑/删除（builtin 只读，删除输入 id 确认）。
- [ ] TemplateFormDrawer.vue：基本信息（id 创建时可填/name/kind select ci|cd|custom）+ stage 表单化（每行 type select + name + params key-value 动态增删）；builtin=true 全只读。
- [ ] 菜单「流水线模板」（推理服务分组下，super_admin）。
- [ ] mock/handlers 对齐（dev 菜单结构）。
- [ ] build 三套通过。

---

## Phase A：构建中实时日志（SSE follow）

### Task A1: BuildLogStreamer 接口 + clientset 桥接

**Files:**
- Create: `internal/devops/builder/logs_streamer.go`（接口 + k8s 实现）
- Modify: `cmd/core/main.go`（clientset → BuildLogStreamer 桥接 → 注入 devops.Handler）

**Interfaces:**
- `BuildLogStreamer` 接口：`StreamBuildLogs(ctx, buildID, tenantID string) (io.ReadCloser, error)`（follow 流；找不到 running Pod 返特定错误让端点降级）。
- k8s 实现：label `job-name=paas-build-<buildID>` 找 Pod → GetLogs(follow, tail=1000) → 返回 ReadCloser。Pod 未 ready 等待逻辑放端点。

**Steps:**
- [ ] builder/logs_streamer.go：`BuildLogStreamer` 接口 + `K8sBuildLogStreamer{Clientset}` 实现（StreamBuildLogs：list Pod by label，无 Pod 返 ErrNoPod，GetLogs follow 返 stream）。
- [ ] cmd/core：装配（clientset 非 nil 时构造 K8sBuildLogStreamer 注入 devops.Handler；nil 时 streamer=nil 降级）。
- [ ] 测：fake clientset 建 Pod + label，StreamBuildLogs 返流（follow 用 fake 不能真测，测 Pod 查找 + GetLogs 调用参数即可）。

### Task A2: devops handler SSE 流式端点

**Files:**
- Modify: `internal/devops/handler.go`（serveBuildLogsStream SSE + Handler 注入 streamer）
- Modify: `cmd/core/main.go`（路由注册 + OpenAPI）

**Interfaces:**
- `GET /api/buildruns/{id}/logs/stream`（build:read + 本租户校验）→ SSE。

**Steps:**
- [ ] Handler 加 `logStreamer BuildLogStreamer` 字段 + WithBuildLogStreamer opt。
- [ ] serveBuildLogsStream：GetBuildRun 校验本租户 → 终态返 BuildRun.Log 全量（text/event-stream 单事件）后关；运行中：logStreamer.StreamBuildLogs follow，逐块 flush（X-Accel-Buffering:no + 状态码 200 + Content-Type text/event-stream）；Pod 未 ready loop 等 30s 发心跳；streamer=nil 返 503。
- [ ] 路由 `/api/buildruns/{id}/logs/stream`（在 serveBuildDetail 同前缀分发，先匹配 /logs/stream 再 /{id}）。
- [ ] OpenAPI 登记 GET（标注 SSE 响应）。
- [ ] 测：streamer=nil 降级 503 + 本租户校验（跨租户 404 不泄漏）+ 终态返全量（fake streamer stub）。

### Task A3: 前端 EventSource 消费实时日志

**Files:**
- Modify: `frontend/console-user/src/views/app-tabs/PipelineRunView.vue`（build stage 展开时 EventSource 拉流）

**Steps:**
- [ ] build stage 展开 + BuildRun.status=running/pending：`new EventSource('/api/buildruns/{id}/logs/stream')`（同源带 cookie）→ onmessage append 日志区 + 自动滚底。
- [ ] 终态（success/failed）：fetch BuildRun.Log 全量展示（现状），不走 EventSource。
- [ ] 折叠/切走/组件卸载：close EventSource（防泄漏，onUnmounted + watch 展开）。
- [ ] 错误处理：EventSource onerror 优雅降级（显示"实时日志中断，显示已有日志"+ fetch 全量）。
- [ ] build 三套通过。

### Task A4: e2e + 部署 + CLAUDE.md

**Steps:**
- [ ] `go test ./...` 全绿 + 三套 build。
- [ ] deploy-k8s.sh。
- [ ] e2e：触发 paas-shop CI run，构建中 curl `/api/buildruns/{id}/logs/stream` 验证 SSE 逐块到达（非空 + 持续增长）；构建完成验证全量落库。
- [ ] CLAUDE.md 加「模板 CRUD + 构建实时日志」章节 + 记忆。

---

## 留后续
- 租户自定义模板（可视化编辑器 + 租户隔离）。
- builtin 模板版本号 + 升级覆盖机制（dogfooding 暴露的 seed 不覆盖问题）。
- process/mock 模式构建中实时日志（builder 边构建边写 buffer）。
- 日志全文搜索/过滤（grep）。
- follow 流式的 SSE 中间件链缓冲深度验证（多副本/ingress 场景）。
