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
// 通过 Gateway.Resolve 按通道路由；调用失败时把该通道标记为 degraded（被动降级）。
// 非 stream 模式本切片也以 SSE 形式返回（前端按 SSE 解析），保持实现单一。
func ChatCompletions(gw *Gateway, meter *Meter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		ch, err := gw.Resolve(req.Model)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)

		stream, err := ch.Impl().Chat(r.Context(), provider.ChatRequest{
			Model: req.Model, Messages: req.Messages, Stream: true,
		})
		if err != nil {
			// Provider 拒绝调用，标记通道降级，便于后续路由切换
			gw.MarkChannelStatus(req.Model, ch.ID, provider.StatusDegraded)
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}

		tokens := 0
		for chunk := range stream {
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
