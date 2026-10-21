package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/core/identity"
)

// groupNames 取顶层可见分组名（含无子项的单页）。
func groupNames(menus []Menu) []string {
	out := make([]string, 0, len(menus))
	for _, m := range menus {
		out = append(out, m.Name)
	}
	return out
}

// groupChildNames 取某分组下的叶子名；分组不存在返回 nil。
func groupChildNames(menus []Menu, group string) []string {
	for _, m := range menus {
		if m.Name != group {
			continue
		}
		out := make([]string, 0, len(m.Children))
		for _, c := range m.Children {
			out = append(out, c.Name)
		}
		return out
	}
	return nil
}

// 平台角色按域裁剪菜单：安全角色看不到财务，运维角色全见。
func TestFilterMenusByPlatformRole(t *testing.T) {
	menus := staticMenus()

	viewer := filterMenus(menus, []string{identity.RolePlatformViewer})
	for _, g := range []string{"access", "aiops", "delivery", "infra", "usage", "secops"} {
		assert.Contains(t, groupNames(viewer), g, "只读运维应可见 %s", g)
	}

	ops := filterMenus(menus, []string{identity.RolePlatformOps})
	assert.NotContains(t, groupNames(ops), "usage", "资源运维不含 finance:read，不应看到用量与账单")
	assert.Contains(t, groupNames(ops), "secops")

	security := filterMenus(menus, []string{identity.RolePlatformSecurity})
	assert.NotContains(t, groupNames(security), "usage", "安全角色不应看到用量与账单")
	assert.Contains(t, groupNames(security), "secops")
	// AI 资产/执行记录/广场归 AI 域，安全角色不含 ai:read。
	assert.NotContains(t, groupChildNames(security, "aiops"), "resAi")
	// 模型与通道属资源域，安全角色只读可见。
	assert.Contains(t, groupChildNames(security, "aiops"), "model")

	finance := filterMenus(menus, []string{identity.RolePlatformFinance})
	assert.Contains(t, groupNames(finance), "usage")
	assert.NotContains(t, groupNames(finance), "secops", "财务角色不应看到安全与运维域")
}

// 未注入角色（nil）时保持原样：超管与兼容路径不裁剪。
func TestFilterMenusEmptyRolesKeepsAll(t *testing.T) {
	menus := staticMenus()
	full := groupNames(menus)
	assert.Equal(t, full, groupNames(filterMenus(menus, nil)))
	assert.Equal(t, full, groupNames(filterMenus(menus, []string{})))
}

// 无平台权限的角色（如租户 viewer）只留公共页，不泄露管理入口。
func TestFilterMenusTenantRoleSeesPublicOnly(t *testing.T) {
	menus := filterMenus(staticMenus(), []string{"viewer"})
	assert.Equal(t, []string{"home", "profile"}, groupNames(menus))
}

// 菜单裁剪与接口策略同域：菜单 Perm 必须能从 PlatformPermissionsFor 推出的域对上。
func TestMenuPermsMatchAPIPolicy(t *testing.T) {
	cases := map[string]string{ //nolint:gosec // 菜单/接口路径字面量，gosec 误判为凭证
		"/system/tenant":            "/api/admin/tenants",
		"/resources/quotas":         "/api/admin/quotas",
		"/resources/secrets":        "/api/admin/secrets",
		"/operations/observability": "/api/admin/alert-rules",
		"/resources/ai":             "/api/admin/ai/agents",
	}
	for menuPath, apiPath := range cases {
		perm, ok := menuPerms[menuPath]
		require.True(t, ok, "菜单 %s 应登记权限", menuPath)
		perms, ok := identity.PlatformPermissionsFor(http.MethodGet, apiPath)
		require.True(t, ok, "接口 %s 应登记策略", apiPath)
		assert.Equal(t, perms[0], perm, "菜单 %s 与接口 %s 权限域应一致", menuPath, apiPath)
	}
}

// Menus handler：注入角色来源后按角色裁剪，超管短路返回全量。
func TestMenusHandlerFiltersByInjectedRoles(t *testing.T) {
	roles := []string{identity.RolePlatformFinance}
	h := newAuthHandler(t).WithMenuRoles(func(*http.Request) []string { return roles })

	rec := httptest.NewRecorder()
	h.Menus(rec, httptest.NewRequest(http.MethodGet, "/api/system/menus", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var menus []Menu
	decodeData(t, rec.Body.Bytes(), &menus)
	assert.Contains(t, groupNames(menus), "usage")
	assert.NotContains(t, groupNames(menus), "secops")

	// 超管：即便角色表只有 super_admin，也应拿到全量（不依赖平台角色展开）。
	roles = []string{"super_admin"}
	rec = httptest.NewRecorder()
	h.Menus(rec, httptest.NewRequest(http.MethodGet, "/api/system/menus", nil))
	var all []Menu
	decodeData(t, rec.Body.Bytes(), &all)
	assert.Equal(t, groupNames(staticMenus()), groupNames(all))
}

// 菜单图标 ↔ console-admin 注册清单对账：菜单 icon 经后端 JSON 下发、前端
// <component :is> 运行时字符串解析（EP 按需引入编译期不可见），main.ts 手工
// 具名清单漏配时该图标静默不渲染、构建不报错（2026-10-21 用户实测 8 个菜单
// 缺图标）。此测试锁两端：rawMenus 每个非空 Icon 必须出现在 main.ts 的
// import 清单中，加菜单忘注册在 CI 即红。
func TestMenuIconsRegisteredInAdmin(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "console-admin", "src", "app", "main.ts"))
	require.NoError(t, err, "读取 console-admin main.ts 失败（路径变动需同步本测试）")
	src := string(b)

	var walk func(ms []Menu) []string
	walk = func(ms []Menu) []string {
		var icons []string
		for _, m := range ms {
			if m.Meta.Icon != "" {
				icons = append(icons, m.Meta.Icon)
			}
			icons = append(icons, walk(m.Children)...)
		}
		return icons
	}
	for _, icon := range walk(rawMenus()) {
		// 整词匹配：防「Box」误匹配「MessageBox」这类子串假绿。
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(icon) + `\b`)
		assert.True(t, re.MatchString(src),
			"菜单图标 %s 未在 console-admin main.ts 注册（该图标将不渲染）", icon)
	}
}
