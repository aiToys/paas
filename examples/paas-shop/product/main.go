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
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/aitoys/paas-examples/paas-shop/internal/ccpull"
	"github.com/aitoys/paas-examples/paas-shop/internal/natspub"
	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
	"github.com/aitoys/paas-examples/paas-shop/internal/search"
	"github.com/aitoys/paas-examples/paas-shop/internal/storage"
	"github.com/aitoys/paas-examples/paas-shop/internal/vector"
)

type Product struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Price       float64   `json:"price"`
	Category    string    `json:"category"`
	Stock       int       `json:"stock"`
	Description string    `json:"description"`
	ImageURL    string    `json:"imageUrl,omitempty"` // minio 图片（storage 绑定，未绑为空）
	CreatedAt   time.Time `json:"created_at"`
}

var db *sql.DB
var pub *natspub.Publisher
var cc *ccpull.Puller   // 配置中心动态拉取（PAAS_CONFIGCENTER_NS 未配则 nil）
var vec *vector.Client  // 语义搜索（qdrant，降级可空）
var mei *search.Client  // 全文搜索（meilisearch，降级可空）
var sto *storage.Client // 图片上传（minio，降级可空）

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
	// 三类可选数据服务客户端（绑定注入 env；未绑则降级 stub，搜索回落 SQL，图片返空 URL）：
	//   vector(qdrant) 语义搜索 / search(meilisearch) 全文搜索 / storage(minio) 图片。
	vec = vector.New()
	mei = search.New()
	sto = storage.New()
	if err := mei.EnsureIndex(ctx(10)); err != nil {
		slog.Warn("meilisearch 索引初始化失败（全文搜索降级）", "err", err)
	}
	if err := sto.EnsureBucket(ctx(10)); err != nil {
		slog.Warn("minio bucket 初始化失败（图片上传降级）", "err", err)
	}
	// qdrant collection 启动即建（幂等）：存量库也要（syncSeedToSearch 仅新库 seed 跑，
	// 存量商品向量靠写路径逐步补，collection 必须先存在）。
	if err := vec.EnsureCollection(ctx(15)); err != nil {
		slog.Warn("qdrant collection 初始化失败（语义搜索降级）", "err", err)
	}
	if err := migrateAndSeed(ctx(10)); err != nil {
		slog.Error("建表/seed 失败", "err", err)
		os.Exit(1)
	}
	// 配置中心动态配置（published 轮询 10s，版本变更热生效；未配 ns 则跳过）。
	// 演示 key：shop_notice（店铺公告）/ product_page_size（分页大小，覆盖 env）。
	cc = ccpull.New()
	if cc != nil {
		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cc.Start(runCtx, 10*time.Second, func(snap map[string]string) {
			if v := snap["product_page_size"]; v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
					os.Setenv("PRODUCT_PAGE_SIZE", v) // 热更新分页（与 env 同一消费点 pageSizeFromEnv）
					slog.Info("product_page_size 热更新", "value", n)
				}
			}
			if n := snap["shop_notice"]; n != "" {
				slog.Info("店铺公告", "notice", n)
			}
		})
	}
	slog.Info("product 服务就绪", "db", maskDSN(dsn))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/configz", func(w http.ResponseWriter, _ *http.Request) {
		// 配置中心演示：当前生效的动态配置快照 + 版本（未启用配置中心时提示）。
		if cc == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "hint": "设 PAAS_CONFIGCENTER_NS 启用"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true, "namespace": cc.Snapshot(), "version": cc.Version(),
		})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/products", productsHandler)       // GET 列表 / POST 创建
	mux.HandleFunc("/products/", productDetailHandler) // GET /products/{id} / POST /products/{id}/image

	h := observ.Recover(observ.Handler("product", observ.LaneMiddleware(mux)))
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
	// 存量库补列（已部署 PG 增量，ADD COLUMN IF NOT EXISTS 幂等）；image_url 归 minio 演示。
	if _, err := db.ExecContext(ctx, `ALTER TABLE products ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("补列 image_url: %w", err)
	}
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
	syncSeedToSearch(ctx, seeds)
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
		// 搜索降级链：语义(qdrant) -> 全文(meili) -> SQL ILIKE。
		// 语义优先（"便宜好用的外设"这类自然语言查询能命中）；q 空或全部降级时 SQL。
		if ids, ok := semanticSearch(r, q, limit); ok {
			out := productsByIDs(r.Context(), ids, category)
			writeJSON(w, http.StatusOK, out)
			return
		}
		if ids, ok := fulltextSearch(r, q, limit); ok {
			out := productsByIDs(r.Context(), ids, category)
			writeJSON(w, http.StatusOK, out)
			return
		}
		query, args := buildSearchQuery(q, category, limit)
		end := observ.MiddlewareSpan(r.Context(), "db.query products",
			observ.AttrDBSystem.String("postgresql"), observ.AttrDBStatement.String(stmtDigest(query)))
		rows, err := db.QueryContext(r.Context(), query, args...)
		end()
		if err != nil {
			slog.Error("query products", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []Product{}
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description, &p.ImageURL, &p.CreatedAt); err != nil {
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
		end := observ.MiddlewareSpan(r.Context(), "db.insert products",
			observ.AttrDBSystem.String("postgresql"), observ.AttrDBOperation.String("INSERT"))
		err := db.QueryRowContext(r.Context(),
			`INSERT INTO products(name,price,category,stock,description) VALUES($1,$2,$3,$4,$5) RETURNING id`,
			p.Name, p.Price, p.Category, p.Stock, p.Description).Scan(&p.ID)
		end()
		if err != nil {
			slog.Error("insert", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// 写路径同步三索引（best-effort：失败仅 log，主流程 PG 已成功）
		syncSearchIndexes(r.Context(), p)
		// 发商品变更事件到 shop-events（recommend 订阅失效缓存）
		publishProductEvent("product.changed", p)
		writeJSON(w, http.StatusCreated, p)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func productDetailHandler(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Path[len("/products/"):]
	// POST /products/{id}/image -> 图片上传（minio 演示）。先于 GET-only 检查（POST 非法路径仍 405）。
	if strings.HasSuffix(idStr, "/image") && r.Method == http.MethodPost {
		idNum, err := strconv.Atoi(strings.TrimSuffix(idStr, "/image"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		imageUploadHandler(w, r, idNum)
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var p Product
	end := observ.MiddlewareSpan(r.Context(), "db.query product by id",
		observ.AttrDBSystem.String("postgresql"), observ.AttrDBOperation.String("SELECT"))
	err = db.QueryRowContext(r.Context(),
		`SELECT id,name,price,category,stock,description,image_url,created_at FROM products WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description, &p.ImageURL, &p.CreatedAt)
	end()
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

