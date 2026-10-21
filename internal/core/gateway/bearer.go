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
	// 会话代次校验（D5-3）：撤销会话后旧 token 失效，带 30s 缓存避免每请求查库。
	sessions := newSessionGate(idb)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 通道 1：access cookie（浏览器会话，优先于 header）。
			// cookie 命中即走 JWT 解析；有 cookie 但无效 -> 401（不降级到 header，防混淆）。
			if c, err := r.Cookie(auth.AccessCookieName); err == nil && c.Value != "" {
				claims, perr := auth.ParseType(c.Value, jwtSecret, auth.TokenAccess)
				if perr != nil {
					httputil.WriteError(w, http.StatusUnauthorized, "invalid token")
					return
				}
				if !sessions.valid(r.Context(), claims.Tenant, claims.Sub, claims.Ver) {
					httputil.WriteError(w, http.StatusUnauthorized, "会话已失效，请重新登录")
					return
				}
				ctx := tenant.WithTenant(r.Context(), claims.Tenant)
				ctx = WithRoles(ctx, claims.Roles)
				ctx = WithUserID(ctx, claims.Sub)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// 通道 2/3：Authorization header（JWT 或 API Key）
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
				if !sessions.valid(r.Context(), c.Tenant, c.Sub, c.Ver) {
					httputil.WriteError(w, http.StatusUnauthorized, "会话已失效，请重新登录")
					return
				}
				ctx = tenant.WithTenant(ctx, c.Tenant)
				ctx = WithRoles(ctx, c.Roles)
				ctx = WithUserID(ctx, c.Sub)
			} else {
				// API Key 通道（兼容现有程序化调用）
				k, err := idb.LookupAPIKey(r.Context(), tok)
				if err != nil {
					// 冻结/到期/吊销是凭证存在但不可用：403 让运维能区分「Key 写错」与「被停用」，
					// 且不泄漏 Key 是否存在之外的任何信息。
					if code, msg, ok := credentialDeny(err); ok {
						httputil.WriteError(w, code, msg)
						return
					}
					httputil.WriteError(w, http.StatusUnauthorized, "invalid api key")
					return
				}
				touchAPIKeyUsage(r.Context(), idb, k.ID)
				ctx = tenant.WithTenant(ctx, k.TenantID)
				ctx = WithRoles(ctx, k.Roles)
				ctx = WithUserID(ctx, k.UserID)
				ctx = WithApp(ctx, k.AppID) // 应用级 Key：用量归因到应用
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
