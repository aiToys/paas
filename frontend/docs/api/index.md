# API 概览

## 契约与认证

- 交互式参考：访问平台 `/api-docs`，使用 Scalar 填写凭证试调用。
- 机器可读契约：`/openapi.json`，由 route registry 生成；以目标部署版本返回的契约为准。

| 层 | 前缀 | 认证与权限 |
|---|---|---|
| 用户 API | `/api/*` | 会话 / API Key，另按操作检查角色与应用权限 |
| 模型 API | `/v1/*` | API Key，模型读取/推理权限 |
| 管理 API | `/api/admin/*` | 平台管理角色及相应领域权限 |
| 数据面 | `/dp/*` | API Key/数据面凭证与 `dp:read` |

## 响应契约

CRUD 成功通常为 `{"data": T}`，失败为 `{"error": "message"}`，500 错误脱敏。协议接口有例外：`/v1/*`、`/livez`、`/dp/*`、配置中心 published 发现接口返回各自协议；流式接口输出 SSE 或日志流。

## 调用示例

```bash
# 列应用
curl -H "Authorization: Bearer <Key>" http://<平台地址>/api/applications

# 应用可观测指标；需真实采集与对应后端
curl -H "Authorization: Bearer <Key>" \
  'http://<平台地址>/api/observability/metrics?targetType=app&targetId=<appID>'

# 预演流水线；检查环境、阶段与产物后再运行
curl -X POST -H "Authorization: Bearer <Key>" -H "Content-Type: application/json" \
  -d '{"branch":"<分支>"}' \
  http://<平台地址>/api/applications/<appID>/pipelines/<pipelineID>/preview

# 触发运行；/api/pipelineruns 集合入口用于 GET 查询运行列表
curl -X POST -H "Authorization: Bearer <Key>" -H "Content-Type: application/json" \
  -d '{"branch":"<分支>"}' \
  http://<平台地址>/api/applications/<appID>/pipelines/<pipelineID>/run
```

审批/人工验证使用运行的结构化 decisions 接口；webhook 投递记录支持查看与重放。具体参数见当前 OpenAPI，操作路径见[交付指南](../features/devops)。

## 写操作的并发与重试

- 泳道配置批量发布先取 `?preview=1` 的 `X-Config-Revision`，在请求体传 `expectedRevision`；版本冲突后重新预览。
- 备份恢复需要 `Idempotency-Key`（8–128 字符）；同一请求重试复用原值，相同键不能换恢复参数。
- 重启/重建等异步运维返回受理结果后，还需查询操作记录和实际运行态。

详见[配置中心](../features/configcenter)、[备份恢复](../features/backup)、[服务运行](../features/runtime)。
