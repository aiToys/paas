// Package maas 是 MaaS 插件实现。
package maas

import (
	"context"

	"github.com/aitoys/paas/pkg/provider"
)

// EchoProvider 是最小推理 Provider：把最后一条 user 消息按 rune 逐 token 回显。
// 用于垂直切片验证数据流；真实推理由后续 vLLM Provider 接入。
type EchoProvider struct{}

// Name 返回 provider 标识。
func (EchoProvider) Name() string { return "echo" }

// Chat 流式回显最后一条 user 消息。
func (EchoProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		last := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				last = req.Messages[i].Content
				break
			}
		}
		// 首块声明 assistant 角色
		select {
		case <-ctx.Done():
			return
		case ch <- provider.Chunk{Role: "assistant"}:
		}
		// 按 rune 切片逐 token 推送
		runes := []rune(last)
		for i := 0; i < len(runes); i++ {
			select {
			case <-ctx.Done():
				return
			case ch <- provider.Chunk{Content: string(runes[i])}:
			}
		}
	}()
	return ch, nil
}
