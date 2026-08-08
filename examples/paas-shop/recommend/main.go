// paas-shop 推荐服务：演示「微服务 + 缓存 + 服务间调用」。
//
// 平台创建 dataservice shop-cache(redis) -> 绑定应用 -> REDIS_URL 注入 env。
// 调 product 服务获取商品候选，推荐结果缓存到 redis（TTL 5min）。
// 调用链路：bff -> recommend -> product（跨服务 trace 链路）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

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

var (
	rdb         *redis.Client
	httpClient  = observ.NewClient()
	productURL  string // product 服务地址
	cacheTTL    = 5 * time.Minute
)

func main() {
	shutdown := observ.Init("paas-shop-recommend")
	defer shutdown()

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		slog.Error("REDIS_URL 未设置（应经平台数据服务绑定注入）")
		os.Exit(1)
	}
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		slog.Error("redis parse url", "err", err)
		os.Exit(1)
	}
	rdb = redis.NewClient(opt)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		slog.Error("redis ping", "err", err)
		os.Exit(1)
	}
	productURL = os.Getenv("PRODUCT_SERVICE_URL")
	if productURL == "" {
		productURL = "http://paas-shop-product:8081" // 同 ns 短名（K8s DNS）
	}
	slog.Info("recommend 服务就绪", "redis", maskRedis(redisURL), "product", productURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/recommend", recommendHandler)

	h := observ.Recover(observ.Handler("recommend", mux))
	srv := &http.Server{Addr: ":8082", Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		http.Error(w, "redis unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func recommendHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		userID = "anon"
	}
	cacheKey := "recommend:" + userID

	// 1. 查缓存
	if cached, err := rdb.Get(r.Context(), cacheKey).Result(); err == nil {
		var out []Product
		if err := json.Unmarshal([]byte(cached), &out); err == nil {
			w.Header().Set("X-Cache", "HIT")
			writeJSON(w, http.StatusOK, map[string]any{"products": out, "source": "cache"})
			return
		}
	}

	// 2. 调 product 服务获取商品（trace 传播）
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, productURL+"/products", nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Error("调 product 失败", "err", err)
		http.Error(w, "product unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	var all []Product
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil || len(all) == 0 {
		slog.Error("解析 product 响应", "err", err)
		http.Error(w, "product empty", http.StatusServiceUnavailable)
		return
	}

	// 3. 简单推荐：随机取 3 个（按 userID 确定性 seed）
	recs := pickRandom(all, userID, 3)

	// 4. 写缓存
	if data, err := json.Marshal(recs); err == nil {
		if err := rdb.Set(r.Context(), cacheKey, data, cacheTTL).Err(); err != nil {
			slog.Warn("写缓存失败", "err", err)
		}
	}
	w.Header().Set("X-Cache", "MISS")
	writeJSON(w, http.StatusOK, map[string]any{"products": recs, "source": "live"})
}

// pickRandom 用 userID 作 seed 确定性取 n 个（同用户稳定推荐，便于演示）。
func pickRandom(all []Product, seed string, n int) []Product {
	if len(all) <= n {
		return all
	}
	var key [32]byte
	copy(key[:], seed)
	rng := rand.New(rand.NewChaCha8(key))
	idxs := rng.Perm(len(all))
	out := make([]Product, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, all[idxs[i]])
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func maskRedis(url string) string {
	if len(url) > 20 {
		return url[:15] + "***" + fmt.Sprintf("(len=%d)", len(url))
	}
	return url
}
