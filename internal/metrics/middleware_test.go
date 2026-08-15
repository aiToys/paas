package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPMiddlewareNormalizesRoute(t *testing.T) {
	reg := NewRegistry()
	paths := []string{"/api/applications", "/api/applications/{id}", "/v1/chat/completions"}
	mw := HTTPMiddleware(reg, paths)

	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	// /api/applications/app-1 应归一化为 /api/applications/{id}（最长前缀）
	req := httptest.NewRequest("GET", "/api/applications/app-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler 未被调用")
	}
	out := dump(t, reg)
	if !strings.Contains(out, `http_requests_total{method="GET",route="/api/applications/{id}",status="200"} 1`) {
		t.Errorf("未按归一化 route 记录:\n%s", out)
	}
}

func TestHTTPMiddlewareUnmatched(t *testing.T) {
	reg := NewRegistry()
	mw := HTTPMiddleware(reg, []string{"/api/applications"})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))

	req := httptest.NewRequest("GET", "/something/unknown", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := dump(t, reg)
	if !strings.Contains(out, `route="unmatched"`) {
		t.Errorf("未识别 path 应归 unmatched:\n%s", out)
	}
}

// 额外覆盖：段数不等 / 字面段不匹配 / 占位段匹配空段（应不匹配）。
func TestMatchesTemplateTable(t *testing.T) {
	cases := []struct {
		tmpl, actual string
		want         bool
	}{
		{"/api/applications/{id}", "/api/applications/app-1", true},
		{"/api/applications", "/api/applications", true},
		{"/api/applications/{id}", "/api/applications", false},     // 段数不等
		{"/api/applications/{id}", "/api/applications/a/b", false}, // 段数不等
		{"/v1/chat/completions", "/v1/chat/completions", true},     // 字面全对齐
		{"/v1/chat/completions", "/v1/chat/other", false},          // 字面段不匹配
		{"/api/{owner}/{repo}", "/api/foo/bar", true},              // 多占位
	}
	for _, c := range cases {
		if got := matchesTemplate(c.tmpl, c.actual); got != c.want {
			t.Errorf("matchesTemplate(%q,%q)=%v want %v", c.tmpl, c.actual, got, c.want)
		}
	}
}

// 额外覆盖：最长前缀优先（/api/applications/{id} 胜过 /api/applications）。
func TestRouteTmplPrefersLongest(t *testing.T) {
	paths := []string{"/api/applications", "/api/applications/{id}"}
	got := routeTmpl(paths, "/api/applications/app-1")
	if got != "/api/applications/{id}" {
		t.Errorf("routeTmpl=%q want /api/applications/{id}", got)
	}
}

// 额外覆盖：statusRecorder 默认 200（handler 未显式 WriteHeader）。
func TestStatusRecorderDefault(t *testing.T) {
	reg := NewRegistry()
	mw := HTTPMiddleware(reg, []string{"/x"})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok")) // 不调 WriteHeader，应记 200
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := dump(t, reg)
	if !strings.Contains(out, `status="200"`) {
		t.Errorf("默认 status 应为 200:\n%s", out)
	}
}

// 额外覆盖：statusRecorder 转发 http.Flusher（SSE 流式必需，防 P1.4 修过的缓冲回归）。
func TestStatusRecorderForwardsFlusher(t *testing.T) {
	reg := NewRegistry()
	mw := HTTPMiddleware(reg, []string{"/sse"})

	flushed := 0
	inner := &flusherRW{ResponseWriter: httptest.NewRecorder(), onFlush: func() { flushed++ }}

	// handler 内断言 w.(http.Flusher) 可达 + Flush 真转发到底层
	var seenFlusher bool
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if ok {
			seenFlusher = true
			f.Flush()
		}
	}))

	req := httptest.NewRequest("GET", "/sse", nil)
	h.ServeHTTP(inner, req)

	if !seenFlusher {
		t.Fatal("handler 内 w.(http.Flusher) 断言失败，SSE 流式会被缓冲")
	}
	if flushed != 1 {
		t.Errorf("底层 Flush 调用次数 = %d, want 1", flushed)
	}
}

// flusherRW 是实现 http.Flusher 的 fake ResponseWriter，记录 Flush 调用。
type flusherRW struct {
	http.ResponseWriter
	onFlush func()
}

func (f *flusherRW) Flush() {
	if f.onFlush != nil {
		f.onFlush()
	}
}
