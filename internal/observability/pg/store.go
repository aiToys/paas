// Package pg 提供 observability.RuleStore 的 PostgreSQL 实现（告警规则持久化）。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；
// Create 以 ctx 租户为准、忽略请求体 TenantID（防越权写）。
package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/observability"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 observability.RuleStore 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建告警规则 PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// ruleCols 与 observability.AlertRule 字段顺序对齐（scan 列顺序必须一致）。
const ruleCols = `id, tenant_id, name, metric_name, target_type, target_id, operator, threshold, severity, enabled, webhook_url, updated_at`

func scanRule(r storagepg.RowScanner, rule *observability.AlertRule) error {
	return r.Scan(&rule.ID, &rule.TenantID, &rule.Name, &rule.MetricName, &rule.TargetType,
		&rule.TargetID, &rule.Operator, &rule.Threshold, &rule.Severity,
		&rule.Enabled, &rule.WebhookURL, &rule.UpdatedAt)
}

// ListAlertRules 列出当前租户的全部规则，按 id 排序。
func (s *Store) ListAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	return s.query(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE tenant_id=$1 ORDER BY id`, tid)
}

// ListAllAlertRules 跨租户列出全部规则（admin 平台总览 + 评估引擎，按 tenant_id, id 排序）。
func (s *Store) ListAllAlertRules(ctx context.Context) ([]observability.AlertRule, error) {
	return s.query(ctx, `SELECT `+ruleCols+` FROM alert_rules ORDER BY tenant_id, id`)
}

func (s *Store) query(ctx context.Context, sql string, args ...any) ([]observability.AlertRule, error) {
	rows, err := s.db.Pool().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []observability.AlertRule
	for rows.Next() {
		var rule observability.AlertRule
		if err = scanRule(rows, &rule); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// CreateAlertRule 创建规则（ID 为空自动生成；ctx 租户为准忽略请求体 TenantID）。
// 同租户同名单一约束与 memory 一致。
func (s *Store) CreateAlertRule(ctx context.Context, rule observability.AlertRule) (observability.AlertRule, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return observability.AlertRule{}, err
	}
	if rule.ID == "" {
		rule.ID = newRuleID()
	}
	rule.TenantID = tid
	rule.UpdatedAt = time.Now()
	err = s.db.Pool().QueryRow(ctx,
		`INSERT INTO alert_rules (`+ruleCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		rule.ID, rule.TenantID, rule.Name, rule.MetricName, rule.TargetType, rule.TargetID,
		rule.Operator, rule.Threshold, rule.Severity, rule.Enabled, rule.WebhookURL, rule.UpdatedAt,
	).Scan()
	if storagepg.IsUniqueViolation(err) {
		return observability.AlertRule{}, fmt.Errorf("告警规则已存在: %s", rule.Name)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return observability.AlertRule{}, err
	}
	return rule, nil
}

// DeleteAlertRule 删除规则（仅当前租户；跨租户统一 not found 不泄漏）。
func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM alert_rules WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("告警规则不存在: %s", id)
	}
	return nil
}

// newRuleID 生成带前缀的短 ID（sha256 前 12 hex，与 devops pg newID 同款）。
func newRuleID() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("alertrule-%d", time.Now().UnixNano())))
	return "rule-" + hex.EncodeToString(h[:6])
}
