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
	Role    string `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content string `json:"content"`
	// ToolCalls 仅 assistant 角色：LLM 决定调用工具时返回的工具调用请求。
	// 多轮工具循环（FunctionCalling）回放历史 assistant 消息时需带上，供下游识别。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID 仅 role="tool" 消息：关联对应的 tool_call.id（OpenAI 规范要求）。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall 是 LLM 发起的工具调用（OpenAI 兼容 choices[].message.tool_calls）。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type,omitempty"` // 固定 "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 工具调用的函数名 + 参数（Arguments 为 JSON 字符串）。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 字符串（OpenAI 规范，非 object）
}

// ToolDef 是暴露给 LLM 的工具定义（OpenAI 兼容 tools[]，type=function）。
// 由 Agent runtime 从工具实体（MCP server 的 ListTools schema）构建。
type ToolDef struct {
	Type     string          `json:"type"` // 固定 "function"
	Function ToolDefFunction `json:"function"`
}

// ToolDefFunction 工具的函数签名（Name + 给 LLM 的 Description + JSON Schema 参数）。
type ToolDefFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema（permissive: {type:object}）
}

// ChatRequest 是一次推理请求。
type ChatRequest struct {
	Model       string
	Messages    []Message
	Stream      bool
	Temperature *float64 `json:"temperature,omitempty"` // 采样温度，nil 表示用上游默认
	MaxTokens   *int     `json:"max_tokens,omitempty"`  // 最大生成 token 数，nil 表示不限
	// Tools 暴露给 LLM 的工具定义（FunctionCalling）；空表示不启用工具调用。
	Tools []ToolDef
	// ToolChoice 工具选择策略："" | "auto"（默认，LLM 自决）| "none"（禁用）。
	ToolChoice string
}

// Chunk 是流式推理的一个增量块。
type Chunk struct {
	Role      string // 首块填 role，后续为空
	Content   string
	Reasoning string // 推理模型的思考过程增量（OpenAI 兼容 delta.reasoning_content），无则空
	// ToolCalls 流末累积的工具调用（仅当 LLM 决定调用工具时；中间 delta 按 index 累积）。
	ToolCalls []ToolCall
	// FinishReason 结束原因（"stop" | "tool_calls" | "length"）；流末块填充。
	// runtime 据 "tool_calls" 判定进入下一轮工具循环。
	FinishReason string
}

// Provider 是推理提供者抽象（echo / mock / OpenAICompatibleProvider 等均实现它）。
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (<-chan Chunk, error)
}

// Embedder 是向量化提供者抽象（OpenAICompatibleProvider 等实现它，用于知识库 embedding）。
// 与 Provider 正交：仅 embedding 通道实现，不在 Provider 接口强制。
// catalog 加载时探测 `if e, ok := p.(Embedder); ok { ... }` 决定该模型是否可用于 KB embedding。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
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
	// UnregisterModel 注销一个逻辑模型（含通道）。未知 ID 忽略（幂等）。
	UnregisterModel(modelID string)
}
