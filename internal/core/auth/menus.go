package auth

import "github.com/aitoys/paas/internal/core/identity"

// Menu 对齐 console-admin 的 MenuDTO（lib/router/types-menu.ts）。
// component 为相对 src/modules/ 的路径（不含扩展名），由前端动态路由装载。
type Menu struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Component string `json:"component,omitempty"`
	// Perm 是访问该页所需的平台权限（D5-2 细分角色）。空 = 所有平台角色可见。
	// 服务端按调用者角色裁剪，前端无需感知（路由与菜单同时消失）。
	Perm string `json:"perm,omitempty"`
	// Redirect 兼容跳转目标（"path?k=v"）；设置后不注册 component，仅注册重定向路由
	// （旧入口迁移到 tab 容器页，保留原 query/hash——spec 2026-10-19 §5.1）。
	Redirect string   `json:"redirect,omitempty"`
	Meta     MenuMeta `json:"meta"`
	Children []Menu   `json:"children,omitempty"`
}

// MenuMeta 是菜单/路由元数据。
type MenuMeta struct {
	Title    string `json:"title"`
	Icon     string `json:"icon"`
	ShowMenu bool   `json:"showMenu"`
	// Keywords 搜索/别名元数据（菜单搜索、快捷入口与导航同源用；当前含俗称与旧名）。
	Keywords []string `json:"keywords,omitempty"`
}

// staticMenus 返回 console-admin 业务菜单（2026-10-19 IA 重构，spec §5）：
//
//	平台总览 + 六职责域（租户与访问 / AI 运营 / 应用与交付 / 基础设施 / 用量与账单 / 安全与运维）。
//
// 设计原则：
//   - 普通导航最多两层「管理域 → 页面」；实体类型收敛为页内 tab（用户与授权/交付记录/
//     数据服务/AI 资产/可观测），侧栏不再出现第三级。
//   - 子菜单 path 保持稳定（不随分组前缀化），前端跳转引用零牵连。
//   - 图标全局唯一（EP PascalCase），同级同维度不复用，降低视觉歧义。
//   - 兼容跳转：旧路径（角色管理/构建/镜像/发布/引擎目录/告警规则）以隐藏 Redirect 菜单
//     保留，原 query/hash 由前端 redirect 函数合并——无死链（spec §5.1 迁移对照）。
//   - 没有真实数据源的新入口不提前上线：平台诊断（无 API）不上；资源广场有
//     GET /api/admin/ai/marketplace 真实数据源故上线；工作流/泳道/告警事件 tab 留后续批次。
//   - 个人中心不入侧栏（ShowMenu:false），由右上角用户下拉入口跳转。
//
// 平台角色可见性：叶子节点按 menuPerms 登记的平台权限裁剪（见 filterMenus），
// 子项被裁空的域整组隐藏；超级管理员短路可见全部。
func staticMenus() []Menu {
	menus := rawMenus()
	applyMenuPerms(menus)
	return menus
}

