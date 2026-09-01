# 模型推理（MaaS）

OpenAI 兼容 API 网关，不自建推理引擎，聚合第三方供应商：

## 三层模型

```
Provider（供应商预设：BaseURL + 凭证模板）
  └── Channel（通道：具体接入点，可多通道）
        └── Model（对外模型目录）
```

## 调用

完全 OpenAI 兼容：

```bash
curl -N -H "Authorization: Bearer <Key>" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.2","messages":[...],"stream":true}' \
  http://<平台地址>/v1/chat/completions
```

- stream + `reasoning_content` 透传 + stream usage 计量
- 请求级故障转移：通道 degraded / offline 自动切换

## 运维

- 模型 / 通道 / 供应商管理在 admin 后台（super_admin）
- 凭证走平台级 Secret（env 注入，不入库；未配凭证 503 降级不 panic）
- token 用量按租户 / 应用归因计量进计费
