package apiroute

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeDocs(t *testing.T) {
	h := ServeDocs("/openapi.json", "PaaS API")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type 应为 text/html，实际 %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/openapi.json") {
		t.Fatalf("HTML 应包含 specURL /openapi.json，实际:\n%s", body)
	}
	if !strings.Contains(body, "PaaS API") {
		t.Fatalf("HTML 应包含 title PaaS API，实际:\n%s", body)
	}
	// 必须引用 Scalar 渲染脚本。
	if !strings.Contains(body, "@scalar/api-reference") {
		t.Fatalf("HTML 应引用 @scalar/api-reference")
	}
	// 必须含离线降级提示。
	if !strings.Contains(body, "noscript") {
		t.Fatalf("HTML 应含 noscript 离线降级提示")
	}
}
