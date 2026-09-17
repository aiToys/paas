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

// TestCrossTenantSameNameNoCollision 锁住 R19-M1：确定性 ID（tp-<name>）下，
// 跨租户同名 topic/消费组各自记录——原全局键控实现租户 B 的写入直接覆盖租户 A
// （A 的记录凭空消失），且唯一性检查不含租户会跨租户误报「已存在」泄漏存在性。
func TestCrossTenantSameNameNoCollision(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	// t-globex 在自己的 mq 下建同名 topic（seed 是 t-acme 的 tp-order）
	require.NoError(t, s.CreateTopic(ctx, messaging.Topic{
		ID: "tp-order", TenantID: "t-globex", MQID: "ds-mq-globex",
		Name: "order-events", Status: messaging.StatusActive,
	}))
	// 双方都可见、互不覆盖
	ts, _ := s.ListTopics(ctx, "t-acme", "")
	require.Len(t, ts, 1)
	assert.Equal(t, "t-acme", ts[0].TenantID)
	ts, _ = s.ListTopics(ctx, "t-globex", "")
	require.Len(t, ts, 1)
	assert.Equal(t, "t-globex", ts[0].TenantID)

	// 消费组同款：跨租户同 TopicID 同名（理论不可达，防御深度）+ 同名不同 topic
	require.NoError(t, s.CreateConsumerGroup(ctx, messaging.ConsumerGroup{
		ID: "cg-order", TenantID: "t-globex", TopicID: "tp-order", Name: "order-processor",
	}))
	gs, _ := s.ListConsumerGroups(ctx, "t-acme", "")
	require.Len(t, gs, 1)
	assert.Equal(t, "t-acme", gs[0].TenantID)

	// 第三方租户删不存在（复合键隔离，404 语义不泄漏）
	assert.Error(t, s.DeleteTopic(ctx, "t-other", "tp-order"))
}
