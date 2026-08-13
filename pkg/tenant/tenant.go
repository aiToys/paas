// Package tenant 提供租户上下文的 ctx 传播。
// Repository 与中间件通过 TenantFrom 取租户；缺失即拒绝，防止绕过多租户隔离。
package tenant

import (
	"context"
	"errors"
)

// ErrMissingContext 表示 ctx 中缺失租户。Repository 写/读方法收到此错误时拒绝执行，
// 防止绕过多租户隔离（admin 跨租户路径用 ListAll 显式不过滤 tenant）。
var ErrMissingContext = errors.New("missing tenant context")

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

// IDOrErr 取出租户 ID，缺失返 ErrMissingContext。是 TenantFrom 的便捷封装，
// 供各 Repository 取 ctx 租户用（消除散落在各 memory/pg store 的 tenantOrErr 重复定义）。
func IDOrErr(ctx context.Context) (string, error) {
	tid, ok := TenantFrom(ctx)
	if !ok {
		return "", ErrMissingContext
	}
	return tid, nil
}
