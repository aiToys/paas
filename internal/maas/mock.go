package maas

import (
	"context"

	"github.com/aitoys/paas/pkg/provider"
)

// MockProvider 是演示用推理 Provider：按预设文本按 rune 流式吐出。
// 让不同模型有不同回复，比 echo 全回显更接近真实；真实推理由 vLLM Provider 接入。
type MockProvider struct {
	text string
}

// NewMockProvider 用预设回复文本构造 MockProvider。
func NewMockProvider(text string) MockProvider { return MockProvider{text: text} }

// Name 返回 provider 类型标识。
func (MockProvider) Name() string { return "mock" }

// Chat 流式吐出预设文本（首块声明 assistant 角色）。
func (m MockProvider) Chat(ctx context.Context, _ provider.ChatRequest) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			return
		case ch <- provider.Chunk{Role: "assistant"}:
		}
		for _, r := range m.text {
			select {
			case <-ctx.Done():
				return
			case ch <- provider.Chunk{Content: string(r)}:
			}
		}
	}()
	return ch, nil
}
