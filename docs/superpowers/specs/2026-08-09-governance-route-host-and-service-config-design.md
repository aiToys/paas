# 服务治理完善：网关对外域名 + 服务配置中心关联

> 阶段：第三阶段「服务治理完善」
> 承接：L1+L2 流水线（deploy/release 分离 + 泳道联调）+ 流水线完善（模板 CRUD + 构建实时日志）

## 背景

用户反馈两个服务治理 gap：

1. **网关处配置对外域名**：现有 governance Route 只有 Path + ServiceID 匹配，无法按域名路由（一个平台多租户/多应用共用同一 ingress，需按 `app.example.com` 路由到不同服务）。
2. **服务的配置在配置中心显示**：configcenter 配置与 governance Service 脱节，开发者看服务详情时看不到该服务相关的动态配置，需跳配置中心手动找。

## 设计

### 需求 1：governance Route 加 Host 字段（对外域名）

**行业对标**：Kong Route（hosts[] + paths[] + service_id）、APISIX Route（host + uri + upstream_id）、Envoy VirtualHost（domains + routes）。域名是路由的一个匹配维度，不是独立实体。故 Route 加 Host 字段，不新增「对外域名」实体（YAGNI）。

- `governance.Route` 加 `Host string json:"host,omitempty"`（对外域名，空=不限 Host，按任意 Host 都可匹配此路由）。
- 匹配语义：Route 匹配时 Host 非空则要求请求 `Host` 头匹配（数据面 SDK/zeus 路由匹配消费，本期控制面只存配置不做实际转发）。
- 多 Host 暂用逗号分隔（`a.com,b.com`），留后续支持通配（`*.example.com`）。
- 不改现有 Path/ServiceID/Methods 匹配逻辑，Host 是新增匹配维度。

**改动**：model + memory（Create/Update 复制 Host）+ pg（migration 加 host 列 + chCols）+ handler（Create/Update 接收 Host）+ 前端（RouteFormDrawer 加 Host 输入 + List 列）。

### 需求 2：configcenter.Namespace 关联 Service + 双向显示

**方案**：configcenter.Namespace 加 `ServiceID string`（可选关联，空=不关联服务），双向显示用「前端聚合」实现避免跨模块后端依赖：

- configcenter Namespace 表单加「关联服务」select（租户内 governance Service 列表）。
- configcenter handler `ListNamespaces` 支持 `?serviceId=` 过滤。
- governance Service 详情页前端调 `GET /api/configcenter/namespaces?serviceId=<id>` 聚合显示「关联配置」section（namespace 列表 + active 配置快照）。

**依赖倒置**：governance 不 import configcenter，前端聚合（KISS，避免跨模块后端耦合）。configcenter 不 import governance（只需 ServiceID 字符串字段，不解析）。

**改动**：
- configcenter model：Namespace 加 `ServiceID`。
- configcenter memory/pg：CreateNamespace/UpdateNamespace 接收 ServiceID；ListNamespaces 支持 `?serviceId=` 过滤。
- pg migration：configcenter_namespaces 加 service_id 列。
- configcenter handler：ListNamespaces 接收 serviceId query。
- 前端 configcenter：NamespaceFormDrawer 加「关联服务」select（调 governance /api/services 拉列表）；List 加关联服务列。
- 前端 governance：ServiceDetail 加「关联配置」section（调 configcenter /api/configcenter/namespaces?serviceId= + active 配置）。

## 横切正确性

- 多租户隔离：Route.Host 不影响 tenant 过滤（仍按 ctx tenant）；configcenter.Namespace.ServiceID 是租户内引用（governance Service 租户内唯一）。
- 跨租户 not found 不泄漏：configcenter ListNamespaces ?serviceId= 仍按 ctx tenant 过滤，跨租户 serviceId 返空。
- prod:write：Route 是逻辑配置不接 prod:write（与现有 Route 一致）；configcenter 不接 prod:write（与现有 configcenter 一致）。
- 幂等 migration：`ADD COLUMN IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS`。

## 不做（YAGNI）

- Route Host 通配符匹配（`*.example.com`）-- 留后续。
- Route 多 Host 数组化 -- 暂用逗号分隔字符串。
- 实际 ingress/hermes 配置下发 -- 数据面消费归后续。
- configcenter 配置变更通知 governance Service -- 留后续。
- 跨模块后端聚合端点 -- 前端聚合已满足，避免过度设计。

## 验证

- `go test ./internal/governance/... ./internal/configcenter/...` 全绿。
- e2e：创建 Route 带 Host -> 列表/详情显示；configcenter Namespace 关联 Service -> governance Service 详情显示关联配置。
- `make manifests` 无需（无 CRD 改动）。
