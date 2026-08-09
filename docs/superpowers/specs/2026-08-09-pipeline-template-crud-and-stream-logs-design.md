# 流水线完善：模板 CRUD（admin 后台）+ 构建实时日志（SSE）

## 背景

dogfooding + 用户反馈暴露两个产品 gap：
1. **构建中看不到实时日志**：build stage 全量日志（`BuildRun.Log`）是 `builder.Build()` 同步阻塞返回时才落库，构建进行中日志面板空，只有 `StageRun.Log` 关键事件。GitHub Actions 每个 step 实时流式日志差一层。
2. **模板无法配置**：`TemplateRepository` 只有 List/Get/Create（缺 Update/Delete），handler `serveTemplates` 只 GET，前端无配置页。「公共流水线模板」只能平台预置 builtin（`tpl-ci`/`tpl-cd`），管理员/用户无法在 UI 创建自定义模板。

## 业界对标

- **GitHub Actions / GitLab CI**：每个 step 实时流式日志（follow），模板有 reusable workflow（平台/组织级，CRUD 可配）。
- **Argo Workflow / Tekton**：WorkflowTemplate CRD（cluster 级 + namespace 级），运行时引用；日志 kubectl logs -f 实时。

PaaS 定位：平台内建「模板 CRUD（admin 维护公共模板）+ 构建实时日志（k8s Pod logs follow）」，不绑定第三方 CI，开发者零配置可用模板 + 构建中实时排障。

## 功能 A：构建中实时日志（SSE follow）

### 架构

```
cmd/core: clientset → BuildLogStreamer 桥接 → 注入 devops.Handler（依赖倒置）
  桥接：label job-name=paas-build-<buildID> 找 Pod → CoreV1().Pods(ns).GetLogs(pod, {Follow:true, TailLines:1000})
devops/handler: GET /api/buildruns/{id}/logs/stream（SSE text/event-stream）
  → GetBuildRun 校验本租户（越权防泄漏源码/凭证）
  → 终态（success/failed）：返 BuildRun.Log 全量后关流
  → 运行中（running/pending）：StreamLogs follow 逐块 flush + X-Accel-Buffering:no
  → Pod 未 ready（ContainerCreating）：SSE 心跳注释保连接，ready 后接管（最多等 30s）
前端 PipelineRunView: build stage 展开 + status running → new EventSource(stream)
  → onmessage append 日志区 + 自动滚底；折叠/切走 close()
```

### 抉择（已确认）

- **协议**：SSE（EventSource）。浏览器原生、自动重连、同源默认带 cookie（console-user cookie 会话可用）。
- **follow vs 轮询快照**：follow SSE（逐行真实时）。
- **非 k8s 模式（mock/process）**：降级返 BuildRun.Log（mock 全量 / process 完成全量，构建中空）。本期只 k8s 模式真 follow。
- **Pod 未 ready**：端点内 loop 等 Pod ready（最多 30s）期间发 `: heartbeat` 保连接。

### 越权

拉 Pod 日志前必须 `GetBuildRun` 校验 `BuildRun.TenantID == ctx tenant`，否则跨租户枚举 buildID 读他人构建日志（泄漏源码/凭证）。与 workload PodLogs 越权校验同语义。

### 集群外降级

无 clientset（dev 单机）时 streamer=nil，端点返 503 + 友好错误（与 dataplane/observability real 同款降级）。

## 功能 B：流水线模板 CRUD（admin 后台）

### 架构

```
后端：
  TemplateRepository 补 UpdateTemplate/DeleteTemplate + memory/pg 实现
  PipelineTemplate 加 Builtin bool（builtin 拒改删，防误删致新应用 OnAppCreate 无默认 binding）
  handler：/api/admin/pipeline-templates CRUD（super_admin，平台级 builtin + 全部租户自定义）
  保护：Builtin=true 的 Update/Delete 返 409
  builtin 升级机制（版本号 + 覆盖）留后续
前端 console-admin：
  modules/pipeline-template/（api.ts + List.vue + TemplateFormDrawer.vue）
  菜单「流水线模板」（super_admin，DevOps/推理服务分组）
  stage 编辑：表单化（每 stage 一行 type select + name + params key-value，与 PipelineDesigner 覆盖同款）
```

### 抉择（已确认）

- **stage 编辑器**：表单化（非可视化拖拽 / JSON）。与 PipelineDesigner 覆盖表单同款风格，DRY，可控不易错。
- **builtin 保护**：Builtin 字段 + 拒改删。改 builtin 走代码发版。
- **模板范围**：admin 管平台级 + 看全部。租户自定义留后续（要租户隔离 + 可视化编辑器，工作量翻倍）。
- **builtin 升级**：本期不做（独立于 CRUD，留后续）。

### 影响范围

- `internal/devops/pipeline/repository.go`：TemplateRepository 加 Update/Delete
- `internal/devops/pipeline/model.go`：PipelineTemplate 加 Builtin
- `internal/devops/pipeline/memory/store.go` + `pg/store.go`：实现 Update/Delete + Builtin 字段
- `internal/storage/pg/migrations/0023_pipeline_template_builtin.up.sql`：pipeline_templates 加 builtin 列 + 回填
- `internal/devops/pipeline/handler.go`：admin CRUD 端点 + Builtin 保护
- `cmd/core/main.go`：admin 路由注册 + OpenAPI 登记
- `frontend/console-admin/`：modules/pipeline-template/ + 菜单

## 顺序

先 B（模板 CRUD，结构化低风险）后 A（实时日志，架构改动 + 集群验证）。

## 不做项（YAGNI）

- 租户自定义模板（要租户隔离 + 可视化编辑器）。
- builtin 模板版本号 + 升级覆盖机制（dogfooding 留后续）。
- process/mock 模式构建中实时日志（要 builder 边构建边写 buffer，大改）。
- 模板 stage 可视化拖拽编辑器。
- 日志全文搜索/过滤（grep）。
