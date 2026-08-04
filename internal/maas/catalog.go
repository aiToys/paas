package maas

import "github.com/aitoys/paas/pkg/provider"

// DeprecatedSeedModelIDs 列出已从默认 catalog 移除的旧 seed 模型 ID。
//
// 早期 catalog 混入两类「看起来可用但实际不可用」的模型：
//   - 直连供应商占位（gpt-4o / qwen-plus / deepseek-* 等）：需各自独立凭证
//     （OpenAI/DeepSeek/通义 Key），未配即返 ErrCredentialMissing，无法真实推理；
//   - mock/echo 演示（echo-demo / qwen2.5-7b / bge-m3）：进程内回显或假数据，
//     非真实模型推理。
//
// 现已统一收敛到 airouter 网关真实通道（配单一 sec-platform-airouter Secret 即全模型可用）。
// 启动 seed 时清理这些遗留记录（maas_channels 对 model_id FK CASCADE，自动连带清除），
// 保证模型市场「全部真实可用」。详见 catalog() 与 airouterCatalog()。
var DeprecatedSeedModelIDs = []string{
	"gpt-4o",
	"gpt-4o-mini",
	"qwen-plus",
	"deepseek-chat",
	"deepseek-reasoner",
	"echo-demo",
	"qwen2.5-7b",
	"bge-m3",
}

// catalog 返回 seed 模型目录，启动时由 MaaSPlugin 加载注册到 Gateway。
//
// 仅保留 airouter 网关真实模型（OpenAI 兼容 Bearer，配 sec-platform-airouter Secret
// 后即真实可推理；airouter 内部已聚合百炼/千帆/豆包多供应商容灾）。
//
// resolver 用于通道运行时解析 API Key（来自 security 平台级 Secret）；
// 为 nil 时（未注入）通道仍可注册、可路由占位，调用即返回 ErrCredentialMissing。
func catalog(resolver provider.CredentialResolver) []*provider.Model {
	return airouterCatalog(resolver)
}

// realCh 构造一个第三方供应商通道（OpenAICompatibleProvider）。
// vendorID 关联预设供应商（非空时 handler 可从 Vendor 解析回填，这里直接写入避免 seed 后丢失关联）。
// resolver 运行时解析 credentialRef 指向的平台级 Secret 明文；nil 时通道仍可注册，
// 调用时 Provider 返回 ErrCredentialMissing（Gateway 标 offline，用户得友好提示）。
func realCh(id string, prio int, vendor, baseURL, upstream, credRef, vendorID string, resolver provider.CredentialResolver) *provider.Channel {
	p := NewOpenAICompatibleProvider(vendor, baseURL, upstream, credRef, resolver, nil)
	c := &provider.Channel{
		ID: id, Type: p.Name(), Priority: prio, Status: provider.StatusHealthy,
		Endpoint: baseURL, Vendor: vendor, UpstreamModel: upstream, CredentialRef: credRef,
		VendorID: vendorID,
	}
	c.SetImpl(p)
	return c
}
