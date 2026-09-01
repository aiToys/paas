# 可观测

三支柱 + 告警，全部多租户隔离（PromQL / LogQL / trace 均带租户过滤）：

## 指标（Prometheus）

- 应用级查询按工作负载 Pod 正则自动聚合
- cAdvisor 容器指标 + 依赖资源（数据服务 / 中间件）指标

## 日志（Loki）

- 按应用 / 级别 / 关键字 / 泳道过滤
- **trace 关联**：`?traceId=` 直查某次请求的全部日志

## 链路（Jaeger）

- trace 按 service.name 查询；瀑布树形可视化
- 错误 span 自动记录 exception 类型 / 消息 / 堆栈
- GenAI 语义约定（gen_ai.*）：智能体调用链含 token usage

## 告警

- 规则持久化（PG），评估引擎后台 30s tick
- 状态机 pending（防毛刺）→ firing（webhook 出站）→ resolved
- **重启不丢状态**；firing / resolved 历史事件可回看
- 漏斗式下钻：告警总览 → 实体健康矩阵 → 指标 / 日志 / trace
