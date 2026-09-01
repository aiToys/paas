// Package pg 提供 observability 的 PostgreSQL 实现（告警规则/状态机/历史事件持久化）。
// 规则 CRUD 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；Create 以 ctx 租户为准。
// 状态机 Load/Save 与历史事件 Append/List 由告警引擎（跨租户组件）经 owner 连接调用，
// RLS 对 NULL app.tenant_id 放行；ListAlertEvents 是租户查询端点（显式 WHERE tenant_id）。
package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/observability"
	alertengine "github.com/aitoys/paas/internal/observability/alert"
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

// ---- 告警状态机持久化（migration 0042，alert.StateStore 实现）----

// LoadStates 恢复全部状态快照（引擎启动时调用，owner 连接 RLS 放行）。
func (s *Store) LoadStates(ctx context.Context) ([]alertengine.PersistedState, error) {
	rows, err := s.db.Pool().Query(ctx, `SELECT state_key, tenant_id, alert, tick_breach FROM alert_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []alertengine.PersistedState
	for rows.Next() {
		var ps alertengine.PersistedState
		var raw []byte
		if err = rows.Scan(&ps.StateKey, &ps.TenantID, &raw, &ps.TickBreach); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &ps.Alert); err != nil {
			return nil, fmt.Errorf("解析告警状态 %s: %w", ps.StateKey, err)
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

// SaveStates 批次 upsert 状态快照（单条 batch 内多键，ON CONFLICT 覆盖）。
func (s *Store) SaveStates(ctx context.Context, states []alertengine.PersistedState) error {
	b := &pgx.Batch{}
	now := time.Now()
	for _, ps := range states {
		raw, err := json.Marshal(ps.Alert)
		if err != nil {
			return fmt.Errorf("序列化告警状态 %s: %w", ps.StateKey, err)
		}
		b.Queue(`INSERT INTO alert_states (state_key, tenant_id, rule_id, target_type, target_id, alert, tick_breach, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (state_key) DO UPDATE SET alert=$6, tick_breach=$7, updated_at=$8`,
			ps.StateKey, ps.TenantID, ps.Alert.RuleID, ps.Alert.TargetType, ps.Alert.TargetID, raw, ps.TickBreach, now)
	}
	return s.db.Pool().SendBatch(ctx, b).Close()
}

// DeleteStates 批次删除过期状态（resolved 展示一轮后清理）。
func (s *Store) DeleteStates(ctx context.Context, keys []string) error {
	b := &pgx.Batch{}
	for _, k := range keys {
		b.Queue(`DELETE FROM alert_states WHERE state_key=$1`, k)
	}
	return s.db.Pool().SendBatch(ctx, b).Close()
}

// ---- 告警历史事件（migration 0042，alert.EventStore 实现 + 租户查询）----

// 事件保留上限（每租户，同 AuditLog LIMIT 1000 惯例；超出裁最旧）。
const alertEventKeepPerTenant = 1000

// AppendEvent 追加状态转变事件（只增不删）+ 租户级保留上限裁剪（删最旧溢出）。
// owner 连接（引擎跨租户调用，RLS 放行）。
func (s *Store) AppendEvent(ctx context.Context, ev observability.AlertEvent) error {
	if ev.ID == "" {
		ev.ID = newRuleID() // 同款短 ID 方案（前缀语义通用：快照 ID 生成器）
	}
	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO alert_events (id, tenant_id, rule_id, rule_name, target_type, target_id,
			metric_name, value, threshold, operator, severity, status, fired_at, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		ev.ID, ev.TenantID, ev.RuleID, ev.RuleName, ev.TargetType, ev.TargetID,
		ev.MetricName, ev.Value, ev.Threshold, ev.Operator, ev.Severity, ev.Status, ev.FiredAt, ev.OccurredAt)
	if err != nil {
		return err
	}
	// 裁剪：仅当超出保留上限时删最旧（子查询定位各租户第 N 条之前的事件）。
	_, err = s.db.Pool().Exec(ctx,
		`DELETE FROM alert_events WHERE tenant_id=$1 AND id IN (
			SELECT id FROM alert_events WHERE tenant_id=$1 ORDER BY occurred_at DESC OFFSET $2)`,
		ev.TenantID, alertEventKeepPerTenant)
	return err
}

// ListAlertEvents 列出租户告警历史（时间倒序；limit<=0 用默认上限）。
func (s *Store) ListAlertEvents(ctx context.Context, limit int) ([]observability.AlertEvent, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > alertEventKeepPerTenant {
		limit = 200
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT id, rule_id, rule_name, target_type, target_id, metric_name, value, threshold,
			operator, severity, status, fired_at, occurred_at
		FROM alert_events WHERE tenant_id=$1 ORDER BY occurred_at DESC LIMIT $2`, tid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []observability.AlertEvent
	for rows.Next() {
		var ev observability.AlertEvent
		if err = rows.Scan(&ev.ID, &ev.RuleID, &ev.RuleName, &ev.TargetType, &ev.TargetID,
			&ev.MetricName, &ev.Value, &ev.Threshold, &ev.Operator, &ev.Severity,
			&ev.Status, &ev.FiredAt, &ev.OccurredAt); err != nil {
			return nil, err
		}
		ev.TenantID = tid
		out = append(out, ev)
	}
	return out, rows.Err()
}
