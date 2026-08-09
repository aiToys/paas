# 服务治理完善：实施计划

> spec: docs/superpowers/specs/2026-08-09-governance-route-host-and-service-config-design.md

## Task 列表

### T1: governance Route 加 Host（后端）
- [ ] `internal/governance/model.go`：Route 加 `Host string json:"host,omitempty"` + 注释（对外域名，空=不限）。
- [ ] `internal/governance/memory/store.go`：cloneRoute 深拷贝 Host；CreateRoute/UpdateRoute 接收 Host（UpdateRoute `ex.Host = r.Host`）。
- [ ] `internal/governance/pg/store.go`：chCols 加 host；CreateRoute/UpdateRoute Scan host；ListRoutes SELECT 加 host。
- [ ] `internal/storage/pg/migrations/`：新增 migration `0023_governance_route_host.up/down.sql`（ADD COLUMN host TEXT + IF NOT EXISTS）。
- [ ] `internal/governance/handler.go`：CreateRoute/UpdateRoute 接收 body.Host 透传（无需校验）。
- [ ] go test ./internal/governance/... 验证。

### T2: configcenter Namespace 加 ServiceID（后端）
- [ ] `internal/configcenter/model.go`：Namespace 加 `ServiceID string json:"serviceId,omitempty"` + 注释。
- [ ] `internal/configcenter/memory/store.go`：cloneNamespace 深拷贝 ServiceID；CreateNamespace/UpdateNamespace 接收；ListNamespaces 支持 serviceId 过滤。
- [ ] `internal/configcenter/pg/store.go`：chCols 加 service_id；CreateNamespace/UpdateNamespace/ListNamespaces Scan + WHERE service_id 过滤。
- [ ] migration `0024_configcenter_namespace_service.up/down.sql`（ADD COLUMN service_id TEXT + index）。
- [ ] `internal/configcenter/handler.go`：ListNamespaces 接收 `?serviceId=` query 透传 store。
- [ ] go test ./internal/configcenter/... 验证。

### T3: 前端 governance Route Host
- [ ] `frontend/console-user/.../governance` RouteFormDrawer：加 Host 输入 + 提示。
- [ ] Route List：加 Host 列。
- [ ] pnpm build 验证。

### T4: 前端 configcenter 关联 Service
- [ ] configcenter NamespaceFormDrawer：加「关联服务」select（调 GET /api/services 拉租户内 Service 列表）。
- [ ] configcenter List：加关联服务列。
- [ ] pnpm build 验证。

### T5: 前端 governance ServiceDetail 关联配置 section
- [ ] governance ServiceDetail：加「关联配置」section，调 GET /api/configcenter/namespaces?serviceId=<id> 聚合 + active 配置显示。
- [ ] pnpm build 验证。

### T6: 收尾
- [ ] go test ./... -race 全绿。
- [ ] commit（T1+T2 后端 / T3+T4+T5 前端 分批或合并）。
- [ ] deploy-k8s.sh 部署。
- [ ] e2e 验证（Route Host + configcenter serviceId 过滤 + ServiceDetail 关联配置显示）。
- [ ] CLAUDE.md 加「服务治理完善」章节。
- [ ] 记忆 ledger + memory。

## 顺序

T1 -> T2（后端独立，可并行）-> T3+T4+T5（前端，依赖后端字段）-> T6 收尾。

## 风险

- GLM 间歇不可用阻塞 go test/commit/deploy。应对：先写代码（Edit/Write 可用），GLM 恢复后批量验证 commit。
- 跨模块前端聚合：governance ServiceDetail 调 configcenter 端点，需 configcenter 端点先就绪（T2 顺序先于 T5）。
