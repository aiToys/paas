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

	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

type Product struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	Stock       int     `json:"stock"`
	Description string  `json:"description"`
}

var db *sql.DB

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
	mux.HandleFunc("/products", productsHandler)          // GET 列表 / POST 创建
	mux.HandleFunc("/products/", productDetailHandler)     // GET /products/{id}

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
		description TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return fmt.Errorf("建表: %w", err)
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
		rows, err := db.QueryContext(r.Context(), `SELECT id,name,price,category,stock,description FROM products ORDER BY id`)
		if err != nil {
			slog.Error("query products", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []Product{}
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description); err != nil {
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
		`SELECT id,name,price,category,stock,description FROM products WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description)
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
