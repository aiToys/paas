// Package web 将三套前端 SPA 构建产物嵌入 core 二进制，由 core 同源 serve（无 CORS）。
//
// 部署形态（单镜像，同域路由）：
//
//	/api/* /v1/* /openapi.json /docs /livez  → API（cmd/core 注册，ServeMux 精确匹配优先）
//	/console/*  → console-user（dist/console-user）
//	/admin/*    → console-admin（dist/console-admin）
//	/*          → landing（dist/landing，兜底）
//
// 前端写死同源相对路径 /api /v1，故同域反代即可，无需 CORS。
package web

import (
	"embed"
	"io/fs"
	"net/http"
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
	// 兜底：找不到文件时返回 index.html（SPA 前端路由）。
	// index.html 不存在（dev 占位）时返原错误 → FileServer 输出 404。
	return s.root.Open("index.html")
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
		return fileServer
	}
	return http.StripPrefix(prefix, fileServer)
}
