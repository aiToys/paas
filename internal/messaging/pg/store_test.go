//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN（make test-pg）。
// 验证 topics/consumer_groups 表 CRUD + 租户隔离 + 级联删除 + 租户内唯一性（migration 0045）。

package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/messaging"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

func newTestDB(t *testing.T) *storagepg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	db, err := storagepg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := storagepg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS consumer_groups CASCADE; DROP TABLE IF EXISTS topics CASCADE; DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func sampleTopic(id, tid, mqID, name string) messaging.Topic {
	return messaging.Topic{
		ID: id, TenantID: tid, MQID: mqID, Name: name,
		Partitions: 3, Retention: "7d", Status: messaging.StatusActive,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
}

func sampleGroup(id, tid, topicID, name string) messaging.ConsumerGroup {
	return messaging.ConsumerGroup{
		ID: id, TenantID: tid, TopicID: topicID, Name: name,
		Mode: messaging.ModeClustering, Members: 2,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestMessagingCRUD(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	// Topic CRUD
	if err := s.CreateTopic(ctx, sampleTopic("tp-1", "t-acme", "ds-mq-1", "orders")); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	topics, err := s.ListTopics(ctx, "t-acme", "")
	if err != nil || len(topics) != 1 || topics[0].Name != "orders" || topics[0].Partitions != 3 {
		t.Fatalf("ListTopics 读回不一致: %v %+v", err, topics)
	}
	only, err := s.ListTopics(ctx, "t-acme", "ds-mq-1")
	if err != nil || len(only) != 1 {
		t.Fatalf("ListTopics 按 mqID 过滤应 1 条: %v %+v", err, only)
	}
	// 同租户同 MQ 同名冲突
	if err := s.CreateTopic(ctx, sampleTopic("tp-1b", "t-acme", "ds-mq-1", "orders")); err == nil {
		t.Fatalf("同租户同 MQ 同名 CreateTopic 应报已存在")
	}

	// ConsumerGroup CRUD
	if err := s.CreateConsumerGroup(ctx, sampleGroup("cg-1", "t-acme", "tp-1", "processors")); err != nil {
		t.Fatalf("CreateConsumerGroup: %v", err)
	}
	groups, err := s.ListConsumerGroups(ctx, "t-acme", "tp-1")
	if err != nil || len(groups) != 1 || groups[0].Name != "processors" || groups[0].Members != 2 {
		t.Fatalf("ListConsumerGroups 读回不一致: %v %+v", err, groups)
	}
	// 同租户同 topic 同名冲突
	if err := s.CreateConsumerGroup(ctx, sampleGroup("cg-1b", "t-acme", "tp-1", "processors")); err == nil {
		t.Fatalf("同 topic 同名 CreateConsumerGroup 应报已存在")
	}

	// DeleteTopic 级联删消费组
	if err := s.DeleteTopic(ctx, "t-acme", "tp-1"); err != nil {
		t.Fatalf("DeleteTopic: %v", err)
	}
	if groups, err = s.ListConsumerGroups(ctx, "t-acme", "tp-1"); err != nil || len(groups) != 0 {
		t.Fatalf("删 topic 后消费组应级联清空: %v %+v", err, groups)
	}
	// 重复删报不存在
	if err := s.DeleteTopic(ctx, "t-acme", "tp-1"); err == nil {
		t.Fatalf("重复 DeleteTopic 应报不存在")
	}
}

// 租户隔离：跨租户同名合法、跨租户删统一「不存在」不泄漏。
func TestMessagingTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	if err := s.CreateTopic(ctx, sampleTopic("tp-same", "t-acme", "ds-mq-1", "orders")); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	// 跨租户同名 topic 合法（R19-M1：唯一性限租户内）
	if err := s.CreateTopic(ctx, sampleTopic("tp-same", "t-globex", "ds-mq-2", "orders")); err != nil {
		t.Fatalf("跨租户同名 CreateTopic 应成功: %v", err)
	}
	for _, tid := range []string{"t-acme", "t-globex"} {
		topics, err := s.ListTopics(ctx, tid, "")
		if err != nil || len(topics) != 1 || topics[0].TenantID != tid {
			t.Fatalf("租户 %s 应只见自己的 topic: %v %+v", tid, err, topics)
		}
	}
	// 跨租户删他租户存在的记录统一「不存在」（不泄漏存在性）。
	// 注意 t-globex 自己也有 tp-same（上一步建的），须用第三方租户验跨租户删除。
	if err := s.DeleteTopic(ctx, "t-other", "tp-same"); err == nil {
		t.Fatalf("跨租户 DeleteTopic 应报不存在")
	}
	if err := s.DeleteConsumerGroup(ctx, "t-globex", "nonexistent"); err == nil {
		t.Fatalf("跨租户 DeleteConsumerGroup 应报不存在")
	}
}

// SeedIfEmpty：空库灌入演示数据；已灌后幂等不重复。
func TestMessagingSeedIfEmpty(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	if err := s.SeedIfEmpty(ctx); err != nil {
		t.Fatalf("SeedIfEmpty 首次: %v", err)
	}
	topics, err := s.ListTopics(ctx, "t-acme", "ds-mq-acme")
	if err != nil || len(topics) != 1 || topics[0].ID != "tp-order" {
		t.Fatalf("seed 后应有演示 topic: %v %+v", err, topics)
	}
	groups, err := s.ListConsumerGroups(ctx, "t-acme", "tp-order")
	if err != nil || len(groups) != 1 || groups[0].ID != "cg-order" {
		t.Fatalf("seed 后应有演示消费组: %v %+v", err, groups)
	}
	// 幂等：再 seed 不重复
	if err := s.SeedIfEmpty(ctx); err != nil {
		t.Fatalf("SeedIfEmpty 二次: %v", err)
	}
	topics, _ = s.ListTopics(ctx, "t-acme", "ds-mq-acme")
	if len(topics) != 1 {
		t.Fatalf("二次 seed 不应重复灌: %+v", topics)
	}
}
