---
name: onepaas-runtime-diagnose
description: 帮助外部 Agent 使用 OnePaaS 工作负载、事件、日志与链路证据定位运行故障，默认只读调查。
license: Apache-2.0
metadata:
  version: 1.0.0
  runtime: external-agent
---

# OnePaaS 运行排障

使用用户指定的实例和凭证，先读取 `/openapi.json` 核对当前端点及查询参数。凭证来自环境或客户端管理器，不输出敏感请求头，不向跨主机重定向发送凭证。

围绕用户给出的应用、工作负载、环境、泳道和时间范围调查。可先读 `/api/workloads` 找到精确对象，再按当前契约读取实例、事件和日志；有 TraceID 时优先 `GET /api/observability/traces/{traceID}` 直查。不要跨租户推测资源是否存在，不因同名而混合测试与生产数据。

将错误发生在哪个实例、哪个时间和调用链步骤写清楚。流量泳道标记不是实例部署证据，下游路由命中也不是当前 span 所在实例。未上报的身份、标签、指标保持未知；Pod Running 不等于 Ready。

默认只读输出证据、假设及下一步检查。重启、重建、扩缩容和回滚仅在具体操作获得授权后执行，继续遵守服务端权限、目标和原因参数，并查询操作记录确认生效。不根据请求受理或 HTTP 200 宣称业务已恢复。
