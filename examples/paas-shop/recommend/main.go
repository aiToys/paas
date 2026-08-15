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
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
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
	rdb        *redis.Client
	httpClient = observ.NewClient()
	productURL string        // product 服务地址
	cacheTTL   time.Duration // main 里从 RECOMMEND_CACHE_TTL env 读
	recCount   int           // main 里从 RECOMMEND_COUNT env 读
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

	// 业务配置读 appconfig 注入的 env（缺省值保证未配可用）
	cacheTTL = 5 * time.Minute
	if v := os.Getenv("RECOMMEND_CACHE_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cacheTTL = time.Duration(n) * time.Second
		}
	}
	recCount = 3
	if v := os.Getenv("RECOMMEND_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			recCount = n
		}
	}

	// NATS consumer：订阅 shop-events，商品变更时失效推荐缓存（事件驱动一致性）
	go startCacheInvalidator(os.Getenv("NATS_URL"))

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
	recs := pickRandom(all, userID, recCount)

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

// startCacheInvalidator 订阅 shop-events，收到商品变更/seed 事件时 DEL recommend:* 失效缓存。
// NATS_URL 空时降级（不订阅，缓存仅靠 TTL 过期，向后兼容未绑 MQ）。
func startCacheInvalidator(natsURL string) {
	if natsURL == "" {
		slog.Warn("NATS_URL 未设置，recommend 缓存失效仅靠 TTL（MQ 链路不可用）")
		return
	}
	nc, err := nats.Connect(natsURL,
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		slog.Warn("NATS 连接失败，缓存失效仅靠 TTL", "err", err)
		return
	}
	// QueueSubscribe + group=recommend-consumer：多副本 clustering 分担（与平台 consumer group 名一致）
	_, err = nc.QueueSubscribe("shop-events", "recommend-consumer", func(msg *nats.Msg) {
		var evt struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		// product.changed / product.bulk-seed 都触发全量失效（按 category 精细失效留后续）
		if evt.Type == "product.changed" || evt.Type == "product.bulk-seed" {
			iter := rdb.Scan(context.Background(), 0, "recommend:*", 0).Iterator()
			var deleted int64
			for iter.Next(context.Background()) {
				if err := rdb.Del(context.Background(), iter.Val()).Err(); err == nil {
					deleted++
				}
			}
			slog.Info("MQ 事件失效推荐缓存", "type", evt.Type, "deleted", deleted)
		}
	})
	if err != nil {
		slog.Warn("NATS 订阅失败", "err", err)
		return
	}
	slog.Info("recommend 已订阅 shop-events（recommend-consumer group）")
}

func maskRedis(url string) string {
	if len(url) > 20 {
		return url[:15] + "***" + fmt.Sprintf("(len=%d)", len(url))
	}
	return url
}
