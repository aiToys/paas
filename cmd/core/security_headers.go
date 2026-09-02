package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// csrfMiddleware 防跨站请求伪造（CSRF）：非 safe 方法（POST/PUT/DELETE/PATCH）校验
// Origin/Referer 的 host 与请求 Host 一致，跨站则 403。
//
// 背景：core 同域 serve SPA + httpOnly cookie 会话，浏览器写请求自动携带 cookie。
// SameSite=Lax 已阻止跨站 POST 携 cookie（主防线），但 Safari<16 Lax 实现不完整——此为纵深防御。
// API Key/Bearer 程序化请求（curl/SDK）一般不带 Origin 头，origin 空时放行（无凭据自动附带，天然免疫）。
// 浏览器同域请求 Origin host == r.Host 放行；跨站 CSRF Origin host != r.Host → 403。
func csrfMiddleware(next http.Handler) http.Handler {
	safe := map[string]bool{
		http.MethodGet: true, http.MethodHead: true,
		http.MethodOptions: true, http.MethodTrace: true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if safe[r.Method] {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = r.Header.Get("Referer")
		}
		if origin != "" {
			if u, err := url.Parse(origin); err == nil && u.Host != "" && !strings.EqualFold(u.Host, r.Host) {
				httputil.WriteError(w, http.StatusForbidden, "forbidden: cross-site request")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware 注入安全响应头。
//   - X-Frame-Options: DENY（防点击劫持）
//   - X-Content-Type-Options: nosniff（防 MIME 嗅探）
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Content-Security-Policy: 同源 + Scalar CDN（/docs 用）+ unsafe-eval（ECharts 需要）
//   - Strict-Transport-Security: 仅 HTTPS（X-Forwarded-Proto=https，ingress TLS 后）下发；HTTP 跳过
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// CSP：script-src 含 'unsafe-eval' —— ECharts（dashboard 图表，zrender 引擎）用
		// new Function 解析 SVG/坐标系，是合法依赖；同源 SPA + JSON API，eval 风险可控。
		// Scalar 文档 JS 已 vendored 同源（/api-docs/scalar.js），零第三方源（离线交付友好）。
		//
		// /docs/*（VitePress 文档站）单独放宽 script-src 'unsafe-inline'：VitePress SSR 产物
		// 含内联脚本（暗色模式检测 + Vue hydration），CSP 拦截内联脚本会致水合失败、整站
		// 不可交互（链接点击无响应）。内联脚本是构建产物非用户输入，且文档站只读无敏感
		// 操作，风险可控；SPA/API 路径维持严格 CSP 不放宽。
		csp := "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'"
		if strings.HasPrefix(r.URL.Path, "/docs/") || r.URL.Path == "/docs" {
			csp = "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'"
		}
		h.Set("Content-Security-Policy", csp)
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
