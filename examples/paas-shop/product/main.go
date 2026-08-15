// paas-shop 商品服务：演示「微服务 + 数据库」绑定。
//
// 平台创建 dataservice shop-db(postgres) -> 绑定到应用 -> DATABASE_URL 注入工作负载 env。
// 启动连 PG，建表 + seed 商品，暴露 REST API。OTel trace + /metrics + slog。
//
// 调用链路：bff -> product（本服务）/ chatbot（function calling 工具调本服务）。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/aitoys/paas-examples/paas-shop/internal/natspub"
	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

type Product struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Price       float64   `json:"price"`
	Category    string    `json:"category"`
	Stock       int       `json:"stock"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

var db *sql.DB
var pub *natspub.Publisher

func main() {
	shutdown := observ.Init("paas-shop-product")
	defer shutdown()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL 未设置（应经平台数据服务绑定注入）")
		os.Exit(1)
	}
	var err error
	db, err = sql.Open("pgx", dsn)
	if err != nil {
		slog.Error("pgx open 失败", "err", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(10)
	if err := db.PingContext(ctx(5)); err != nil {
		slog.Error("PG ping 失败", "err", err, "dsn", maskDSN(dsn))
		os.Exit(1)
	}
	// NATS producer（shop-mq 绑定注入 NATS_URL；空则降级 stub，不阻断）。
	// 必须在 migrateAndSeed 之前初始化：seed 完成时要发 product.bulk-seed 事件。
	pub = natspub.Connect(os.Getenv("NATS_URL"))
	defer pub.Close()
	if err := migrateAndSeed(ctx(10)); err != nil {
		slog.Error("建表/seed 失败", "err", err)
		os.Exit(1)
	}
	slog.Info("product 服务就绪", "db", maskDSN(dsn))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/products", productsHandler)       // GET 列表 / POST 创建
	mux.HandleFunc("/products/", productDetailHandler) // GET /products/{id}

	h := observ.Recover(observ.Handler("product", mux))
	addr := ":8081"
	slog.Info("监听", "addr", addr)
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

func ctx(sec time.Duration) context.Context {
	c, cancel := context.WithTimeout(context.Background(), sec*time.Second)
	_ = cancel
	return c
}

func migrateAndSeed(ctx context.Context) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS products (
		id SERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		price NUMERIC(10,2) NOT NULL,
		category TEXT NOT NULL,
		stock INT NOT NULL DEFAULT 0,
		description TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return fmt.Errorf("建表: %w", err)
	}
	// category 索引加速按分类搜索/过滤
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_products_category ON products(category)`); err != nil {
		return fmt.Errorf("建索引: %w", err)
	}
	// 存量库补列（已部署 PG 增量，ADD COLUMN IF NOT EXISTS 幂等）
	if _, err := db.ExecContext(ctx, `ALTER TABLE products ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now()`); err != nil {
		return fmt.Errorf("补列: %w", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM products`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	seeds := []Product{
		{Name: "机械键盘 X1", Price: 299, Category: "外设", Stock: 100, Description: "热插拔轴体，RGB 背光"},
		{Name: "无线鼠标 M2", Price: 159, Category: "外设", Stock: 200, Description: "静音微动，续航 6 个月"},
		{Name: "4K 显示器 27寸", Price: 1899, Category: "显示", Stock: 50, Description: "IPS 面板，Type-C 65W 反充"},
		{Name: "降噪耳机 Pro", Price: 1299, Category: "音频", Stock: 80, Description: "主动降噪，LDAC 编码"},
		{Name: "USB-C 拓展坞", Price: 199, Category: "配件", Stock: 300, Description: "11 合 1，支持双 4K 输出"},
	}
	for _, p := range seeds {
		_, err := db.ExecContext(ctx, `INSERT INTO products(name,price,category,stock,description) VALUES($1,$2,$3,$4,$5)`,
			p.Name, p.Price, p.Category, p.Stock, p.Description)
		if err != nil {
			return fmt.Errorf("seed %s: %w", p.Name, err)
		}
	}
	slog.Info("seed 完成", "count", len(seeds))
	if pub != nil {
		payload, _ := json.Marshal(map[string]any{
			"type":  "product.bulk-seed",
			"count": len(seeds),
			"at":    time.Now().UTC().Format(time.RFC3339),
		})
		_ = pub.Publish("shop-events", payload)
	}
	return nil
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	if err := db.PingContext(ctx(2)); err != nil {
		http.Error(w, "db unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func productsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query().Get("q")
		category := r.URL.Query().Get("category")
		limit := pageSizeFromEnv(r, 20)
		query, args := buildSearchQuery(q, category, limit)
		rows, err := db.QueryContext(r.Context(), query, args...)
		if err != nil {
			slog.Error("query products", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []Product{}
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description, &p.CreatedAt); err != nil {
				slog.Error("scan", "err", err)
				continue
			}
			out = append(out, p)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var p Product
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if p.Name == "" || p.Price <= 0 {
			http.Error(w, "name 和 price 必填", http.StatusBadRequest)
			return
		}
		err := db.QueryRowContext(r.Context(),
			`INSERT INTO products(name,price,category,stock,description) VALUES($1,$2,$3,$4,$5) RETURNING id`,
			p.Name, p.Price, p.Category, p.Stock, p.Description).Scan(&p.ID)
		if err != nil {
			slog.Error("insert", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// 发商品变更事件到 shop-events（recommend 订阅失效缓存）
		publishProductEvent("product.changed", p)
		writeJSON(w, http.StatusCreated, p)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func productDetailHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idStr := r.URL.Path[len("/products/"):]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var p Product
	err = db.QueryRowContext(r.Context(),
		`SELECT id,name,price,category,stock,description,created_at FROM products WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description, &p.CreatedAt)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("query detail", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// publishProductEvent 发商品事件到 shop-events topic（NATS 降级时静默丢弃）。
func publishProductEvent(eventType string, p Product) {
	if pub == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"type":      eventType,
		"productId": p.ID,
		"name":      p.Name,
		"category":  p.Category,
		"at":        time.Now().UTC().Format(time.RFC3339),
	})
	if err := pub.Publish("shop-events", payload); err != nil {
		slog.Warn("发 NATS 事件失败", "type", eventType, "err", err)
	} else {
		slog.Info("发 NATS 事件", "type", eventType, "productId", p.ID)
	}
}

