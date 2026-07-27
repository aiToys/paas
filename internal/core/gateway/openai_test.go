package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/pkg/provider"
)

// fakeStreamProvider 推送固定 chunk 序列，便于断言。
type fakeStreamProvider struct{ chunks []provider.Chunk }

func (f fakeStreamProvider) Name() string { return "echo" }
func (f fakeStreamProvider) Chat(_ context.Context, _ provider.ChatRequest) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		for _, c := range f.chunks {
			ch <- c
		}
	}()
	return ch, nil
}

// 注册一个挂单通道的模型，通道绑定 fakeStreamProvider。
func registerStreamModel(gw *Gateway, modelID string, chunks []provider.Chunk) {
	c := &provider.Channel{ID: modelID + "#a", Priority: 0, Status: provider.StatusHealthy}
	c.SetImpl(fakeStreamProvider{chunks: chunks})
	_ = gw.RegisterModel(&provider.Model{ID: modelID, Vendor: "test-vendor", Channels: []*provider.Channel{c}})
}

func TestChatCompletionsStreamsContentAndDone(t *testing.T) {
	gw := New()
	registerStreamModel(gw, "echo", []provider.Chunk{
		{Role: "assistant"},
		{Content: "你"},
		{Content: "好"},
	})
	meter := &Meter{}

	h := ChatCompletions(gw, meter)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"echo","messages":[{"role":"user","content":"你好"}],"stream":true}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

	out := rec.Body.String()
	assert.Contains(t, out, `"content":"你"`)
	assert.Contains(t, out, `"content":"好"`)
	assert.Contains(t, out, `"role":"assistant"`)
	assert.Contains(t, out, "data: [DONE]")
	assert.Equal(t, 2, meter.Count(), "应按 rune 数计量 token")
}

func TestChatCompletionsUnknownModel(t *testing.T) {
	gw := New()
	h := ChatCompletions(gw, &Meter{})
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"ghost","messages":[]}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "not found")
}

func TestListModelsOpenAICompat(t *testing.T) {
	gw := New()
	registerStreamModel(gw, "echo", nil)
	rec := httptest.NewRecorder()
	ListModels(gw).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	body := rec.Body.String()
	assert.Contains(t, body, `"id":"echo"`)
	assert.Contains(t, body, `"object":"model"`)
	assert.Contains(t, body, `"owned_by":"test-vendor"`, "应含 vendor 作 owned_by")
}

func TestCatalogModelsRichInfo(t *testing.T) {
	gw := New()
	registerStreamModel(gw, "echo", nil)
	rec := httptest.NewRecorder()
	CatalogModels(gw).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))

	body := rec.Body.String()
	assert.Contains(t, body, `"id":"echo"`)
	assert.Contains(t, body, `"vendor":"test-vendor"`)
	assert.Contains(t, body, `"channels"`, "富信息应含通道列表")
}
