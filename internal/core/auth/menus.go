package auth

// Menu 对齐 console-admin 的 MenuDTO（lib/router/types-menu.ts）。
// component 为相对 src/modules/ 的路径（不含扩展名），由前端动态路由装载。
type Menu struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	Component string   `json:"component,omitempty"`
	Meta      MenuMeta `json:"meta"`
	Children  []Menu   `json:"children,omitempty"`
}

// MenuMeta 是菜单/路由元数据。
type MenuMeta struct {
	Title    string `json:"title"`
	Icon     string `json:"icon"`
	ShowMenu bool   `json:"showMenu"`
}

// staticMenus 返回 console-admin 业务菜单（dashboard/system/profile）。
// system 下含租户/用户/角色管理；按角色过滤留后续（super_admin 可见全部，普通 admin 仅本租户相关）。
func staticMenus() []Menu {
	return []Menu{
		{
			Path:      "/",
			Name:      "home",
			Component: "dashboard/views/Home",
			Meta:      MenuMeta{Title: "首页", Icon: "HomeFilled", ShowMenu: true},
		},
		{
			Path: "/system",
			Name: "system",
			Meta: MenuMeta{Title: "系统管理", Icon: "Setting", ShowMenu: true},
			Children: []Menu{
				{
					Path:      "/system/tenant",
					Name:      "systemTenant",
					Component: "system/tenant/views/List",
					Meta:      MenuMeta{Title: "租户管理", Icon: "OfficeBuilding", ShowMenu: true},
				},
				{
					Path:      "/system/user",
					Name:      "systemUser",
					Component: "system/user/views/List",
					Meta:      MenuMeta{Title: "用户管理", Icon: "User", ShowMenu: true},
				},
				{
					Path:      "/system/role",
					Name:      "systemRole",
					Component: "system/role/views/List",
					Meta:      MenuMeta{Title: "角色管理", Icon: "UserFilled", ShowMenu: true},
				},
			},
		},
		{
			Path:      "/profile",
			Name:      "profile",
			Component: "profile/views/Profile",
			Meta:      MenuMeta{Title: "个人中心", Icon: "User", ShowMenu: true},
		},
	}
}
