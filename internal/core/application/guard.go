package application

import (
	"context"
	"net/http"
)

// AppGuard 是应用级权限 enforcement（依赖倒置装配点）。
// 各业务 handler（devops/pipeline/workload/application）注入后，在写操作前校验：
// 目标应用 restricted=true 时，调用者需是应用成员且角色授权该动作。
//
// 语义：
//   - 应用非受限（restricted=false）或 guard 未注入 -> 放行（向后兼容，渐进启用）。
//   - 租户管理员/平台超管 -> 通行（IsTenantAdmin 由 cmd/core 桥接 gateway.IsAdmin）。
//   - 其余 -> MemberRole 查角色，MemberAllowed 判定；非成员 fail-closed 拒绝。
type AppGuard struct {
	Apps     Repository
	Members  MemberRepository
	IsAdmin  func(r *http.Request) bool // 租户管理员判定（tenant:admin）
	UserIDFn func(ctx context.Context) string
}

// Allow 校验 r 对 appID 的 action 是否放行。不通过时返回 false（调用方回 403）。
// 应用不存在/查询失败时放行（后续业务路径自然 404/500，不在此重复存在性判定）。
func (g *AppGuard) Allow(r *http.Request, appID, action string) bool {
	if g == nil || g.Apps == nil || g.Members == nil || appID == "" {
		return true
	}
	ctx := r.Context()
	a, err := g.Apps.Get(ctx, appID)
	if err != nil || !a.Restricted {
		return true // 应用不存在（后续 404）或未开启受限模式
	}
	if g.IsAdmin != nil && g.IsAdmin(r) {
		return true
	}
	uid := ""
	if g.UserIDFn != nil {
		uid = g.UserIDFn(ctx)
	}
	if uid == "" {
		return false
	}
	role, err := g.Members.MemberRole(ctx, appID, uid)
	if err != nil {
		return false // 查询失败 fail-closed
	}
	return MemberAllowed(role, action)
}
