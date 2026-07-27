package maas

import "github.com/aitoys/paas/pkg/provider"

// catalog 返回 seed 模型目录，启动时由 MaaSPlugin 加载注册到 Gateway。
// 未来接 CRD/operator 时只改此加载源，三层抽象不动。
//
// 通道 Type 取 provider.Name()：mock / echo。
// 多通道模型用于演示优先级路由与故障降级（如 qwen2.5-7b 挂主备两通道）。
func catalog() []*provider.Model {
	echo := EchoProvider{}
	return []*provider.Model{
		mk("qwen2.5-7b", "Qwen2.5-7B-Instruct", "阿里云", 32768,
			[]string{"chat"}, 0.5, 1.5, "通义千问 7B，对话与中文场景均衡",
			// 主备两通道，演示 failover
			ch("qwen2.5-7b#mock-a", 0, provider.StatusHealthy, NewMockProvider("我是 Qwen2.5-7B，欢迎使用 PaaS 推理平台。这是 mock 主通道的流式回复。")),
			ch("qwen2.5-7b#echo-b", 1, provider.StatusHealthy, echo),
		),
		mk("qwen2.5-72b", "Qwen2.5-72B-Instruct", "阿里云", 131072,
			[]string{"chat", "reasoning"}, 4, 12, "通义千问 72B 旗舰，复杂推理与长文",
			ch("qwen2.5-72b#mock-a", 0, provider.StatusHealthy, NewMockProvider("Qwen2.5-72B 旗舰模型，擅长复杂推理与长上下文。")),
		),
		mk("deepseek-v3", "DeepSeek-V3", "DeepSeek", 65536,
			[]string{"chat", "reasoning"}, 2, 8, "DeepSeek-V3 MoE，高性价比推理",
			ch("deepseek-v3#mock-a", 0, provider.StatusHealthy, NewMockProvider("DeepSeek-V3（MoE 架构），推理能力强且成本可控。")),
		),
		mk("deepseek-r1", "DeepSeek-R1", "DeepSeek", 131072,
			[]string{"reasoning"}, 4, 16, "DeepSeek-R1，深度思考与数学代码",
			ch("deepseek-r1#mock-a", 0, provider.StatusDegraded, NewMockProvider("DeepSeek-R1 深度思考模型（当前通道降级中，仍可服务）。")),
		),
		mk("llama3.3-70b", "Llama-3.3-70B", "Meta", 131072,
			[]string{"chat"}, 1, 3, "Llama 3.3 70B，开源英文强通用",
			ch("llama3.3-70b#mock-a", 0, provider.StatusHealthy, NewMockProvider("Llama-3.3-70B，Meta 开源通用大模型。")),
		),
		mk("glm-4-9b", "GLM-4-9B-Chat", "智谱", 131072,
			[]string{"chat"}, 0.5, 1.5, "智谱 GLM-4 9B，中文对话友好",
			ch("glm-4-9b#mock-a", 0, provider.StatusHealthy, NewMockProvider("GLM-4-9B，智谱开源，中文对话体验佳。")),
		),
		mk("qwen2.5-coder-32b", "Qwen2.5-Coder-32B", "阿里云", 131072,
			[]string{"code"}, 0.7, 2, "Qwen2.5 Coder 32B，代码补全与生成",
			ch("qwen2.5-coder-32b#mock-a", 0, provider.StatusHealthy, NewMockProvider("Qwen2.5-Coder-32B，专注代码生成与补全。")),
		),
		mk("bge-m3", "BGE-M3", "BAAI", 8192,
			[]string{"embedding"}, 0.07, 0, "BGE-M3，通用向量编码",
			ch("bge-m3#mock-a", 0, provider.StatusOffline, NewMockProvider("（embedding 模型，本期通道离线占位）")),
		),
	}
}

// mk 构造一个挂单/多通道的模型。
func mk(id, name, vendor string, ctxWin int, caps []string, in, out float64, desc string, channels ...*provider.Channel) *provider.Model {
	return &provider.Model{
		ID: id, Name: name, Vendor: vendor, ContextWindow: ctxWin,
		Capabilities: caps, InputPrice: in, OutputPrice: out, Description: desc,
		Channels: channels,
	}
}

// ch 构造一个通道并绑定 Provider 实例。
func ch(id string, prio int, status string, p provider.Provider) *provider.Channel {
	c := &provider.Channel{ID: id, Type: p.Name(), Priority: prio, Status: status}
	c.SetImpl(p)
	return c
}
