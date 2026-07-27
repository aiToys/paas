package gateway

import "context"

// rolesKey / userIDKey 是 ctx 中身份信息的键。
// 由 APIKeyAuth 注入，供 Require 与下游 handler 读取。
type rolesKey struct{}
type userIDKey struct{}

// WithRoles 把角色名列表注入 ctx。
func WithRoles(ctx context.Context, roles []string) context.Context {
	return context.WithValue(ctx, rolesKey{}, roles)
}

// RolesFrom 取出角色名列表。
func RolesFrom(ctx context.Context) ([]string, bool) {
	v, ok := ctx.Value(rolesKey{}).([]string)
	return v, ok && len(v) > 0
}

// WithUserID 把用户 ID 注入 ctx。
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey{}, id)
}

// UserIDFrom 取出用户 ID。
func UserIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey{}).(string)
	return v
}
