// store_pg_delivery.go Webhook 投递记录的 PostgreSQL 实现（migration 0059）。
//
// 与 memoryStore 同构：按 ctx 租户过滤、Create 以 ctx 租户为准、跨租户 Get/Update 统一
// ErrDeliveryNotFound。主键 (tenant_id, pipeline_id, id)：幂等键在单 pipeline 内唯一，
// 不跨 pipeline 冲突（不同流水线可各自收到同一 delivery 来源的事件）。
package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// deliveryCols 与 scan 列序严格对齐（加列必须三处同步：常量 / scan / INSERT·UPDATE）。
const deliveryCols = `id, tenant_id, pipeline_id, app_id, event, branch, commit_sha, status, reason, run_id, replays, received_at, last_replay_at`

func (s *pgStore) ListDeliveries(ctx context.Context, pipelineID string, limit int) ([]WebhookDelivery, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+deliveryCols+` FROM pipeline_webhook_deliveries
		 WHERE tenant_id=$1 AND pipeline_id=$2
		 ORDER BY received_at DESC, id DESC LIMIT $3`, tid, pipelineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WebhookDelivery, 0, limit)
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *pgStore) GetDelivery(ctx context.Context, pipelineID, id string) (WebhookDelivery, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return WebhookDelivery{}, err
	}
	row := s.db.QueryRow(ctx,
		`SELECT `+deliveryCols+` FROM pipeline_webhook_deliveries
		 WHERE tenant_id=$1 AND pipeline_id=$2 AND id=$3`, tid, pipelineID, id)
	d, err := scanDelivery(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WebhookDelivery{}, ErrDeliveryNotFound
		}
		return WebhookDelivery{}, err
	}
	return d, nil
}

func (s *pgStore) CreateDelivery(ctx context.Context, d WebhookDelivery) (WebhookDelivery, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return WebhookDelivery{}, err
	}
	if d.ReceivedAt.IsZero() {
		d.ReceivedAt = time.Now()
	}
	d.TenantID = tid
	_, err = s.db.Exec(ctx,
		`INSERT INTO pipeline_webhook_deliveries (`+deliveryCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		d.ID, d.TenantID, d.PipelineID, d.AppID, d.Event, d.Branch, d.Commit,
		d.Status, d.Reason, d.RunID, d.Replays, d.ReceivedAt, d.LastReplayAt)
	if err != nil {
		return WebhookDelivery{}, err
	}
	return d, nil
}

func (s *pgStore) UpdateDelivery(ctx context.Context, d WebhookDelivery) (WebhookDelivery, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return WebhookDelivery{}, err
	}
	// 归属与首次接收时刻不可改（WHERE 已锁租户；不更新 received_at）。
	tag, err := s.db.Exec(ctx,
		`UPDATE pipeline_webhook_deliveries
		 SET app_id=$1, event=$2, branch=$3, commit_sha=$4, status=$5, reason=$6,
		     run_id=$7, replays=$8, last_replay_at=$9
		 WHERE tenant_id=$10 AND pipeline_id=$11 AND id=$12`,
		d.AppID, d.Event, d.Branch, d.Commit, d.Status, d.Reason,
		d.RunID, d.Replays, d.LastReplayAt, tid, d.PipelineID, d.ID)
	if err != nil {
		return WebhookDelivery{}, err
	}
	if tag.RowsAffected() == 0 {
		return WebhookDelivery{}, ErrDeliveryNotFound
	}
	return s.GetDelivery(ctx, d.PipelineID, d.ID)
}

// scanDelivery 复用 store_pg.go 的 rowScanner（同时兼容 pgx.Row 与 pgx.Rows）。
func scanDelivery(row rowScanner) (WebhookDelivery, error) {
	var d WebhookDelivery
	err := row.Scan(&d.ID, &d.TenantID, &d.PipelineID, &d.AppID, &d.Event, &d.Branch,
		&d.Commit, &d.Status, &d.Reason, &d.RunID, &d.Replays, &d.ReceivedAt, &d.LastReplayAt)
	if err != nil {
		return WebhookDelivery{}, err
	}
	return d, nil
}
