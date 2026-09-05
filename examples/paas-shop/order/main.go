// examples/paas-shop/order/main.go
// order 服务：paas-shop 订单最小闭环（下单事务扣库存 → 支付/发货/确认 → 退款/超时关单）。
// 事件经 NATS shop-events 主题广播（事务提交后发，at-most-once）；金额单位分（cents）。
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aitoys/paas-examples/paas-shop/internal/natspub"
	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

// Item 订单行（快照商品名/价格，防商品改价影响历史订单展示）。
type Item struct {
	ProductID  int    `json:"productId"`
	Name       string `json:"name"`
	Qty        int    `json:"qty"`
	PriceCents int    `json:"priceCents"`
}

// Order 订单聚合。TotalCents 服务端按商品现价计算（防客户端伪造）。
type Order struct {
	ID         string    `json:"id"`
	UserID     string    `json:"userId"`
	Status     string    `json:"status"`
	TotalCents int       `json:"totalCents"`
	Items      []Item    `json:"items"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type createReq struct {
	UserID string `json:"userId"`
	Items  []struct {
		ProductID int `json:"productId"`
		Qty       int `json:"qty"`
	} `json:"items"`
}

// 业务指标（Grafana 演示：下单 QPS + 订单金额分布）。
var (
	orderCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_order_created_total", Help: "下单成功总数",
	})
	orderAmount = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "shop_order_amount_cents", Help: "订单金额分布（分）",
		Buckets: []float64{1000, 5000, 10000, 50000, 100000, 500000},
	})
)

func init() { prometheus.MustRegister(orderCreated, orderAmount) }

var actionToken string // env PAAS_ORDER_ACTION_TOKEN；空=不校验（本地 dev）

// actionWhitelist 状态动作白名单（POST /orders/{id}/{action} 可用动作）。
var actionWhitelist = map[string]bool{
	"pay": true, "ship": true, "confirm": true, "refund": true, "close": true,
}

// eventNames 动作 → 事件类型（事件表事实用过去式，spec §4；显式枚举防拼接泄漏内部动作名）。
// 注意 confirm → order.confirmed（动作过去式）而非状态值 order.completed。
var eventNames = map[string]string{
	"pay": "order.paid", "ship": "order.shipped", "confirm": "order.confirmed",
	"refund": "order.refunded", "close": "order.closed",
}

// sentinel 错误：handler 经 errors.Is 分类映射 HTTP 状态码（禁中文子串匹配）。
var (
	ErrProductNotFound   = errors.New("商品不存在")
	ErrOrderNotFound     = errors.New("订单不存在")
	ErrInsufficientStock = errors.New("库存不足")
	ErrIllegalTransition = errors.New("非法状态迁移")
)

// newOrderID 生成 ORD-<unixnano 后 10 位>（spec §3）。
func newOrderID() string {
	return fmt.Sprintf("ORD-%010d", time.Now().UnixNano()%1e10)
}

func main() {
	shutdown := observ.Init("paas-shop-order")
	defer shutdown()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL 必填（与商品同库）")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("PG 连接失败", "err", err, "dsn", maskDSN(dsn))
		os.Exit(1)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		slog.Error("迁移失败", "err", err)
		os.Exit(1)
	}
	np := natspub.Connect(os.Getenv("NATS_URL")) // 降级 stub 不阻断
	actionToken = os.Getenv("PAAS_ORDER_ACTION_TOKEN")

	h := observ.Recover(observ.Handler("order", observ.LaneMiddleware(buildMux(db, np))))
	srv := &http.Server{Addr: ":8084", Handler: h, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("order 就绪", "addr", ":8084")
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

// migrate 建表幂等（与 product 服务同库，ALTER 兼容追加）。
func migrate(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS orders (
		id          TEXT PRIMARY KEY,
		user_id     TEXT NOT NULL,
		status      TEXT NOT NULL,
		total_cents INT NOT NULL,
		items       JSONB NOT NULL,
		created_at  TIMESTAMPTZ DEFAULT now(),
		updated_at  TIMESTAMPTZ DEFAULT now()
	)`)
	return err
}

