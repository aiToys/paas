package maas

import "github.com/aitoys/paas/pkg/provider"

// 供应商 BaseURL（OpenAI 兼容端点；Provider 拼 baseURL + /chat/completions）。
// 真实 Key 不入库：部署后运维在「平台能力 → 安全」填写平台级 Secret，catalog 仅引用其 ID。
const (
	baseOpenAI    = "https://api.openai.com/v1"
	baseDeepSeek  = "https://api.deepseek.com"
	baseDashScope = "https://dashscope.aliyuncs.com/compatible-mode/v1"
)

// 平台级凭证 ID（由 internal/security/memory seed，全租户共享，值空占位）。
const (
	credOpenAI   = "sec-platform-openai"   //nolint:gosec // G101 误报：Secret ID 引用，非凭据
	credDeepSeek = "sec-platform-deepseek" //nolint:gosec
	credQwen     = "sec-platform-qwen"     //nolint:gosec
)

// catalog 返回 seed 模型目录，启动时由 MaaSPlugin 加载注册到 Gateway。
// resolver 用于真实供应商通道运行时解析 API Key（来自 security 平台级 Secret）；
// 为 nil 时（未注入）真实通道调用即返回 ErrCredentialMissing，仍可注册、可路由占位。
//
// 两类模型并存：
//   - 真实供应商模型（gpt-4o / qwen-plus / deepseek-*）：OpenAICompatibleProvider，配凭证后即真模型
//   - mock/echo 演示模型：进程内，开箱可演示路由/降级，不依赖外部凭证
func catalog(resolver provider.CredentialResolver) []*provider.Model {
	echo := EchoProvider{}
	models := []*provider.Model{
		// —— 真实第三方供应商模型（OpenAI 兼容协议）——
		mk("gpt-4o", "GPT-4o", "OpenAI", 128000,
			[]string{"chat", "vision", "reasoning"}, 2.5, 10, "OpenAI 旗舰多模态，推理与视觉强",
			realCh("gpt-4o#openai", 0, "openai", baseOpenAI, "gpt-4o", credOpenAI, resolver),
		),
		mk("gpt-4o-mini", "GPT-4o-mini", "OpenAI", 128000,
			[]string{"chat"}, 0.15, 0.6, "OpenAI 轻量高性价比",
			realCh("gpt-4o-mini#openai", 0, "openai", baseOpenAI, "gpt-4o-mini", credOpenAI, resolver),
		),
		mk("qwen-plus", "通义千问 Plus", "阿里云", 131072,
			[]string{"chat", "reasoning"}, 0.8, 2, "通义千问 Plus，中文场景主力",
			// 跨供应商容灾互备：DashScope 主 + DeepSeek 备
			realCh("qwen-plus#dashscope", 0, "qwen", baseDashScope, "qwen-plus", credQwen, resolver),
			realCh("qwen-plus#deepseek", 1, "deepseek", baseDeepSeek, "deepseek-chat", credDeepSeek, resolver),
		),
		mk("deepseek-chat", "DeepSeek Chat", "DeepSeek", 65536,
			[]string{"chat"}, 0.14, 0.28, "DeepSeek-V3，高性价比通用对话",
			realCh("deepseek-chat#deepseek", 0, "deepseek", baseDeepSeek, "deepseek-chat", credDeepSeek, resolver),
		),
		mk("deepseek-reasoner", "DeepSeek Reasoner", "DeepSeek", 65536,
			[]string{"reasoning"}, 0.55, 2.19, "DeepSeek-R1，深度思考与数理代码",
			realCh("deepseek-reasoner#deepseek", 0, "deepseek", baseDeepSeek, "deepseek-reasoner", credDeepSeek, resolver),
		),

		// —— mock/echo 演示模型（进程内，开箱可演示，不依赖凭证）——
		mk("echo-demo", "Echo 演示", "平台内置", 8192,
			[]string{"chat"}, 0, 0, "回显用户输入，开箱即可体验流式协议（无需配置供应商凭证）",
			ch("echo-demo#echo", 0, provider.StatusHealthy, echo),
		),
		mk("qwen2.5-7b", "Qwen2.5-7B-Instruct", "阿里云", 32768,
			[]string{"chat"}, 0.5, 1.5, "通义千问 7B（开源模型 mock 占位，自建 vLLM 后可替换为真实通道）",
			// 主备两通道，演示优先级路由与故障降级
			ch("qwen2.5-7b#mock-a", 0, provider.StatusHealthy, NewMockProvider("我是 Qwen2.5-7B，欢迎使用 PaaS 推理平台。这是 mock 主通道的流式回复。")),
			ch("qwen2.5-7b#echo-b", 1, provider.StatusHealthy, echo),
		),
		mk("bge-m3", "BGE-M3", "BAAI", 8192,
			[]string{"embedding"}, 0.07, 0, "BGE-M3 向量编码（embedding 模型，演示通道）",
			ch("bge-m3#mock-a", 0, provider.StatusOffline, NewMockProvider("（embedding 模型，演示通道）")),
		),
	}

	// airouter 精选真实模型（OpenAI 兼容 Bearer，需配 sec-platform-airouter Secret）。
	// 未配凭证时通道注册仍成功，调用返 ErrCredentialMissing（与直连供应商同款语义）。
	models = append(models, airouterCatalog(resolver)...)
	return models
}

// mk 构造一个挂单/多通道的模型。
func mk(id, name, vendor string, ctxWin int, caps []string, in, out float64, desc string, channels ...*provider.Channel) *provider.Model {
	return &provider.Model{
		ID: id, Name: name, Vendor: vendor, ContextWindow: ctxWin,
		Capabilities: caps, InputPrice: in, OutputPrice: out, Description: desc,
		Channels: channels,
	}
}

// ch 构造一个进程内通道（mock/echo）并绑定 Provider 实例。
func ch(id string, prio int, status string, p provider.Provider) *provider.Channel {
	c := &provider.Channel{ID: id, Type: p.Name(), Priority: prio, Status: status}
	c.SetImpl(p)
	return c
}

// realCh 构造一个第三方供应商通道（OpenAICompatibleProvider）。
// resolver 运行时解析 credentialRef 指向的平台级 Secret 明文；nil 时通道仍可注册，
// 调用时 Provider 返回 ErrCredentialMissing（Gateway 标 offline，用户得友好提示）。
func realCh(id string, prio int, vendor, baseURL, upstream, credRef string, resolver provider.CredentialResolver) *provider.Channel {
	p := NewOpenAICompatibleProvider(vendor, baseURL, upstream, credRef, resolver, nil)
	c := &provider.Channel{
		ID: id, Type: p.Name(), Priority: prio, Status: provider.StatusHealthy,
		Endpoint: baseURL, Vendor: vendor, UpstreamModel: upstream, CredentialRef: credRef,
	}
	c.SetImpl(p)
	return c
}
