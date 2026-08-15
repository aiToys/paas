// paas-shop AI 客服服务：演示「调平台 Agent（MaaS 虚拟模型）+ SSE 流式透传 + 多轮对话记忆」。
//
// 平台能力组合：
//   - MaaS：调平台 /v1/chat/completions，model 用平台 Agent 虚拟模型（agent:<id>，PAAS_AGENT_MODEL），
//     工具调用/知识库检索由平台 Agent 侧编排，chatbot 只透传 SSE（content + reasoning_content）。
//   - 记忆：按 userId 保存多轮对话历史（memory 进程内 / redis 共享，PAAS_MEMORY_MODE）。
//
// 调用链路：bff -> chatbot -> MaaS(平台 Agent 虚拟模型)。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

var (
	gatewayURL   string // 平台 /v1/chat/completions 入口（paas-core）
	apiKey       string // 平台 API Key
	agentModel   string // 平台 Agent 虚拟模型（agent:<id>，PAAS_AGENT_MODEL 必填）
	memoryMode   string // memory（默认）/ redis
	store        historyStore
	httpClient   = observ.NewClient()
	streamClient = observ.NewStreamingClient() // 调平台 gateway 用（agent 长 reasoning 超 10s）
)

// chatMsg 是对话历史的一条消息（OpenAI role/content 形态）。
type chatMsg struct {
	Role    string
	Content string
}

// toMsgs 把 chatMsg 序列化为 OpenAI messages 请求体形态。
func toMsgs(msgs []chatMsg) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{"role": m.Role, "content": m.Content})
	}
	return out
}

// trimHistory 裁剪对话历史到 max 条：保 msgs[0]（system 在首）+ 连续 max-1 条窗口；
// 窗口起点从末尾回退对齐到 user（assistant 开头语义不完整），找不到 user 则从 msgs[1] 起。
func trimHistory(msgs []chatMsg, max int) []chatMsg {
	if len(msgs) <= max {
		return msgs
	}
	start := len(msgs) - (max - 1)
	for start > 1 && msgs[start].Role != "user" {
		start--
	}
	return append([]chatMsg{msgs[0]}, msgs[start:start+max-1]...)
}

// --- 记忆（小接口 + 两实现）---

// historyStore 是按 userId 的多轮对话记忆。
type historyStore interface {
	Load(ctx context.Context, userID string) []chatMsg
	Append(ctx context.Context, userID string, user, assistant string)
}

// memHistory 是进程内记忆（默认；重启丢失，演示够用）。
type memHistory struct {
	mu   sync.Mutex
	data map[string][]chatMsg
}

func newMemHistory() *memHistory { return &memHistory{data: map[string][]chatMsg{}} }

func (m *memHistory) Load(_ context.Context, userID string) []chatMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]chatMsg{}, m.data[userID]...)
}

func (m *memHistory) Append(_ context.Context, userID, user, assistant string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[userID] = trimHistory(append(m.data[userID], chatMsg{"user", user}, chatMsg{"assistant", assistant}), 20)
}

// redisHistory 是 Redis 共享记忆（key chat:history:<userId>，TTL 24h）。
type redisHistory struct {
	rdb *redis.Client
}

func (r *redisHistory) Load(ctx context.Context, userID string) []chatMsg {
	b, err := r.rdb.Get(ctx, "chat:history:"+userID).Bytes()
	if err != nil {
		slog.Warn("redis 记忆读取失败，降级为空历史", "err", err)
		return nil
	}
	var msgs []chatMsg
	if err := json.Unmarshal(b, &msgs); err != nil {
		slog.Warn("redis 记忆解析失败，降级为空历史", "err", err)
		return nil
	}
	return msgs
}

func (r *redisHistory) Append(ctx context.Context, userID, user, assistant string) {
	msgs := append(r.Load(ctx, userID), chatMsg{"user", user}, chatMsg{"assistant", assistant})
	msgs = trimHistory(msgs, 20)
	b, err := json.Marshal(msgs)
	if err != nil {
		slog.Warn("redis 记忆序列化失败", "err", err)
		return
	}
	if err := r.rdb.SetEx(ctx, "chat:history:"+userID, b, 24*time.Hour).Err(); err != nil {
		slog.Warn("redis 记忆写入失败", "err", err)
	}
}

