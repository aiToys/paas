package apiroute

import (
	_ "embed"
	"html/template"
	"net/http"
)

// scalarJS 是 vendored 的 Scalar standalone 浏览器版（@scalar/api-reference 1.67.0，MIT，
// IIFE 挂 window.Scalar）。原从 jsdelivr CDN 加载，离线交付（airsync 私有化）内网不可达——
// 本地化后零外网依赖，CSP 也不再需要第三方源。升级：换文件 + 改此处版本注释。
//
//go:embed static/scalar-standalone.js
var scalarJS []byte

// scalarTpl 是嵌入式 Scalar API 文档页面模板。加载同源 /api-docs/scalar.js 渲染交互文档。
const scalarTpl = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>{{.Title}}</title>
<style>body{margin:0}</style>
</head>
<body>
<noscript>启用 JavaScript 以查看 API 交互文档。</noscript>
<script id="api-reference" data-url="{{.SpecURL}}"></script>
<script src="/api-docs/scalar.js"></script>
</body>
</html>`

// ServeDocs 返回 /api-docs 交互文档 handler（Scalar，vendored 本地 JS，零外网依赖）。
// 公开无鉴权（与 /openapi.json 一致，契约文档非敏感数据）。
func ServeDocs(specURL, title string) http.Handler {
	tpl := template.Must(template.New("scalar").Parse(scalarTpl))
	mux := http.NewServeMux()
	mux.HandleFunc("/api-docs/scalar.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400") // 构建产物不可变，缓存 1 天
		_, _ = w.Write(scalarJS)
	})
	mux.HandleFunc("/api-docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tpl.Execute(w, struct {
			Title   string
			SpecURL string
		}{Title: title, SpecURL: specURL})
	})
	return mux
}
