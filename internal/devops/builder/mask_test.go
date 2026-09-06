package builder

import (
	"strings"
	"testing"
)

// TestCapLog 验证日志截断：不超限原样返回；超限保留头尾各半 + 截断标记（集成审计 I6）。
func TestCapLog(t *testing.T) {
	// 不超限：原样返回
	small := "docker build 完成"
	if got := CapLog(small); got != small {
		t.Fatalf("小日志应原样返回, got %q", got)
	}

	// 恰好等于上限：原样返回（边界，不截断）
	exact := strings.Repeat("a", MaxLogBytes)
	if got := CapLog(exact); len(got) != MaxLogBytes {
		t.Fatalf("等于上限应原样返回, got len=%d", len(got))
	}

	// 超限：头尾各半 + 标记，总长略超上限（标记本身占字节，可接受）
	head := strings.Repeat("H", 100)
	tail := strings.Repeat("T", 100)
	big := head + strings.Repeat("x", MaxLogBytes) + tail
	got := CapLog(big)
	if !strings.Contains(got, "H") || !strings.Contains(got, "T") {
		t.Fatal("截断后应保留头尾段")
	}
	if !strings.Contains(got, "已截断") {
		t.Fatal("截断后应含截断标记")
	}
	if len(got) > MaxLogBytes+256 { // 标记 + 头尾余量
		t.Fatalf("截断后长度异常: %d", len(got))
	}
	// 头部前缀必须保留（失败根因上下文在头部）
	if !strings.HasPrefix(got, head) {
		t.Fatal("头部前缀应完整保留")
	}
	// 尾部后缀必须保留（结论行在尾部）
	if !strings.HasSuffix(got, tail) {
		t.Fatal("尾部后缀应完整保留")
	}
}
