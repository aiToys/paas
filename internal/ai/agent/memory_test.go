package agent

import (
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

func TestConversationStoreAppendLoad(t *testing.T) {
	c := &conversationStore{hist: map[convKey][]provider.Message{}}
	if h := c.loadHistory("a1", "c1"); len(h) != 0 {
		t.Fatalf("空会话应无历史: %d", len(h))
	}
	c.appendHistory("a1", "c1", provider.Message{Role: "user", Content: "hi"}, provider.Message{Role: "assistant", Content: "hello"})
	h := c.loadHistory("a1", "c1")
	if len(h) != 2 || h[0].Content != "hi" || h[1].Content != "hello" {
		t.Fatalf("历史不符: %+v", h)
	}
	// 返回副本：改副本不污染 store
	h[0].Content = "mutated"
	if c.loadHistory("a1", "c1")[0].Content != "hi" {
		t.Fatal("loadHistory 返回非副本")
	}
	// agent 隔离
	if h := c.loadHistory("a2", "c1"); len(h) != 0 {
		t.Fatal("跨 agent 泄漏历史")
	}
	// 截断
	for i := 0; i < 30; i++ {
		c.appendHistory("a3", "c1", provider.Message{Role: "user", Content: "u"}, provider.Message{Role: "assistant", Content: "a"})
	}
	if h := c.loadHistory("a3", "c1"); len(h) != maxHistoryPerConv {
		t.Fatalf("未截断: %d", len(h))
	}
}
