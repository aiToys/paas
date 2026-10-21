package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aitoys/paas/internal/observability"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// channelCols 与 scanChannel 顺序对齐（加列必须三处同步：常量/scan/INSERT·UPDATE）。
const channelCols = `id, tenant_id, name, kind, url, headers, enabled, cooldown_minutes, mute_until, created_at, updated_at`

func scanChannel(r storagepg.RowScanner, ch *observability.NotificationChannel) error {
	var headers []byte
	var mute *time.Time
	if err := r.Scan(&ch.ID, &ch.TenantID, &ch.Name, &ch.Kind, &ch.URL, &headers,
		&ch.Enabled, &ch.CooldownMinutes, &mute, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
		return err
	}
	ch.Headers = nil
	if len(headers) > 0 {
		if err := json.Unmarshal(headers, &ch.Headers); err != nil {
			return fmt.Errorf("解析通知渠道 %s 的 headers 失败: %w", ch.ID, err)
		}
	}
	if mute != nil {
		ch.MuteUntil = *mute
	}
	return nil
}

func encodeHeaders(h map[string]string) any {
	if len(h) == 0 {
		return nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil
	}
	return b
}

func encodeMute(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ListNotificationChannels 列出当前租户的通知渠道，按 name 排序。
func (s *Store) ListNotificationChannels(ctx context.Context) ([]observability.NotificationChannel, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+channelCols+` FROM notification_channels WHERE tenant_id=$1 ORDER BY name, id`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []observability.NotificationChannel
	for rows.Next() {
		var ch observability.NotificationChannel
		if err := scanChannel(rows, &ch); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// CreateNotificationChannel 创建渠道（ID 自动生成；ctx 租户为准忽略请求体 TenantID）。
func (s *Store) CreateNotificationChannel(ctx context.Context, ch observability.NotificationChannel) (observability.NotificationChannel, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return observability.NotificationChannel{}, err
	}
	ch.Normalize()
	if err := ch.Validate(); err != nil {
		return observability.NotificationChannel{}, err
	}
	now := time.Now()
	ch.ID = fmt.Sprintf("ch-%d", now.UnixNano())
	ch.TenantID = tid
	ch.CreatedAt, ch.UpdatedAt = now, now
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO notification_channels (`+channelCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		ch.ID, ch.TenantID, ch.Name, ch.Kind, ch.URL, encodeHeaders(ch.Headers),
		ch.Enabled, ch.CooldownMinutes, encodeMute(ch.MuteUntil), ch.CreatedAt, ch.UpdatedAt)
	if storagepg.IsUniqueViolation(err) {
		return observability.NotificationChannel{}, fmt.Errorf("通知渠道已存在: %s", ch.Name)
	}
	if err != nil {
		return observability.NotificationChannel{}, err
	}
	return ch, nil
}

// UpdateNotificationChannel 更新渠道（仅当前租户；敏感字段掩码占位保留原值）。
func (s *Store) UpdateNotificationChannel(ctx context.Context, id string, ch observability.NotificationChannel) (observability.NotificationChannel, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return observability.NotificationChannel{}, err
	}
	cur, err := s.getNotificationChannel(ctx, tid, id)
	if err != nil {
		return observability.NotificationChannel{}, err
	}
	ch = ch.MergeSecretUpdates(cur)
	ch.Normalize()
	if err := ch.Validate(); err != nil {
		return observability.NotificationChannel{}, err
	}
	ch.UpdatedAt = time.Now()
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE notification_channels SET name=$3, kind=$4, url=$5, headers=$6, enabled=$7,
		 cooldown_minutes=$8, mute_until=$9, updated_at=$10 WHERE id=$1 AND tenant_id=$2`,
		ch.ID, ch.TenantID, ch.Name, ch.Kind, ch.URL, encodeHeaders(ch.Headers),
		ch.Enabled, ch.CooldownMinutes, encodeMute(ch.MuteUntil), ch.UpdatedAt)
	if storagepg.IsUniqueViolation(err) {
		return observability.NotificationChannel{}, fmt.Errorf("通知渠道已存在: %s", ch.Name)
	}
	if err != nil {
		return observability.NotificationChannel{}, err
	}
	if tag.RowsAffected() == 0 {
		return observability.NotificationChannel{}, fmt.Errorf("渠道不存在: %s", id)
	}
	return ch, nil
}

// DeleteNotificationChannel 删除渠道（仅当前租户；跨租户统一 not found 不泄漏）。
func (s *Store) DeleteNotificationChannel(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM notification_channels WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("渠道不存在: %s", id)
	}
	return nil
}

// getNotificationChannel 读取单条（含明文 URL/Headers——仅供更新合并，不对外输出）。
func (s *Store) getNotificationChannel(ctx context.Context, tid, id string) (observability.NotificationChannel, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+channelCols+` FROM notification_channels WHERE id=$1 AND tenant_id=$2`, id, tid)
	var ch observability.NotificationChannel
	if err := scanChannel(row, &ch); err != nil {
		return observability.NotificationChannel{}, fmt.Errorf("渠道不存在: %s", id)
	}
	return ch, nil
}
