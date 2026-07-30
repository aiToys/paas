package messaging

import "context"

// Repository 是 messaging 持久化抽象。全方法租户强制过滤；跨租户访问 not found（不泄漏）。
type Repository interface {
	// Topic
	ListTopics(ctx context.Context, tenantID, mqID string) ([]Topic, error)
	CreateTopic(ctx context.Context, t Topic) error
	DeleteTopic(ctx context.Context, tenantID, id string) error // 级联清消费组

	// ConsumerGroup
	ListConsumerGroups(ctx context.Context, tenantID, topicID string) ([]ConsumerGroup, error)
	CreateConsumerGroup(ctx context.Context, g ConsumerGroup) error
	DeleteConsumerGroup(ctx context.Context, tenantID, id string) error
}
