package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEEmitsDependencyErrorAndRawDone(t *testing.T) {
	r := &Runtime{agents: &stubRepo{a: Agent{Enabled: false}}}
	w := httptest.NewRecorder()
	if err := r.ServeSSE(w, context.Background(), "disabled", nil); err == nil {
		t.Fatal("expected disabled error")
	}
	body := w.Body.String()
	if !strings.Contains(body, `"error"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") || strings.Contains(body, `"[DONE]"`) {
		t.Fatalf("SSE 错误或终止帧不正确: %s", body)
	}
}
