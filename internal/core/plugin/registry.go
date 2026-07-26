// Package plugin 实现 Platform Core 的插件注册中心：
// 注册、去重、依赖拓扑排序与环检测。
package plugin

import (
	"fmt"

	pkgplugin "github.com/aitoys/paas/pkg/plugin"
)

// Registry 管理已注册的插件。
type Registry struct {
	plugins map[string]pkgplugin.Plugin
}

// NewRegistry 创建空注册中心。
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]pkgplugin.Plugin)}
}

// Register 注册插件；重名返回错误。
func (r *Registry) Register(p pkgplugin.Plugin) error {
	name := p.Manifest().Name
	if name == "" {
		return fmt.Errorf("插件名不能为空")
	}
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("插件已注册: %s", name)
	}
	r.plugins[name] = p
	return nil
}

// Get 按名取插件。
func (r *Registry) Get(name string) (pkgplugin.Plugin, bool) {
	p, ok := r.plugins[name]
	return p, ok
}

// All 返回所有插件（顺序不保证）。
func (r *Registry) All() []pkgplugin.Plugin {
	out := make([]pkgplugin.Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	return out
}

// LoadOrder 按依赖做拓扑排序，返回加载顺序；
// 缺失依赖或循环依赖返回错误。
func (r *Registry) LoadOrder() ([]pkgplugin.Plugin, error) {
	// 入度表与邻接表
	indeg := make(map[string]int)
	adj := make(map[string][]string)
	for name, p := range r.plugins {
		indeg[name] += 0 // 确保节点存在
		for _, dep := range p.Manifest().Depends {
			if _, ok := r.plugins[dep]; !ok {
				return nil, fmt.Errorf("插件 %s 依赖未注册的 %s", name, dep)
			}
			adj[dep] = append(adj[dep], name)
			indeg[name]++
		}
	}

	// 入度为 0 的节点入队
	var queue []string
	for name, d := range indeg {
		if d == 0 {
			queue = append(queue, name)
		}
	}

	var ordered []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ordered = append(ordered, cur)
		for _, nxt := range adj[cur] {
			indeg[nxt]--
			if indeg[nxt] == 0 {
				queue = append(queue, nxt)
			}
		}
	}

	if len(ordered) != len(r.plugins) {
		return nil, fmt.Errorf("检测到插件依赖循环")
	}

	out := make([]pkgplugin.Plugin, 0, len(ordered))
	for _, name := range ordered {
		out = append(out, r.plugins[name])
	}
	return out, nil
}
