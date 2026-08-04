package maas

import (
	"strconv"
	"strings"

	"github.com/aitoys/paas/pkg/provider"
)

// airouter —— OpenAI 兼容协议 + Bearer 鉴权的 LLM 路由网关。
//
// 推理端点：POST https://airouter.ddmc-inc.com/api/v1/chat/completions
// 鉴权    ：Authorization: Bearer <api_key>（api_key 形如 airouter-xxx）
// 模型字段：model 用 internalModel（如 qwen-plus / glm-5.2）。
//
// airouter 内部已聚合百炼(dashscope)/千帆(qianfan)/豆包(doubao) 多供应商容灾与限流，
// 把它当作单一 OpenAI 兼容供应商接入，复用 OpenAICompatibleProvider（Bearer），
// 白嫖其容灾链路，无需各自申请百炼/千帆/豆包 API Key。
const (
	baseAirouter  = "https://airouter.ddmc-inc.com/api/v1"
	credAirouter  = "sec-platform-airouter"  //nolint:gosec // G101 误报：Secret ID 引用，非凭证明文
	vendorAirouter = "airouter"              // 预置供应商 ID（airouter 通道 VendorID 关联）
)

// AirouterVendor 返回 airouter 预置供应商（seed 时灌入 Vendor 表，admin 可见可改）。
// 12 个 airouter 模型通道的 VendorID 均指向它；admin 修改其 BaseURL/凭证后，
// 新创建通道选此供应商即带入（存量通道字段不自动同步，留后续）。
func AirouterVendor() *provider.Vendor {
	return &provider.Vendor{
		ID:            vendorAirouter,
		Name:          "airouter 网关",
		Type:          ProviderOpenAICompatible,
		BaseURL:       baseAirouter,
		CredentialRef: credAirouter,
		Description:   "聚合百炼/千帆/豆包多供应商容灾的 OpenAI 兼容网关，配单一 api_key 即全模型可用",
	}
}

// airSpec 是精选模型的静态数据（提炼自上游模型目录，启动即注册到模型市场）。
type airSpec struct {
	ID     string   // internalModel（调 airouter 推理时的 model 字段）
	Vendor string   // 厂商展示名（通义千问/智谱/Doubao/月之暗面/deepseek/通义万相）
	Ctx    string   // contextWindow 原始字符串（"131,072" 或空）
	Caps   []string // 能力（chat/reasoning/vision/image/embedding）
	In     float64  // 输入价（元/百万 token）
	Out    float64  // 输出价
	Desc   string   // 精简描述
}

// airouterModels 精选策略：覆盖多厂商 + 多模态（文本/推理/视觉/图片/向量），各取代表，
// 避免全量灌入。此列表即默认 catalog 的唯一内容（catalog() 直接返回 airouterCatalog），
// 配单一 sec-platform-airouter Secret 后全部真实可推理。
var airouterModels = []airSpec{
	// —— 通义千问（文本 + 长文本 + 视觉）——
	{ID: "qwen-turbo", Vendor: "通义千问", Ctx: "131072", Caps: []string{"chat", "reasoning"}, In: 0.3, Out: 3, Desc: "Qwen3-Turbo，轻量高性价比，高频对话"},
	{ID: "qwen3.6-plus", Vendor: "通义千问", Ctx: "", Caps: []string{"chat", "reasoning", "vision"}, In: 2, Out: 12, Desc: "Qwen3.6-Plus 新旗舰，agentic coding 与多模态增强"},
	{ID: "qwen-long", Vendor: "通义千问", Ctx: "10000000", Caps: []string{"chat"}, In: 0.5, Out: 2, Desc: "Qwen-Long，1000 万 token 超长上下文，长文档分析"},
	{ID: "qwq-plus", Vendor: "通义千问", Ctx: "131072", Caps: []string{"reasoning", "chat"}, In: 1.6, Out: 4, Desc: "QwQ-Plus 推理增强，数学代码达 R1 满血水平"},
	{ID: "qwen-vl-max", Vendor: "通义千问", Ctx: "131072", Caps: []string{"vision"}, In: 3, Out: 9, Desc: "Qwen-VL-Max 视觉语言旗舰，复杂视觉推理"},
	{ID: "qwen3-vl-flash", Vendor: "通义千问", Ctx: "256000", Caps: []string{"vision"}, In: 0.3, Out: 3, Desc: "Qwen3-VL-Flash，轻量视觉理解，图文/视频/定位"},
	// —— DeepSeek（通用，推理版走直连 catalog）——
	{ID: "deepseek-v3.1", Vendor: "deepseek", Ctx: "131072", Caps: []string{"chat", "reasoning"}, In: 4, Out: 12, Desc: "DeepSeek-V3.1 混合推理，思考/非思考双模式 + Agent"},
	// —— 智谱 / 月之暗面 / 豆包（国产旗舰）——
	{ID: "glm-5.2", Vendor: "智谱", Ctx: "", Caps: []string{"chat"}, In: 8, Out: 28, Desc: "GLM-5.2 开源旗舰，1M 上下文，长程工程与 Agent"},
	{ID: "kimi-k2.5", Vendor: "月之暗面", Ctx: "", Caps: []string{"chat", "reasoning", "vision"}, In: 4, Out: 21, Desc: "Kimi-K2.5 原生多模态，思考/Agent 全能"},
	{ID: "doubao-seed-1.6", Vendor: "Doubao", Ctx: "256000", Caps: []string{"chat", "reasoning", "vision"}, In: 4.8, Out: 24, Desc: "豆包 Seed-1.6，多模态 Agent + 深度思考"},
	// —— 图片生成 + 向量（非对话模态代表）——
	{ID: "wanx2.1-t2i-plus", Vendor: "通义万相", Ctx: "1", Caps: []string{"image"}, In: 2, Out: 2, Desc: "通义万相 2.1 文生图 Plus，高美感真实感"},
	{ID: "text-embedding-v4", Vendor: "通义千问", Ctx: "81920", Caps: []string{"embedding"}, In: 0.5, Out: 0.5, Desc: "通用向量 v4，多语言文本/多模态统一向量"},
}

// airouterCatalog 把精选模型映射为 provider.Model，每个挂一个 airouter 通道（复用 realCh，
// 标准 OpenAI 兼容 Bearer）。resolver 为 nil 时通道仍注册，调用返 ErrCredentialMissing
// （需运维在「平台能力→安全」配置 sec-platform-airouter Secret，值为 airouter api_key）。
func airouterCatalog(resolver provider.CredentialResolver) []*provider.Model {
	out := make([]*provider.Model, 0, len(airouterModels))
	for _, m := range airouterModels {
		out = append(out, &provider.Model{
			ID:            m.ID,
			Name:          m.ID,
			Vendor:        m.Vendor,
			ContextWindow: parseCtxWindow(m.Ctx),
			Capabilities:  m.Caps,
			InputPrice:    m.In,
			OutputPrice:   m.Out,
			Description:   m.Desc,
			Channels: []*provider.Channel{
				realCh(m.ID+"#airouter", 0, "airouter", baseAirouter, m.ID, credAirouter, vendorAirouter, resolver),
			},
		})
	}
	return out
}

// parseCtxWindow 解析 contextWindow 字符串（"131,072"→131072；空/非法→0）。
func parseCtxWindow(s string) int {
	s = strings.ReplaceAll(s, ",", "")
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