// menuPerms 登记「页面路径 → 所需平台权限」。
// 未登记的路径（首页/个人中心）对所有平台角色可见。
// 权限值与 /api/admin 端点策略（identity.PlatformPermissionsFor）保持同域，
// 否则会出现「菜单可见但接口 403」的错配。
var menuPerms = map[string]identity.Permission{
	"/system/tenant":             identity.PermPlatformIdentityRead,
	"/system/user":               identity.PermPlatformIdentityRead,
	"/system/apikey":             identity.PermPlatformIdentityRead,
	"/model":                     identity.PermPlatformResourceRead,
	"/model/:id":                 identity.PermPlatformResourceRead,
	"/provider":                  identity.PermPlatformResourceRead,
	"/resources/ai":              identity.PermPlatformAIRead,
	"/resources/agent-runs":      identity.PermPlatformAIRead,
	"/resources/marketplace":     identity.PermPlatformAIRead,
	"/resources/applications":    identity.PermPlatformResourceRead,
	"/resources/app-members":     identity.PermPlatformIdentityRead,
	"/resources/workloads":       identity.PermPlatformResourceRead,
	"/resources/pipelineruns":    identity.PermPlatformResourceRead,
	"/pipeline-template":         identity.PermPlatformResourceRead,
	"/resources/dataservices":    identity.PermPlatformResourceRead,
	"/resources/environments":    identity.PermPlatformResourceRead,
	"/resources/namespaces":      identity.PermPlatformResourceRead,
	"/resources/backups":         identity.PermPlatformResourceRead,
	"/resources/services":        identity.PermPlatformResourceRead,
	"/resources/quotas":          identity.PermPlatformFinanceRead,
	"/resources/bills":           identity.PermPlatformFinanceRead,
	"/resources/budgets":         identity.PermPlatformFinanceRead,
	"/resources/settlements":     identity.PermPlatformFinanceRead,
	"/resources/secrets":         identity.PermPlatformSecurityRead,
	"/operations/observability":  identity.PermPlatformObservabilityRead,
	"/resources/audit-logs":      identity.PermPlatformSecurityRead,
	"/operations/diagnostics":    identity.PermPlatformObservabilityRead,
	"/operations/agent-diagnose": identity.PermPlatformDiagnostics,
	// 旧链接兼容入口：与目标页同权限，避免「跳过去 403」。
	"/system/role":           identity.PermPlatformIdentityRead,
	"/resources/buildruns":   identity.PermPlatformResourceRead,
	"/resources/images":      identity.PermPlatformResourceRead,
	"/resources/releases":    identity.PermPlatformResourceRead,
	"/resources/engines":     identity.PermPlatformResourceRead,
	"/resources/alert-rules": identity.PermPlatformObservabilityRead,
}

// applyMenuPerms 原地给菜单树补齐 Perm（按路径查表）。
func applyMenuPerms(menus []Menu) {
	for i := range menus {
		if p, ok := menuPerms[menus[i].Path]; ok {
			menus[i].Perm = string(p)
		}
		applyMenuPerms(menus[i].Children)
	}
}

// filterMenus 按平台权限裁剪菜单树：叶子无权限则移除，子项全被移除的域整组隐藏。
// roles 为空时返回原样（未注入角色来源的场景，保持超管语义）。
func filterMenus(menus []Menu, roles []string) []Menu {
	if len(roles) == 0 {
		return menus
	}
	out := make([]Menu, 0, len(menus))
	for _, m := range menus {
		if len(m.Children) > 0 {
			kids := filterMenus(m.Children, roles)
			if len(kids) == 0 {
				continue
			}
			m.Children = kids
			out = append(out, m)
			continue
		}
		if m.Perm == "" || identity.PlatformGrantsAny(roles, identity.Permission(m.Perm)) {
			out = append(out, m)
		}
	}
	return out
}

