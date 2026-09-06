// paas-shop BFF 聚合层：前端统一入口，聚合 product/recommend/chatbot。
//
// 前端（nginx）反代 /api/* 到本服务。本服务转发到各微服务，统一错误处理 + trace 传播。
// SSE 透传：/api/chat 流式响应透传 chatbot 的 SSE（按 data: 行透传，保持 chunk 实时到达）。
package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/aitoys/paas-examples/paas-shop/internal/dpdisc"
	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

var (
	httpClient   = observ.NewClient()
	streamClient = observ.NewStreamingClient() // SSE 透传用（chatbot 客服链路含长 reasoning，无整体超时）
	dp           *dpdisc.Discoverer            // 数据面发现（PAAS_DP_URL 未配则 nil，静态 URL 兜底）
	fallback     = map[string]string{          // 静态兜底：K8s Service DNS（发现不可用时与原行为一致）
		"product":   "http://paas-shop-product:8081",
		"recommend": "http://paas-shop-recommend:8082",
		"chatbot":   "http://paas-shop-chatbot:8083",
		"order":     "http://paas-shop-order:8084",
	}
)

// svcSuffix 下游查询名环境后缀（prod 桶注入 "-prod"）：dpdisc 查询名与治理 Service 名
// 同源派生。不带后缀时 bff-prod 会经数据面发现查到 test 实例（同租户同 ns，/dp/instances
// 无环境维度），跨环境串流量（R1 审计 C2）。
var svcSuffix = os.Getenv("PAAS_SVC_SUFFIX")

// svcURL 解析下游地址：数据面发现优先（/dp/instances，含泳道感知），静态 URL 兜底。
// 平台 K8s Service 名与工作负载名一致（test=paas-shop-product，prod=paas-shop-product-prod），
// dpdisc 按「基线名 + 环境后缀」查询。
func svcURL(r *http.Request, service string) string {
	if dp.Enabled() {
		lane := r.Header.Get("x-paas-lane")
		if u := dp.Addr(r.Context(), "paas-shop-"+service+svcSuffix, lane); u != "" {
			return u
		}
	}
	return fallback[service]
}

func main() {
	shutdown := observ.Init("paas-shop-bff")
	defer shutdown()
	if v := os.Getenv("PRODUCT_SERVICE_URL"); v != "" {
		fallback["product"] = v
	}
	if v := os.Getenv("RECOMMEND_SERVICE_URL"); v != "" {
		fallback["recommend"] = v
	}
	if v := os.Getenv("CHATBOT_SERVICE_URL"); v != "" {
		fallback["chatbot"] = v
	}
	if v := os.Getenv("ORDER_SERVICE_URL"); v != "" {
		fallback["order"] = v
	}
	// 数据面服务发现（平台 /dp/instances；未配 PAAS_DP_URL 时纯静态兜底，行为与原版一致）。
	dp = dpdisc.New()
	slog.Info("bff 就绪", "fallback", fallback, "dpDiscovery", dp.Enabled())

	events := newEventRing(100)
	stats := newStatsAgg()
	subscribeShopEvents(events, stats)

	// LaneMiddleware 在 Handler 内层（span 建立后提取 header 写 paas.lane 属性）；
	// 转发透传：proxy* 的 copyHeaders 已复制全部 header（x-paas-lane 天然到下游）。
	h := observ.Recover(observ.Handler("bff", observ.LaneMiddleware(buildMuxWithStats(events, stats))))
	srv := &http.Server{Addr: ":8080", Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

// buildMux 兼容入口（旧测试引用；stats 用空聚合器）。
func buildMux(events *eventRing) *http.ServeMux {
	return buildMuxWithStats(events, newStatsAgg())
}

// buildMuxWithStats 构造 bff 路由（events 通知中心 + stats 经营统计，测试可注入）。
func buildMuxWithStats(events *eventRing, stats *statsAgg) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/products", proxyTo("product", "/products"))        // GET 列表
	mux.HandleFunc("/api/products/", proxyPrefixTo("product", "/products")) // GET /{id}
	mux.HandleFunc("/api/recommend", proxyTo("recommend", "/recommend"))    // GET 推荐
	// 图片直读代理：minio 集群内 URL 浏览器不可达，经 bff 反代（公开读 bucket，无凭证）。
	mux.HandleFunc("/images/", imageProxy)
	mux.HandleFunc("/api/chat", chatProxy) // POST SSE 透传
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(events.latest(limit))
	})
	// 订单代理：/api/orders（列表/下单）与 /api/orders/{id}[/{action}]（查询/动作）统一转发。
	mux.HandleFunc("/api/orders", proxyOrder)
	mux.HandleFunc("/api/orders/", proxyOrder)
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONStats(w, stats.snapshot())
	})
	return mux
}

