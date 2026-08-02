// Package memory 提供 application.Repository 的内存实现。
// 初始化时 seed 一批示例应用（含绑定项），便于控制台演示；Plan 2 替换为 PostgreSQL。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/pkg/tenant"
)

// 支持绑定的资源类型（资源中心 = 数据服务全集）。
// models/mq/dal 计入列表摘要计数；其余类型仅在应用详情 bindings 展示。
// dal/gov 保留以兼容历史 seed 与「应用接入治理」语义。
var supportedTypes = map[string]struct{}{
	"models":  {},
	"db":      {},
	"cache":   {},
	"mq":      {},
	"storage": {},
	"vector":  {},
	"search":  {},
	"dal":     {},
	"gov":     {},
}

type Store struct {
	mu   sync.RWMutex
	apps map[string]application.Application
}

// NewStore 创建仓储并 seed 示例应用。
func NewStore() *Store {
	s := &Store{apps: map[string]application.Application{}}
	for _, a := range seed() {
		a.Recount()
		s.apps[a.ID] = a
	}
	return s
}

// cloneBindings 深拷贝绑定切片，确保返回值与 store 内底层数组独立（并发读写安全）。
func cloneBindings(bs []application.Binding) []application.Binding {
	if len(bs) == 0 {
		return nil
	}
	cp := make([]application.Binding, len(bs))
	copy(cp, bs)
	return cp
}

func (s *Store) List(ctx context.Context) ([]application.Application, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("missing tenant context")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]application.Application, 0, len(s.apps))
	for _, a := range s.apps {
		if a.TenantID == tid {
			a.Bindings = cloneBindings(a.Bindings)
			out = append(out, a)
		}
	}
	// 稳定排序，避免 map 迭代乱序导致前端列表抖动
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (application.Application, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return application.Application{}, fmt.Errorf("missing tenant context")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, hit := s.apps[id]
	// 跨租户访问统一返回 not found，不泄漏存在性
	if !hit || a.TenantID != tid {
		return application.Application{}, fmt.Errorf("应用不存在: %s", id)
	}
	a.Bindings = cloneBindings(a.Bindings)
	return a, nil
}

func (s *Store) Create(ctx context.Context, a application.Application) error {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return fmt.Errorf("missing tenant context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[a.ID]; exists {
		return fmt.Errorf("应用已存在: %s", a.ID)
	}
	if a.ID == "" {
		return fmt.Errorf("应用 ID 不能为空")
	}
	a.TenantID = tid // 以 ctx 为准，忽略请求体
	a.Recount()
	s.apps[a.ID] = a
	return nil
}

func (s *Store) BindResource(ctx context.Context, id, resourceType, name string) (application.Application, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return application.Application{}, fmt.Errorf("missing tenant context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := supportedTypes[resourceType]; !ok {
		return application.Application{}, fmt.Errorf("未知资源类型: %s", resourceType)
	}
	if name == "" {
		return application.Application{}, fmt.Errorf("资源名称不能为空")
	}
	a, hit := s.apps[id]
	if !hit || a.TenantID != tid {
		return application.Application{}, fmt.Errorf("应用不存在: %s", id)
	}
	// 同类型同名视为已绑定，幂等返回
	for _, b := range a.Bindings {
		if b.Type == resourceType && b.Name == name {
			return a, nil
		}
	}
	a.Bindings = append(a.Bindings, application.Binding{Type: resourceType, Name: name})
	a.Recount()
	s.apps[id] = a
	a.Bindings = cloneBindings(a.Bindings) // 返回前深拷贝，与 store 独立
	return a, nil
}

func (s *Store) Unbind(ctx context.Context, id, resourceType, name string) (application.Application, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return application.Application{}, fmt.Errorf("missing tenant context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, hit := s.apps[id]
	if !hit || a.TenantID != tid {
		return application.Application{}, fmt.Errorf("应用不存在: %s", id)
	}
	// 构建新 slice，避免原地 filter（a.Bindings[:0]）污染与 store 共享的底层数组。
	next := make([]application.Binding, 0, len(a.Bindings))
	removed := false
	for _, b := range a.Bindings {
		if b.Type == resourceType && b.Name == name {
			removed = true
			continue
		}
		next = append(next, b)
	}
	if !removed {
		return application.Application{}, fmt.Errorf("绑定不存在: %s/%s", resourceType, name)
	}
	a.Bindings = next
	a.Recount()
	s.apps[id] = a
	a.Bindings = cloneBindings(a.Bindings) // 返回前深拷贝，与 store 独立
	return a, nil
}

// SeedApps 返回平台预置示例应用，供内存仓储自灌与 PG 仓储迁移后 seed 复用同一真源。
// 导出以避免 seed 数据在 cmd/core 重复定义（DRY）。
func SeedApps() []application.Application { return seed() }

// seed 生成跨两租户的示例应用骨架（演示主线，无 mock 绑定/假指标）。
// Replicas/RPS/Status 由 handler 从真实工作负载聚合派生（ApplyStats），不在此硬编码假值。
// Bindings 移除：原 seed 绑定指向不存在的 mock 资源（qwen-cs-route 等），属假数据；
// 用户经控制台绑定真实资源（模型路由/数据服务）后在此展示。
func seed() []application.Application {
	return []application.Application{
		{ID: "app-cs", TenantID: "t-acme", Name: "智能客服", Initial: "客", Env: "生产", Status: "idle",
			Gradient: "linear-gradient(135deg,#6366f1,#8b5cf6)", Desc: "对话式客服，多模型路由 + 消息异步落库"},
		{ID: "app-rec", TenantID: "t-acme", Name: "推荐服务", Initial: "推", Env: "生产", Status: "idle",
			Gradient: "linear-gradient(135deg,#10b981,#06b6d4)", Desc: "实时推荐，Embedding 召回 + 重排"},
		{ID: "app-etl", TenantID: "t-globex", Name: "数据导入", Initial: "数", Env: "预发", Status: "idle",
			Gradient: "linear-gradient(135deg,#f59e0b,#f43f5e)", Desc: "批处理管道，MQ 削峰 + DAL 写入"},
		{ID: "app-lab", TenantID: "t-acme", Name: "实验沙盒", Initial: "沙", Env: "开发", Status: "idle",
			Gradient: "linear-gradient(135deg,#64748b,#475569)", Desc: "模型效果评测，按需启动"},
		{ID: "app-agent", TenantID: "t-globex", Name: "智能体平台", Initial: "体", Env: "开发", Status: "idle",
			Gradient: "linear-gradient(135deg,#ec4899,#8b5cf6)", Desc: "工具调用 Agent，多模型协同"},
	}
}
