package gateway

import (
	"net/http"
	"strings"
)

// APIKeyAuth 返回一个中间件，校验 Authorization: Bearer <key>。
// 失败返回 401 JSON。
func APIKeyAuth(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) || strings.TrimPrefix(h, prefix) != key {
				writeErr(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
