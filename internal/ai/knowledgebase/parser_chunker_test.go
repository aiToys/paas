package knowledgebase

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseText(t *testing.T) {
	s, err := Parse("text/plain", strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if s != "hello world" {
		t.Errorf("应返原文，got %q", s)
	}
}

func TestParseMarkdown(t *testing.T) {
	s, err := Parse("text/markdown", strings.NewReader("# 标题\n正文"))
	if err != nil {
		t.Fatalf("Parse markdown: %v", err)
	}
	if !strings.Contains(s, "标题") {
		t.Errorf("markdown 应按文本返，got %q", s)
	}
}

func TestParseHTML(t *testing.T) {
	html := `<html><head><script>evil()</script><style>x{}</style></head><body><h1>标题</h1><p>正文</p></body></html>`
	s, err := Parse("text/html", bytes.NewReader([]byte(html)))
	if err != nil {
		t.Fatalf("Parse html: %v", err)
	}
	if !strings.Contains(s, "标题") || !strings.Contains(s, "正文") {
		t.Errorf("应提取文本，got %q", s)
	}
	if strings.Contains(s, "evil") || strings.Contains(s, "x{}") {
		t.Errorf("应跳过 script/style，got %q", s)
	}
}

func TestParseUnsupported(t *testing.T) {
	_, err := Parse("application/pdf", strings.NewReader("%PDF-1.4"))
	if !errors.Is(err, ErrUnsupportedMIME) {
		t.Errorf("pdf 应返 ErrUnsupportedMIME，got %v", err)
	}
}

func TestChunkTextSizeOverlap(t *testing.T) {
	content := "0123456789"            // 10 字符
	chunks := ChunkText(content, 4, 2) // size=4 overlap=2 step=2
	// 切片：[0:4]="0123" [2:6]="2345" [4:8]="4567" [6:10]="6789" [8:10]... end=10 break
	// 实际：i=0 end=4 "0123"; i=2 end=6 "2345"; i=4 end=8 "4567"; i=6 end=10 "6789"; end=10 break
	if len(chunks) != 4 {
		t.Fatalf("应 4 片，got %d: %v", len(chunks), chunks)
	}
	if chunks[0] != "0123" || chunks[3] != "6789" {
		t.Errorf("切片错误: %v", chunks)
	}
	// 验证 overlap
	if chunks[1][:2] != chunks[0][2:] {
		t.Errorf("overlap 应为 2，chunks[0]=%q chunks[1]=%q", chunks[0], chunks[1])
	}
}

func TestChunkTextChinese(t *testing.T) {
	content := strings.Repeat("中", 10) // 10 个中文字符（rune）
	chunks := ChunkText(content, 4, 0)
	// size=4 overlap=0 step=4：[0:4][4:8][8:12->10] = 3 片
	if len(chunks) != 3 {
		t.Fatalf("应 3 片，got %d", len(chunks))
	}
	if len([]rune(chunks[0])) != 4 {
		t.Errorf("每片应 4 rune，got %d", len([]rune(chunks[0])))
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if chunks := ChunkText("", 100, 10); chunks != nil {
		t.Errorf("空原文应返 nil，got %v", chunks)
	}
}

func TestChunkTextDefaults(t *testing.T) {
	// size=0 overlap 负 -> 用默认
	content := strings.Repeat("a", 600)
	chunks := ChunkText(content, 0, -1) // size=500 overlap=100 step=400
	if len(chunks) != 2 {
		t.Errorf("size=0 默认 500，应 2 片，got %d", len(chunks))
	}
}
