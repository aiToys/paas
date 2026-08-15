package main

import "testing"

func mk(role string) chatMsg { return chatMsg{Role: role, Content: "x"} }

func TestTrimHistoryKeepsSystemFirst(t *testing.T) {
	msgs := []chatMsg{mk("system")}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, mk("user"), mk("assistant"))
	}
	out := trimHistory(msgs, 20)
	if len(out) != 20 {
		t.Fatalf("裁到上限 20，got %d", len(out))
	}
	if out[0].Role != "system" {
		t.Fatalf("system 必须保留在首位，got %s", out[0].Role)
	}
	// 裁剪后首轮必须是 user（assistant 开头语义不完整）
	if out[1].Role != "user" {
		t.Fatalf("裁剪后应从 user 起，got %s", out[1].Role)
	}
}

func TestTrimHistoryShortNoop(t *testing.T) {
	msgs := []chatMsg{mk("system"), mk("user")}
	if got := trimHistory(msgs, 20); len(got) != 2 {
		t.Fatalf("未超限不应裁剪，got %d", len(got))
	}
}
