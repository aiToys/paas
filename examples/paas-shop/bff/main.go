// paas-shop BFF 聚合层：前端统一入口，聚合 product/recommend/chatbot。
//
// 前端（nginx）反代 /api/* 到本服务。本服务转发到各微服务，统一错误处理 + trace 传播。
// SSE 透传：/api/chat 流式响应透传 chatbot 的 SSE（按 data: 行透传，保持 chunk 实时到达）。
package main

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

var (
	httpClient       = observ.NewClient()
	streamClient     = observ.NewStreamingClient() // SSE 透传用（chatbot 客服链路含长 reasoning，无整体超时）
	productURL   = "http://paas-shop-product:8081"
	recommendURL = "http://paas-shop-recommend:8082"
	chatbotURL   = "http://paas-shop-chatbot:8083"
)

func main() {
	shutdown := observ.Init("paas-shop-bff")
	defer shutdown()
	if v := os.Getenv("PRODUCT_SERVICE_URL"); v != "" {
		productURL = v
	}
	if v := os.Getenv("RECOMMEND_SERVICE_URL"); v != "" {
		recommendURL = v
	}
	if v := os.Getenv("CHATBOT_SERVICE_URL"); v != "" {
		chatbotURL = v
	}
	slog.Info("bff 就绪", "product", productURL, "recommend", recommendURL, "chatbot", chatbotURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/products", proxy(productURL+"/products"))       // GET 列表
	mux.HandleFunc("/api/products/", proxyPrefix(productURL+"/products")) // GET /{id}
	mux.HandleFunc("/api/recommend", proxy(recommendURL+"/recommend"))    // GET 推荐
	mux.HandleFunc("/api/chat", chatProxy)                                // POST SSE 透传

	h := observ.Recover(observ.Handler("bff", mux))
	srv := &http.Server{Addr: ":8080", Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

// proxy 透传到目标 URL（透传 body + headers + traceparent）。
func proxy(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		copyHeaders(req.Header, r.Header)
		resp, err := httpClient.Do(req)
		if err != nil {
			slog.Error("转发失败", "target", target, "err", err)
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// proxyPrefix 透传路径后缀（/api/products/{id} -> product/products/{id}）。
func proxyPrefix(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		suffix := strings.TrimPrefix(r.URL.Path, "/api/products")
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target+suffix, r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		copyHeaders(req.Header, r.Header)
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
}

// chatProxy SSE 流式透传：按 data: 行透传 chatbot 的 SSE（含 reasoning_content）。
func chatProxy(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, chatbotURL+"/chat", r.Body)
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
