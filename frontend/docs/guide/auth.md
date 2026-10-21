# 登录与 API Key

## 控制台登录

经典 `/console/` 与新版 `/next/` 采用密码登录、`httpOnly` cookie 会话；管理后台 `/admin/` 为平台管理入口，具体页面受角色权限限制。

- access token 15 分钟 / refresh token 7 天，cookie 使用 `SameSite=Lax`。
- 生产必须配置 `PAAS_JWT_SECRET`（至少 32 字节）、`PAAS_COOKIE_SECURE=true` 与 TLS。
- 登录按 IP/用户名限流；失败统一返回 401。

演示 seed 开启时有 `acme-admin`、`acme-dev`、`globex-admin`，密码 `123456`；`admin / 123456` 是演示平台管理员。生产必须设置 `PAAS_DISABLE_DEMO_SEED=true`（Helm `seed.demo=false`）。已有数据库应另行检查和处置历史演示账号，关闭 seed 不等于删除存量凭证。

生产账号由管理员在后台开通租户及用户；不要将演示密码用于实际部署。

## 创建 API Key

新版「组织与访问 → API 密钥」，经典版从 API Key 页面进入。Key 绑定租户、用户、角色，可选择应用归属。

```bash
curl -H "Authorization: Bearer <你的 Key>" http://<平台地址>/api/applications
```

创建时只能选择自己已有角色的子集。新 Key 的完整值在创建结果中保存，列表掩码展示；撤销后应更新调用方。绑定应用的 Key 才能将对应推理用量归因到该应用。

生产写操作需 `prod:write`，资源或能力还可能需要应用成员权限和专用操作权限；以接口返回为准。

## OpenAI 风格接口

`/v1/chat/completions`、`/v1/models`、`/v1/embeddings` 使用 API Key。可用能力取决于模型和通道配置；不要假定任意 OpenAI 接口或参数均支持。见[MaaS](../features/maas)。

平台管理员可设置 Key 到期、轮换和撤销，并撤销用户会话，见[平台管理](../admin/)。
