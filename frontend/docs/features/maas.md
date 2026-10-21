# 模型推理（MaaS）

平台聚合第三方供应商，不自建推理引擎。新版「模型目录」查看可用模型，Playground 试用；模型、通道和供应商由管理后台配置。

## 模型与接入关系

```text
Model（对外模型目录）
 ├─ Channel A（具体端点、上游模型、优先级与凭证引用）
 │   └─ 可引用 Vendor 预设（供应商 BaseURL、类型和凭证）
 └─ Channel B（另一个可用接入点）

Provider：运行时协议适配器，由 Channel 类型和配置构造
```

模型目录是平台级共享资源；调用身份、Key 与用量归属按租户管理。供应商预设不等于为任意模型自动提供可用通道。

## 聊天与流式推理

支持 OpenAI 风格的 chat completions、模型列表与 embeddings；具体参数、模型能力与响应以当前契约和通道为准，不能理解为兼容全部 OpenAI API。

```bash
curl -N -H "Authorization: Bearer <Key>" -H "Content-Type: application/json" \
  -d '{"model":"<可用聊天模型 ID>","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://<平台地址>/v1/chat/completions
```

聊天支持 SSE 与 `reasoning_content` 透传、usage 计量。候选通道按优先级选择，跳过 offline；可降级的请求失败会尝试其他候选。没有可用通道或凭证时按返回错误排查，不将模型出现在目录当作调用已验证。

## Embeddings 与智能体调用

```bash
curl -H "Authorization: Bearer <Key>" -H "Content-Type: application/json" \
  -d '{"model":"<可用向量模型 ID>","input":"需要向量化的文本"}' \
  http://<平台地址>/v1/embeddings
```

需选择具备向量能力的模型。平台智能体可通过聊天入口使用 `model="agent:<智能体ID>"`，由运行时解析提示词、技能、工具与知识库；所需依赖不可用时应先补齐依赖，不能把其失败当作普通模型文本结果。

## 凭证与计量

凭证通过平台级 Secret 托管，API 掩码展示；配置 `PAAS_SECRET_MASTER_KEY` 后加密持久化，调用时在服务端解析。开发模式未配主密钥时支持明文兼容；env 注入不代表 Secret 永不落库。

模型调用需 `model:infer`。应用归因需要绑定应用的 Key，租户级 Key 不会自动获得应用归因；智能体内部部分路径采用估算计量，用量和账单不等于供应商真实结算。见[账户说明](../guide/auth)与[平台管理](../admin/)。
