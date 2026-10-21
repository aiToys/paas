package gateway

import (
	"context"
	"net/http"

	"github.com/aitoys/paas/pkg/attribution"
)

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

// RolesFromRequest 是 RolesFrom 的 request 形态，供装配层以依赖倒置方式注入
// 回调（如 auth 的菜单裁剪），避免下游包反向依赖 gateway 的私有 ctx 键。
func RolesFromRequest(r *http.Request) []string {
	v, _ := RolesFrom(r.Context())
	return v
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

// WithApp 把应用 ID 注入 ctx（应用级 Key 鉴权后调用）。
// 委托 pkg/attribution 共享实现，供 billing 消费（不破坏领域→基础设施分层）。
func WithApp(ctx context.Context, appID string) context.Context {
	return attribution.WithApp(ctx, appID)
}

// AppFrom 取出应用 ID（空 = 租户级 Key，用量归"未分配"桶）。
func AppFrom(ctx context.Context) string {
	return attribution.AppFrom(ctx)
}
