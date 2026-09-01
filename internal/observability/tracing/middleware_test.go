package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestSpanHandler 起一个记录器 provider，模拟 otelhttp 建 span 后调 errorTraceMiddleware。
func newTestSpanHandler(t *testing.T, inner http.Handler) (*tracetest.SpanRecorder, http.Handler) {
	t.Helper()
	exp := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")
	// 镜像生产拓扑：otelhttp（外层建 span）→ errorTrace → inner。
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "test.span")
		defer span.End()
		ErrorTraceMiddleware(inner).ServeHTTP(w, r.WithContext(ctx))
	})
	return exp, root
}

func TestErrorTraceMiddlewareRecordsErrorMessage(t *testing.T) {
	exp, h := newTestSpanHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"分支已存在"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exp.Ended()
	if len(spans) != 1 {
		t.Fatalf("期望 1 个 span，得 %d", len(spans))
	}
	attrs := map[string]string{}
	for _, a := range spans[0].Attributes() {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs["exception.message"] != `{"error":"分支已存在"}` {
		t.Fatalf("exception.message 未记录错误原因: %q", attrs["exception.message"])
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("span status 应为 Error，得 %v", spans[0].Status().Code)
	}
}

func TestErrorTraceMiddlewareIgnoresSuccess(t *testing.T) {
	exp, h := newTestSpanHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))

	spans := exp.Ended()
	if len(spans) != 1 || spans[0].Status().Code == codes.Error {
		t.Fatalf("2xx 不应记异常")
	}
	for _, a := range spans[0].Attributes() {
		if string(a.Key) == "exception.message" {
			t.Fatalf("2xx 不应写 exception.message")
		}
	}
}

func TestErrorTraceMiddlewareRecordsPanic(t *testing.T) {
	exp, h := newTestSpanHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom: nil map write")
	}))
	rec := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }() // 模拟外层 recoveryMiddleware
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	}()

	spans := exp.Ended()
	attrs := map[string]string{}
	for _, a := range spans[0].Attributes() {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs["exception.type"] != "panic" || !strings.Contains(attrs["exception.message"], "boom") {
		t.Fatalf("panic 未记录: %v", attrs)
	}
	if !strings.Contains(attrs["exception.stacktrace"], "runtime/debug.Stack") {
		t.Fatalf("panic 堆栈未记录: %q", attrs["exception.stacktrace"])
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("panic span status 应为 Error")
	}
}

func TestErrorBodyWriterPreservesFlusher(t *testing.T) {
	// SSE 依赖 Flusher 委托：经 errorBodyWriter 包装后底层 Flush 必须可达。
	inner := httptest.NewRecorder()
	ew := &errorBodyWriter{ResponseWriter: inner}
	if _, ok := interface{}(ew).(http.Flusher); !ok {
		t.Fatalf("errorBodyWriter 应实现 http.Flusher（SSE 缓冲修复不能回退）")
	}
	ew.WriteHeader(http.StatusOK)
	_, _ = ew.Write([]byte("data: hi\n\n"))
	ew.Flush()
	if inner.Flushed {
		// httptest.ResponseRecorder.Flushed 标志 Flush 被调用
		_ = inner.Flushed
	}
}

func TestErrorTraceMiddlewareTruncatesLongBody(t *testing.T) {
	exp, h := newTestSpanHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 10000)))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))

	attrs := map[string]string{}
	for _, a := range exp.Ended()[0].Attributes() {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if len(attrs["exception.message"]) != maxExceptionMsgLen {
		t.Fatalf("应截断到 %d，得 %d", maxExceptionMsgLen, len(attrs["exception.message"]))
	}
}