// syncSearchIndexes 写路径同步 meili + qdrant 索引（best-effort）。
func syncSearchIndexes(ctx context.Context, p Product) {
	if err := mei.UpsertProduct(ctx, search.Doc{ID: p.ID, Name: p.Name, Description: p.Description, Category: p.Category}); err != nil {
		slog.Warn("meili 同步失败", "id", p.ID, "err", err)
	}
	if err := vec.UpsertProduct(ctx, p.ID, p.Name, p.Description, p.Category); err != nil {
		slog.Warn("qdrant 同步失败", "id", p.ID, "err", err)
	}
}

// syncSeedToSearch seed 后把存量商品同步进索引（best-effort；qdrant collection 需维度探测成功）。
func syncSeedToSearch(ctx context.Context, seeds []Product) {
	for _, p := range seeds {
		syncSearchIndexes(ctx, p)
	}
}

// semanticSearch qdrant 语义搜索；ok=false 表示不可用/失败（调用方走下一级）。
func semanticSearch(r *http.Request, q string, limit int) ([]int, bool) {
	if q == "" || !vec.Available() {
		return nil, false
	}
	end := observ.MiddlewareSpan(r.Context(), "vector.search products",
		observ.AttrDBSystem.String("qdrant"))
	scored, err := vec.Search(r.Context(), q, limit)
	end()
	if err != nil {
		slog.Warn("语义搜索失败（回落全文）", "q", q, "err", err)
		return nil, false
	}
	if len(scored) == 0 {
		return nil, false // 无命中回落全文/SQL
	}
	ids := make([]int, 0, len(scored))
	for _, s := range scored {
		ids = append(ids, s.ID)
	}
	return ids, true
}

