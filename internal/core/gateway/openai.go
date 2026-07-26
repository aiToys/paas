package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aitoys/paas/pkg/provider"
)

type chatReq struct {
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

type deltaMessage struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chatChoice struct {
	Delta deltaMessage `json:"delta"`
}

// ChatCompletions 实现 OpenAI 兼容 /v1/chat/completions（流式 SSE）。
// 非 stream 模式本切片也以 SSE 形式返回（前端按 SSE 解析），保持实现单一。
func ChatCompletions(gw *Gateway, meter *Meter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		p, ok := gw.Get(req.Model)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("model %q not found", req.Model))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)

		ch, err := p.Chat(r.Context(), provider.ChatRequest{
			Model: req.Model, Messages: req.Messages, Stream: true,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		tokens := 0
		for chunk := range ch {
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
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		meter.Record("default", req.Model, tokens)
	}
}

// ListModels 实现 /v1/models。
func ListModels(gw *Gateway) http.HandlerFunc {
	type modelObj struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		models := gw.Models()
		data := make([]modelObj, 0, len(models))
		for _, id := range models {
			data = append(data, modelObj{ID: id, Object: "model"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data})
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
