// Package attribution 提供 ctx 中的应用归因与 agent 软标签。
//
// 模型推理用量归因维度（业界 Org → Project → Key 共识）：
//   - AppID（强制计费维度）：应用级 API Key 鉴权后由 gateway 注入 ctx，
//     billing 据此把 token 用量归因到具体应用。空 = 租户级 Key（归"未分配"桶）。
//   - User（软标签，可选）：OpenAI 兼容的请求 user 字段，应用内多 agent 归因细分
//     （如 "researcher"/"coder"）。不做配额、仅看板聚合；与 AppID 正交。
//
// 放 pkg 层供 gateway（注入）与 billing（消费）共享，避免领域包反向依赖基础设施。
package attribution

import "context"

type appIDKey struct{}
type userKey struct{}

// WithApp 把应用 ID 注入 ctx（空值原样返回，不写入）。
func WithApp(ctx context.Context, appID string) context.Context {
	if appID == "" {
		return ctx
	}
	return context.WithValue(ctx, appIDKey{}, appID)
}

// AppFrom 取出应用 ID（空 = 未归因，租户级 Key 调用）。
func AppFrom(ctx context.Context) string {
	v, _ := ctx.Value(appIDKey{}).(string)
	return v
}

// WithUser 把 agent 软标签注入 ctx。
func WithUser(ctx context.Context, user string) context.Context {
	if user == "" {
		return ctx
	}
	return context.WithValue(ctx, userKey{}, user)
}

// UserFrom 取出 agent 软标签（可空）。
func UserFrom(ctx context.Context) string {
	v, _ := ctx.Value(userKey{}).(string)
	return v
}
