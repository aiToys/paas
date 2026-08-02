// Package gateway 是 Platform Core 的数据面入口：API Gateway。
// 提供 OpenAI 兼容端点、API Key 鉴权、模型路由（按通道优先级与健康状态）与 Token 计量。
package gateway

import (
	"fmt"
	"sort"
	"sync"

	"github.com/aitoys/paas/pkg/provider"
)

// Gateway 维护逻辑模型路由表，实现 provider.GatewayRegistrar。
// 路由策略：按通道 Priority 升序取首个非 offline 通道的 Provider。
type Gateway struct {
	mu     sync.RWMutex
	models map[string]*provider.Model
}

// New 创建空 Gateway。
func New() *Gateway {
	return &Gateway{models: map[string]*provider.Model{}}
}

// RegisterModel 注册一个逻辑模型（含其全部通道）。同 ID 覆盖。
func (g *Gateway) RegisterModel(m *provider.Model) error {
	if m == nil || m.ID == "" {
		return fmt.Errorf("model 与 ID 不能为空")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.models[m.ID] = m
	return nil
}

// Resolve 按通道优先级取首个非 offline 通道。
// 返回选中通道（含 Provider 与 ID），供 handler 调用失败时按 channel.ID 降级。
// 全部通道 offline 时返回错误。
func (g *Gateway) Resolve(model string) (*provider.Channel, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	m, ok := g.models[model]
	if !ok {
		return nil, fmt.Errorf("model %q not found", model)
	}
	chs := m.HealthyChannels()
	if len(chs) == 0 {
		return nil, fmt.Errorf("model %q 无可用通道", model)
	}
	sort.SliceStable(chs, func(i, j int) bool { return chs[i].Priority < chs[j].Priority })
	return chs[0], nil
}

// ResolveChannels 返回某模型的全部候选通道（非 offline，按优先级升序），供请求级 failover。
// 与 Resolve 同策略，但返回全部候选而非仅首个，使首通道失败时可自动切下一通道。
func (g *Gateway) ResolveChannels(model string) ([]*provider.Channel, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	m, ok := g.models[model]
	if !ok {
		return nil, fmt.Errorf("model %q not found", model)
	}
	chs := m.HealthyChannels()
	if len(chs) == 0 {
		return nil, fmt.Errorf("model %q 无可用通道", model)
	}
	sort.SliceStable(chs, func(i, j int) bool { return chs[i].Priority < chs[j].Priority })
	return chs, nil
}

// Models 返回全部已注册模型（富信息），按 ID 稳定排序。
// 返回深拷贝，隔离调用方与内部状态，避免锁外 JSON 编码读 Channel.Status
// 与 MarkChannelStatus 写竞态（string 非原子读写）。
func (g *Gateway) Models() []*provider.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*provider.Model, 0, len(g.models))
	for _, m := range g.models {
		out = append(out, m.Clone())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MarkChannelStatus 更新某模型某通道的健康状态（调用失败被动降级用）。
// 未知模型或通道时忽略。
func (g *Gateway) MarkChannelStatus(modelID, channelID, status string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.models[modelID]
	if !ok {
		return
	}
	for _, c := range m.Channels {
		if c.ID == channelID {
			c.Status = status
			return
		}
	}
}