func rawMenus() []Menu {
	return []Menu{
		{
			Path:      "/",
			Name:      "home",
			Component: "dashboard/views/Home",
			Meta: MenuMeta{Title: "平台总览", Icon: "HomeFilled", ShowMenu: true,
				Keywords: []string{"工作台", "首页", "dashboard"}},
		},
		{
			// 租户与访问：租户/用户/角色/API 密钥的跨租户管理（super_admin 通行）。
			Path: "/access",
			Name: "access",
			Meta: MenuMeta{Title: "租户与访问", Icon: "Lock", ShowMenu: true},
			Children: []Menu{
				{
					Path:      "/system/tenant",
					Name:      "systemTenant",
					Component: "system/tenant/views/List",
					Meta: MenuMeta{Title: "租户管理", Icon: "OfficeBuilding", ShowMenu: true,
						Keywords: []string{"tenant", "组织", "开通"}},
				},
				{
					// 用户与授权：用户 / 内置角色与权限两 tab（spec §5 页内收敛；角色只读）。
					Path:      "/system/user",
					Name:      "systemUser",
					Component: "system/user/views/Index",
					Meta: MenuMeta{Title: "用户与授权", Icon: "UserFilled", ShowMenu: true,
						Keywords: []string{"用户", "角色", "权限", "role"}},
				},
				{
					Path:      "/system/apikey",
					Name:      "systemApikey",
					Component: "system/apikey/views/List",
					Meta: MenuMeta{Title: "API 密钥", Icon: "Key", ShowMenu: true,
						Keywords: []string{"apikey", "凭证", "入站"}},
				},
			},
		},
		{
			// AI 运营：模型供给、资产观测、执行与广场治理（MaaS + AI 编排管理域）。
			Path: "/aiops",
			Name: "aiops",
			Meta: MenuMeta{Title: "AI 运营", Icon: "Cpu", ShowMenu: true},
			Children: []Menu{
				{
					Path:      "/model",
					Name:      "model",
					Component: "model/views/List",
					Meta: MenuMeta{Title: "模型与通道", Icon: "MagicStick", ShowMenu: true,
						Keywords: []string{"模型", "通道", "channel", "推理"}},
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
					Meta: MenuMeta{Title: "供应商", Icon: "Connection", ShowMenu: true,
						Keywords: []string{"vendor", "供应商", "凭证", "出站"}},
				},
				{
					// AI 资产：Agent/技能/工具/知识库/提示词聚合 tab（跨租户只读总览；
					// 工作流管理读取留 D3——spec M08）。
					Path:      "/resources/ai",
					Name:      "resAi",
					Component: "resources/views/AiOverview",
					Meta: MenuMeta{Title: "AI 资产", Icon: "ChatDotRound", ShowMenu: true,
						Keywords: []string{"智能体", "agent", "提示词", "prompt", "技能", "skill", "工具", "知识库"}},
				},
				{
					// 执行记录：外部 AgentRun（含 token 成本汇总）；工作流运行 tab 留 D3（M09）。
					Path:      "/resources/agent-runs",
					Name:      "resAgentRuns",
					Component: "resources/views/AgentRuns",
					Meta: MenuMeta{Title: "执行记录", Icon: "VideoCamera", ShowMenu: true,
						Keywords: []string{"agentrun", "运行", "执行"}},
				},
				{
					// 资源广场：跨租户已发布内容总览（GET /api/admin/ai/marketplace）；
					// 下架治理闭环（影响确认/理由/审计）留 D3（M10）。
					Path:      "/resources/marketplace",
					Name:      "resMarketplace",
					Component: "resources/views/Marketplace",
					Meta: MenuMeta{Title: "资源广场", Icon: "Goods", ShowMenu: true,
						Keywords: []string{"市场", "广场", "marketplace", "安装"}},
				},
			},
		},
		{
			// 应用与交付：产品运行、交付链路与编排模板（跨租户观测视角）。
			Path: "/delivery",
			Name: "delivery",
			Meta: MenuMeta{Title: "应用与交付", Icon: "Ticket", ShowMenu: true},
			Children: []Menu{
				{
					// 产品：application 实体的跨租户展示名（文案「产品（应用）」在页面内首次解释；
					// 领域模型不变——spec §4）。
					Path:      "/resources/applications",
					Name:      "resApplications",
					Component: "resources/views/Applications",
					Meta: MenuMeta{Title: "产品", Icon: "Files", ShowMenu: true,
						Keywords: []string{"应用", "application", "产品"}},
				},
				{
					// 应用成员（隐藏）：应用级权限跨租户观测。兼容入口：无 appId 看全局成员表，
					// 带 appId 由产品详情跳入（spec §5.1）。
					Path:      "/resources/app-members",
					Name:      "resAppMembers",
					Component: "resources/views/AppMembers",
					Meta:      MenuMeta{Title: "应用成员", Icon: "Stamp", Keywords: []string{"成员", "member"}},
				},
				{
					Path:      "/resources/workloads",
					Name:      "resWorkloads",
					Component: "resources/views/Workloads",
					Meta: MenuMeta{Title: "工作负载", Icon: "Monitor", ShowMenu: true,
						Keywords: []string{"workload", "负载", "部署"}},
				},
				{
					// 交付记录：流水线运行 / 构建 / 镜像 / 发布 四 tab（同一故障链不拆断——I06）。
					Path:      "/resources/pipelineruns",
					Name:      "resPipelineRuns",
					Component: "resources/views/DeliveryRecords",
					Meta: MenuMeta{Title: "交付记录", Icon: "VideoPlay", ShowMenu: true,
						Keywords: []string{"流水线", "构建", "镜像", "发布", "pipelinerun", "build", "image", "release"}},
				},
				{
					// 流水线模板：平台级公共模板 CRUD（builtin 拒改删），super_admin 维护，应用绑定用。
					Path:      "/pipeline-template",
					Name:      "pipeline-template",
					Component: "pipeline-template/views/List",
					Meta: MenuMeta{Title: "流水线模板", Icon: "Stopwatch", ShowMenu: true,
						Keywords: []string{"模板", "template", "编排"}},
				},
			},
		},
		{
			// 基础设施：引擎供给、数据服务、环境泳道与横切配置治理。
			Path: "/infra",
			Name: "infra",
			Meta: MenuMeta{Title: "基础设施", Icon: "SetUp", ShowMenu: true},
			Children: []Menu{
				{
					// 数据服务：实例 / 引擎目录两 tab（引擎是平台级供给，spec I04 收敛）。
					Path:      "/resources/dataservices",
					Name:      "resDataservices",
					Component: "resources/views/DataservicesTabs",
					Meta: MenuMeta{Title: "数据服务", Icon: "Coin", ShowMenu: true,
						Keywords: []string{"数据库", "实例", "引擎", "dataservice", "engine"}},
				},
				{
					// 环境与泳道：环境 tab 先行；泳道跨租户视图依赖管理 API（M17，留后续）。
					Path:      "/resources/environments",
					Name:      "resEnvironments",
					Component: "resources/views/EnvironmentsTabs",
					Meta: MenuMeta{Title: "环境与泳道", Icon: "Location", ShowMenu: true,
						Keywords: []string{"环境", "泳道", "lane", "environment"}},
				},
				{
					// 配置中心：admin 视角当前为命名空间总览（副标题注明覆盖范围，spec §5.1）。
					Path:      "/resources/namespaces",
					Name:      "resNamespaces",
					Component: "resources/views/Namespaces",
					Meta: MenuMeta{Title: "配置中心", Icon: "FolderOpened", ShowMenu: true,
						Keywords: []string{"配置", "命名空间", "namespace", "config"}},
				},
				{
					// 备份与恢复：跨租户记录总览（真实产物 + 恢复作业链只读；record 模式无恢复能力，
					// snapshot 校验时间 ≠ 恢复演练通过，页面内显式标注）。
					Path:      "/resources/backups",
					Name:      "resBackups",
					Component: "resources/views/Backups",
					Meta: MenuMeta{Title: "备份与恢复", Icon: "FolderChecked", ShowMenu: true,
						Keywords: []string{"备份", "恢复", "backup", "restore", "快照"}},
				},
				{
					// 流量治理：注册服务与实例（副标题注明当前覆盖；路由/熔断管理视图留 D3，M19）。
					Path:      "/resources/services",
					Name:      "resServices",
					Component: "resources/views/Services",
					Meta: MenuMeta{Title: "流量治理", Icon: "Share", ShowMenu: true,
						Keywords: []string{"服务", "治理", "路由", "熔断", "service"}},
				},
			},
		},
		{
			// 用量与账单：消耗归因与财务核对（估算口径在页面内明确，不与真实结算混淆）。
			Path: "/usage",
			Name: "usage",
			Meta: MenuMeta{Title: "用量与账单", Icon: "Wallet", ShowMenu: true},
			Children: []Menu{
				{
					Path:      "/resources/quotas",
					Name:      "resQuotas",
					Component: "resources/views/UsageQuotasTabs",
					Meta: MenuMeta{Title: "用量与配额", Icon: "Odometer", ShowMenu: true,
						Keywords: []string{"配额", "quota", "用量"}},
				},
				{
					Path:      "/resources/bills",
					Name:      "resBills",
					Component: "resources/views/Bills",
					Meta: MenuMeta{Title: "账单", Icon: "CreditCard", ShowMenu: true,
						Keywords: []string{"账单", "bill", "结算"}},
				},
				{
					// 月度预算：费用上限（估算口径）+ 超限强制拒绝推理；不是结算账单。
					Path:      "/resources/budgets",
					Name:      "resBudgets",
					Component: "resources/views/Budgets",
					Meta: MenuMeta{Title: "预算", Icon: "Money", ShowMenu: true,
						Keywords: []string{"预算", "budget", "限额", "费用"}},
				},
				{
					// 供应商对账：平台估算 vs 人工登记账单（人工登记不代表已付款）。
					Path:      "/resources/settlements",
					Name:      "resSettlements",
					Component: "resources/views/Settlements",
					Meta: MenuMeta{Title: "供应商对账", Icon: "Tickets", ShowMenu: true,
						Keywords: []string{"对账", "结算", "供应商", "settlement", "vendor"}},
				},
			},
		},
		{
			// 安全与运维：凭证、可观测、审计与平台诊断（合规排障域）。
			Path: "/secops",
			Name: "secops",
			Meta: MenuMeta{Title: "安全与运维", Icon: "Warning", ShowMenu: true},
			Children: []Menu{
				{
					// 凭证与证书：平台共享与租户范围分 tab（与用户端入站 API 密钥严格分开——I04）。
					Path:      "/resources/secrets",
					Name:      "resSecrets",
					Component: "resources/views/Secrets",
					Meta: MenuMeta{Title: "凭证与证书", Icon: "DocumentCopy", ShowMenu: true,
						Keywords: []string{"密钥", "secret", "证书", "凭证"}},
				},
				{
					// 可观测：告警规则 tab 先行；当前告警/事件/指标/日志/trace 跨租户视图留 D3（M20）。
					Path:      "/operations/observability",
					Name:      "opsObservability",
					Component: "operations/views/Observability",
					Meta: MenuMeta{Title: "可观测", Icon: "Bell", ShowMenu: true,
						Keywords: []string{"告警", "监控", "observability", "alert"}},
				},
				{
					Path:      "/resources/audit-logs",
					Name:      "resAuditLogs",
					Component: "resources/views/AuditLogs",
					Meta: MenuMeta{Title: "审计日志", Icon: "Document", ShowMenu: true,
						Keywords: []string{"审计", "audit", "操作记录"}},
				},
				{
					// 平台诊断：只读（版本/部署/存储迁移/依赖可达性/RBAC/安全配置状态），
					// 漂移检查在「工作负载 → Drift 对账」，不在此页触发写入。
					Path:      "/operations/diagnostics",
					Name:      "opsDiagnostics",
					Component: "operations/views/Diagnostics",
					Meta: MenuMeta{Title: "平台诊断", Icon: "FirstAidKit", ShowMenu: true,
						Keywords: []string{"诊断", "版本", "依赖", "迁移", "diagnostics", "health"}},
				},
				{
					// 智能诊断：LLM Agent 动态根因分析（internal/diagnose，2026-10-27），
					// 与「平台诊断」（internal/diagnostics 静态自检）是两回事——
					// 本页触发跨全 ns 只读证据采集，同步返回结构化假设/证据/建议。
					Path:      "/operations/agent-diagnose",
					Name:      "opsAgentDiagnose",
					Component: "operations/views/AgentDiagnose",
					Meta: MenuMeta{Title: "智能诊断", Icon: "MagicStick", ShowMenu: true,
						Keywords: []string{"智能诊断", "根因", "agent", "diagnose", "AI"}},
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
		{
			// —— 旧链接兼容（隐藏 redirect，spec §5.1 迁移对照；原 query/hash 由前端保留）——
			Path:     "/system/role",
			Name:     "legacyRole",
			Redirect: "/system/user?tab=roles",
			Meta:     MenuMeta{Title: "角色管理（已并入用户与授权）", Icon: "Avatar"},
		},
		{
			Path:     "/resources/buildruns",
			Name:     "legacyBuildruns",
			Redirect: "/resources/pipelineruns?tab=builds",
			Meta:     MenuMeta{Title: "构建（已并入交付记录）", Icon: "Tools"},
		},
		{
			Path:     "/resources/images",
			Name:     "legacyImages",
			Redirect: "/resources/pipelineruns?tab=images",
			Meta:     MenuMeta{Title: "镜像（已并入交付记录）", Icon: "Box"},
		},
		{
			Path:     "/resources/releases",
			Name:     "legacyReleases",
			Redirect: "/resources/pipelineruns?tab=releases",
			Meta:     MenuMeta{Title: "发布（已并入交付记录）", Icon: "Promotion"},
		},
		{
			Path:     "/resources/engines",
			Name:     "legacyEngines",
			Redirect: "/resources/dataservices?tab=engines",
			Meta:     MenuMeta{Title: "引擎目录（已并入数据服务）", Icon: "Grid"},
		},
		{
			Path:     "/resources/alert-rules",
			Name:     "legacyAlertRules",
			Redirect: "/operations/observability?tab=rules",
			Meta:     MenuMeta{Title: "告警规则（已并入可观测）", Icon: "Bell"},
		},
	}
}
