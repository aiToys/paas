# MaaS 对接第三方供应商设计

> 状态：设计稿（待评审）｜日期：2026-07-28｜作者：The PaaS Authors
> 关联：[[maas-platform-foundation-design]]（MaaS 基座）、[[platform-modules-blueprint]]

## 1. 背景与动机

当前 MaaS 切片已打通控制面链路（Gateway 鉴权 + OpenAI 兼容 SSE + Token 计量 + 按通道路由 + 多租户隔离），但 8 个模型通道全部是 `MockProvider`/`EchoProvider`，请求返回的是预设文本，**不是真实推理**。

本切片把通道替换为**真实第三方供应商**：平台作为统一接入网关，转发请求到 OpenAI / DeepSeek / 通义千问等云端推理服务，把流式响应回传用户。

**不自建推理集群、不部署 vLLM**（轻资产路线）——自建 vLLM 需 GPU（7B 模型即占 16GB+ 显存，72B 需多卡 A100）、重运维、闲置烧钱，是云厂商（Together/Fireworks/硅基流动）的赛道。开源 PaaS 起步期把推理外包给供应商，平台聚焦网关/计量/计费/多租户/容灾，零启动成本、开箱可用。

**产品方向修正**：本设计替代 `maas-product-direction` 记忆中的「② 真实 vLLM 纳管 + K8s 部署闭环」——改为聚合第三方供应商。

## 2. 目标与非目标

### 目标

- Playground 打的字由**真模型**回复（非 mock）。
- **一个 `OpenAICompatibleProvider` 适配器覆盖三家**：OpenAI / DeepSeek / 通义千问（DashScope OpenAI 兼容模式），三者协议同源。
- 平台托管供应商凭证，复用 `internal/security` 模块（KMS 抽象 + 掩码 + 审计），新增「平台级」Scope。
- 平台预置模型目录（seed 声明「供应商 × 模型」清单）。
- 同模型多供应商**容灾降级**：主通道失败自动切备通道。

### 非目标（YAGNI，本期不做）

- 不自建推理集群 / 不部署 vLLM / 不接 GPU。
- 不做租户自带 Key（BYOK）—— 租户自带通道归后续。
- 不做用户自建通道（运维管理通道，用户只读消费）。
- 不做真实计费扣费（Token 计量已有；按供应商成本出账归 `billing` 后续切片）。
- 不做凭证加密存储（KMS/Vault 后续；本期明文存储 + API 掩码，与现有 security 一致）。
- 不做 SSE 之外的特性（function call / vision / tool_use）—— 契约 `ChatRequest` 仅含 text messages，后续按需扩展。

## 3. 架构与数据流

```
用户请求 /v1/chat/completions
  → Gateway: API Key 鉴权 + 注入身份（已有，零改动）
  → Model.Resolve(): 按通道 Priority 取首个非 offline 通道（已有）
  → Channel.Impl().Chat(ctx, req)  ← 本切片核心
      ├─ mock/echo 通道: 原样保留（演示 + 回退）
      └─ OpenAICompatibleProvider.Chat()
           ① CredentialResolver.Resolve(credentialRef) → 明文 Key（仅内存）
           ② POST {baseURL}/chat/completions  (stream=true, model=upstreamModel)
              Authorization: Bearer {Key}
           ③ 逐行解析 SSE `data: {...}` → provider.Chunk 流
           ④ 上游错误分类 → sentinel error（Gateway 据此降级）
  → 失败 → MarkChannelStatus(degraded) → 切下一通道重试
  → 成功 → 流式回传 + Token 计量（已有）
```

**契约不变**：`provider.Provider.Chat(ctx, ChatRequest) (<-chan Chunk, error)` 零改动，Gateway 全程无感。本切片是「换通道 impl」+「通道配置数据化」。

## 4. 数据模型变更

### 4.1 Channel 扩展（`pkg/provider/model.go`）

现有 `Channel` 已有 `Endpoint` 字段（原注释「未来 vllm 远端地址」）——复用为供应商 BaseURL。新增两个字段：

```go
type Channel struct {
    ID       string `json:"id"`
    Type     string `json:"type"`               // echo/mock/openai-compatible
    Priority int    `json:"priority"`
    Status   string `json:"status"`             // healthy/degraded/offline
    Endpoint string `json:"endpoint,omitempty"` // 供应商 BaseURL（如 https://api.deepseek.com）
    // 新增
    UpstreamModel string `json:"upstreamModel,omitempty"` // 供应商侧模型名（如 deepseek-chat / qwen-plus / gpt-4o）
    CredentialRef string `json:"credentialRef,omitempty"` // 凭证引用（指向 security 平台级 Secret 的 ID）
    impl Provider `json:"-"`
}
```

- mock/echo 通道这三个字段为空（向后兼容）。
- `Type="openai-compatible"` 的通道，注册时由插件构造 `OpenAICompatibleProvider` 绑定到 `impl`，Provider 从 Channel 持有 baseURL/upstreamModel/credentialRef。

