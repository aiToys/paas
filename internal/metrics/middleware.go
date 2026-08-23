package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// routeTmpl 把实际 path 映射回已注册模板（含 {id} 占位），按最长前缀匹配。
// 例：paths 含 /api/applications/{id}，实际 /api/applications/app-1 → /api/applications/{id}。
// 未匹配返 "unmatched"（防高基数 label）。
func routeTmpl(paths []string, actual string) string {
	best := ""
	for _, p := range paths {
		if matchesTemplate(p, actual) && len(p) > len(best) {
			best = p
		}
	}
	if best == "" {
		return "unmatched"
	}
	return best
}

// matchesTemplate 判 actual 是否匹配模板 tmpl（段对齐，{xxx} 段匹配任意非空段）。
func matchesTemplate(tmpl, actual string) bool {
	ts := splitPath(tmpl)
	as := splitPath(actual)
	if len(ts) != len(as) {
		return false
	}
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") {
			continue // 占位段匹配任意非空段
		}
		if ts[i] != as[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// RouteTemplate 导出 routeTmpl 供 otelhttp span 命名复用（HTTP semconv：span 名 = "{method} {route}"，
// route 用注册模板归一化防高基数，与 http_requests_total 的 route label 同源单一真源）。
func RouteTemplate(paths []string, actual string) string { return routeTmpl(paths, actual) }

// HTTPMiddleware 记录 http_requests_total{method,route,status} + http_request_duration_seconds。
// route 经 paths（已注册模板）归一化，防高基数。
func HTTPMiddleware(reg *Registry, paths []string) func(http.Handler) http.Handler {
	// 预排序：长的在前，匹配走最长前缀优先（routeTmpl 按 len 比较）。
	sorted := append([]string(nil), paths...)
	sort.Sort(byLen(sorted))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)
			route := routeTmpl(sorted, r.URL.Path)
			reg.httpReqs.WithLabelValues(r.Method, route, fmt.Sprintf("%d", rw.status)).Inc()
			reg.httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		})
	}
}

type byLen []string

func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) > len(b[j]) } // 长的在前，最长优先
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush 转发底层 http.Flusher（如 httptest.ResponseRecorder / zeus / hermes statusRecorder）。
// Go 接口嵌入 http.ResponseWriter 不自动转发额外接口，下游 handler 的 `w.(http.Flusher)`
// 断言会失败 -> SSE 流式端点（/v1/chat/completions）无法逐 chunk flush，回归 P1.4 修过的缓冲问题。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
