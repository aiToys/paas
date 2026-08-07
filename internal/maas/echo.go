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

// Embed 返 hash 派生向量（固定维度），演示用。真实向量化由 OpenAICompatibleProvider.Embed。
func (EchoProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 1024)
		// 简单 FNV-1a 派生：文本内容决定向量值，便于测试区分不同输入
		var h uint32 = 2166136261
		for _, c := range t {
			h ^= uint32(c)
			h *= 16777619
		}
		for j := range v {
			v[j] = float32((h >> (j % 32)) & 1)
		}
		out[i] = v
	}
	return out, nil
}