// fulltextSearch meilisearch 全文搜索；ok=false 表示不可用/失败（调用方走 SQL）。
func fulltextSearch(r *http.Request, q string, limit int) ([]int, bool) {
	if q == "" || !mei.Available() {
		return nil, false
	}
	end := observ.MiddlewareSpan(r.Context(), "fulltext.search products",
		observ.AttrDBSystem.String("meilisearch"))
	hits, err := mei.Search(r.Context(), q, limit)
	end()
	if err != nil {
		slog.Warn("全文搜索失败（回落 SQL）", "q", q, "err", err)
		return nil, false
	}
	if len(hits) == 0 {
		return nil, false
	}
	ids := make([]int, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return ids, true
}

// productsByIDs 按 ID 序回表（保持搜索相关性排序），category 非空时过滤。
// 空结果返空切片（语义搜到的 ID 可能已被删）。
func productsByIDs(ctx context.Context, ids []int, category string) []Product {
	out := []Product{}
	byID := make(map[int]Product, len(ids))
	args := []any{}
	ph := []string{}
	for _, id := range ids {
		ph = append(ph, "$"+strconv.Itoa(len(args)+1))
		args = append(args, id)
	}
	query := "SELECT id,name,price,category,stock,description,image_url,created_at FROM products WHERE id IN (" + strings.Join(ph, ",") + ")"
	if category != "" {
		query += " AND category = $" + strconv.Itoa(len(args)+1)
		args = append(args, category)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		slog.Error("productsByIDs", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description, &p.ImageURL, &p.CreatedAt); err != nil {
			continue
		}
		byID[p.ID] = p
	}
	for _, id := range ids { // 按搜索相关性序输出
		if p, ok := byID[id]; ok {
			out = append(out, p)
		}
	}
	return out
}

// imageUploadHandler POST /products/{id}/image —— minio 对象存储演示（multipart 图片上传）。
// 返回 {url}；未绑 storage 返 503 提示绑定。
func imageUploadHandler(w http.ResponseWriter, r *http.Request, id int) {
	if !sto.Available() {
		http.Error(w, "storage 未绑定（平台创建 storage 数据服务并绑定应用后可用）", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB 上限
		http.Error(w, "bad multipart", http.StatusBadRequest)
		return
	}
	f, hdr, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "缺 image 文件字段", http.StatusBadRequest)
		return
	}
	defer f.Close()
	ct := hdr.Header.Get("Content-Type")
	ext := ""
	switch {
	case strings.Contains(ct, "png"):
		ext = "png"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		ext = "jpg"
	case strings.Contains(ct, "webp"):
		ext = "webp"
	case strings.HasSuffix(hdr.Filename, ".png"):
		ext = "png"
	case strings.HasSuffix(hdr.Filename, ".jpg"), strings.HasSuffix(hdr.Filename, ".jpeg"):
		ext = "jpg"
	default:
		http.Error(w, "仅支持 png/jpg/webp", http.StatusUnsupportedMediaType)
		return
	}
	key := fmt.Sprintf("products/%d/%d.%s", id, time.Now().UnixMilli(), ext)
	end := observ.MiddlewareSpan(r.Context(), "object.put image",
		observ.AttrDBSystem.String("minio"))
	url, err := sto.PutImage(r.Context(), key, f, hdr.Size, ct)
	end()
	if err != nil {
		slog.Error("图片上传失败", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 图片 URL 回写商品（前端详情抽屉展示）。存 bff 代理相对路径 /images/<bucket>/<key>：
	// minio 集群内 FQDN 浏览器不可达，bff imageProxy 反代（前端同域可达）。
	_, _ = db.ExecContext(r.Context(), `ALTER TABLE products ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT ''`)
	_, err = db.ExecContext(r.Context(), `UPDATE products SET image_url=$1 WHERE id=$2`, url, id)
	if err != nil {
		slog.Warn("image_url 回写失败", "err", err)
	}
	webURL := ""
	if url != "" {
		if u, err := urlParse(url); err == nil {
			webURL = "/images/" + strings.Join(u[3:], "/") // http://host/bucket/key -> /images/bucket/key
		}
	}
	// 回写用浏览器可达路径；响应返两个 URL（集群内 + web）。
	if webURL != "" {
		_, _ = db.ExecContext(r.Context(), `UPDATE products SET image_url=$1 WHERE id=$2`, webURL, id)
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": webURL, "internalUrl": url})
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

// urlParse 极简 URL 拆段：[scheme,host,path...]，path 首段起拼（url.Parse 的轻量替身，示例够用）。
func urlParse(u string) ([]string, error) {
	if !strings.Contains(u, "://") {
		return nil, fmt.Errorf("invalid url")
	}
	parts := strings.SplitN(u, "://", 2)
	segs := append([]string{parts[0]}, strings.Split(parts[1], "/")...)
	if len(segs) < 4 {
		return nil, fmt.Errorf("url 缺 bucket/key")
	}
	return segs, nil
}

// stmtDigest SQL 语句摘要（截 80 字符）作 span 属性 db.statement——足够辨认语句，
// 又不把完整 SQL（含参数位）灌进 trace。
func stmtDigest(q string) string {
	if len(q) <= 80 {
		return q
	}
	return q[:80] + "…"
}

// buildSearchQuery 拼商品搜索 SQL（参数化防注入）。q→name ILIKE，category→精确，limit→分页。
func buildSearchQuery(q, category string, limit int) (string, []any) {
	base := "SELECT id,name,price,category,stock,description,image_url,created_at FROM products"
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
	w.Header().Set("X-Paas-Service", observ.ServiceFingerprint()) // 泳道演示：实例指纹可见
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
