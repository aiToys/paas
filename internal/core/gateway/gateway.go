// Package gateway 是 Platform Core 的数据面入口：API Gateway。
// 提供 OpenAI 兼容端点、API Key 鉴权、模型路由与 Token 计量。
package gateway

import (
	"sync"

	"github.com/aitoys/paas/pkg/provider"
)

// Gateway 维护模型名到 Provider 的路由表，实现 provider.GatewayRegistrar。
type Gateway struct {
	mu        sync.RWMutex
	providers map[string]provider.Provider
}

// New 创建空 Gateway。
func New() *Gateway {
	return &Gateway{providers: map[string]provider.Provider{}}
}

// Register 注册模型到 Provider 的路由（实现 provider.GatewayRegistrar）。
func (g *Gateway) Register(model string, p provider.Provider) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.providers[model] = p
}

// Get 按模型名取 Provider。
func (g *Gateway) Get(model string) (provider.Provider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p, ok := g.providers[model]
	return p, ok
}

// Models 返回所有已注册模型名。
func (g *Gateway) Models() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.providers))
	for k := range g.providers {
		out = append(out, k)
	}
	return out
}
