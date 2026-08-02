package gateway

import (
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// BearerAuth 是双通道鉴权中间件：按 token 形态分发。
//   - JWT（含两个 '.'）：auth.ParseType 校验 access token，注入 (tenant, roles, userID)
//   - API Key（无 '.'）：LookupAPIKey 解析，注入同一套 ctx
//
// 下游 handler 只认 ctx，不关心 token 来源；两种 token 共存
// （admin 浏览器用 JWT，程序化调用用 API Key）。失败统一 401。
func BearerAuth(idb identity.Repository, jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, err := auth.BearerToken(r)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			ctx := r.Context()
			if strings.Contains(tok, ".") {
				// JWT 通道
				c, err := auth.ParseType(tok, jwtSecret, auth.TokenAccess)
				if err != nil {
					httputil.WriteError(w, http.StatusUnauthorized, "invalid token")
					return
				}
				ctx = tenant.WithTenant(ctx, c.Tenant)
				ctx = WithRoles(ctx, c.Roles)
				ctx = WithUserID(ctx, c.Sub)
			} else {
				// API Key 通道（兼容现有程序化调用）
				k, err := idb.LookupAPIKey(r.Context(), tok)
				if err != nil {
					httputil.WriteError(w, http.StatusUnauthorized, "invalid api key")
					return
				}
				ctx = tenant.WithTenant(ctx, k.TenantID)
				ctx = WithRoles(ctx, k.Roles)
				ctx = WithUserID(ctx, k.UserID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
