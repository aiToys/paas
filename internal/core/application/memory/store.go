// Package memory 提供 application.Repository 的内存实现。
// 初始化时 seed 一批示例应用，便于控制台演示；Plan 2 替换为 PostgreSQL。
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/aitoys/paas/internal/core/application"
)

type Store struct {
	mu   sync.RWMutex
	apps map[string]application.Application
}

// NewStore 创建仓储并 seed 示例应用。
func NewStore() *Store {
	s := &Store{apps: map[string]application.Application{}}
	for _, a := range seed() {
		s.apps[a.ID] = a
	}
	return s
}

func (s *Store) List(_ context.Context) ([]application.Application, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]application.Application, 0, len(s.apps))
	for _, a := range s.apps {
		out = append(out, a)
	}
	return out, nil
}

func (s *Store) Get(_ context.Context, id string) (application.Application, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.apps[id]
	if !ok {
		return application.Application{}, fmt.Errorf("应用不存在: %s", id)
	}
	return a, nil
}

func (s *Store) Create(_ context.Context, a application.Application) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[a.ID]; exists {
		return fmt.Errorf("应用已存在: %s", a.ID)
	}
	if a.ID == "" {
		return fmt.Errorf("应用 ID 不能为空")
	}
	s.apps[a.ID] = a
	return nil
}

func (s *Store) BindResource(_ context.Context, id, resourceType string) (application.Application, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.apps[id]
	if !ok {
		return application.Application{}, fmt.Errorf("应用不存在: %s", id)
	}
	switch resourceType {
	case "models":
		a.Resources.Models++
	case "mq":
		a.Resources.MQ++
	case "dal":
		a.Resources.DAL++
	default:
		return a, fmt.Errorf("未知资源类型: %s", resourceType)
	}
	s.apps[id] = a
	return a, nil
}

func seed() []application.Application {
	return []application.Application{
		{ID: "app-cs", Name: "智能客服", Initial: "客", Env: "生产", Status: "healthy", Gradient: "linear-gradient(135deg,#6366f1,#8b5cf6)", Desc: "对话式客服，多模型路由 + 消息异步落库", Resources: application.ResourceCount{Models: 2, MQ: 1, DAL: 1}, Replicas: "6/6", RPS: "1.2k"},
		{ID: "app-rec", Name: "推荐服务", Initial: "推", Env: "生产", Status: "healthy", Gradient: "linear-gradient(135deg,#10b981,#06b6d4)", Desc: "实时推荐，Embedding 召回 + 重排", Resources: application.ResourceCount{Models: 1, MQ: 0, DAL: 2}, Replicas: "4/4", RPS: "3.8k"},
		{ID: "app-etl", Name: "数据导入", Initial: "数", Env: "预发", Status: "degraded", Gradient: "linear-gradient(135deg,#f59e0b,#f43f5e)", Desc: "批处理管道，MQ 削峰 + DAL 写入", Resources: application.ResourceCount{Models: 0, MQ: 2, DAL: 1}, Replicas: "2/3", RPS: "320"},
		{ID: "app-lab", Name: "实验沙盒", Initial: "沙", Env: "开发", Status: "idle", Gradient: "linear-gradient(135deg,#64748b,#475569)", Desc: "模型效果评测，按需启动", Resources: application.ResourceCount{Models: 1, MQ: 0, DAL: 0}, Replicas: "0/1", RPS: "0"},
		{ID: "app-agent", Name: "智能体平台", Initial: "体", Env: "开发", Status: "healthy", Gradient: "linear-gradient(135deg,#ec4899,#8b5cf6)", Desc: "工具调用 Agent，多模型协同", Resources: application.ResourceCount{Models: 3, MQ: 1, DAL: 0}, Replicas: "2/2", RPS: "86"},
	}
}
