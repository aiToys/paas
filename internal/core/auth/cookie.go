package auth

import (
	"errors"
	"net/http"
)

const (
	accessCookieName  = "paas_access"
	refreshCookieName = "paas_refresh"
)

// setSessionCookies 签发 access + refresh 两个 httpOnly cookie。
//   - access: Path=/（所有 /api/* 携带），有效期 = AccessTTL
//   - refresh: Path=/api/auth（收窄暴露面，仅刷新端点携带），有效期 = RefreshTTL
//
// secure 由 PAAS_COOKIE_SECURE 控制：HTTP 部署需 false（否则浏览器拒收），配 TLS 后 true。
// SameSite=Lax 防 CSRF（同源 SPA 足够）。
func setSessionCookies(w http.ResponseWriter, access, refresh string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: accessCookieName, Value: access, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(AccessTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: refresh, Path: "/api/auth",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(RefreshTTL.Seconds()),
	})
}

// clearSessionCookies 设过期清除两个 cookie（登出用）。
func clearSessionCookies(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: accessCookieName, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Path: "/api/auth", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// refreshFromCookie 从请求读 refresh cookie；无则返错误。
func refreshFromCookie(r *http.Request) (string, error) {
	c, err := r.Cookie(refreshCookieName)
	if err != nil || c.Value == "" {
		return "", errors.New("missing refresh cookie")
	}
	return c.Value, nil
}
