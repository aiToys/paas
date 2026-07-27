// Package provider 定义推理 Provider 的平台级公共契约。
// 放在 pkg 下供插件（internal/maas）与 Gateway（internal/core/gateway）共享，
// 避免 internal/maas 与 pkg/plugin 之间的 import 循环。
package provider

import "context"

// Message 表示一条对话消息。
type Message struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

// ChatRequest 是一次推理请求。
type ChatRequest struct {
	Model    string
	Messages []Message
	Stream   bool
}

// Chunk 是流式推理的一个增量块。
type Chunk struct {
	Role    string // 首块填 role，后续为空
	Content string
}

// Provider 是推理提供者抽象（echo / 未来 vLLM 等均实现它）。
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (<-chan Chunk, error)
}

// GatewayRegistrar 由 API Gateway 实现，供插件在 Init 阶段注册模型目录。
type GatewayRegistrar interface {
	// RegisterModel 注册一个逻辑模型（含其全部通道）。
	// 同 ID 视为覆盖更新。
	RegisterModel(m *Model) error
}
