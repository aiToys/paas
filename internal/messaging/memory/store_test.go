package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/messaging"
)

func TestSeedTopic(t *testing.T) {
	s := NewStore()
	ts, err := s.ListTopics(context.Background(), "t-acme", "ds-mq-acme")
	require.NoError(t, err)
	require.Len(t, ts, 1)
	assert.Equal(t, "order-events", ts[0].Name)
}

func TestTopicCRUDAndCascade(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	require.NoError(t, s.CreateTopic(ctx, messaging.Topic{
		ID: "tp-new", TenantID: "t-acme", MQID: "ds-mq-acme",
		Name: "new-topic", Partitions: 2, Status: messaging.StatusActive,
	}))
	require.NoError(t, s.CreateConsumerGroup(ctx, messaging.ConsumerGroup{
		ID: "cg-new", TenantID: "t-acme", TopicID: "tp-new", Name: "cg", Mode: messaging.ModeClustering,
	}))

	// 删 Topic 级联清消费组
	require.NoError(t, s.DeleteTopic(ctx, "t-acme", "tp-new"))
	cgs, _ := s.ListConsumerGroups(ctx, "t-acme", "tp-new")
	assert.Empty(t, cgs)
}

func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	// t-globex 看不到 t-acme 的 topic
	ts, _ := s.ListTopics(context.Background(), "t-globex", "")
	assert.Empty(t, ts)
	// 跨租户删失败
	assert.Error(t, s.DeleteTopic(context.Background(), "t-globex", "tp-order"))
}

func TestDuplicateTopic(t *testing.T) {
	s := NewStore()
	err := s.CreateTopic(context.Background(), messaging.Topic{
		ID: "tp-dup", TenantID: "t-acme", MQID: "ds-mq-acme", Name: "order-events",
	})
	assert.Error(t, err) // 同 MQ + 同名冲突
}
