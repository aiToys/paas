package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/messaging"
)

// Store 是 messaging.Repository 的内存实现（含 seed 演示数据）。
// 键控用「租户/ID」复合键：handler 生成的 ID 是确定性 "tp-<name>"，
// 全局键控下跨租户同名会互相覆盖（对方记录凭空消失）（R19 审计 M1）。
type Store struct {
	mu     sync.RWMutex
	topics map[string]messaging.Topic
	groups map[string]messaging.ConsumerGroup
}

func key(tenantID, id string) string { return tenantID + "/" + id }

func NewStore() *Store {
	s := &Store{topics: map[string]messaging.Topic{}, groups: map[string]messaging.ConsumerGroup{}}
	s.seed()
	return s
}

// SeedTopics 返回演示 topic（PG seed 复用同一真源，与 SeedApps 同款模式）。
func SeedTopics() []messaging.Topic {
	return []messaging.Topic{{
		ID: "tp-order", TenantID: "t-acme", MQID: "ds-mq-acme",
		Name: "order-events", Partitions: 3, Retention: "7d", Status: messaging.StatusActive, CreatedAt: time.Now(),
	}}
}

// SeedGroups 返回演示消费组（PG seed 复用同一真源）。
func SeedGroups() []messaging.ConsumerGroup {
	return []messaging.ConsumerGroup{{
		ID: "cg-order", TenantID: "t-acme", TopicID: "tp-order",
		Name: "order-processor", Mode: messaging.ModeClustering, Members: 2, CreatedAt: time.Now(),
	}}
}

// seed 注入演示 topic + 消费组（t-acme / mq 资源 ds-mq-acme 下）。
func (s *Store) seed() {
	for _, t := range SeedTopics() {
		s.topics[key(t.TenantID, t.ID)] = t
	}
	for _, g := range SeedGroups() {
		s.groups[key(g.TenantID, g.ID)] = g
	}
}

func (s *Store) ListTopics(_ context.Context, tenantID, mqID string) ([]messaging.Topic, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []messaging.Topic
	for _, t := range s.topics {
		if t.TenantID == tenantID && (mqID == "" || t.MQID == mqID) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Store) CreateTopic(_ context.Context, t messaging.Topic) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 唯一性限租户内（同租户同 MQ 同名才冲突）：跨租户同名合法，不得报「已存在」泄漏存在性。
	for _, ex := range s.topics {
		if ex.TenantID == t.TenantID && ex.MQID == t.MQID && ex.Name == t.Name {
			return fmt.Errorf("topic 已存在: %s", t.Name)
		}
	}
	s.topics[key(t.TenantID, t.ID)] = t
	return nil
}

func (s *Store) DeleteTopic(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenantID, id)
	t, ok := s.topics[k]
	if !ok || t.TenantID != tenantID {
		return fmt.Errorf("topic 不存在: %s", id)
	}
	delete(s.topics, k)
	// 级联删该 topic 下消费组（同租户，TopicID 对齐）。
	for gk, g := range s.groups {
		if g.TenantID == tenantID && g.TopicID == id {
			delete(s.groups, gk)
		}
	}
	return nil
}

func (s *Store) ListConsumerGroups(_ context.Context, tenantID, topicID string) ([]messaging.ConsumerGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []messaging.ConsumerGroup
	for _, g := range s.groups {
		if g.TenantID == tenantID && (topicID == "" || g.TopicID == topicID) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *Store) CreateConsumerGroup(_ context.Context, g messaging.ConsumerGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 唯一性限租户内（与 CreateTopic 同款）：跨租户同 TopicID 同名不冲突（TopicID 本身租户隔离）。
	for _, ex := range s.groups {
		if ex.TenantID == g.TenantID && ex.TopicID == g.TopicID && ex.Name == g.Name {
			return fmt.Errorf("消费组已存在: %s", g.Name)
		}
	}
	s.groups[key(g.TenantID, g.ID)] = g
	return nil
}

func (s *Store) DeleteConsumerGroup(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenantID, id)
	g, ok := s.groups[k]
	if !ok || g.TenantID != tenantID {
		return fmt.Errorf("消费组不存在: %s", id)
	}
	delete(s.groups, k)
	return nil
}