func main() {
	shutdown := observ.Init("paas-shop-chatbot")
	defer shutdown()

	gatewayURL = os.Getenv("PAAS_GATEWAY_URL")
	if gatewayURL == "" {
		// 模型绑定注入的 PAAS_LLM_BASE_URL（可能含 /v1 后缀，规整去尾，chatbot 统一调 gatewayURL+"/v1/..."）
		gatewayURL = strings.TrimSuffix(os.Getenv("PAAS_LLM_BASE_URL"), "/v1")
		gatewayURL = strings.TrimSuffix(gatewayURL, "/")
	}
	if gatewayURL == "" {
		// paas-core Service port=80（非 Pod 直连 :8080）；集群内访问用默认 :80。
		gatewayURL = "http://paas-core.paas.svc.cluster.local"
	}
	// 优先应用级 Key（模型绑定注入，gateway meter 据此把 token 用量归因到应用）；
	// PAAS_API_KEY（租户级）仅兜底，归因不到具体应用。
	apiKey = os.Getenv("PAAS_LLM_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("PAAS_API_KEY")
	}
	agentModel = os.Getenv("PAAS_AGENT_MODEL")
	if agentModel == "" {
		slog.Error("PAAS_AGENT_MODEL 未设置（chatbot 需平台 Agent 虚拟模型 agent:<id>）")
		os.Exit(1)
	}
	if apiKey == "" {
		slog.Error("PAAS_API_KEY 未设置（chatbot 需平台 API Key 调 MaaS）")
		os.Exit(1)
	}

	memoryMode = "memory"
	redisURL := os.Getenv("REDIS_URL")
	if os.Getenv("PAAS_MEMORY_MODE") == "redis" {
		if redisURL == "" {
			slog.Warn("PAAS_MEMORY_MODE=redis 但 REDIS_URL 为空，降级 memory 模式")
		} else {
			rdb := redis.NewClient(&redis.Options{Addr: redisURL})
			if err := rdb.Ping(context.Background()).Err(); err != nil {
				slog.Error("redis 连接失败", "addr", redisURL, "err", err)
				os.Exit(1)
			}
			store = &redisHistory{rdb: rdb}
			memoryMode = "redis"
		}
	}
	if store == nil {
		store = newMemHistory()
	}
	slog.Info("chatbot 服务就绪", "gateway", gatewayURL, "agentModel", agentModel, "memory", memoryMode)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/chat", chatHandler) // POST {message,userId} -> SSE 流式

	h := observ.Recover(observ.Handler("chatbot", mux))
	srv := &http.Server{Addr: ":8083", Handler: h, ReadHeaderTimeout: 30 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// chatHandler 处理客服对话：加载多轮历史 -> 调平台 Agent（虚拟模型）-> SSE 透传 -> 落记忆。
func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Message string `json:"message"`
		UserID  string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "message 必填", http.StatusBadRequest)
		return
	}
	if req.UserID == "" {
		req.UserID = "anon"
	}

	// 历史 + 本轮 user 消息（system 人设在历史首条，Append 时保证）
	hist := store.Load(r.Context(), req.UserID)
	if len(hist) == 0 || hist[0].Role != "system" {
		hist = append([]chatMsg{{"system", "你是 PaasShop 智能客服，友好专业。只回答商品、订单、售后相关问题。回答简洁。"}}, hist...)
	}
	msgs := append(hist, chatMsg{"user", req.Message})

	streamAgent(w, r.Context(), req.UserID, req.Message, msgs)
}

// streamAgent 流式调平台 Agent 虚拟模型，透传 SSE chunks 到前端，并累积 assistant 文本落记忆。
func streamAgent(w http.ResponseWriter, ctx context.Context, userID, userMsg string, msgs []chatMsg) {
	body := map[string]any{"model": agentModel, "messages": toMsgs(msgs), "stream": true}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := streamClient.Do(req)
	if err != nil {
		http.Error(w, "MaaS 不可用", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		slog.Error("Agent 调用失败", "status", resp.StatusCode, "body", string(b))
		http.Error(w, "MaaS 不可用", http.StatusServiceUnavailable)
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	var assistant strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			store.Append(ctx, userID, userMsg, assistant.String())
			return
		}
		// 透传 chunk（含 content + reasoning_content 帧——reasoning 原样透传不解析）
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
		// 累积 assistant content（供记忆；reasoning_content 不入记忆）
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 跳过非 JSON 帧（心跳/异常），不中断透传
		}
		if len(chunk.Choices) > 0 {
			assistant.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("SSE 流中断", "err", err)
	}
	// 流异常结束（无 [DONE]）：已累积部分仍落记忆（宁少丢勿全丢）
	store.Append(ctx, userID, userMsg, assistant.String())
}
