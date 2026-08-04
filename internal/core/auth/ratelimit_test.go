package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }
func newFakeClock() *fakeClock      { return &fakeClock{t: time.Unix(1000000, 0)} }

func TestLoginLimiter_LocksAfter5Fails(t *testing.T) {
	l := newLoginLimiter()
	l.clock = newFakeClock()
	ip, user := "1.2.3.4", "acme-admin"
	for i := 0; i < 5; i++ {
		l.recordFailure(ip, user)
	}
	if ok, _ := l.allow(ip, user); ok {
		t.Error("5 次失败后应锁定")
	}
}

func TestLoginLimiter_AllowsBefore5Fails(t *testing.T) {
	l := newLoginLimiter()
	l.clock = newFakeClock()
	ip, user := "1.2.3.4", "acme-admin"
	for i := 0; i < 4; i++ {
		l.recordFailure(ip, user)
	}
	if ok, _ := l.allow(ip, user); !ok {
		t.Error("4 次失败不应锁定")
	}
}

func TestLoginLimiter_SuccessResetsCount(t *testing.T) {
	l := newLoginLimiter()
	l.clock = newFakeClock()
	ip, user := "1.2.3.4", "acme-admin"
	l.recordFailure(ip, user)
	l.recordSuccess(ip, user)
	if ok, _ := l.allow(ip, user); !ok {
		t.Error("成功后应放行")
	}
}

func TestLoginLimiter_LockExpires(t *testing.T) {
	l := newLoginLimiter()
	fc := newFakeClock()
	l.clock = fc
	ip, user := "1.2.3.4", "acme-admin"
	for i := 0; i < 5; i++ {
		l.recordFailure(ip, user)
	}
	fc.t = fc.t.Add(loginLockout + time.Second)
	if ok, _ := l.allow(ip, user); !ok {
		t.Error("锁定期过后应放行")
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	// 多段 XFF：首段（9.9.9.9）可能是客户端伪造，最右段（10.0.0.1）是 ingress 追加的真实 IP。
	// 取最右防攻击者伪造首段绕过 per-IP 限流。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 10.0.0.1")
	if got := clientIP(req); got != "10.0.0.1" {
		t.Errorf("应取 XFF 最右段（ingress 追加），got %s", got)
	}
}

func TestClientIP_XRealIP(t *testing.T) {
	// X-Real-IP（ingress 注入，覆盖客户端伪造）优先于 XFF。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "7.7.7.7")
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 10.0.0.1")
	if got := clientIP(req); got != "7.7.7.7" {
		t.Errorf("X-Real-IP 应优先，got %s", got)
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	if got := clientIP(req); got != "1.2.3.4" {
		t.Errorf("RemoteAddr 去端口，got %s", got)
	}
}
