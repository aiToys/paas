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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api-docs", nil))

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
	// 必须引用同源 vendored Scalar 渲染脚本（零外网依赖，离线交付友好）。
	if !strings.Contains(body, "/api-docs/scalar.js") {
		t.Fatalf("HTML 应引用同源 /api-docs/scalar.js")
	}
	if strings.Contains(body, "cdn.jsdelivr.net") {
		t.Fatalf("HTML 不应再引用 CDN（已 vendored 本地化）")
	}
	// 必须含降级提示。
	if !strings.Contains(body, "noscript") {
		t.Fatalf("HTML 应含 noscript 降级提示")
	}
}

// vendored Scalar JS 经同源端点下发（离线交付关键：不依赖 CDN）。
func TestServeDocsScalarJS(t *testing.T) {
	h := ServeDocs("/openapi.json", "PaaS API")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api-docs/scalar.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("scalar.js 应 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("Content-Type 应为 application/javascript，实际 %q", ct)
	}
	if len(rec.Body.Bytes()) < 1_000_000 { // vendored 完整 standalone ~3.7MB
		t.Fatalf("scalar.js 应为完整 standalone 产物，实际 %d bytes", rec.Body.Len())
	}
	if !strings.Contains(rec.Body.String(), "Scalar") {
		t.Fatalf("scalar.js 应含 Scalar 挂载点")
	}
}
