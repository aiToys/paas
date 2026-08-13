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

// staticMenus 返回 console-admin 业务菜单，按平台运维职责分组：
//
//	工作台 / 身份与权限 / 推理服务 / 资源总览 / 平台治理 / 计费审计。
//
// 设计原则：
//   - 菜单按「管理域」聚合（非散落顶级）；同类资源按业务域二级分组避免臃肿。
//   - 「资源总览」只含租户业务资源（应用运行态 + DevOps 链路）；横切配置归「平台治理」，
//     财务合规归「计费审计」——三者语义不同，分顶级避免「资源」概念被滥用。
//   - 图标全局唯一（EP PascalCase），同级同维度不复用，降低视觉歧义。
//   - 个人中心不入侧栏（ShowMenu:false），由右上角用户下拉入口跳转。
//   - 子菜单 path 保持稳定（不随分组前缀化），前端跳转引用零牵连。
//   - 中间分组节点（如「应用运行态」）无 component，dynamic.ts 只注册叶子路由。
//
// 按角色过滤留后续（super_admin 可见全部，普通 admin 仅本租户相关）。
func staticMenus() []Menu {
	return []Menu{
		{
			Path:      "/",
			Name:      "home",
			Component: "dashboard/views/Home",
			Meta:      MenuMeta{Title: "工作台", Icon: "HomeFilled", ShowMenu: true},
		},
		{
			// 身份与权限：租户/用户/角色/API 密钥的跨租户管理（super_admin 通行）。
			Path: "/system",
			Name: "system",
			Meta: MenuMeta{Title: "身份与权限", Icon: "Lock", ShowMenu: true},
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
					Meta:      MenuMeta{Title: "角色管理", Icon: "Avatar", ShowMenu: true},
				},
				{
					Path:      "/system/apikey",
					Name:      "systemApikey",
					Component: "system/apikey/views/List",
					Meta:      MenuMeta{Title: "API 密钥", Icon: "Key", ShowMenu: true},
				},
			},
		},
		{
			// 推理服务：模型目录 + 通道 + 供应商预设（MaaS 平台级配置）。
			Path: "/maas",
			Name: "maas",
			Meta: MenuMeta{Title: "推理服务", Icon: "Cpu", ShowMenu: true},
			Children: []Menu{
				{
					Path:      "/model",
					Name:      "model",
					Component: "model/views/List",
					Meta:      MenuMeta{Title: "模型管理", Icon: "MagicStick", ShowMenu: true},
				},
				{
					// 模型详情（通道管理）：动态段路由，侧栏隐藏，由列表「通道」按钮跳入。
					Path:      "/model/:id",
					Name:      "modelDetail",
					Component: "model/views/Detail",
					Meta:      MenuMeta{Title: "模型详情", Icon: "Cpu", ShowMenu: false},
				},
				{
					// 供应商管理：预设 BaseURL+凭证+Type，创建通道选供应商即带入（免去手填）。
					Path:      "/provider",
					Name:      "provider",
					Component: "provider/views/List",
					Meta:      MenuMeta{Title: "供应商管理", Icon: "Connection", ShowMenu: true},
				},
			},
		},
		{
			// 资源总览：跨租户查看所有租户的业务资源（super_admin 平台运维视角）。
			// 只含业务资源（应用运行态 + DevOps 链路）；横切配置归「平台治理」，财务合规归「计费审计」。
			Path: "/resources",
			Name: "resources",
			Meta: MenuMeta{Title: "资源总览", Icon: "Grid", ShowMenu: true},
			Children: []Menu{
				{
					// 应用运行态：应用及其运行形态（工作负载/数据服务/环境）。
					Path: "/resources/runtime",
					Name: "resRuntime",
					Meta: MenuMeta{Title: "应用运行态", Icon: "Menu", ShowMenu: true},
					Children: []Menu{
						{
							Path:      "/resources/applications",
							Name:      "resApplications",
							Component: "resources/views/Applications",
							Meta:      MenuMeta{Title: "应用", Icon: "Files", ShowMenu: true},
						},
						{
							Path:      "/resources/workloads",
							Name:      "resWorkloads",
							Component: "resources/views/Workloads",
							Meta:      MenuMeta{Title: "工作负载", Icon: "Monitor", ShowMenu: true},
						},
						{
							Path:      "/resources/dataservices",
							Name:      "resDataservices",
							Component: "resources/views/Dataservices",
							Meta:      MenuMeta{Title: "数据服务", Icon: "Coin", ShowMenu: true},
						},
						{
							Path:      "/resources/environments",
							Name:      "resEnvironments",
							Component: "resources/views/Environments",
							Meta:      MenuMeta{Title: "环境", Icon: "Location", ShowMenu: true},
						},
					},
				},
				{
					// DevOps 链路：代码到上线的构建/镜像/发布产物。
					Path: "/resources/cicd",
					Name: "resCicd",
					Meta: MenuMeta{Title: "DevOps 链路", Icon: "Ticket", ShowMenu: true},
					Children: []Menu{
						{
							Path:      "/resources/buildruns",
							Name:      "resBuildRuns",
							Component: "resources/views/BuildRuns",
							Meta:      MenuMeta{Title: "构建", Icon: "Tools", ShowMenu: true},
						},
						{
							Path:      "/resources/images",
							Name:      "resImages",
							Component: "resources/views/Images",
							Meta:      MenuMeta{Title: "镜像", Icon: "Box", ShowMenu: true},
						},
						{
							Path:      "/resources/releases",
							Name:      "resReleases",
							Component: "resources/views/Releases",
							Meta:      MenuMeta{Title: "发布", Icon: "Promotion", ShowMenu: true},
						},
						{
							// 流水线运行：跨租户 PipelineRun 总览（运维看全平台流水线运行状态），10s 轮询。
							Path:      "/resources/pipelineruns",
							Name:      "resPipelineRuns",
							Component: "resources/views/PipelineRuns",
							Meta:      MenuMeta{Title: "流水线运行", Icon: "VideoPlay", ShowMenu: true},
						},
						{
							// 流水线模板：平台级公共模板 CRUD（builtin 拒改删），super_admin 维护，应用绑定用。
							// 属 DevOps 链路（构建→部署→发布编排），非推理服务。
							Path:      "/pipeline-template",
							Name:      "pipeline-template",
							Component: "pipeline-template/views/List",
							Meta:      MenuMeta{Title: "流水线模板", Icon: "Stopwatch", ShowMenu: true},
						},
					},
				},
			},
		},
		{
			// 平台治理：横切基础设施配置（配置中心/服务治理/告警/密钥/引擎目录）。
			// 与「资源总览」平级——这些是平台级配置而非租户业务资源。子菜单 path 保持稳定（前端零牵连）。
			Path: "/govern",
			Name: "govern",
			Meta: MenuMeta{Title: "平台治理", Icon: "SetUp", ShowMenu: true},
			Children: []Menu{
				{
					Path:      "/resources/namespaces",
					Name:      "resNamespaces",
					Component: "resources/views/Namespaces",
					Meta:      MenuMeta{Title: "配置中心", Icon: "FolderOpened", ShowMenu: true},
				},
				{
					Path:      "/resources/services",
					Name:      "resServices",
					Component: "resources/views/Services",
					Meta:      MenuMeta{Title: "服务治理", Icon: "Share", ShowMenu: true},
				},
				{
					Path:      "/resources/alert-rules",
					Name:      "resAlertRules",
					Component: "resources/views/AlertRules",
					Meta:      MenuMeta{Title: "告警规则", Icon: "Bell", ShowMenu: true},
				},
				{
					Path:      "/resources/secrets",
					Name:      "resSecrets",
					Component: "resources/views/Secrets",
					Meta:      MenuMeta{Title: "密钥", Icon: "DocumentCopy", ShowMenu: true},
				},
				{
					// 引擎目录：admin 配置数据服务引擎（managed/external-shared/dedicated），用户从 enabled 引擎创建实例。
					Path:      "/resources/engines",
					Name:      "resEngines",
					Component: "engine/views/List",
					Meta:      MenuMeta{Title: "引擎目录", Icon: "Coin", ShowMenu: true},
				},
			},
		},
		{
			// 计费审计：配额/账单/审计日志（财务 + 合规）。独立顶级——非业务资源。
			Path: "/billing",
			Name: "billing",
			Meta: MenuMeta{Title: "计费审计", Icon: "Wallet", ShowMenu: true},
			Children: []Menu{
				{
					Path:      "/resources/quotas",
					Name:      "resQuotas",
					Component: "resources/views/Quotas",
					Meta:      MenuMeta{Title: "配额", Icon: "Odometer", ShowMenu: true},
				},
				{
					Path:      "/resources/bills",
					Name:      "resBills",
					Component: "resources/views/Bills",
					Meta:      MenuMeta{Title: "账单", Icon: "CreditCard", ShowMenu: true},
				},
				{
					Path:      "/resources/audit-logs",
					Name:      "resAuditLogs",
					Component: "resources/views/AuditLogs",
					Meta:      MenuMeta{Title: "审计日志", Icon: "Document", ShowMenu: true},
				},
			},
		},
		{
			// 个人中心：移出侧栏（ShowMenu:false），路由保留供右上角用户下拉入口跳转。
			Path:      "/profile",
			Name:      "profile",
			Component: "profile/views/Profile",
			Meta:      MenuMeta{Title: "个人中心", Icon: "User", ShowMenu: false},
		},
	}
}
