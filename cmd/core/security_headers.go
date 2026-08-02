package main

import "net/http"

// securityHeadersMiddleware 注入安全响应头。
//   - X-Frame-Options: DENY（防点击劫持）
//   - X-Content-Type-Options: nosniff（防 MIME 嗅探）
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Content-Security-Policy: 同源 + Scalar CDN（/docs 用）
//   - Strict-Transport-Security: 仅 HTTPS（X-Forwarded-Proto=https，ingress TLS 后）下发；HTTP 跳过
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
