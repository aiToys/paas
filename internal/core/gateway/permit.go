package gateway

import (
	"net/http"

	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/httputil"
)

// Require 返回粗粒度权限校验中间件。
// ctx 中任一角色（经 BuiltinRoles）持有 perm 即放行；否则 403。
// tenant-admin 因 Grants 通行所有权限。
func Require(perm identity.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasPermission(r.Context(), perm) {
				httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+string(perm))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