// proxyOrder 订单代理：路径后缀透传（/api/orders[...] → order /orders[...]），
// 变更类 POST 注入 X-Action-Token（前端不感知，spec §3）；地址解析泳道感知同其他代理。
func proxyOrder(w http.ResponseWriter, r *http.Request) {
	target := svcURL(r, "order") + "/orders" + strings.TrimPrefix(r.URL.Path, "/api/orders")
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.URL.RawQuery = r.URL.RawQuery // 透传 query（GET /orders?status=pending 过滤）
	copyHeaders(req.Header, r.Header)
	if tok := os.Getenv("PAAS_ORDER_ACTION_TOKEN"); tok != "" && r.Method != http.MethodGet {
		req.Header.Set("X-Action-Token", tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Error("order 转发失败", "target", target, "err", err)
		http.Error(w, "order unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	// 泳道演示可见性：响应带请求实际泳道 + bff 实例指纹（下游经 X-Paas-Service 透传）。
	w.Header().Set("X-Paas-Lane", observ.LaneOrBase(r.Context()))
	w.Header().Set("X-Paas-Bff", observ.ServiceFingerprint())
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSONStats(w http.ResponseWriter, s statsSnapshot) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}

// subscribeShopEvents 双消费组订阅 shop-events：bff-consumer 入环形缓冲（通知中心）、
// stats-consumer 累计经营统计（独立 group，各自 at-most-once 互不影响）。
// NATS 不可达仅告警不阻断（/api/events 返回空数组，BFF 其余转发不受影响）。
func subscribeShopEvents(events *eventRing, stats *statsAgg) {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		return
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		slog.Warn("NATS 连接失败，/api/events 将返回空", "err", err)
		return
	}
	_, err = nc.QueueSubscribe("shop-events", "bff-consumer", func(msg *nats.Msg) {
		var e shopEvent
		if err := json.Unmarshal(msg.Data, &e); err != nil {
			return
		}
		e.ReceivedAt = time.Now()
		events.push(e)
	})
	if err != nil {
		slog.Warn("NATS 订阅失败", "err", err)
		return
	}
	if _, err = nc.QueueSubscribe("shop-events", "stats-consumer", func(msg *nats.Msg) {
		stats.consume(msg.Data)
	}); err != nil {
		slog.Warn("NATS stats 订阅失败", "err", err)
		return
	}
	nc.Flush()
	slog.Info("bff 已订阅 shop-events（bff-consumer + stats-consumer 双消费组）")
}

// shopEvent 是 shop-events 主题的一条事件（product 商品变更 / order 订单生命周期）。
// 订单事件 payload：{type,orderId,userId,status,amount,at}（order 服务 publishEvent）。
type shopEvent struct {
	Type       string    `json:"type"`
	ProductID  int       `json:"productId"`
	Name       string    `json:"name"`
	Category   string    `json:"category"`
	OrderID    string    `json:"orderId,omitempty"`
	UserID     string    `json:"userId,omitempty"`
	Status     string    `json:"status,omitempty"`
	Amount     int       `json:"amount,omitempty"`
	At         time.Time `json:"at"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// statsAgg 经营统计（stats-consumer 消费组累计，内存态重启清零）：
// 维护 orderId → 最新状态映射，快照口径=「当前各状态订单数」（订单状态转移不重复计数，
// 区别于按事件累计）；GMV 只计已成交状态（paid/shipped/completed，pending/closed/refunded 不计）。
// 含锁实体不出包：对外一律经 snapshot() 转无锁 DTO（copylocks）。
type statsAgg struct {
	mu    sync.Mutex
	state map[string]orderStat // orderId -> 最新状态与金额
}

// orderStat 订单最新快照（事件每次全量携带，直接覆盖）。
type orderStat struct {
	status string
	amount int
}

// statsSnapshot 对外无锁快照（深拷贝，防外部读与 consume 并发写 race）。
type statsSnapshot struct {
	Orders   int            `json:"orders"`
	GMVCents int            `json:"gmvCents"`
	ByStatus map[string]int `json:"byStatus"`
}

func newStatsAgg() *statsAgg { return &statsAgg{state: map[string]orderStat{}} }

// consume 解析一条 shop-events 消息更新订单状态映射（解析失败静默跳过，统计不阻断通知链路）。
// 只认 order.* 事件（防未来非订单事件带 status 字段误计 ByStatus）。
func (a *statsAgg) consume(data []byte) {
	var e shopEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return
	}
	if !strings.HasPrefix(e.Type, "order.") || e.OrderID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state[e.OrderID] = orderStat{status: e.Status, amount: e.Amount}
}

func (a *statsAgg) snapshot() statsSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	byStatus := make(map[string]int, len(a.state))
	gmv := 0
	for _, os := range a.state {
		byStatus[os.status]++
		switch os.status {
		case "paid", "shipped", "completed": // 已成交口径：退款/关单/待支付不计 GMV
			gmv += os.amount
		}
	}
	return statsSnapshot{Orders: len(a.state), GMVCents: gmv, ByStatus: byStatus}
}

// eventRing 固定容量环形缓冲（mutex 保护），存最近 N 条事件供 /api/events 轮询。
type eventRing struct {
	mu   sync.Mutex
	buf  []shopEvent
	next int // 下一个覆写位置
}

func newEventRing(n int) *eventRing { return &eventRing{buf: make([]shopEvent, 0, n)} }

func (r *eventRing) push(e shopEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) < cap(r.buf) {
		r.buf = append(r.buf, e)
		return
	}
	r.buf[r.next] = e
	r.next = (r.next + 1) % cap(r.buf)
}

// latest 返回最近 n 条，最新在前（倒序）。
// buf 满后最旧元素在 next 位置，故从 next-1 倒退遍历才是真正的时间倒序。
func (r *eventRing) latest(n int) []shopEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.buf) {
		n = len(r.buf)
	}
	full := len(r.buf) == cap(r.buf)
	out := make([]shopEvent, n)
	for i := 0; i < n; i++ {
		idx := len(r.buf) - 1 - i
		if full {
			idx = (r.next - 1 - i + len(r.buf)) % len(r.buf)
		}
		out[i] = r.buf[idx]
	}
	return out
}

// proxyTo 透传到下游服务（svcURL 请求时解析：dp 发现优先 + 泳道感知，静态兜底）。
func proxyTo(service, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := svcURL(r, service) + path
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.URL.RawQuery = r.URL.RawQuery // 透传 query（搜索 q/category/limit 经 bff 生效的关键）
		copyHeaders(req.Header, r.Header)
		resp, err := httpClient.Do(req)
		if err != nil {
			slog.Error("转发失败", "target", target, "err", err)
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		copyHeaders(w.Header(), resp.Header)
		// 泳道演示可见性：响应带请求实际泳道 + bff 实例指纹（下游经 X-Paas-Service 透传）。
		w.Header().Set("X-Paas-Lane", observ.LaneOrBase(r.Context()))
		w.Header().Set("X-Paas-Bff", observ.ServiceFingerprint())
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// proxyPrefixTo 透传路径后缀（/api/products/{id} -> product/products/{id}），地址请求时解析。
func proxyPrefixTo(service, base string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		suffix := strings.TrimPrefix(r.URL.Path, "/api/products")
		req, err := http.NewRequestWithContext(r.Context(), r.Method, svcURL(r, service)+base+suffix, r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.URL.RawQuery = r.URL.RawQuery // 透传 query
		copyHeaders(req.Header, r.Header)
		resp, err := httpClient.Do(req)
		if err != nil {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		copyHeaders(w.Header(), resp.Header)
		// 泳道演示可见性：响应带请求实际泳道 + bff 实例指纹（下游经 X-Paas-Service 透传）。
		w.Header().Set("X-Paas-Lane", observ.LaneOrBase(r.Context()))
		w.Header().Set("X-Paas-Bff", observ.ServiceFingerprint())
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// imageProxy 把 /images/<bucket>/<key> 反代到 minio 公开读（浏览器可达集群内对象）。
// MINIO_ENDPOINT 未配（未绑 storage）返 503。
func imageProxy(w http.ResponseWriter, r *http.Request) {
	ep := os.Getenv("MINIO_ENDPOINT")
	if ep == "" {
		http.Error(w, "storage 未绑定", http.StatusServiceUnavailable)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/images/")
	if key == "" || strings.Contains(key, "..") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, ep+"/"+key, nil)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// chatProxy SSE 流式透传：按 data: 行透传 chatbot 的 SSE（含 reasoning_content）。
func chatProxy(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, svcURL(r, "chatbot")+"/chat", r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := streamClient.Do(req)
	if err != nil {
		slog.Error("chatbot 转发失败", "err", err)
		http.Error(w, "chatbot unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// SSE 行原样透传（data: / event: / 注释 / 空行）。
		_, _ = w.Write([]byte(line + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
