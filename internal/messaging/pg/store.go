// Package pg 提供 messaging.Repository 的 PostgreSQL 实现（migration 0045）。
// 显式 WHERE tenant_id=$1 多租户过滤，语义与 memory 版 1:1（含租户内唯一性、
// 跨租户同名合法不泄漏、删 topic 级联删消费组）。
package pg

import (
	"context"
	"fmt"
	"os"

	"github.com/aitoys/paas/internal/messaging"
	msgmemory "github.com/aitoys/paas/internal/messaging/memory"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 messaging.Repository 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 messaging PG 仓储。db 必须已完成迁移（0045）。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

const tpCols = `id, tenant_id, mq_id, name, partitions, retention, status, created_at`
const cgCols = `id, tenant_id, topic_id, name, mode, members, created_at`

func scanTopic(r storagepg.RowScanner, t *messaging.Topic) error {
	return r.Scan(&t.ID, &t.TenantID, &t.MQID, &t.Name, &t.Partitions, &t.Retention, &t.Status, &t.CreatedAt)
}

func scanGroup(r storagepg.RowScanner, g *messaging.ConsumerGroup) error {
	return r.Scan(&g.ID, &g.TenantID, &g.TopicID, &g.Name, &g.Mode, &g.Members, &g.CreatedAt)
}

// ListTopics 列出租户 topic（mqID 空 = 全部 MQ 资源），按创建时间正序（稳定序）。
func (s *Store) ListTopics(ctx context.Context, tenantID, mqID string) ([]messaging.Topic, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+tpCols+` FROM topics WHERE tenant_id=$1 AND ($2='' OR mq_id=$2)
		 ORDER BY created_at, id`, tenantID, mqID)
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	defer rows.Close() //nolint:staticcheck // SA3000 defer rows.Close 在 err 判断前已有值
	var out []messaging.Topic
	for rows.Next() {
		var t messaging.Topic
		if err := scanTopic(rows, &t); err != nil {
			return nil, fmt.Errorf("scan topic: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTopic 写入；同租户同 MQ 同名冲突返「topic 已存在」（uq_topics_tenant_mq_name 拦截，
// handler 映射 409；跨租户同名合法不冲突）。memory 版按名字判重，此处 ON CONFLICT 指向
// 名字唯一索引同语义（handler 生成确定性 ID "tp-<name>"，两冲突维度在此场景一致）。
func (s *Store) CreateTopic(ctx context.Context, t messaging.Topic) error {
	tag, err := s.db.Pool().Exec(ctx,
		`INSERT INTO topics (`+tpCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (tenant_id, mq_id, name) DO NOTHING`,
		t.ID, t.TenantID, t.MQID, t.Name, t.Partitions, t.Retention, t.Status, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("create topic: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("topic 已存在: %s", t.Name)
	}
	return nil
}

// DeleteTopic 删除（级联删该 topic 下消费组，与 memory 版同语义）。
// 不存在/跨租户统一「topic 不存在」（不泄漏存在性）。
func (s *Store) DeleteTopic(ctx context.Context, tenantID, id string) error {
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete topic: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 提交/回滚二选一，错误由后续路径暴露
	tag, err := tx.Exec(ctx, `DELETE FROM topics WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("delete topic: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("topic 不存在: %s", id)
	}
	// 级联清消费组（同租户 + TopicID 对齐；跨租户同 topicID 不受影响）。
	if _, err := tx.Exec(ctx,
		`DELETE FROM consumer_groups WHERE tenant_id=$1 AND topic_id=$2`, tenantID, id); err != nil {
		return fmt.Errorf("cascade delete groups: %w", err)
	}
	return tx.Commit(ctx)
}

// ListConsumerGroups 列出租户消费组（topicID 空 = 全部 topic）。
func (s *Store) ListConsumerGroups(ctx context.Context, tenantID, topicID string) ([]messaging.ConsumerGroup, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+cgCols+` FROM consumer_groups WHERE tenant_id=$1 AND ($2='' OR topic_id=$2)
		 ORDER BY created_at, id`, tenantID, topicID)
	if err != nil {
		return nil, fmt.Errorf("list consumer groups: %w", err)
	}
	defer rows.Close() //nolint:staticcheck // SA3000 defer rows.Close 在 err 判断前已有值
	var out []messaging.ConsumerGroup
	for rows.Next() {
		var g messaging.ConsumerGroup
		if err := scanGroup(rows, &g); err != nil {
			return nil, fmt.Errorf("scan consumer group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CreateConsumerGroup 写入；同租户同 topic 同名冲突返「消费组已存在」。
// 名字唯一性（uq_groups_tenant_topic_name）先于主键冲突判定：ON CONFLICT 只能指定一个
// 约束，先试名字唯一索引，命中即报语义错误；未命中再由主键兜底（重复 ID 罕见，
// 与 memory 版「以名字判重」语义对齐）。
func (s *Store) CreateConsumerGroup(ctx context.Context, g messaging.ConsumerGroup) error {
	tag, err := s.db.Pool().Exec(ctx,
		`INSERT INTO consumer_groups (`+cgCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (tenant_id, topic_id, name) DO NOTHING`,
		g.ID, g.TenantID, g.TopicID, g.Name, g.Mode, g.Members, g.CreatedAt)
	if err != nil {
		return fmt.Errorf("create consumer group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 同名命中（tenant+topic 维度唯一索引）或主键重复；memory 版只按名字判重，同语义报已存在。
		return fmt.Errorf("消费组已存在: %s", g.Name)
	}
	return nil
}

// DeleteConsumerGroup 删除。不存在/跨租户统一「消费组不存在」（不泄漏存在性）。
func (s *Store) DeleteConsumerGroup(ctx context.Context, tenantID, id string) error {
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM consumer_groups WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return fmt.Errorf("delete consumer group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("消费组不存在: %s", id)
	}
	return nil
}

// 接口实现编译期断言。
var _ messaging.Repository = (*Store)(nil)

// SeedIfEmpty 表空时灌入演示数据（memory.SeedTopics/SeedGroups 同一真源；幂等：
// ON CONFLICT DO NOTHING + topics 表空判定，重启/并存不重复灌）。
// PAAS_DISABLE_DEMO_SEED=true（生产）跳过（security pg seed 同款内部门控）。
func (s *Store) SeedIfEmpty(ctx context.Context) error {
	if os.Getenv("PAAS_DISABLE_DEMO_SEED") == "true" {
		return nil
	}
	var n int
	if err := s.db.Pool().QueryRow(ctx, `SELECT count(*) FROM topics`).Scan(&n); err != nil {
		return fmt.Errorf("count topics: %w", err)
	}
	if n > 0 {
		return nil
	}
	for _, t := range msgmemory.SeedTopics() {
		if _, err := s.db.Pool().Exec(ctx,
			`INSERT INTO topics (`+tpCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			 ON CONFLICT (tenant_id, mq_id, name) DO NOTHING`,
			t.ID, t.TenantID, t.MQID, t.Name, t.Partitions, t.Retention, t.Status, t.CreatedAt); err != nil {
			return fmt.Errorf("seed topic %s: %w", t.ID, err)
		}
	}
	for _, g := range msgmemory.SeedGroups() {
		if _, err := s.db.Pool().Exec(ctx,
			`INSERT INTO consumer_groups (`+cgCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
			 ON CONFLICT (tenant_id, topic_id, name) DO NOTHING`,
			g.ID, g.TenantID, g.TopicID, g.Name, g.Mode, g.Members, g.CreatedAt); err != nil {
			return fmt.Errorf("seed group %s: %w", g.ID, err)
		}
	}
	return nil
}
