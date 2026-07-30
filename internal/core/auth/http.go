package auth

import (
	"errors"
	"net/http"
	"strings"
)

// ErrMissingBearer 表示请求未携带合法的 Bearer token。
var ErrMissingBearer = errors.New("missing bearer token")

// BearerToken 从 Authorization 头取 Bearer token。
func BearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", ErrMissingBearer
	}
	return strings.TrimPrefix(h, prefix), nil
}
