package maas

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/provider"
)

func TestEchoChatReturnsLastUserMessage(t *testing.T) {
	p := EchoProvider{}
	ch, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:    "echo",
		Messages: []provider.Message{{Role: "user", Content: "你好世界"}},
	})
	require.NoError(t, err)

	var got strings.Builder
	var firstRole string
	first := true
	for c := range ch {
		if first {
			firstRole = c.Role
			first = false
		}
		got.WriteString(c.Content)
	}
	assert.Equal(t, "assistant", firstRole, "首块应声明 assistant 角色")
	assert.Equal(t, "你好世界", got.String(), "应完整回显最后一条 user 消息")
}

func TestEchoChatUsesLastUserWhenMultiple(t *testing.T) {
	p := EchoProvider{}
	ch, _ := p.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.Message{
			{Role: "user", Content: "第一句"},
			{Role: "assistant", Content: "回复"},
			{Role: "user", Content: "第二句"},
		},
	})
	var got strings.Builder
	for c := range ch {
		got.WriteString(c.Content)
	}
	assert.Equal(t, "第二句", got.String())
}
