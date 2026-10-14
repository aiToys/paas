package agentrun

import (
	"fmt"
	"net/http"
	nethttputil "net/http/httputil"
	"net/url"
	"strings"

	"github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/httputil"
)

// Proxy 是观测通道反向代理：/api/agentrun/* -> ${PAAS_AGENTRUN_URL}/api/*。
// 身份透传（契约 §5.2）：paas_access cookie 值（即 access JWT）或原 Bearer JWT 转
// Authorization 头；API Key（sk-*）非 JWT 无法被 agentrun 验签，明确 401。
// ns 服务端强制 agent（review focus ①：ns=租户边界，剥离客户端传入防越权）。
type Proxy struct {
	baseURL   *url.URL
	authorize func(r *http.Request, perm string) bool
	audit     func(r *http.Request, action string)
	rp        *nethttputil.ReverseProxy
}

// NewProxy 创建观测代理；baseURL 空=未配置（全部请求 503 引导，平台其余功能不受影响）。
// authorize/audit 可为 nil（装配层注入 gateway.RequestAllowed / 审计 adapter）。
func NewProxy(baseURL string, authorize func(r *http.Request, perm string) bool, audit func(r *http.Request, action string)) http.Handler {
	if baseURL == "" {
		return &unconfiguredProxy{}
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return &unconfiguredProxy{}
	}
	p := &Proxy{baseURL: u, authorize: authorize, audit: audit}
	p.rp = &nethttputil.ReverseProxy{
		Rewrite: func(pr *nethttputil.ProxyRequest) {
			pr.SetURL(p.baseURL) // 目标 scheme/host（admin env 配置，非用户输入）
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// 脱敏：dial tcp/上游内部错误不外发（错误契约），统一 502
			_ = err
			httputil.WriteError(w, http.StatusBadGateway, "Agent 编排平台不可达")
		},
	}
	return p
}

type unconfiguredProxy struct{}

func (unconfiguredProxy) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	httputil.WriteError(w, http.StatusServiceUnavailable, "Agent 编排未配置（PAAS_AGENTRUN_URL）")
}

// isJWT 契约 §1.3：Bearer token 含两个 '.' 即 JWT 形态；sk-* API Key 不是。
func isJWT(token string) bool { return strings.Count(token, ".") == 2 }

// UserBearerToken 提取用户 JWT：Authorization Bearer（JWT 形态）优先，否则 paas_access cookie 值。
// 导出供委派端点复用（cmd/core 桥接注入 change handler 的 tokenFrom）。
func UserBearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		t := strings.TrimPrefix(h, "Bearer ")
		if isJWT(t) {
			return t
		}
		return "" // API Key 等非 JWT：不用（agentrun 无法验签）
	}
	if c, err := r.Cookie(auth.AccessCookieName); err == nil && isJWT(c.Value) {
		return c.Value
	}
	return ""
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	perm := PermRead
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		perm = PermWrite
	}
	if p.authorize != nil && !p.authorize(r, perm) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
		return
	}
	token := UserBearerToken(r)
	if token == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "Agent 编排需要登录会话（API Key 不支持，请用控制台登录）")
		return
	}
	// ns 服务端强制：剥离客户端传入，防跨命名空间越权（agentrun 侧 ns=租户边界）。
	q := r.URL.Query()
	q.Del("ns")
	q.Set("ns", Namespace)
	r.URL.RawQuery = q.Encode()
	// 前缀剥离：/api/agentrun/x -> /api/x（路径拼接不做任何用户可控的 URL 解释）。
	r.URL.Path = "/api" + strings.TrimPrefix(r.URL.Path, "/api/agentrun")
	r.Host = p.baseURL.Host
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Del("Cookie") // 凭证最小化：平台会话 cookie 不外发
	if p.audit != nil && perm == PermWrite {
		p.audit(r, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
	}
	p.rp.ServeHTTP(w, r)
}
