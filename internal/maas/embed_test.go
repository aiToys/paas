package maas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

// newEmbedServer 起一个模拟 OpenAI 兼容 /embeddings 上游，按 status 返 embedding JSON 或错误。
// embeddings 按 index 乱序返回，验证 Embed 按 index 排序还原输入顺序。
func newEmbedServer(t *testing.T, status int, embeddings [][]float32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("上游应收 /embeddings，got %q", r.URL.Path)
		}
		if status != http.StatusOK {
			http.Error(w, `{"error":{"message":"upstream"}}`, status)
			return
		}
		// 校验请求体含 input 数组
		var req openaiEmbedReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析 embed 请求失败: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("上游应收 Bearer sk-test，got %q", got)
		}
		// 构造响应，index 逆序返回验证排序
		resp := openaiEmbedResp{Data: make([]struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}, len(embeddings))}
		for i, emb := range embeddings {
			rev := len(embeddings) - 1 - i
			resp.Data[rev].Embedding = emb
			resp.Data[rev].Index = i
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestOpenAICompatible_EmbedOK 验证批量 embedding + index 乱序排序还原输入顺序。
func TestOpenAICompatible_EmbedOK(t *testing.T) {
	emb1 := []float32{0.1, 0.2, 0.3}
	emb2 := []float32{0.4, 0.5, 0.6}
	srv := newEmbedServer(t, http.StatusOK, [][]float32{emb1, emb2})
	defer srv.Close()

	p := NewOpenAICompatibleProvider("qwen", srv.URL, "text-embedding-v4", "sec-test", stubResolver{v: "sk-test"}, srv.Client())
	out, err := p.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("应返 2 个向量，got %d", len(out))
	}
	if len(out[0]) != 3 || len(out[1]) != 3 {
		t.Fatalf("向量维度应 3，got %d/%d", len(out[0]), len(out[1]))
	}
	// 验证 index 排序正确：out[0]=emb1 out[1]=emb2
	if out[0][0] != 0.1 || out[1][0] != 0.4 {
		t.Errorf("向量顺序错乱: out[0]=%v out[1]=%v", out[0], out[1])
	}
}

// TestOpenAICompatible_EmbedEmpty 空输入直接返 nil，不调上游。
func TestOpenAICompatible_EmbedEmpty(t *testing.T) {
	p := NewOpenAICompatibleProvider("qwen", "http://unused", "m", "sec", stubResolver{v: "sk-test"}, nil)
	out, err := p.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("空输入不应报错: %v", err)
	}
	if out != nil {
		t.Errorf("空输入应返 nil，got %v", out)
	}
}

// TestOpenAICompatible_EmbedCredentialMissing 凭证缺失返 ErrCredentialMissing。
func TestOpenAICompatible_EmbedCredentialMissing(t *testing.T) {
	p := NewOpenAICompatibleProvider("qwen", "http://unused", "m", "sec", stubResolver{v: ""}, nil)
	_, err := p.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, provider.ErrCredentialMissing) {
		t.Errorf("应返 ErrCredentialMissing，got %v", err)
	}
}

// TestOpenAICompatible_EmbedUpstreamError 上游 500 返 ErrUpstreamUnavailable。
func TestOpenAICompatible_EmbedUpstreamError(t *testing.T) {
	srv := newEmbedServer(t, http.StatusInternalServerError, nil)
	defer srv.Close()
	p := NewOpenAICompatibleProvider("qwen", srv.URL, "m", "sec", stubResolver{v: "sk-test"}, srv.Client())
	_, err := p.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, provider.ErrUpstreamUnavailable) {
		t.Errorf("应返 ErrUpstreamUnavailable，got %v", err)
	}
}

// TestOpenAICompatible_EmbedderTypeAssertion 验证 *OpenAICompatibleProvider 实现 provider.Embedder。
func TestOpenAICompatible_EmbedderTypeAssertion(t *testing.T) {
	var p provider.Provider = NewOpenAICompatibleProvider("qwen", "http://x", "m", "sec", stubResolver{v: "k"}, nil)
	if _, ok := p.(provider.Embedder); !ok {
		t.Error("OpenAICompatibleProvider 应实现 provider.Embedder")
	}
}

// TestMockEmbed 验证 MockProvider.Embed 返固定维度零向量。
func TestMockEmbed(t *testing.T) {
	m := NewMockProvider("test")
	var p provider.Provider = m
	e, ok := p.(provider.Embedder)
	if !ok {
		t.Fatal("MockProvider 应实现 provider.Embedder")
	}
	out, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Mock Embed 失败: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("应返 2 个向量，got %d", len(out))
	}
	if len(out[0]) != mockEmbedDim {
		t.Errorf("向量维度应 %d，got %d", mockEmbedDim, len(out[0]))
	}
}

// TestEchoEmbed 验证 EchoProvider.Embed 返非零向量（hash 派生）。
func TestEchoEmbed(t *testing.T) {
	var p provider.Provider = EchoProvider{}
	e, ok := p.(provider.Embedder)
	if !ok {
		t.Fatal("EchoProvider 应实现 provider.Embedder")
	}
	out, _ := e.Embed(context.Background(), []string{"hello", "world"})
	if len(out) != 2 || len(out[0]) != 1024 {
		t.Fatalf("应返 2 个 1024 维向量，got %v", out)
	}
	// 不同输入应产不同向量
	same := true
	for i := range out[0] {
		if out[0][i] != out[1][i] {
			same = false
			break
		}
	}
	if same {
		t.Error("不同输入应产不同向量")
	}
}
