package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetSessionCookies_Attributes(t *testing.T) {
	rec := httptest.NewRecorder()
	setSessionCookies(rec, "access.jwt", "refresh.jwt", false)

	a := findCookie(rec.Result().Cookies(), "paas_access")
	if a == nil {
		t.Fatal("缺 paas_access cookie")
	}
	if !a.HttpOnly {
		t.Error("paas_access 必须 HttpOnly")
	}
	if a.Secure {
		t.Error("secure=false 时 Secure 应为 false")
	}
	if a.SameSite != http.SameSiteLaxMode {
		t.Error("应 SameSite=Lax")
	}
	if a.Path != "/" {
		t.Error("paas_access Path 应为 /")
	}
	if a.MaxAge != int(AccessTTL.Seconds()) {
		t.Errorf("paas_access MaxAge 应为 %d，got %d", int(AccessTTL.Seconds()), a.MaxAge)
	}

	rf := findCookie(rec.Result().Cookies(), "paas_refresh")
	if rf == nil {
		t.Fatal("缺 paas_refresh cookie")
	}
	if rf.Path != "/api/auth" {
		t.Error("paas_refresh Path 应限定 /api/auth")
	}
	if rf.MaxAge != int(RefreshTTL.Seconds()) {
		t.Errorf("paas_refresh MaxAge 应为 %d，got %d", int(RefreshTTL.Seconds()), rf.MaxAge)
	}
}

func TestSetSessionCookies_SecureFlag(t *testing.T) {
	rec := httptest.NewRecorder()
	setSessionCookies(rec, "a", "r", true)
	a := findCookie(rec.Result().Cookies(), "paas_access")
	if !a.Secure {
		t.Error("secure=true 时 Secure 应为 true")
	}
}

func TestClearSessionCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	clearSessionCookies(rec, false)
	cs := rec.Result().Cookies()
	if len(cs) != 2 {
		t.Fatalf("应清 2 个 cookie，got %d", len(cs))
	}
	for _, c := range cs {
		if c.MaxAge >= 0 {
			t.Errorf("cookie %s 未过期清除（MaxAge=%d）", c.Name, c.MaxAge)
		}
	}
}

func TestRefreshFromCookie(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/auth/tokens/refresh", nil)
	if _, err := refreshFromCookie(req); err == nil {
		t.Error("无 cookie 应返错")
	}
	req.AddCookie(&http.Cookie{Name: "paas_refresh", Value: "rt"}) //nolint:gosec // G124 测试 cookie 无需安全属性
	v, err := refreshFromCookie(req)
	if err != nil || v != "rt" {
		t.Errorf("有 cookie 应返值，got v=%q err=%v", v, err)
	}
}

func findCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}
