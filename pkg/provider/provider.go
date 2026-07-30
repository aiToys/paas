// Package provider 定义推理 Provider 的平台级公共契约。
// 放在 pkg 下供插件（internal/maas）与 Gateway（internal/core/gateway）共享，
// 避免 internal/maas 与 pkg/plugin 之间的 import 循环。
package provider

import (
	"context"
	"errors"
)

// Message 表示一条对话消息。
type Message struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

// ChatRequest 是一次推理请求。
type ChatRequest struct {
	Model       string
	Messages    []Message
	Stream      bool
	Temperature *float64 `json:"temperature,omitempty"` // 采样温度，nil 表示用上游默认
	MaxTokens   *int     `json:"max_tokens,omitempty"`  // 最大生成 token 数，nil 表示不限
}

// Chunk 是流式推理的一个增量块。
type Chunk struct {
	Role    string // 首块填 role，后续为空
	Content string
}

// Provider 是推理提供者抽象（echo / mock / OpenAICompatibleProvider 等均实现它）。
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (<-chan Chunk, error)
}

// CredentialResolver 解析平台级 Secret 明文（仅内存，不日志、不持久化）。
// 由 security store 在 cmd/core 注入实现（依赖倒置，破除 maas→security import）。
// 解析失败（凭证被删/未配置）返回错误，调用方据此把通道标记 offline。
type CredentialResolver interface {
	Resolve(credentialRef string) (plaintext string, err error)
}

// 以下 sentinel 由真实通道（如 OpenAICompatibleProvider）返回，驱动 Gateway 降级决策：
//   - offline 类（不重试本通道，需运维修）：ErrCredentialMissing / ErrCredentialInvalid / ErrUpstreamConfig
//   - degraded 类（请求级 failover 到备通道）：ErrUpstreamRateLimit / ErrUpstreamUnavailable
var (
	ErrCredentialMissing   = errors.New("凭证未配置")
	ErrCredentialInvalid   = errors.New("凭证无效或被拒")
	ErrUpstreamRateLimit   = errors.New("上游限流")
	ErrUpstreamUnavailable = errors.New("上游不可用")
	ErrUpstreamConfig      = errors.New("上游配置错误")
)

// GatewayRegistrar 由 API Gateway 实现，供插件在 Init 阶段注册模型目录。
type GatewayRegistrar interface {
	// RegisterModel 注册一个逻辑模型（含其全部通道）。
	// 同 ID 视为覆盖更新。
	RegisterModel(m *Model) error
}
