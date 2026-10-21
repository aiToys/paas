package identity

import (
	"net/http"
	"strings"
)

// 平台管理权限（console-admin 的 /api/admin/* 域，D5-2）。
//
// 与租户域权限（application:read 等）刻意分命名空间：平台角色只持有 platform:*，
// 且 PlatformGrantsAny 不认 tenant:admin 通配——若复用 Role.Grants，任意租户管理员
// 都会自动获得平台权限（提权）。读写按 HTTP 方法派生：GET/HEAD → :read，其余 → :write。
const (
	PermPlatformIdentityRead       Permission = "platform:identity:read"
	PermPlatformIdentityWrite      Permission = "platform:identity:write"
	PermPlatformResourceRead       Permission = "platform:resource:read"
	PermPlatformResourceWrite      Permission = "platform:resource:write"
	PermPlatformFinanceRead        Permission = "platform:finance:read"
	PermPlatformFinanceWrite       Permission = "platform:finance:write"
	PermPlatformSecurityRead       Permission = "platform:security:read"
	PermPlatformSecurityWrite      Permission = "platform:security:write"
	PermPlatformObservabilityRead  Permission = "platform:observability:read"
	PermPlatformObservabilityWrite Permission = "platform:observability:write"
	PermPlatformAIRead             Permission = "platform:ai:read"
	PermPlatformAIWrite            Permission = "platform:ai:write"
)

// 平台内置角色（与租户域角色 BuiltinRoles 分开：不进租户成员的角色选择器）。
const (
	// RolePlatformViewer 平台只读运维：可看全部管理视图，不能写。
	RolePlatformViewer = "platform-viewer"
	// RolePlatformOps 资源运维：产品/负载/数据服务等资源的读写 + 可观测运维；不含财务与平台身份写。
	RolePlatformOps = "platform-ops"
	// RolePlatformFinance 财务：用量/配额/账单读写，资源只读；不能改资源或身份。
	RolePlatformFinance = "platform-finance"
	// RolePlatformSecurity 安全：审计/凭证/冻结租户（安全动作）；资源只读。
	RolePlatformSecurity = "platform-security"
)

// PlatformRoles 返回平台内置角色定义（D5-2 细分角色）。
//
// 最低权限原则：
//   - 只读运维覆盖全部 :read，用于巡检与代客排障；
//   - 资源运维不碰账单与身份写；
//   - 财务不碰资源写与身份写；
//   - 安全不碰资源写，但可冻结/恢复租户（安全动作走 PermPlatformSecurityWrite）。
func PlatformRoles() map[string]Role {
	return map[string]Role{
		RolePlatformViewer: {Name: RolePlatformViewer, Permissions: []Permission{
			PermPlatformIdentityRead, PermPlatformResourceRead, PermPlatformFinanceRead,
			PermPlatformSecurityRead, PermPlatformObservabilityRead, PermPlatformAIRead,
		}},
		RolePlatformOps: {Name: RolePlatformOps, Permissions: []Permission{
			PermPlatformIdentityRead, PermPlatformResourceRead, PermPlatformResourceWrite,
			PermPlatformObservabilityRead, PermPlatformObservabilityWrite,
			PermPlatformAIRead, PermPlatformSecurityRead,
		}},
		RolePlatformFinance: {Name: RolePlatformFinance, Permissions: []Permission{
			PermPlatformFinanceRead, PermPlatformFinanceWrite,
			PermPlatformResourceRead, PermPlatformIdentityRead,
		}},
		RolePlatformSecurity: {Name: RolePlatformSecurity, Permissions: []Permission{
			PermPlatformSecurityRead, PermPlatformSecurityWrite,
			PermPlatformIdentityRead, PermPlatformObservabilityRead, PermPlatformResourceRead,
		}},
	}
}

// IsPlatformRole 判断角色名是否为平台内置角色。
func IsPlatformRole(name string) bool {
	_, ok := PlatformRoles()[name]
	return ok
}

// PlatformGrantsAny 判断角色集是否持有任一平台权限。
// 只看平台角色定义，**不认 tenant:admin 通配**（见文件头说明）。
func PlatformGrantsAny(roles []string, perms ...Permission) bool {
	if len(roles) == 0 || len(perms) == 0 {
		return false
	}
	defs := PlatformRoles()
	for _, name := range roles {
		r, ok := defs[name]
		if !ok {
			continue
		}
		for _, granted := range r.Permissions {
			for _, want := range perms {
				if granted == want {
					return true
				}
			}
		}
	}
	return false
}

// platformDomain 把 /api/admin 路径前缀映射到权重域。未登记 = 仅超管（fail-closed）。
type platformDomain struct {
	prefix string
	read   Permission
	write  Permission
}

