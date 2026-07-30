package apiroute

import (
	"html/template"
	"net/http"
)

// scalarTpl 是嵌入式 Scalar API 文档页面模板。
// Scalar（Apache 2.0）经 jsdelivr CDN 加载；页面拉 specURL 渲染交互文档。
// 离线/内网场景 CDN 不可达时，noscript 与文案降级提示（vendored 本地 JS 留后续）。
const scalarTpl = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>{{.Title}}</title>
<style>body{margin:0}</style>
</head>
<body>
<noscript>启用 JavaScript 以查看 API 交互文档（需联网加载 Scalar）。</noscript>
<script id="api-reference" data-url="{{.SpecURL}}"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`

// ServeDocs 返回 /docs 交互文档 handler（Scalar，嵌入式 HTML）。
// 公开无鉴权（与 /openapi.json 一致，契约文档非敏感数据）。
func ServeDocs(specURL, title string) http.Handler {
	tpl := template.Must(template.New("scalar").Parse(scalarTpl))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tpl.Execute(w, struct {
			Title   string
			SpecURL string
		}{Title: title, SpecURL: specURL})
	})
}
