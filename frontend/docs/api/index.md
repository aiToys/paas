# API 概览

## 交互式文档

完整 API 契约（OpenAPI 3.0）由平台 route registry 单一真源生成：

- **交互式文档**：`/api-docs`（Scalar，可在线试调用）
- **机器可读**：`/openapi.json`

## API 分层

| 层 | 前缀 | 权限 | 说明 |
|----|------|------|------|
| 用户 API | `/api/*` | 会话 / API Key | 控制台后端 |
| OpenAI 兼容 | `/v1/*` | API Key | chat completions / models |
| 管理员 API | `/api/admin/*` | super_admin | 跨租户总览与平台配置 |
| 数据面 | `/dp/*` | DP token | 实例发现 |

## 响应契约

- 成功：`{"data": T}`
- 失败：`{"error": "message"}`；500 脱敏不泄漏内部细节
- 例外（裸 JSON）：`/v1/*`（OpenAI 协议）、`/livez`、`/dp/*`

## 示例

```bash
# 列应用
curl -H "Authorization: Bearer <Key>" http://<平台地址>/api/applications

# 查某应用可观测指标
curl -H "Authorization: Bearer <Key>" \
  "http://<平台地址>/api/observability/metrics?targetType=app&targetId=<appID>"

# 触发流水线
curl -X POST -H "Authorization: Bearer <Key>" -H "Content-Type: application/json" \
  -d '{"pipelineId":"<ID>","branch":"main"}' \
  http://<平台地址>/api/pipelineruns
```
