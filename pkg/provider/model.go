package provider

// Model 是逻辑模型：模型市场的展示主体与路由键。
// 一个 Model 可挂多个 Channel（推理实例），由 Gateway 按通道优先级与健康状态路由。
type Model struct {
	ID            string     `json:"id"`            // 路由键，如 "qwen2.5-7b"
	Name          string     `json:"name"`          // 展示名
	Vendor        string     `json:"vendor"`        // 供应商
	ContextWindow int        `json:"contextWindow"` // 上下文长度（token）
	Capabilities  []string   `json:"capabilities"`  // chat/embedding/vision/tool/reasoning/code
	InputPrice    float64    `json:"inputPrice"`    // 元/百万 token
	OutputPrice   float64    `json:"outputPrice"`
	Description   string     `json:"description,omitempty"`
	Channels      []*Channel `json:"channels"`
}

// Channel 是模型的某个推理实例通道。
// impl 持有实际 Provider，不参与 JSON 序列化。
type Channel struct {
	ID       string `json:"id"`                 // 如 "qwen2.5-7b#mock-a"
	Type     string `json:"type"`               // echo/mock/openai-compatible
	Priority int    `json:"priority"`           // 数字越小优先级越高
	Status   string `json:"status"`             // healthy/degraded/offline
	Endpoint string `json:"endpoint,omitempty"` // 供应商 BaseURL（如 https://api.deepseek.com）
	Vendor   string `json:"vendor,omitempty"`   // 供应商展示名（openai/deepseek/qwen，观测用）
	// 第三方供应商通道配置（mock/echo 通道为零值）。
	UpstreamModel string   `json:"upstreamModel,omitempty"` // 供应商侧模型名（deepseek-chat / qwen-plus / gpt-4o）
	CredentialRef string   `json:"credentialRef,omitempty"` // 凭证引用（security 平台级 Secret ID）
	// VendorID 关联预设供应商（Vendor.ID）：非空时由 handler 从 Vendor 解析 BaseURL/CredentialRef/Vendor
	// 回填到本通道字段（「选供应商自动带入」，免去每次创建通道手填 BaseURL+凭证）。
	// 为空时通道仍可用自身的 Endpoint/CredentialRef 直填（向后兼容）。
	VendorID      string   `json:"vendorId,omitempty"`
	impl          Provider `json:"-"`                       // 实际执行者
}

// Impl 返回通道绑定的 Provider。
func (c *Channel) Impl() Provider { return c.impl }

// SetImpl 绑定通道的实际 Provider（注册时由插件调用）。
func (c *Channel) SetImpl(p Provider) { c.impl = p }

// 通道健康状态常量。
const (
	StatusHealthy  = "healthy"
	StatusDegraded = "degraded"
	StatusOffline  = "offline"
)

// Vendor 是预设供应商（平台级，全租户共享）：把 BaseURL + 凭证 + Type 抽成可复用实体。
// 创建通道时选 Vendor（VendorID）即自动带入 BaseURL/CredentialRef/Vendor 展示名，
// 免去每个通道手填。Vendor 不是路由实体（不进 gateway），仅是通道配置源。
type Vendor struct {
	ID            string `json:"id"`            // 如 "airouter"
	Name          string `json:"name"`          // 展示名（如 "airouter 网关"）
	Type          string `json:"type"`          // openai-compatible（Channel.Type 同源）
	BaseURL       string `json:"baseUrl"`       // 供应商 BaseURL（OpenAI 兼容网关地址，如 https://api.openai.com/v1）
	CredentialRef string `json:"credentialRef"` // 凭证引用（security 平台级 Secret ID）
	Description   string `json:"description,omitempty"`
}

// HealthyChannels 返回非 offline 的通道（按优先级升序）。
// 路由时取首个；degraded 仍可降级服务，故纳入候选。
func (m *Model) HealthyChannels() []*Channel {
	out := make([]*Channel, 0, len(m.Channels))
	for _, c := range m.Channels {
		if c.Status != StatusOffline {
			out = append(out, c)
		}
	}
	return out
}

// Clone 返回 Model 的深拷贝：标量字段值复制，Capabilities 与 Channels 切片新建，
// 每个 Channel 也深拷贝。impl 引用保留（Provider 实现自身应线程安全，可安全共享）。
// 用于隔离调用方与 Gateway 内部状态，避免锁外读 Channel.Status 与 MarkChannelStatus 写竞态。
func (m *Model) Clone() *Model {
	if m == nil {
		return nil
	}
	cp := *m
	if m.Capabilities != nil {
		cp.Capabilities = append([]string(nil), m.Capabilities...)
	}
	if m.Channels != nil {
		cp.Channels = make([]*Channel, len(m.Channels))
		for i, c := range m.Channels {
			cp.Channels[i] = c.Clone()
		}
	}
	return &cp
}

// Clone 返回 Channel 的深拷贝。字段均为标量或接口引用，值复制即可；
// impl 保留同一 Provider 实例（其实现应线程安全）。
func (c *Channel) Clone() *Channel {
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}
