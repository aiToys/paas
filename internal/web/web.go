// Package web 将三套前端 SPA 构建产物嵌入 core 二进制，由 core 同源 serve（无 CORS）。
//
// 部署形态（单镜像，同域路由）：
//
//	/api/* /v1/* /openapi.json /docs /livez  → API（cmd/core 注册，ServeMux 精确匹配优先）
//	/console/*  → console-user（dist/console-user）
//	/next/*     → console-user-v2（dist/console-user-v2，新 IA 试验版）
//	/admin/*    → console-admin（dist/console-admin）
//	/*          → landing（dist/landing，兜底）
//
// 前端写死同源相对路径 /api /v1，故同域反代即可，无需 CORS。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
)

// distFS 嵌入 dist 下三套前端产物。Dockerfile 构建期填充；本地 dev 仅有 .gitkeep 占位
// （目录必须存在否则编译期 embed 报错）——dev 时 core 不 serve 前端，仅 API，不影响功能。
//
//go:embed all:dist
var distFS embed.FS

// spaFS 把未命中的文件请求 fallback 到 index.html（vue-router history 模式前端路由）。
// 静态资源（js/css/图片）命中则原样返回；找不到的路径返回 index.html 由前端路由接管。
type spaFS struct {
	root fs.FS
}

func (s spaFS) Open(name string) (fs.File, error) {
	if f, err := s.root.Open(name); err == nil {
		return f, nil
	}
	// 静态资源后缀未命中直接 404（不 fallback）：部署滚动窗口期旧 index.html 引用的
	// hash 资源已不存在，fallback 返回 index.html(HTML) 会被浏览器当 JS 模块执行抛
	// SyntaxError("Unexpected token '<'")——router 自愈正则难覆盖该形态且 404 语义才
	// 正确（2026-10-13 Agent 详情页白屏根因）。
	if isStaticAsset(name) {
		return nil, fs.ErrNotExist
	}
	// 兜底：找不到的导航路径返回 index.html（SPA 前端路由）。
	// index.html 不存在（dev 占位）时返原错误 → FileServer 输出 404。
	return s.root.Open("index.html")
}

// isStaticAsset 按扩展名判断静态资源（SPA 前端路由路径无扩展名，两者不相交）。
func isStaticAsset(name string) bool {
	switch filepath.Ext(name) {
	case ".js", ".mjs", ".css", ".map", ".json", ".png", ".jpg", ".jpeg", ".gif",
		".svg", ".ico", ".webp", ".avif", ".woff", ".woff2", ".ttf", ".eot",
		".txt", ".webmanifest":
		return true
	}
	return false
}

// cachePolicy 按资源形态设置缓存策略（SPA 标准实践）：
//   - /assets/*（Vite content-hash 文件名，内容变文件名必变）：immutable 长缓存；
//   - 其余（index.html 入口、SPA 路由 fallback、favicon 等无 hash 资源）：no-cache
//     （允许缓存但每次协商）——无任何缓存头时浏览器对 index.html 走启发式缓存，
//     发版后旧入口滞留 → 引用已删除的旧 chunk → 用户看到老版本或白屏。
func cachePolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// ServeStatic 返回服务指定前端子目录的 handler。
//
//	prefix 是 URL 前缀（如 "/console/"，必须带尾斜杠）；subDir 是 dist 下子目录名。
//	prefix 为 "/" 时不剥离，直接 serve（landing 兜底）。
func ServeStatic(prefix, subDir string) http.Handler {
	sub, err := fs.Sub(distFS, "dist/"+subDir)
	if err != nil {
		// dev 占位场景：子目录缺失，返回提示（不 panic，不影响 API）。
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "frontend not built (dev mode): "+subDir, http.StatusNotFound)
		})
	}
	fileServer := http.FileServer(http.FS(spaFS{root: sub}))
	if prefix == "/" {
		return cachePolicy(fileServer)
	}
	return cachePolicy(http.StripPrefix(prefix, fileServer))
}

// ServeDocs 服务 VitePress 文档站产物（多页静态 HTML，非 SPA）。
// 与 spaFS 不同：未命中路径返回 404（不 fallback index.html——文档站无前端路由），
// 目录路径（如 /docs/guide/）由 http.FileServer 自动补 index.html。
func ServeDocs() http.Handler {
	sub, err := fs.Sub(distFS, "dist/docs")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "docs not built (dev mode)", http.StatusNotFound)
		})
	}
	return http.StripPrefix("/docs/", http.FileServer(http.FS(sub)))
}
