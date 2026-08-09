# 泳道联调 L2 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development。Steps 用 checkbox `- [ ]`。

**Goal:** 跨泳道服务发现（feature 泳道优先、缺失降级 default）+ x-paas-lane 流量染色，让测试环境「变更服务在 feature、未变更服务降级 default」真正联调。

**Architecture:** 数据面 `/dp/instances?service=x&lane=feature-x` 复用 L1 的 Service 命名派生 lane（`<app>-svc-<lane>` vs default `<app>-svc`，方案 A 零额外 label），优先查 feature 泳道 Endpoints 缺失降级 default。SDK 从请求 `x-paas-lane` header 取 lane 存 ctx，GetService 透传 lane，outgoing HTTP 注入 header 链式染色。

**Tech Stack:** Go（controller-runtime/k8s clientset + dataplane net/http + sdk/paas-registry）+ Vue 3 前端。

**Spec:** `docs/superpowers/specs/2026-08-09-lane-federation-l2-design.md`

## Global Constraints
- Go 主语言；中文注释；多租户隔离（DB/K8s label 强制 tenant）；main 分支 SDD commit。
- 方案 A（Service 名派生 lane）：复用 L1 命名 `<app>-svc-<lane>`，**不打额外 lane label 用于发现**（lane label 仅作前端/governance 分组补充）。
- 向后兼容：lane 空/default = 现状（返 default 实例）。
- 集群外（无 clientset）降级返空/默认，不 panic。

---

## Task 1: dataplane Instances 加 lane 降级发现

**Files:**
- Modify: `internal/dataplane/endpoints.go`（Instances 签名 + 降级逻辑）
- Test: `internal/dataplane/endpoints_test.go`

**Interfaces:**
- Produces: `EndpointsReader.Instances(ctx, namespace, serviceName, lane string)`；lane 非 default 时先查 `<service>-<lane>` Endpoints，无/空降级 `<service>`。

**Steps:**
- [ ] 写失败测试：fake clientset 两个 Endpoints（`user-svc` default + `user-svc-feature-x`），断言 lane=feature-x 返 feature 实例、lane=feature-y（无）降级 default、lane=default/default 返 default。
- [ ] 改 `Instances` 签名加 `lane string`。实现：lane!="" && lane!=LaneDefault → 先 Get `<service>-<lane>` Endpoints，isNotFound 或空 addresses 则 Get `<service>`；否则 Get `<service>`。
- [ ] handler 调用点同步传 lane（Task 2）。

## Task 2: /dp/instances handler 加 lane query

**Files:**
- Modify: `internal/dataplane/handler.go`（serveInstances 读 lane query 传 Instances）

**Interfaces:**
- `GET /dp/instances?service=x&lane=feature-x` → Instances(ctx, ns, service, lane)。

**Steps:**
- [ ] serveInstances 读 `r.URL.Query().Get("lane")`，传 Instances。
- [ ] 测：query 带 lane 透传到 reader。

## Task 3: sdk/paas-registry GetService 从 ctx 取 lane + lane middleware

**Files:**
- Modify: `sdk/paas-registry/registry.go`（GetService 取 ctx lane + URL 带 lane）、新增 `sdk/paas-registry/lane.go`（LaneMiddleware + ctx key + OutgoingLaneHeader helper）

**Interfaces:**
- `WithLane(ctx, lane)`/`LaneFromCtx(ctx)`；`LaneMiddleware(http.Handler)` 从 `x-paas-lane` header 取 lane 存 ctx；`ApplyLaneHeader(ctx, req)` outgoing 注入 header。
- GetService: `lane := LaneFromCtx(ctx); GET /dp/instances?service=x&lane=<lane>`。

**Steps:**
- [ ] lane.go：ctx key + WithLane/LaneFromCtx + LaneMiddleware（incoming header→ctx）+ ApplyLaneHeader（ctx→outgoing header）。
- [ ] registry.go GetService 取 lane 加 URL query。
- [ ] 测：middleware 提取 header、GetService 传 lane、ApplyLaneHeader 注入。

## Task 4: governance Instance.LaneID 启用

**Files:**
- Modify: `internal/governance/model.go`（Instance.LaneID 注释改「启用」）、`internal/dataplane/endpoints.go`（endpointsToInstances 派生 lane 从 Service 名）、handler/前端列表按 lane。

**Interfaces:**
- Instance.LaneID 从 Service 名派生（`<app>-svc-<lane>` → lane，`<app>-svc` → default）。

**Steps:**
- [ ] endpointsToInstances 加 lane 派生（从 serviceName 反解 lane 后缀）。
- [ ] governance 列表/发现支持 lane 过滤（?lane=）。

## Task 5: reconciler Workload lane label（前端/governance 分组用）

**Files:**
- Modify: `internal/controller/workload_controller.go`（labelsFor 加 `paas.aitoys/lane`）、applier。

**Interfaces:**
- Deployment/Service/Pod label 加 `paas.aitoys/lane=<lane>`（default/feature-x）。

**Steps:**
- [ ] labelsFor 加 lane（从 Workload.Spec.LaneID 取，空=default）。
- [ ] 测：label 含 lane。

## Task 6: gateway 入口染色（x-paas-lane 注入）

**Files:**
- Modify: `internal/core/gateway/`（ReverseProxy 注入 x-paas-lane header 按目标 lane）

**Interfaces:**
- gateway 转发到 upstream 时，按请求 lane（query/header）注入 x-paas-lane。

**Steps:**
- [ ] gateway 按目标 lane 注入 header（Playground/测试触发器带 lane）。

## Task 7: 前端 Workload 按 lane 分组 + lane 选择

**Files:**
- Modify: `frontend/console-user/src/views/Environments.vue`（详情页 Workload 按 lane 分组）、`ApplicationDetail.vue` 部署 tab、`Playground.vue`（lane 选择）。

**Steps:**
- [ ] 环境详情页 Workload 按 lane 分组渲染（default 基线 + feature 泳道）。
- [ ] Playground/测试触发 lane 选择（请求带 x-paas-lane）。

## Task 8: e2e 验证 + 部署

**Steps:**
- [ ] `go test ./...` + `pnpm build` 全绿。
- [ ] deploy-k8s.sh。
- [ ] e2e：paas-shop feature-x 泳道（CI deploy）+ 测试触发 x-paas-lane:feature-x → 变更服务发现 feature 实例、未变更降级 default。
- [ ] CLAUDE.md 加 L2 章节 + 记忆。

---

## L3 留后续（本 plan 不实现）
- 全链路 mesh（Istio/zeus sidecar 自动染色，应用零改动）。
- EndpointSlice + lane label selector（方案 B）。
- 泳道自动回收（baseline 触发/闲置 TTL）。
- 拓扑可视化。
