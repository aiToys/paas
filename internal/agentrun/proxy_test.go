package agentrun

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeUpstream 记录上游收到的请求（断言 ns/Authorization/path 透传形态）。
type fakeUpstream struct {
	gotNS   string
	gotAuth string
	gotPath string
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.gotNS = r.URL.Query().Get("ns")
	f.gotAuth = r.Header.Get("Authorization")
	f.gotPath = r.URL.Path
	if r.Header.Get("Cookie") != "" {
		w.WriteHeader(http.StatusTeapot) // 凭证最小化：平台 cookie 不得外发
		return
	}
	_, _ = w.Write([]byte(`[{"name":"r1","phase":"Succeeded"}]`))
}

func newTestProxy(t *testing.T, up http.Handler, authorize func(r *http.Request, perm string) bool) http.Handler {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	return NewProxy(srv.URL, authorize, nil)
}

func TestProxyForcesNSAndJWT(t *testing.T) {
	up := &fakeUpstream{}
	// 客户端恶意传 ns=agent-t-victim（越权尝试）+ cookie 会话
	p := newTestProxy(t, up, func(*http.Request, string) bool { return true })
	req := httptest.NewRequest(http.MethodGet, "/api/agentrun/runs?ns=agent-t-victim", nil)
	req.AddCookie(&http.Cookie{Name: "paas_access", Value: "eyJhb.eyJ9.sig", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200, got %d body %s", rec.Code, rec.Body.String())
	}
	if up.gotNS != "agent" {
		t.Errorf("上游 ns = %s, 期望强制 agent（剥离客户端传入）", up.gotNS)
	}
	if up.gotAuth != "Bearer eyJhb.eyJ9.sig" {
		t.Errorf("上游 Authorization = %s, 期望 cookie 值转 Bearer JWT", up.gotAuth)
	}
	if up.gotPath != "/api/runs" {
		t.Errorf("上游 path = %s, 期望 strip /api/agentrun 前缀", up.gotPath)
	}
}

func TestProxyBearerPassthrough(t *testing.T) {
	up := &fakeUpstream{}
	p := newTestProxy(t, up, func(*http.Request, string) bool { return true })
	req := httptest.NewRequest(http.MethodGet, "/api/agentrun/runs", nil)
	req.Header.Set("Authorization", "Bearer eyJx.eyJy.sig2")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if up.gotAuth != "Bearer eyJx.eyJy.sig2" {
		t.Errorf("JWT 形态 Authorization 应原样透传, got %s", up.gotAuth)
	}
}

func TestProxyAPIKeyRejected(t *testing.T) {
	p := newTestProxy(t, &fakeUpstream{}, func(*http.Request, string) bool { return true })
	req := httptest.NewRequest(http.MethodGet, "/api/agentrun/runs", nil)
	req.Header.Set("Authorization", "Bearer sk-acme-admin") // API Key 非 JWT，agentrun 无法验签
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("API Key 通道应 401（需登录会话）, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "登录") {
		t.Errorf("错误文案应明确登录会话要求: %s", rec.Body.String())
	}
}

func TestProxyNotConfigured(t *testing.T) {
	p := NewProxy("", func(*http.Request, string) bool { return true }, nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agentrun/runs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置应 503 引导, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "PAAS_AGENTRUN_URL") {
		t.Errorf("503 文案应引导配置: %s", rec.Body.String())
	}
}

func TestProxyUpstreamUnreachable(t *testing.T) {
	p := NewProxy("http://127.0.0.1:1", func(*http.Request, string) bool { return true }, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/agentrun/runs", nil)
	req.AddCookie(&http.Cookie{Name: "paas_access", Value: "eyJa.eyJb.c", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("上游不可达应 502 脱敏, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "dial") || strings.Contains(rec.Body.String(), "127.0.0.1") {
		t.Errorf("502 文案泄漏内部细节: %s", rec.Body.String())
	}
}

func TestProxyMethodPermission(t *testing.T) {
	up := &fakeUpstream{}
	var gotPerm string
	p := newTestProxy(t, up, func(_ *http.Request, perm string) bool { gotPerm = perm; return true })
	req := httptest.NewRequest(http.MethodDelete, "/api/agentrun/runs/r1", nil)
	req.Header.Set("Authorization", "Bearer eyJ1.eyJ2.s")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if gotPerm != PermWrite {
		t.Errorf("DELETE 权限 = %s, 期望 %s", gotPerm, PermWrite)
	}
	if up.gotPath != "/api/runs/r1" {
		t.Errorf("DELETE 上游 path = %s", up.gotPath)
	}
}

func TestProxyCookieNotForwarded(t *testing.T) {
	up := &fakeUpstream{}
	p := newTestProxy(t, up, func(*http.Request, string) bool { return true })
	req := httptest.NewRequest(http.MethodGet, "/api/agentrun/runs", nil)
	req.AddCookie(&http.Cookie{Name: "paas_access", Value: "eyJp.eyJq.z", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	req.AddCookie(&http.Cookie{Name: "paas_other", Value: "leak", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200, got %d body %s", rec.Code, rec.Body.String())
	}
}
