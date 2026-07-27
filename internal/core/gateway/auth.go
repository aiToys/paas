package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/pkg/tenant"
)

// APIKeyAuth 校验 Authorization: Bearer <key>，解析为 (租户, 用户, 角色) 注入 ctx。
// API Key 是身份三元组的唯一凭证；失败返回 401。
func APIKeyAuth(idb identity.Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				writeErr(w, http.StatusUnauthorized, "missing api key")
				return
			}
			k, err := idb.LookupAPIKey(r.Context(), strings.TrimPrefix(h, prefix))
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid api key")
				return
			}
			ctx := tenant.WithTenant(r.Context(), k.TenantID)
			ctx = WithRoles(ctx, k.Roles)
			ctx = WithUserID(ctx, k.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// hasPermission 校验 ctx 中角色是否持有某权限（tenant-admin 通行）。
// 供方法级权限校验与 Require 中间件复用。
func hasPermission(ctx context.Context, perm identity.Permission) bool {
	roles, ok := RolesFrom(ctx)
	if !ok {
		return false
	}
	builtin := identity.BuiltinRoles()
	for _, name := range roles {
		if r, ok := builtin[name]; ok && r.Grants(perm) {
			return true
		}
	}
	return false
}

// RequestAllowed 供需方法级权限判定的 handler 复用（如应用 API 按方法区分读写权限）。
// 以字符串形式接收权限标识，避免下游领域包反向依赖 identity。
func RequestAllowed(r *http.Request, perm string) bool {
	return hasPermission(r.Context(), identity.Permission(perm))
}
