package knowledgebase

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// ErrUnsupportedMIME 暂不支持的文档类型（PDF/Office 等）。
// MVP 仅支持 text/plain / text/markdown / text/html；其他类型走供应商文档解析 API（接口预留）。
var ErrUnsupportedMIME = errors.New("暂不支持的文档类型")

// Parse 按 MIME 解析文档为纯文本。
//
// MVP 实现：
//   - text/plain / text/markdown：直接读取（markdown 不渲染，按文本切即可，RAG 不需格式）
//   - text/html：用 golang.org/x/net/html 提取文本节点（去标签）
//   - 其他（pdf/word/excel/ppt）：返 ErrUnsupportedMIME，后续接供应商文档解析 API
//
// 空_mime 或 application/octet-stream 按 text 处理（兼容未识别类型降级）。
func Parse(mime string, r io.Reader) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "text/plain", "text/markdown", "text/txt", "", "application/octet-stream":
		b, err := io.ReadAll(r)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case "text/html", "application/xhtml+xml":
		return extractHTMLText(r)
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedMIME, mime)
	}
}

// extractHTMLText 用 HTML 解析器提取所有文本节点内容（去标签），保留空白分隔。
func extractHTMLText(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			s := strings.TrimSpace(n.Data)
			if s != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(s)
			}
		}
		// 跳过 script/style 内容
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return sb.String(), nil
}