// buildSearchQuery 拼商品搜索 SQL（参数化防注入）。q→name ILIKE，category→精确，limit→分页。
func buildSearchQuery(q, category string, limit int) (string, []any) {
	base := "SELECT id,name,price,category,stock,description,created_at FROM products"
	where := ""
	args := []any{}
	if q != "" {
		where += "name ILIKE $" + strconv.Itoa(len(args)+1)
		args = append(args, "%"+q+"%")
	}
	if category != "" {
		if where != "" {
			where += " AND "
		}
		where += "category = $" + strconv.Itoa(len(args)+1)
		args = append(args, category)
	}
	if where != "" {
		base += " WHERE " + where
	}
	base += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	return base, args
}

// pageSizeFromEnv 读 PRODUCT_PAGE_SIZE appconfig 注入的 env，缺省 20，上限 100。
func pageSizeFromEnv(r *http.Request, fallback int) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		v = os.Getenv("PRODUCT_PAGE_SIZE")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > 100 {
		return 100
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func maskDSN(dsn string) string {
	// 日志脱敏：隐藏密码段，保留 host/db 便于排查。
	for i := 0; i < len(dsn)-1; i++ {
		if dsn[i] == ':' && dsn[i+1] == '/' {
			if at := indexOf(dsn, '@'); at > i {
				return dsn[:i+3] + "***" + dsn[at:]
			}
		}
	}
	return dsn
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
