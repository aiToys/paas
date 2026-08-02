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
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 10.0.0.1")
	if got := clientIP(req); got != "9.9.9.9" {
		t.Errorf("XFF 首段，got %s", got)
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	if got := clientIP(req); got != "1.2.3.4" {
		t.Errorf("RemoteAddr 去端口，got %s", got)
	}
}
