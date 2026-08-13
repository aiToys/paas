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
