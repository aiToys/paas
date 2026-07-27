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
	ID       string   `json:"id"`                 // 如 "qwen2.5-7b#mock-a"
	Type     string   `json:"type"`               // echo/mock/vllm
	Priority int      `json:"priority"`           // 数字越小优先级越高
	Status   string   `json:"status"`             // healthy/degraded/offline
	Endpoint string   `json:"endpoint,omitempty"` // 未来 vllm 远端地址
	impl     Provider `json:"-"`                  // 实际执行者
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
