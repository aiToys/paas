package httputil

import (
	"net/http"
	"time"
)

// noFollowRedirects 标记客户端不跟随 HTTP 重定向（CheckRedirect 返回 ErrUseLastResponse，
// 把 3xx 原样交给调用方处理）。
//
// 防御目的（SSRF 纵深）：这些客户端携带凭证（gitea basic auth / 各后端 query token），
// 若端点被劫持/误配为返 302→攻击者主机或云元数据接口（169.254.169.254），
// 跟随重定向会把 Authorization 头/query 凭证外发。不跟随即阻断该泄漏路径。
// 与 maas.OpenAICompatibleProvider 的 CheckRedirect 同源（深度检测第 3 轮 Critical 修复）。
func noFollowRedirects() func(*http.Request, []*http.Request) error {
	return func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
}

// NewClient 构造一个不跟随重定向的出站 HTTP 客户端（统一 SSRF 纵深）。
// gitea/registry/observability 等平台内部适配器统一用此构造，避免各自遗漏 CheckRedirect。
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: noFollowRedirects(),
	}
}