var platformDomains = []platformDomain{
	{"/api/admin/tenants", PermPlatformIdentityRead, PermPlatformIdentityWrite},
	{"/api/admin/users", PermPlatformIdentityRead, PermPlatformIdentityWrite},
	{"/api/admin/api-keys", PermPlatformIdentityRead, PermPlatformIdentityWrite},
	{"/api/admin/roles", PermPlatformIdentityRead, PermPlatformIdentityRead},
	{"/api/admin/app-members", PermPlatformIdentityRead, PermPlatformIdentityWrite},
	{"/api/admin/applications", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/workloads", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/dataservices", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/environments", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/services", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/lanes", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/pipelines", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/pipelineruns", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/releases", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/buildruns", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/images", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/engines", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/models", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/providers", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/pipeline-templates", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/governance", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/configcenter", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/namespaces", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/backups", PermPlatformResourceRead, PermPlatformResourceWrite},
	{"/api/admin/dashboard", PermPlatformResourceRead, PermPlatformResourceRead},
	{"/api/admin/quotas", PermPlatformFinanceRead, PermPlatformFinanceWrite},
	{"/api/admin/bills", PermPlatformFinanceRead, PermPlatformFinanceWrite},
	{"/api/admin/usage", PermPlatformFinanceRead, PermPlatformFinanceWrite},
	// 预算/结算（D5-4）：与菜单 /resources/budgets|settlements 的 FinanceRead 登记对齐，
	// 缺这条会让 finance 角色菜单可见但接口 403（menus_test 全表对账锁定）。
	{"/api/admin/budgets", PermPlatformFinanceRead, PermPlatformFinanceWrite},
	{"/api/admin/settlements", PermPlatformFinanceRead, PermPlatformFinanceWrite},
	{"/api/admin/token-usage", PermPlatformFinanceRead, PermPlatformFinanceRead},
	// dashboard 首页聚合（跨租户统计只读，无写端点；write 列同 read——POST 不存在，登记只为对账测试完备）
	{"/api/admin/dashboard", PermPlatformResourceRead, PermPlatformResourceRead},
	{"/api/admin/secrets", PermPlatformSecurityRead, PermPlatformSecurityWrite},
	{"/api/admin/audit-logs", PermPlatformSecurityRead, PermPlatformSecurityRead},
	{"/api/admin/observability", PermPlatformObservabilityRead, PermPlatformObservabilityWrite},
	{"/api/admin/alert-rules", PermPlatformObservabilityRead, PermPlatformObservabilityWrite},
	{"/api/admin/diagnostics", PermPlatformObservabilityRead, PermPlatformObservabilityRead},
	// 执行记录（外部 AgentRun 观测）在 admin 侧归「AI 运营」视图，故按 AI 域授权。
	{"/api/admin/agentrun", PermPlatformAIRead, PermPlatformAIRead},
	{"/api/admin/ai", PermPlatformAIRead, PermPlatformAIWrite},
}

// PlatformPermissionsFor 返回访问某管理端点所需的候选平台权限（任一满足即放行）。
// ok=false 表示该路径未登记，调用方应按「仅超管」处理（fail-closed）。
//
// 特例：冻结/恢复租户是安全动作，安全角色亦可执行——故该路径同时接受
// PermPlatformIdentityWrite 与 PermPlatformSecurityWrite。
func PlatformPermissionsFor(method, path string) ([]Permission, bool) {
	isWrite := method != http.MethodGet && method != http.MethodHead
	for _, d := range platformDomains {
		if !pathUnderPrefix(path, d.prefix) {
			continue
		}
		perm := d.read
		if isWrite {
			perm = d.write
		}
		perms := []Permission{perm}
		if perm == PermPlatformIdentityWrite && isTenantFreezePath(path) {
			perms = append(perms, PermPlatformSecurityWrite)
		}
		return perms, true
	}
	return nil, false
}

// pathUnderPrefix 按路径段边界匹配前缀：/api/admin/ai 匹配 /api/admin/ai/agents，
// 但不匹配 /api/admin/alert-rules。
func pathUnderPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix) && len(path) > len(prefix) && path[len(prefix)] == '/'
}

// isTenantFreezePath 判断是否为租户冻结/恢复端点（POST /api/admin/tenants/{id}/freeze|unfreeze）。
func isTenantFreezePath(path string) bool {
	if !strings.HasPrefix(path, "/api/admin/tenants/") {
		return false
	}
	trimmed := strings.TrimRight(path, "/")
	return strings.HasSuffix(trimmed, "/freeze") || strings.HasSuffix(trimmed, "/unfreeze")
}
