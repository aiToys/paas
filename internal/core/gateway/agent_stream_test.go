package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

type reportedAgentStream struct{}

func (reportedAgentStream) Match(string) bool { return true }
func (reportedAgentStream) ServeSSE(w http.ResponseWriter, _ *http.Request, _ string, _ []provider.Message) error {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"error\":\"dependency failed\"}\n\ndata: [DONE]\n\n"))
	return ErrAgentStreamReported
}
func (s reportedAgentStream) ServeSSEConv(w http.ResponseWriter, r *http.Request, m, _ string, messages []provider.Message) error {
	return s.ServeSSE(w, r, m, messages)
}

func TestReportedAgentStreamHasNoJSONAfterDone(t *testing.T) {
	w := httptest.NewRecorder()
	holder := &AgentDispatcherHolder{}
	holder.Set(reportedAgentStream{})
	ChatCompletions(New(), &Meter{}, holder).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"agent:a","messages":[{"role":"user","content":"hi"}]}`)))
	if !strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n") || strings.Contains(w.Body.String(), "agent unavailable") {
		t.Fatalf("DONE 后被追加 HTTP 错误: %s", w.Body.String())
	}
}