### 4.2 平台级 Secret（`internal/security/model.go`）

现有 `Secret` 是租户私有（有 `TenantID`，Repository 强制租户过滤）。供应商凭证需**全租户共享**，加 `Scope` 字段：

```go
type Secret struct {
    ID, TenantID, Name, Type, Value, Desc string
    Scope string `json:"scope"` // 新增：tenant（默认）| platform（全租户共享，TenantID 空）
    ...
}
```

- `Scope="platform"` 的 Secret：`TenantID=""`，Repository 查询时**平台级不按租户过滤**（admin 可见、所有租户可用）。
- `Scope="tenant"`（默认）：现有行为不变。
- 权限：平台级 Secret 仅 `tenant-admin` 可 CRUD（新增 `platform-secret` 权限或复用 `security:write` + Scope 校验）。

**选型理由（DRY）**：复用 security 已有的掩码/审计/校验逻辑，避免新建 `ProviderCredential` 实体重复造 KMS 轮子。

## 5. 组件设计

### 5.1 `OpenAICompatibleProvider`（新增 `internal/maas/openai_compatible.go`）

```go
// OpenAICompatibleProvider 对接所有 OpenAI 兼容协议的供应商
// （OpenAI / DeepSeek / 通义千问 DashScope 兼容模式）。
type OpenAICompatibleProvider struct {
    baseURL       string            // 如 https://api.deepseek.com
    upstreamModel string            // 如 deepseek-chat
    credentialRef string            // security Secret ID
    resolver      CredentialResolver // 运行时解析明文 Key
    http          *http.Client      // 可注入，测试用 httptest
    vendor        string            // 展示用（openai/deepseek/qwen）
}

func (p *OpenAICompatibleProvider) Name() string { return "openai-compatible" }

func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.Chunk, error) {
    // ① 解析凭证（缺失 → ErrCredentialMissing，通道标 offline）
    apiKey, err := p.resolver.Resolve(p.credentialRef)
    if err != nil { return nil, ErrCredentialMissing }
    // ② 构造 OpenAI 兼容请求体，POST baseURL/chat/completions stream=true
    // ③ 起 goroutine 逐行解析 SSE，转 Chunk 写入 channel
    // ④ 上游 HTTP 错误分类返回 sentinel（见 5.3）
}
```

**实现要点**：
- SSE 解析：按行扫描 `data: {json}`，提取 `choices[0].delta.content`；遇到 `data: [DONE]` 关闭 channel。
- goroutine 内 `ctx.Done()` 中断（与 Gateway openai.go 的 ctx 感知一致，避免断连后继续解析）。
- HTTP 客户端超时（建议 120s，流式长连接）+ `http.Client` 可注入便于测试。

### 5.2 `CredentialResolver`（依赖倒置，定义在 `internal/maas`）

```go
// CredentialResolver 解析平台级 Secret 明文（仅内存，不日志）。
// 由 security.Repository 在 cmd/core 注入实现。
type CredentialResolver interface {
    Resolve(credentialRef string) (plaintext string, err error)
}
```

- maas 包不直接 import security（解耦），定义接口，`cmd/core` 注入 security store 的适配实现。
- 解析失败（凭证被删/未配置）→ `ErrCredentialMissing`，Provider 返回该 sentinel，Gateway 标通道 offline 并 fallback。

### 5.3 错误分类（驱动降级决策）

`OpenAICompatibleProvider` 把上游错误映射为 sentinel，Gateway 据此决定降级级别：

| 上游状态 | sentinel | 通道状态 | 处理 |
|---------|----------|---------|------|
| 凭证缺失/401/403 | `ErrCredentialInvalid` | offline | 需运维修，不重试本通道 |
| 429 限流 | `ErrUpstreamRateLimit` | degraded | 切备通道 |
| 5xx / 网络超时 | `ErrUpstreamUnavailable` | degraded | 切备通道 |
| 4xx（非鉴权，如模型不存在） | `ErrUpstreamConfig` | offline | 配置错误，不重试 |

## 6. 降级容灾

- **已有**：`Model.Resolve()` 按优先级取首个非 offline；`MarkChannelStatus` 被动降级。
- **本切片补全（请求级 failover）**：Gateway `ChatCompletions` 首通道返回降级类 sentinel 时，**自动取下一通道重试**（当前是「本次失败、下次绕开」，本切片升级为「本次就 failover」）。全部通道失败才报 503。
- offline 类错误不重试（配置/凭证问题，重试无意义）。

## 7. 模型目录 seed（平台预置）

`catalog()` 改造为 `catalog(resolver CredentialResolver)`，真实通道构造 `OpenAICompatibleProvider`，演示模型保留 mock：

