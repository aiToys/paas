# 登录与 API Key

## 控制台登录

console-user 走密码登录，`httpOnly` cookie 会话：

- access token 15 分钟 / refresh token 7 天（`SameSite=Lax`）
- 生产强制 JWT secret ≥ 32 字节 + `PAAS_COOKIE_SECURE=true`（配 TLS 后）
- 登录限流（per-IP / per-username）+ 防账号枚举（统一 401）

## API Key

「个人设置 → API Key」自助管理，Key 绑定（租户 × 用户 × 角色）：

```bash
curl -H "Authorization: Bearer <你的 Key>" http://<平台地址>/api/applications
```

- 自助创建只能选**自己已有角色**的子集（零提权）
- 敏感操作按角色权限拦截；生产写需 admin 角色
- Key 全程掩码显示，日志脱敏

## OpenAI 兼容调用

`/v1/*` 端点同样走 API Key 鉴权，支持 chat completions（含 stream + reasoning 透传）、models 列表；token 用量按应用归因进计费。
