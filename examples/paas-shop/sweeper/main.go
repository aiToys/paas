// examples/paas-shop/sweeper/main.go
// sweeper：定时关单 CronJob——扫 pending 且超 TTL 的订单，事务内置 closed + 回补库存 + 发 order.closed。
// 平台 CronJob 语义：单次跑完退出（schedule 控制周期）；ORDER_TTL_MINUTES test=2（演示立等可取）/prod=30。
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitoys/paas-examples/paas-shop/internal/natspub"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL 必填")
		os.Exit(1)
	}
	ttl := 30 * time.Minute
	if v := os.Getenv("ORDER_TTL_MINUTES"); v != "" {
		if d, err := time.ParseDuration(v + "m"); err == nil {
			ttl = d
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("PG 连接失败", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	np := natspub.Connect(os.Getenv("NATS_URL"))

	n, err := sweep(ctx, db, np, ttl)
	if err != nil {
		slog.Error("关单失败", "err", err)
		os.Exit(1)
	}
	slog.Info("关单完成", "closed", n, "ttl", ttl)
}

// sweep 扫描超时 pending 订单逐个事务关闭（复用 order.Transition 同款语义：仅 pending 可 close）。
func sweep(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, ttl time.Duration) (int, error) {
	rows, err := db.Query(ctx,
		`SELECT id FROM orders WHERE status='pending' AND created_at < now() - $1::interval`, ttl.String())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	closed := 0
	for _, id := range ids {
		if err := closeOrder(ctx, db, np, id); err != nil {
			slog.Warn("单笔关单失败（跳过）", "id", id, "err", err)
			continue
		}
		closed++
	}
	return closed, nil
}

// closeOrder 事务：锁订单校验 pending → 回补库存 → 置 closed → 提交后发事件。
// （独立于 order 服务实现：CronJob 直连 DB，不经 HTTP/Action-Token。）
func closeOrder(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, id string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var totalCents int
	var itemsJSON []byte
	if err := tx.QueryRow(ctx,
		`SELECT status,total_cents,items FROM orders WHERE id=$1 FOR UPDATE`, id).
		Scan(&status, &totalCents, &itemsJSON); err != nil {
		return err
	}
	if status != "pending" { // 已被支付/退款并发变更，跳过
		return nil
	}
	var items []struct {
		ProductID int `json:"productId"`
		Qty       int `json:"qty"`
	}
	_ = json.Unmarshal(itemsJSON, &items)
	for _, it := range items {
		if _, err := tx.Exec(ctx,
			`UPDATE products SET stock = stock + $1 WHERE id = $2`, it.Qty, it.ProductID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status='closed', updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "order.closed", "orderId": id, "status": "closed",
		"amount": totalCents, "at": time.Now().Format(time.RFC3339),
	})
	np.Publish("shop-events", payload)
	return nil
}
