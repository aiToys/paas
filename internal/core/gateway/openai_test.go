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
	body := strings.NewReader(`{"model":"ghost","messages":[{"role":"user","content":"x"}]}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "not found")
}

// errProvider 调用即返回预设错误，模拟上游故障通道。
type errProvider struct{ err error }

func (errProvider) Name() string { return "err" }
func (e errProvider) Chat(_ context.Context, _ provider.ChatRequest) (<-chan provider.Chunk, error) {
	return nil, e.err
}

// TestChatCompletionsFailoverOnDegraded 验证请求级 failover：
// 主通道返回 degraded 类错误（ErrUpstreamUnavailable）→ 自动切备通道，用户拿到备通道内容。
func TestChatCompletionsFailoverOnDegraded(t *testing.T) {
	gw := New()
	primary := &provider.Channel{ID: "m#primary", Priority: 0, Status: provider.StatusHealthy}
	primary.SetImpl(errProvider{err: provider.ErrUpstreamUnavailable})
	backup := &provider.Channel{ID: "m#backup", Priority: 1, Status: provider.StatusHealthy}
	backup.SetImpl(fakeStreamProvider{chunks: []provider.Chunk{{Content: "备通道OK"}}})
	require.NoError(t, gw.RegisterModel(&provider.Model{
		ID: "m", Vendor: "test", Channels: []*provider.Channel{primary, backup},
	}))

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`)
	ChatCompletions(gw, &Meter{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

	assert.Contains(t, rec.Body.String(), "备通道OK")
	assert.Contains(t, rec.Body.String(), "data: [DONE]")
	// 主通道应被标 degraded（degraded 类错误）
	assert.Equal(t, provider.StatusDegraded, primary.Status)
}

// TestChatCompletionsAllChannelsFail 验证全部通道失败 → 503。
func TestChatCompletionsAllChannelsFail(t *testing.T) {
	gw := New()
	a := &provider.Channel{ID: "m#a", Priority: 0, Status: provider.StatusHealthy}
	a.SetImpl(errProvider{err: provider.ErrUpstreamUnavailable})
	b := &provider.Channel{ID: "m#b", Priority: 1, Status: provider.StatusHealthy}
	b.SetImpl(errProvider{err: provider.ErrCredentialMissing})
	require.NoError(t, gw.RegisterModel(&provider.Model{
		ID: "m", Vendor: "test", Channels: []*provider.Channel{a, b},
	}))

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`)
	ChatCompletions(gw, &Meter{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	// degraded 类标 degraded，offline 类（凭证缺失）标 offline
	assert.Equal(t, provider.StatusDegraded, a.Status)
	assert.Equal(t, provider.StatusOffline, b.Status)
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