// buildMux 构造路由（db/np 参数化供测试注入 nil——鉴权层不触 DB）。
func buildMux(db *pgxpool.Pool, np *natspub.Publisher) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			requireToken(handleCreate(db, np))(w, r)
		case http.MethodGet:
			handleList(db)(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/orders/")
		parts := strings.Split(rest, "/")
		if len(parts) == 1 && r.Method == http.MethodGet {
			handleGet(db)(w, r, parts[0])
			return
		}
		// POST /orders/{id}/{action}
		if len(parts) == 2 && r.Method == http.MethodPost {
			requireToken(handleAction(db, np, parts[0], parts[1]))(w, r)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	return mux
}

// requireToken 变更类端点鉴权：X-Action-Token == env（env 空跳过，本地 dev）。
// 常量时间比较防时序侧信道（与主仓 pipeline webhook trigger 同款先例）。
func requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if actionToken != "" &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Action-Token")), []byte(actionToken)) != 1 {
			http.Error(w, "invalid action token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Paas-Service", observ.ServiceFingerprint())
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleCreate 下单：服务端查价 + 单事务扣库存（条件更新防超卖）+ 提交后发事件。
func handleCreate(db *pgxpool.Pool, np *natspub.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" || len(req.Items) == 0 {
			http.Error(w, "userId 和 items 必填", http.StatusBadRequest)
			return
		}
		for _, it := range req.Items {
			if it.ProductID <= 0 || it.Qty <= 0 {
				http.Error(w, "productId/qty 必须为正", http.StatusBadRequest)
				return
			}
		}
		o, err := createOrder(r.Context(), db, np, req)
		if err != nil {
			switch {
			case errors.Is(err, ErrProductNotFound):
				http.Error(w, err.Error(), http.StatusBadRequest)
			case errors.Is(err, ErrInsufficientStock):
				http.Error(w, err.Error(), http.StatusConflict)
			default:
				slog.Error("下单失败", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
			return
		}
		orderCreated.Inc()
		orderAmount.Observe(float64(o.TotalCents))
		writeJSON(w, http.StatusCreated, o)
	}
}

// createOrder 事务：锁商品行读现价 → INSERT 订单 → 条件更新扣库存（rows=0 回滚 409）。
func createOrder(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, req createReq) (Order, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	items := make([]Item, 0, len(req.Items))
	total := 0
	for _, it := range req.Items {
		var name string
		var price float64
		err := tx.QueryRow(ctx,
			`SELECT name, price FROM products WHERE id=$1 FOR UPDATE`, it.ProductID).Scan(&name, &price)
		if err != nil {
			return Order{}, fmt.Errorf("%w: %d", ErrProductNotFound, it.ProductID)
		}
		cents := int(math.Round(price * 100))
		items = append(items, Item{ProductID: it.ProductID, Name: name, Qty: it.Qty, PriceCents: cents})
		total += cents * it.Qty
	}

	o := Order{ID: newOrderID(), UserID: req.UserID, Status: StatusPending, TotalCents: total, Items: items}
	itemsJSON, _ := json.Marshal(items)
	// RETURNING 读回 DB 生成的时间戳，create 响应与 GET 口径一致。
	if err := tx.QueryRow(ctx,
		`INSERT INTO orders(id,user_id,status,total_cents,items) VALUES($1,$2,$3,$4,$5)
		 RETURNING created_at, updated_at`,
		o.ID, o.UserID, o.Status, o.TotalCents, itemsJSON).Scan(&o.CreatedAt, &o.UpdatedAt); err != nil {
		return Order{}, err
	}
	for _, it := range items {
		tag, err := tx.Exec(ctx,
			`UPDATE products SET stock = stock - $1 WHERE id = $2 AND stock >= $1`, it.Qty, it.ProductID)
		if err != nil {
			return Order{}, err
		}
		if tag.RowsAffected() == 0 {
			return Order{}, fmt.Errorf("%w: 商品 %s 剩余不足 %d", ErrInsufficientStock, it.Name, it.Qty)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	publishEvent(np, "order.created", o)
	return o, nil
}

// handleAction 统一动作端点：事务内 FOR UPDATE 锁订单 → 状态机校验 →
// refund/close 回补库存 → 更新状态 → 提交后发事件。
func handleAction(db *pgxpool.Pool, np *natspub.Publisher, id, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !actionWhitelist[action] {
			http.Error(w, "unknown action", http.StatusNotFound)
			return
		}
		o, err := applyAction(r.Context(), db, np, id, action)
		if err != nil {
			switch {
			case errors.Is(err, ErrOrderNotFound):
				http.Error(w, err.Error(), http.StatusNotFound)
			case errors.Is(err, ErrIllegalTransition):
				http.Error(w, err.Error(), http.StatusConflict)
			default:
				slog.Error("订单动作失败", "id", id, "action", action, "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
			return
		}
		writeJSON(w, http.StatusOK, o)
	}
}

// applyAction sweeper 复用（close）；回补库存按 items 快照数量。
func applyAction(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, id, action string) (Order, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var o Order
	var itemsJSON []byte
	err = tx.QueryRow(ctx,
		`SELECT id,user_id,status,total_cents,items,created_at,updated_at FROM orders WHERE id=$1 FOR UPDATE`,
		id).Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &itemsJSON, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return Order{}, fmt.Errorf("%w: %s", ErrOrderNotFound, id)
	}
	_ = json.Unmarshal(itemsJSON, &o.Items)

	to, err := Transition(o.Status, action)
	if err != nil {
		return Order{}, fmt.Errorf("%w: %v", ErrIllegalTransition, err)
	}
	// 退款/关单回补库存（spec §3：refunded/closed 均回补）。
	if action == "refund" || action == "close" {
		for _, it := range o.Items {
			if _, err := tx.Exec(ctx,
				`UPDATE products SET stock = stock + $1 WHERE id = $2`, it.Qty, it.ProductID); err != nil {
				return Order{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status=$1, updated_at=now() WHERE id=$2`, to, id); err != nil {
		return Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	o.Status = to
	publishEvent(np, eventNames[action], o)
	return o, nil
}

// publishEvent 事务提交后广播（at-most-once 可接受，spec §3）。
func publishEvent(np *natspub.Publisher, typ string, o Order) {
	if np == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"type": typ, "orderId": o.ID, "userId": o.UserID, "status": o.Status,
		"amount": o.TotalCents, "at": time.Now().Format(time.RFC3339),
	})
	np.Publish("shop-events", payload)
}

func handleList(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		where, args := statusFilter(r.URL.Query().Get("status"))
		rows, err := db.Query(r.Context(),
			`SELECT id,user_id,status,total_cents,items,created_at,updated_at FROM orders`+
				where+` ORDER BY created_at DESC LIMIT 100`,
			args...)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		list := []Order{}
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				continue
			}
			list = append(list, o)
		}
		// 遍历中断（网络/服务端错误）必须显式感知，否则截断列表伪装成 200。
		if err := rows.Err(); err != nil {
			slog.Error("订单列表遍历失败", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}
}

// statusFilter status 过滤条件与参数同步构造（pgx 要求占位符与参数严格匹配，空条件零参数）。
func statusFilter(status string) (string, []any) {
	if status == "" {
		return "", nil
	}
	return ` WHERE status=$1`, []any{status}
}

// handleGet 返回三参闭包（路由层解出 id 后注入），非 http.HandlerFunc 签名。
func handleGet(db *pgxpool.Pool) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		row := db.QueryRow(r.Context(),
			`SELECT id,user_id,status,total_cents,items,created_at,updated_at FROM orders WHERE id=$1`, id)
		o, err := scanOrder(row)
		if err != nil {
			http.Error(w, "订单不存在", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, o)
	}
}

// scanner pgx.Row/Rows 共同接口（Get 单行 / List 多行复用）。
type scanner interface{ Scan(dest ...any) error }

func scanOrder(s scanner) (Order, error) {
	var o Order
	var itemsJSON []byte
	if err := s.Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &itemsJSON, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return Order{}, err
	}
	_ = json.Unmarshal(itemsJSON, &o.Items)
	return o, nil
}

// maskDSN 日志脱敏（与 product 服务同款）。
func maskDSN(dsn string) string {
	if i := strings.Index(dsn, "@"); i > 0 {
		return dsn[i:]
	}
	return dsn
}
