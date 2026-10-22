---
name: onepaas-get-started
description: 帮助外部 Agent 连接用户指定的 OnePaaS 实例，核对可见应用和模型，并为后续平台操作确定范围。
license: Apache-2.0
metadata:
  version: 1.0.0
  runtime: external-agent
---

# OnePaaS 接入

本技能运行在有 HTTP 能力的外部 Agent，不能作为平台内部的纯指令 Skill 自动执行 API。

使用用户指定的 `PAAS_BASE` 和通过环境或客户端凭证管理器提供的 `PAAS_TOKEN`。不预置域名或演示 Key；连接目的地必须与用户配置一致，不向重定向后的其他主机转发凭证。

先读取该实例的 `/openapi.json` 获取当前契约，再按授权任务读取 `/api/applications`、`/api/environments` 或 `/api/models`。平台 CRUD 响应为 `{data:...}`，错误为 `{error:...}`；401/403 是认证或权限失败，不能显示为空资源。不要输出 token、带凭证的请求头或未经脱敏的配置。

列出实际返回的应用和环境 ID，后续写操作沿用用户指定的目标，不能默认选择列表首项。API Key 与浏览器登录会话的可用身份端点可能不同；身份无法确认时说明未知，不能仅凭一个会话端点拒绝就判定所有 API 不可用。

交付连接结果、已核对的访问范围、阻塞原因与下一步。未实际请求的能力标为未验证；本技能不创建应用、不触发构建、不部署。
