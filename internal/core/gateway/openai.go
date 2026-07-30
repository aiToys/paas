package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aitoys/paas/pkg/provider"
	"github.com/aitoys/paas/pkg/tenant"
)

type chatReq struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
	MaxTokens   *int               `json:"max_tokens,omitempty"`
}

type deltaMessage struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chatChoice struct {
	Delta deltaMessage `json:"delta"`
}

// ChatCompletions 实现 OpenAI 兼容 /v1/chat/completions（流式 SSE + 请求级 failover）。
//
// 按通道优先级依次尝试：某通道 Chat 返回 degraded 类错误（限流/不可用）→ 标 degraded，
// 自动切下一通道；offline 类错误（凭证缺失/配置错误）→ 标 offline，亦切下一通道。
// 全部通道失败才返回 503。非 stream 请求本切片也以 SSE 形式返回（前端按 SSE 解析）。
func ChatCompletions(gw *Gateway, meter *Meter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 限制请求体 1MiB，防超大 JSON 撑爆内存（DoS 硬化）。
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Model == "" || len(req.Messages) == 0 {
			writeErr(w, http.StatusBadRequest, "model 与 messages 必填")
			return
		}
		channels, err := gw.ResolveChannels(req.Model)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}

		// 请求级 failover：依次尝试候选通道，首个成功者 serve。
		var lastErr error
		for _, ch := range channels {
			impl := ch.Impl()
			if impl == nil {
				gw.MarkChannelStatus(req.Model, ch.ID, provider.StatusDegraded)
				lastErr = fmt.Errorf("通道 %s 未就绪", ch.ID)
				continue
			}
			stream, chatErr := impl.Chat(r.Context(), provider.ChatRequest{
				Model: req.Model, Messages: req.Messages, Stream: true,
				Temperature: req.Temperature, MaxTokens: req.MaxTokens,
			})
			if chatErr != nil {
				// 按错误类型降级：offline 类（配置/凭证）不重试本通道，degraded 类可 failover。
				if isOfflineErr(chatErr) {
					gw.MarkChannelStatus(req.Model, ch.ID, provider.StatusOffline)
				} else {
					gw.MarkChannelStatus(req.Model, ch.ID, provider.StatusDegraded)
				}
				lastErr = chatErr
				continue // failover 到下一通道（尚未写 SSE headers，可安全切换）
			}
			serveStream(w, r, stream, meter, req.Model)
			return
		}
		// 全部通道失败
		writeErr(w, http.StatusServiceUnavailable, fmt.Sprintf("全部通道不可用: %v", lastErr))
	}
}

// isOfflineErr 判定是否 offline 类错误（配置/凭证问题，重试本通道无意义）。
func isOfflineErr(err error) bool {
	return errors.Is(err, provider.ErrCredentialMissing) ||
		errors.Is(err, provider.ErrCredentialInvalid) ||
		errors.Is(err, provider.ErrUpstreamConfig)
}

// serveStream 把 provider.Chunk 流转为 OpenAI 兼容 SSE 写入响应，并计量 token。
// 客户端断开（ctx.Done）即停止消费，避免为已断开请求继续计费。
func serveStream(w http.ResponseWriter, r *http.Request, stream <-chan provider.Chunk, meter *Meter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	tokens := 0
	ctx := r.Context()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case chunk, ok := <-stream:
			if !ok {
				break loop
			}
			if chunk.Role != "" {
				writeSSE(w, chatChoice{Delta: deltaMessage{Role: chunk.Role}})
			}
			if chunk.Content != "" {
				tokens += len([]rune(chunk.Content))
				writeSSE(w, chatChoice{Delta: deltaMessage{Content: chunk.Content}})
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	if meter != nil {
		tid, _ := tenant.TenantFrom(r.Context())
		meter.Record(tid, model, tokens)
	}
}

// ListModels 实现 /v1/models（OpenAI 兼容：id/object/owned_by）。
func ListModels(gw *Gateway) http.HandlerFunc {
	type modelObj struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		models := gw.Models()
		data := make([]modelObj, 0, len(models))
		for _, m := range models {
			data = append(data, modelObj{ID: m.ID, Object: "model", OwnedBy: m.Vendor})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data})
	}
}

// CatalogModels 实现 /api/models（完整富信息，含通道列表，供模型市场前端）。
func CatalogModels(gw *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": gw.Models()})
	}
}

func writeSSE(w http.ResponseWriter, v interface{}) {
	b, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{v}})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
