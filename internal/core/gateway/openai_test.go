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

	h := ChatCompletions(gw, meter, nil)
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
	h := ChatCompletions(gw, &Meter{}, nil)
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
	ChatCompletions(gw, &Meter{}, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

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
	ChatCompletions(gw, &Meter{}, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

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

// stubAgentDispatcher 记录收到的 model 与 msgs，写一段固定 SSE。
type stubAgentDispatcher struct {
	gotModel string
	gotN     int
	gotConv  string
}

func (s *stubAgentDispatcher) Match(model string) bool { return strings.HasPrefix(model, "agent:") }
func (s *stubAgentDispatcher) ServeSSE(w http.ResponseWriter, _ *http.Request, model string, msgs []provider.Message) error {
	s.gotModel = model
	s.gotN = len(msgs)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"agent-echo\"}}]}\n\ndata: [DONE]\n\n"))
	return nil
}
func (s *stubAgentDispatcher) ServeSSEConv(w http.ResponseWriter, r *http.Request, model, convID string, msgs []provider.Message) error {
	s.gotConv = convID
	return s.ServeSSE(w, r, model, msgs)
}

// agent:{id} 虚拟模型路由：holder.Match 命中时委托 dispatcher，不查 catalog 通道。
func TestChatCompletionsAgentVirtualModel(t *testing.T) {
	gw := New()
	// 即便同名 "agent:bot" 也没注册进 catalog，应走 dispatcher 而非 ResolveChannels（否则 404）。
	stub := &stubAgentDispatcher{}
	holder := &AgentDispatcherHolder{}
	holder.Set(stub)

	h := ChatCompletions(gw, &Meter{}, holder)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"agent:bot","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))

	require.Equal(t, http.StatusOK, rec.Code, "agent 走 dispatcher 不经通道解析")
	assert.Equal(t, "agent:bot", stub.gotModel)
	assert.Equal(t, 1, stub.gotN, "透传 messages")
	assert.Contains(t, rec.Body.String(), "agent-echo")
}

// holder 未注入 dispatcher 时 Match 安全返 false，agent 模型走 catalog 解析 → 404 不 panic。
func TestChatCompletionsAgentHolderEmptySafe(t *testing.T) {
	gw := New()
	holder := &AgentDispatcherHolder{} // 未 Set
	h := ChatCompletions(gw, &Meter{}, holder)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"agent:bot","messages":[{"role":"user","content":"hi"}]}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// errAgentDispatcher 模拟预检失败：返回指定 sentinel（测 ChatCompletions 错误映射）。
type errAgentDispatcher struct{ sentinel error }

func (e *errAgentDispatcher) Match(model string) bool { return strings.HasPrefix(model, "agent:") }
func (e *errAgentDispatcher) ServeSSE(_ http.ResponseWriter, _ *http.Request, _ string, _ []provider.Message) error {
	return e.sentinel
}
func (e *errAgentDispatcher) ServeSSEConv(w http.ResponseWriter, r *http.Request, model, _ string, msgs []provider.Message) error {
	return e.ServeSSE(w, r, model, msgs)
}

// agent 不存在 -> 404（SSE 写头前预检，干净状态码非空流）。
func TestChatCompletionsAgentNotFound(t *testing.T) {
	gw := New()
	holder := &AgentDispatcherHolder{}
	holder.Set(&errAgentDispatcher{sentinel: ErrAgentNotFound})
	h := ChatCompletions(gw, &Meter{}, holder)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"agent:ghost","messages":[{"role":"user","content":"hi"}]}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "agent not found")
}

// 护栏拦截 -> 422。
func TestChatCompletionsAgentBlocked(t *testing.T) {
	gw := New()
	holder := &AgentDispatcherHolder{}
	holder.Set(&errAgentDispatcher{sentinel: ErrAgentBlocked})
	h := ChatCompletions(gw, &Meter{}, holder)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"agent:bot","messages":[{"role":"user","content":"x"}]}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}
