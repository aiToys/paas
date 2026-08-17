# 可观测大屏多维度改造设计（2026-08-17）

## 问题

主模块「平台能力 → 可观测」把查询维度锁死在单应用下拉（无选中即空），违反业界可观测平台共识（Datadog/Grafana/Jaeger/Kibana：**入口全局，维度是过滤器不是门槛**）。且与 `app-tabs/AppObservability.vue` 高度同构，主模块沦为应用 tab 复制品。

**确立 IA 原则**：应用详情 = 应用维度聚合；主模块 = 综合平台（租户全局视角 + 维度过滤下钻）。审计确认其余主模块（DevOps 值班台/ConfigCenter/Security/ServiceRegistry）已符合，唯一偏差是可观测。

## 设计

### 前端 `views/Observability.vue` 重构

页面结构（漏斗式：先看哪里红了 → 下钻）：

1. **维度过滤器条**（顶部）：`全部 / 环境 / 应用 / 数据服务`，默认「全部」。环境/应用/数据服务为级联下拉（选环境过滤应用列表）。
2. **告警总览**（置顶第一区块，入口级）：firing 告警按 severity 着色，点击告警 → 自动切到对应 target 维度（targetType+targetId 解析）。
3. **实体健康矩阵**（全部视图核心）：各应用 + 各数据服务一行卡片（CPU/内存/RPS 当前值 + sparkline + 副本就绪），异常置顶/高亮，点击卡片下钻切到该实体维度。
4. **指标卡 / 日志 / trace / TraceID 直查**：沿用现有区块，但查询参数跟随维度过滤器（全部=租户全局不带 appId/targetType；环境=该环境应用聚合）。
5. **告警规则创建**：解除 `targetType:'app'` 硬编码，支持 app/env/dataservice/workload。

### 后端 `internal/observability` 补齐

- `logs` 查询的 `targetType` 目前只特判 dataservice；补齐 workload/env 分支（与 metrics 的 targetType 语义对齐），空=全部（契约不变）。
- 零新端点：metrics/logs/traces/alerts 现有 API 已支持空参数=租户全局。

### 不做（YAGNI）

- 告警→下钻深度联动（时间窗/告警历史）——告警是即时评估无历史，留后续
- 自定义仪表板——Grafana 侧已预配面板兜底
- 其他主模块改造——已符合原则，不动

## 验证

- `make test`（observability memory store targetType 分支）
- 三套前端 `pnpm build`
- k8s 部署 e2e：全部视图有数据、维度切换/告警点击下钻、TraceID 直查不回归
