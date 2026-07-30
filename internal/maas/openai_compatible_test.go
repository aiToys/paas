package maas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

// stubResolver 返回固定明文（或错误），驱动凭证解析测试。
type stubResolver struct {
	v   string
	err error
}

func (s stubResolver) Resolve(string) (string, error) { return s.v, s.err }

// newSSEServer 起一个模拟 OpenAI 兼容流式上游，按 status 吐 SSE 或错误。
func newSSEServer(t *testing.T, status int, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 校验上游收到正确鉴权头与请求体（仅正常流场景检查）
		if status == http.StatusOK {
			if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
				t.Errorf("上游应收 Bearer sk-test，got %q", got)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("上游应收 application/json，got %q", ct)
			}
		}
		if status != http.StatusOK {
			http.Error(w, `{"error":{"message":"upstream"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, ln := range lines {
			_, _ = w.Write([]byte(ln))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
}

func collect(ch <-chan provider.Chunk) string {
	var sb strings.Builder
	for c := range ch {
		sb.WriteString(c.Content)
	}
	return sb.String()
}

// TestOpenAICompatible_StreamOK 验证正常流式解析 + 多块拼接 + [DONE] 关闭。
func TestOpenAICompatible_StreamOK(t *testing.T) {
	srv := newSSEServer(t, http.StatusOK, []string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"你好"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"，世界"}}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	})
	defer srv.Close()

	p := NewOpenAICompatibleProvider("test", srv.URL, "m", "ref", stubResolver{v: "sk-test"}, srv.Client())
	ch, err := p.Chat(context.Background(), provider.ChatRequest{
		Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}, Stream: true,
	})
	if err != nil {
		t.Fatalf("Chat 应成功，got %v", err)
	}
	if got := collect(ch); got != "你好，世界" {
		t.Fatalf("流式拼接应完整，got %q", got)
	}
}

// TestOpenAICompatible_ErrorClassification 验证四类 HTTP 错误映射到正确 sentinel。
func TestOpenAICompatible_ErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"401 鉴权失败→offline", http.StatusUnauthorized, provider.ErrCredentialInvalid},
		{"403 禁止→offline", http.StatusForbidden, provider.ErrCredentialInvalid},
		{"429 限流→degraded", http.StatusTooManyRequests, provider.ErrUpstreamRateLimit},
		{"500 服务端→degraded", http.StatusInternalServerError, provider.ErrUpstreamUnavailable},
		{"400 配置错误→offline", http.StatusBadRequest, provider.ErrUpstreamConfig},
		{"404 模型不存在→offline", http.StatusNotFound, provider.ErrUpstreamConfig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newSSEServer(t, c.status, nil)
			defer srv.Close()
			p := NewOpenAICompatibleProvider("t", srv.URL, "m", "ref", stubResolver{v: "sk-test"}, srv.Client())
			_, err := p.Chat(context.Background(), provider.ChatRequest{
				Model: "m", Messages: []provider.Message{{Role: "user", Content: "x"}},
			})
			if !errors.Is(err, c.want) {
				t.Fatalf("status %d 应映射为 %v，got %v", c.status, c.want, err)
			}
		})
	}
}

// TestOpenAICompatible_CredentialMissing 验证凭证缺失/解析失败 → offline（不重试）。
func TestOpenAICompatible_CredentialMissing(t *testing.T) {
	t.Run("resolver 为 nil", func(t *testing.T) {
		p := NewOpenAICompatibleProvider("t", "https://x", "m", "ref", nil, nil)
		_, err := p.Chat(context.Background(), provider.ChatRequest{})
		if !errors.Is(err, provider.ErrCredentialMissing) {
			t.Fatalf("resolver nil 应 ErrCredentialMissing，got %v", err)
		}
	})
	t.Run("解析出错", func(t *testing.T) {
		p := NewOpenAICompatibleProvider("t", "https://x", "m", "ref", stubResolver{err: fmt.Errorf("not found")}, nil)
		_, err := p.Chat(context.Background(), provider.ChatRequest{})
		if !errors.Is(err, provider.ErrCredentialMissing) {
			t.Fatalf("解析失败应 ErrCredentialMissing，got %v", err)
		}
	})
	t.Run("明文为空", func(t *testing.T) {
		p := NewOpenAICompatibleProvider("t", "https://x", "m", "ref", stubResolver{v: ""}, nil)
		_, err := p.Chat(context.Background(), provider.ChatRequest{})
		if !errors.Is(err, provider.ErrCredentialMissing) {
			t.Fatalf("空明文应 ErrCredentialMissing，got %v", err)
		}
	})
}

// TestOpenAICompatible_ContextCancel 验证客户端断连后 goroutine 不泄漏（channel 可退出）。
func TestOpenAICompatible_ContextCancel(t *testing.T) {
	// 上游吐一块后不结束（无 [DONE]），靠 ctx 取消终止。
	srv := newSSEServer(t, http.StatusOK, []string{
		`data: {"choices":[{"delta":{"content":"a"}}]}` + "\n\n",
	})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	p := NewOpenAICompatibleProvider("t", srv.URL, "m", "ref", stubResolver{v: "sk-test"}, srv.Client())
	ch, err := p.Chat(ctx, provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat 应成功: %v", err)
	}
	cancel()
	// channel 应能 range 退出（ctx 取消或 body 关闭触发）
	for range ch {
	}
}
