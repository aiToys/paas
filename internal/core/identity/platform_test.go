package identity

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 读写按 HTTP 方法派生：GET/HEAD → :read，其余 → :write。
func TestPlatformPermissionsForMethodDerivation(t *testing.T) {
	perms, ok := PlatformPermissionsFor(http.MethodGet, "/api/admin/bills")
	require.True(t, ok)
	assert.Equal(t, []Permission{PermPlatformFinanceRead}, perms)

	perms, ok = PlatformPermissionsFor(http.MethodHead, "/api/admin/bills")
	require.True(t, ok)
	assert.Equal(t, []Permission{PermPlatformFinanceRead}, perms)

	perms, ok = PlatformPermissionsFor(http.MethodPost, "/api/admin/bills/b-1/pay")
	require.True(t, ok)
	assert.Equal(t, []Permission{PermPlatformFinanceWrite}, perms)

	perms, ok = PlatformPermissionsFor(http.MethodDelete, "/api/admin/workloads/w-1")
	require.True(t, ok)
	assert.Equal(t, []Permission{PermPlatformResourceWrite}, perms)
}

// 未登记路径 fail-closed（仅超管）；前缀必须按段边界匹配。
func TestPlatformPermissionsForUnregisteredAndBoundary(t *testing.T) {
	_, ok := PlatformPermissionsFor(http.MethodGet, "/api/admin/unknown-domain/x")
	assert.False(t, ok, "未登记路径不得放行任何平台角色")

	// /api/admin/ai 不得吞掉 /api/admin/alert-rules（同前缀但不同段）。
	perms, ok := PlatformPermissionsFor(http.MethodGet, "/api/admin/alert-rules")
	require.True(t, ok)
	assert.Equal(t, []Permission{PermPlatformObservabilityRead}, perms)

	// 真实子路径应命中 /api/admin/ai。
	perms, ok = PlatformPermissionsFor(http.MethodGet, "/api/admin/ai/agents")
	require.True(t, ok)
	assert.Equal(t, []Permission{PermPlatformAIRead}, perms)
}

// 冻结/恢复租户是安全动作：租户管理写权限之外，安全写权限也可执行。
func TestPlatformPermissionsForTenantFreeze(t *testing.T) {
	for _, path := range []string{"/api/admin/tenants/t-1/freeze", "/api/admin/tenants/t-1/unfreeze"} {
		perms, ok := PlatformPermissionsFor(http.MethodPost, path)
		require.True(t, ok)
		assert.ElementsMatch(t, []Permission{PermPlatformIdentityWrite, PermPlatformSecurityWrite}, perms)
	}
	// 普通租户写（如创建租户）不应被安全权限放行。
	perms, ok := PlatformPermissionsFor(http.MethodPost, "/api/admin/tenants")
	require.True(t, ok)
	assert.Equal(t, []Permission{PermPlatformIdentityWrite}, perms)
}

// 租户域 tenant:admin 通配不得外溢成平台权限（提权防护）。
func TestPlatformGrantsAnyRejectsTenantAdminWildcard(t *testing.T) {
	assert.False(t, PlatformGrantsAny([]string{"tenant-admin"}, PermPlatformIdentityWrite),
		"tenant:admin 通配只作用于租户域权限")
	assert.False(t, PlatformGrantsAny([]string{"super_admin"}, PermPlatformIdentityWrite),
		"super_admin 由 IsPlatformAdmin 短路，不依赖权限展开")
	assert.False(t, PlatformGrantsAny(nil, PermPlatformResourceRead))
}

// 角色矩阵：只读运维全读、资源运维可写资源、财务不碰资源写、安全不碰资源。
func TestPlatformRoleMatrix(t *testing.T) {
	cases := []struct {
		role string
		perm Permission
		want bool
	}{
		{RolePlatformViewer, PermPlatformResourceRead, true},
		{RolePlatformViewer, PermPlatformResourceWrite, false},
		{RolePlatformViewer, PermPlatformFinanceRead, true},
		{RolePlatformOps, PermPlatformResourceWrite, true},
		{RolePlatformOps, PermPlatformIdentityWrite, false},
		{RolePlatformOps, PermPlatformFinanceRead, false},
		{RolePlatformFinance, PermPlatformFinanceWrite, true},
		{RolePlatformFinance, PermPlatformResourceRead, true},
		{RolePlatformFinance, PermPlatformResourceWrite, false},
		{RolePlatformSecurity, PermPlatformSecurityWrite, true},
		{RolePlatformSecurity, PermPlatformResourceWrite, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, PlatformGrantsAny([]string{c.role}, c.perm),
			"role=%s perm=%s", c.role, c.perm)
	}
}

// 平台角色与租户角色分表：平台角色不进 BuiltinRoles（租户成员角色选择器不暴露）。
func TestPlatformRolesSeparateFromTenantRoles(t *testing.T) {
	builtin := BuiltinRoles()
	for name, role := range PlatformRoles() {
		assert.True(t, IsPlatformRole(name))
		_, leaked := builtin[name]
		assert.False(t, leaked, "平台角色 %s 不应出现在租户域内置角色表", name)
		for _, p := range role.Permissions {
			assert.Contains(t, string(p), "platform:", "平台角色只持有 platform:* 权限")
		}
	}
	for name := range builtin {
		assert.False(t, IsPlatformRole(name), "租户角色 %s 不应被识别为平台角色", name)
	}
}

// TestPlatformDomainsCoverAllPermissions 全表对账（R3 审计 P6）：每个平台权限常量必须被
// platformDomains 至少一个路径引用（read 或 write 列）。未引用 = 死权限（角色授了也没接口认）
// 或漏登记路径（菜单可见但接口 403）——新增 Perm 时两处必须同步。
func TestPlatformDomainsCoverAllPermissions(t *testing.T) {
	all := map[Permission]bool{
		PermPlatformIdentityRead: false, PermPlatformIdentityWrite: false,
		PermPlatformResourceRead: false, PermPlatformResourceWrite: false,
		PermPlatformFinanceRead: false, PermPlatformFinanceWrite: false,
		PermPlatformSecurityRead: false, PermPlatformSecurityWrite: false,
		PermPlatformObservabilityRead: false, PermPlatformObservabilityWrite: false,
		PermPlatformAIRead: false, PermPlatformAIWrite: false,
		PermPlatformDiagnostics: false,
	}
	for _, d := range platformDomains {
		if _, ok := all[d.read]; ok {
			all[d.read] = true
		} else {
			t.Errorf("platformDomains 引用了未登记权限 %q（补 all 表或删路径）", d.read)
		}
		if _, ok := all[d.write]; ok {
			all[d.write] = true
		} else {
			t.Errorf("platformDomains 引用了未登记权限 %q", d.write)
		}
	}
	for perm, used := range all {
		if !used {
			t.Errorf("权限 %q 未被任何 platformDomains 路径引用（死权限或漏登记路径）", perm)
		}
	}
}
