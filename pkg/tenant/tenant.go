// Package tenant 提供租户上下文的 ctx 传播。
// Repository 与中间件通过 TenantFrom 取租户；缺失即拒绝，防止绕过多租户隔离。
package tenant

import "context"

type ctxKey struct{}

// WithTenant 把租户 ID 注入 ctx。
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, tenantID)
}

// TenantFrom 取出租户 ID；不存在或空返回 ("", false)。
func TenantFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok && v != ""
}
