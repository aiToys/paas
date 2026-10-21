// Package admin 提供各模块 admin_handler 的公共抽象，消除散落在 8+ 个模块的重复定义：
//
//   - AuditRecorder 接口（admin 写操作审计，统一签名）
//   - TenantCtx（admin 跨租户操作：派生资源所属租户 ctx）
//
// 设计：admin handler 模式（super_admin 跨租户 + 绕过 prod:write + 写必记审计）横切全平台，
// 抽到本包作单一真源，避免各模块 AdminAuditRecorder/adminTenantCtx 漂移。
package admin

import (
	"context"
	"net/http"

	"github.com/aitoys/paas/pkg/tenant"
)

// AuditRecorder admin 写操作审计（依赖倒置，避免各 admin_handler 反向依赖 security）。
// 各模块的 AdminAuditRecorder 接口统一为别名指向本接口（消除 9 处重复定义）。
//
// 参数：
//   - tenantID     资源所属租户（target_tenant；平台级资源用 "platform"）
//   - actor        操作者（super_admin UserID）
//   - action       动作（带 admin: 前缀，如 "admin:scale"）
//   - resourceType 资源类型（如 "workload" / "environment"）
//   - resourceID   资源 ID（平台级操作可空）
//   - detail       补充说明
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// TenantCtx 派生资源所属租户 ctx，返回新 ctx 与基于该 ctx 的 request。
// admin 跨租户操作需以资源租户身份执行下游（Repository 按 ctx tenant 过滤），故派生。
// 替代各 admin_handler 重复定义的 adminTenantCtx。
func TenantCtx(r *http.Request, tenantID string) (context.Context, *http.Request) {
	ctx := tenant.WithTenant(r.Context(), tenantID)
	return ctx, r.WithContext(ctx)
}

// FilterTenant 按请求 query 的 tenantId 过滤跨租户列表（浏览范围，spec 2026-10-19 §7.1）。
// 顶栏浏览范围是纯过滤器：不改变登录身份、资源归属或写入目标，只收敛列表返回集合。
// 各 admin handler 在 ListAll 后调用；tenantId 缺省返回原列表（全部租户）。
//
// 平台级供给（模型/供应商/引擎/平台凭证/广场快照）不按租户过滤，不接入本函数——
// 前端以「平台级，不按租户过滤」声明。
func FilterTenant[T any](r *http.Request, items []T, tenantOf func(T) string) []T {
	tid := r.URL.Query().Get("tenantId")
	if tid == "" {
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		if tenantOf(it) == tid {
			out = append(out, it)
		}
	}
	return out
}