| 平台模型 ID | 通道 | 供应商 | upstreamModel | 优先级 | 备注 |
|------------|------|--------|---------------|--------|------|
| `gpt-4o` | `gpt-4o#openai` | OpenAI | `gpt-4o` | 0 | |
| `gpt-4o-mini` | `gpt-4o-mini#openai` | OpenAI | `gpt-4o-mini` | 0 | |
| `qwen-plus` | `qwen-plus#dashscope` | 通义千问 | `qwen-plus` | 0 | |
| `qwen-plus` | `qwen-plus#deepseek` | DeepSeek | `deepseek-chat` | 1 | 跨供应商容灾互备 |
| `deepseek-chat` | `deepseek-chat#deepseek` | DeepSeek | `deepseek-chat` | 0 | |
| `deepseek-reasoner` | `deepseek-reasoner#deepseek` | DeepSeek | `deepseek-reasoner` | 0 | R1 推理 |

> `qwen-plus` 挂两通道演示跨供应商容灾（你选的「降级/容灾」）。其余先单通道。

**保留 mock 通道**：1-2 个演示模型（如 `echo-demo`）仍挂 `EchoProvider`，保证未配置任何供应商 Key 时平台开箱可演示，不报错。

## 8. 安全考量

- 凭证明文**仅内存**：Provider 运行时经 Resolver 解析，不写日志、不进响应、不持久化到 Channel。
- security `List/Get/Create` 返回掩码（已有 `Masked()`），平台级 Secret 同样掩码。
- 凭证增删改自动记审计（已有 `RecordAudit`）。
- 凭证缺失时通道 offline + 明确错误（`ErrCredentialMissing`），绝不把明文 Key 暴露给前端或日志。

## 9. 开源交付形态

- **适配器代码全开源**（Apache 2.0），无第三方 SDK 依赖（纯 `net/http` + SSE 解析）。
- **不含真实 Key**：用户部署后自行在「平台能力 → 安全」录入供应商 Key，绑定到通道。
- **未配置凭证**：真实通道 offline，Playground 自动 fallback 到 mock/echo 演示模型（前端标注「未配置，演示中」）。

## 10. 测试策略

- **`OpenAICompatibleProvider` 单测**（核心）：`httptest.Server` 模拟上游 SSE 流，验证：
  - 正常流式解析（多 chunk 拼接 == 期望文本，`[DONE]` 正确关闭）。
  - 错误分类（401→`ErrCredentialInvalid`、429→rateLimit、500→unavailable、超时→unavailable）。
  - `ctx.Done()` 中断（取消后 channel 关闭，无泄漏）。
  - 凭证缺失→`ErrCredentialMissing`。
- **CredentialResolver 适配单测**：平台级 Secret 解析（TenantID 空可读）。
- **Catalog seed 单测**：真实通道 baseURL/upstreamModel/credentialRef 正确填充。
- **Gateway failover 集成测试**：主通道返回 `ErrUpstreamUnavailable` → 自动切备通道成功。
- **回归**：现有 mock 通道 + 全部既有测试零改动通过。

## 11. 文件结构

**新增**：
- `internal/maas/openai_compatible.go` —— OpenAICompatibleProvider + CredentialResolver 接口 + 错误 sentinel。
- `internal/maas/openai_compatible_test.go` —— httptest 驱动的流式/错误单测。

**修改**：
- `pkg/provider/model.go` —— Channel 加 `UpstreamModel` / `CredentialRef` 字段。
- `pkg/plugin/plugin.go` —— `CoreDeps` 加 `SecretResolver()` 注入点。
- `internal/security/model.go` —— Secret 加 `Scope` 字段 + 常量 `ScopePlatform`/`ScopeTenant`。
- `internal/security/memory/store.go` —— 平台级 Secret 查询不按租户过滤。
- `internal/security/handler.go` —— 平台级 Secret 权限校验（仅 admin）。
- `internal/maas/catalog.go` —— seed 真实通道（OpenAICompatibleProvider）+ 保留演示 mock。
- `internal/maas/plugin.go` —— `Init` 从 CoreDeps 取 resolver，传入 `catalog(resolver)`。
- `cmd/core/main.go` —— 注入 security store 作 CredentialResolver + SecretResolver。
- `internal/core/gateway/openai.go` —— 请求级 failover（首通道降级类错误→切下一通道）。

## 12. 风险与开放问题

1. **DashScope 兼容端点稳定性**：通义千问 OpenAI 兼容模式（`dashscope.aliyuncs.com/compatible-mode/v1`）为后加，需验证 stream 格式与标准 OpenAI SSE 完全一致。spec 落地时实测确认。
2. **凭证注入循环依赖**：maas 需 CredentialResolver，security 是独立插件。通过 `CoreDeps` 注入（接口定义在 maas，实现在 security/cmd/core）破环。需确认插件 Init 顺序（security 先于 maas）。
3. **流式 HTTP 长连接的资源占用**：高并发下每个推理请求占一条长连接。本期 KISS 不做连接池调优，后续可观测切片监控。
4. **平台级 Secret 权限模型**：是否新建 `platform-secret` 权限粒度，还是复用 `security:write` + Scope 校验？倾向后者（YAGNI，避免权限爆炸），spec 落地时定。
