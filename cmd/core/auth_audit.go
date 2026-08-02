package main

import (
	"context"

	"github.com/aitoys/paas/internal/core/auth"
	"github.com/aitoys/paas/internal/security"
	"github.com/aitoys/paas/pkg/tenant"
)

// authAuditAdapter 桥接 auth.AuditRecorder -> security.AuditStore。
// auth 包不能 import security（反向依赖），由 cmd/core 注入。
//
// security store 的 RecordAudit 从 ctx 取 tenant（覆盖 log.TenantID），ctx 无 tenant 则报错。
// 登录端点公开（无 BearerAuth），login_failed 时 ctx 无 tenant -> adapter 注入 ctx tenant：
//   - 已知租户（login 成功/logout）：用传入 tenantID
//   - 未知租户（login_failed 用户不存在）：归平台级 "platform"（audit_logs.tenant_id NOT NULL）
type authAuditAdapter struct {
	store security.AuditStore
}

func (a *authAuditAdapter) Record(ctx context.Context, tenantID, actor, action, detail string) error {
	if tenantID == "" {
		tenantID = "platform"
	}
	ctx = tenant.WithTenant(ctx, tenantID)
	return a.store.RecordAudit(ctx, security.AuditLog{
		Actor:        actor,
		Action:       action,
		ResourceType: "session",
		ResourceID:   actor,
		Detail:       detail,
	})
}

var _ auth.AuditRecorder = (*authAuditAdapter)(nil)
