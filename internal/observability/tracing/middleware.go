// 异常 trace 归因：拦截错误响应与 panic，将原因写入当前 span（exception.* 属性），
// 供 Jaeger 查询端（real/traces.go）透传 ErrorMessage 展示异常原因。
//
// 背景：otelhttp 只按 HTTP 状态码设 span status，不记录异常消息；
// real/traces.go 从 exception.type/exception.message tag 取异常原因，但无人写入——
// 异常 trace 只有 error 标记、无原因。本中间件是唯一写入点（handler 零改动）。

package tracing

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// maxExceptionMsgLen 截断异常消息（防超大错误 body 膨胀 span）。
const maxExceptionMsgLen = 512

// ErrorTraceMiddleware 在当前 span（由外层 otelhttp 创建）上记录错误原因。
//   - 响应状态码 >=400：SetStatus(Error) + exception.message = {error:...} body；
//   - panic：recover 后记 exception.type=panic + 消息，再原样上抛（外层 recovery 兜底响应）。
//
// 时序关键：otelhttp 的 span.End() 在本中间件返回后才执行，但测试路径（以及任何
// 「内层先建 span 并 End」的组装）要求属性必须在 handler 执行期间写入——因此
// WriteHeader/Write 时（span 仍 recording）即时落属性，而非 ServeHTTP 返回后补写。
func ErrorTraceMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())

		// panic 捕获：记异常原因 + 堆栈后原样 re-panic，由外层 recoveryMiddleware 兜底写 500。
		// 此刻 span 仍在 recording（外层 otelhttp 的 End 尚未执行）。
		// 堆栈只 panic 有（业务 4xx/5xx 无异常对象，诚实不造）；recovery 日志另有全量栈。
		defer func() {
			if rec := recover(); rec != nil {
				span.SetAttributes(attribute.String("exception.stacktrace", string(debug.Stack())))
				recordException(span, "panic", truncateMsg(toStr(rec)))
				panic(rec)
			}
		}()

		h.ServeHTTP(&errorBodyWriter{ResponseWriter: w, span: span}, r)
	})
}

// recordException 在 span 上记异常原因 + Error status（幂等，多次调用属性合并）。
func recordException(span trace.Span, typ, msg string) {
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(
		attribute.String("exception.type", typ),
		attribute.String("exception.message", msg),
	)
	span.SetStatus(codes.Error, typ)
}

// errorBodyWriter 捕获错误响应的 {error:msg} body（业务错误原因真源）与状态码，
// 并在写入时机（span recording 期间）即时记入 span。
type errorBodyWriter struct {
	http.ResponseWriter
	span      trace.Span
	status    int
	msgRecord bool
}

func (w *errorBodyWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	if w.status >= 400 && !w.msgRecord {
		w.msgRecord = true
		recordException(w.span, http.StatusText(w.status), "") // type 先落，message 随 body 补
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *errorBodyWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	// 只截留错误响应的 body 前 512 字节（{error:msg} 契约下 msg 即原因；
	// 2xx/SSE 大 body 不截留，零开销）。
	if w.status >= 400 {
		if !w.msgRecord {
			w.msgRecord = true
			recordException(w.span, http.StatusText(w.status), truncateMsg(string(b)))
		} else {
			w.span.SetAttributes(attribute.String("exception.message", truncateMsg(string(b))))
		}
	}
	return w.ResponseWriter.Write(b)
}

// Flush 委托底层（SSE/流式响应经本中间件时不破坏 http.Flusher 断言——
// Go 接口嵌入不转发额外接口，必须显式实现，否则 ingress SSE 缓冲修复失效）。
func (w *errorBodyWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func truncateMsg(s string) string {
	if len(s) > maxExceptionMsgLen {
		return s[:maxExceptionMsgLen]
	}
	return s
}

func toStr(v interface{}) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
