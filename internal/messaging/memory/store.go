package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/messaging"
)

// Store 是 messaging.Repository 的内存实现（含 seed 演示数据）。
type Store struct {
	mu     sync.RWMutex
	topics map[string]messaging.Topic
	groups map[string]messaging.ConsumerGroup
}

func NewStore() *Store {
	s := &Store{topics: map[string]messaging.Topic{}, groups: map[string]messaging.ConsumerGroup{}}
	s.seed()
	return s
}

// seed 注入演示 topic + 消费组（t-acme / mq 资源 ds-mq-acme 下）。
func (s *Store) seed() {
	now := time.Now()
	s.topics["tp-order"] = messaging.Topic{
		ID: "tp-order", TenantID: "t-acme", MQID: "ds-mq-acme",
		Name: "order-events", Partitions: 3, Retention: "7d", Status: messaging.StatusActive, CreatedAt: now,
	}
	s.groups["cg-order"] = messaging.ConsumerGroup{
		ID: "cg-order", TenantID: "t-acme", TopicID: "tp-order",
		Name: "order-processor", Mode: messaging.ModeClustering, Members: 2, CreatedAt: now,
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
	for _, ex := range s.topics {
		if ex.MQID == t.MQID && ex.Name == t.Name {
			return fmt.Errorf("topic 已存在: %s", t.Name)
		}
	}
	s.topics[t.ID] = t
	return nil
}

func (s *Store) DeleteTopic(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.topics[id]
	if !ok || t.TenantID != tenantID {
		return fmt.Errorf("topic 不存在: %s", id)
	}
	delete(s.topics, id)
	for gid, g := range s.groups {
		if g.TopicID == id {
			delete(s.groups, gid)
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
	for _, ex := range s.groups {
		if ex.TopicID == g.TopicID && ex.Name == g.Name {
			return fmt.Errorf("消费组已存在: %s", g.Name)
		}
	}
	s.groups[g.ID] = g
	return nil
}

func (s *Store) DeleteConsumerGroup(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok || g.TenantID != tenantID {
		return fmt.Errorf("消费组不存在: %s", id)
	}
	delete(s.groups, id)
	return nil
}
