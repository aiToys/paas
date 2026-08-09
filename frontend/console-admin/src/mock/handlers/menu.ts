import { http } from 'msw'
import { ok } from './_utils'

// 全部菜单（mock 演示用；生产由后端 internal/core/auth/menus.go 的 staticMenus() 下发）。
// 与后端保持结构一致：工作台 / 身份与权限 / 推理服务 / 资源总览（含 4 业务子分组）。
// 修改菜单时务必同步 menus.go，避免 dev 与生产菜单漂移。
export const ALL_MENUS = [
  {
    path: '/',
    name: 'home',
    component: 'dashboard/views/Home',
    meta: { title: '工作台', icon: 'HomeFilled', showMenu: true }
  },
  {
    path: '/system',
    name: 'system',
    meta: { title: '身份与权限', icon: 'Lock', showMenu: true },
    children: [
      {
        path: '/system/tenant',
        name: 'systemTenant',
        component: 'system/tenant/views/List',
        meta: { title: '租户管理', icon: 'OfficeBuilding', showMenu: true }
      },
      {
        path: '/system/user',
        name: 'systemUser',
        component: 'system/user/views/List',
        meta: { title: '用户管理', icon: 'User', showMenu: true }
      },
      {
        path: '/system/role',
        name: 'systemRole',
        component: 'system/role/views/List',
        meta: { title: '角色管理', icon: 'Avatar', showMenu: true }
      },
      {
        path: '/system/apikey',
        name: 'systemApikey',
        component: 'system/apikey/views/List',
        meta: { title: 'API 密钥', icon: 'Key', showMenu: true }
      }
    ]
  },
  {
    path: '/maas',
    name: 'maas',
    meta: { title: '推理服务', icon: 'Cpu', showMenu: true },
    children: [
      {
        path: '/model',
        name: 'model',
        component: 'model/views/List',
        meta: { title: '模型管理', icon: 'MagicStick', showMenu: true }
      },
      {
        path: '/model/:id',
        name: 'modelDetail',
        component: 'model/views/Detail',
        meta: { title: '模型详情', icon: 'Cpu', showMenu: false }
      },
      {
        path: '/provider',
        name: 'provider',
        component: 'provider/views/List',
        meta: { title: '供应商管理', icon: 'Connection', showMenu: true }
      }
    ]
  },
  {
    path: '/resources',
    name: 'resources',
    meta: { title: '资源总览', icon: 'Grid', showMenu: true },
    children: [
      {
        path: '/resources/runtime',
        name: 'resRuntime',
        meta: { title: '应用运行态', icon: 'Menu', showMenu: true },
        children: [
          {
            path: '/resources/applications',
            name: 'resApplications',
            component: 'resources/views/Applications',
            meta: { title: '应用', icon: 'Files', showMenu: true }
          },
          {
            path: '/resources/workloads',
            name: 'resWorkloads',
            component: 'resources/views/Workloads',
            meta: { title: '工作负载', icon: 'Monitor', showMenu: true }
          },
          {
            path: '/resources/dataservices',
            name: 'resDataservices',
            component: 'resources/views/Dataservices',
            meta: { title: '数据服务', icon: 'Coin', showMenu: true }
          },
          {
            path: '/resources/environments',
            name: 'resEnvironments',
            component: 'resources/views/Environments',
            meta: { title: '环境', icon: 'Location', showMenu: true }
          }
        ]
      },
      {
        path: '/resources/cicd',
        name: 'resCicd',
        meta: { title: 'DevOps 链路', icon: 'Ticket', showMenu: true },
        children: [
          {
            path: '/resources/buildruns',
            name: 'resBuildRuns',
            component: 'resources/views/BuildRuns',
            meta: { title: '构建', icon: 'Tools', showMenu: true }
          },
          {
            path: '/resources/images',
            name: 'resImages',
            component: 'resources/views/Images',
            meta: { title: '镜像', icon: 'Box', showMenu: true }
          },
          {
            path: '/resources/releases',
            name: 'resReleases',
            component: 'resources/views/Releases',
            meta: { title: '发布', icon: 'Promotion', showMenu: true }
          },
          {
            path: '/pipeline-template',
            name: 'pipeline-template',
            component: 'pipeline-template/views/List',
            meta: { title: '流水线模板', icon: 'Stopwatch', showMenu: true }
          }
        ]
      },
      {
        path: '/resources/platform',
        name: 'resPlatform',
        meta: { title: '平台能力', icon: 'SetUp', showMenu: true },
        children: [
          {
            path: '/resources/namespaces',
            name: 'resNamespaces',
            component: 'resources/views/Namespaces',
            meta: { title: '配置中心', icon: 'FolderOpened', showMenu: true }
          },
          {
            path: '/resources/services',
            name: 'resServices',
            component: 'resources/views/Services',
            meta: { title: '服务治理', icon: 'Share', showMenu: true }
          },
          {
            path: '/resources/alert-rules',
            name: 'resAlertRules',
            component: 'resources/views/AlertRules',
            meta: { title: '告警规则', icon: 'Bell', showMenu: true }
          },
          {
            path: '/resources/secrets',
            name: 'resSecrets',
            component: 'resources/views/Secrets',
            meta: { title: '密钥', icon: 'DocumentCopy', showMenu: true }
          }
        ]
      },
      {
        path: '/resources/billing',
        name: 'resBilling',
        meta: { title: '计费审计', icon: 'Wallet', showMenu: true },
        children: [
          {
            path: '/resources/quotas',
            name: 'resQuotas',
            component: 'resources/views/Quotas',
            meta: { title: '配额', icon: 'Odometer', showMenu: true }
          },
          {
            path: '/resources/bills',
            name: 'resBills',
            component: 'resources/views/Bills',
            meta: { title: '账单', icon: 'CreditCard', showMenu: true }
          },
          {
            path: '/resources/audit-logs',
            name: 'resAuditLogs',
            component: 'resources/views/AuditLogs',
            meta: { title: '审计日志', icon: 'Document', showMenu: true }
          }
        ]
      }
    ]
  }
]

export const menuHandlers = [
  http.get('/api/system/menus', ({ request }) => {
    const auth = request.headers.get('authorization') ?? ''
    const token = auth.replace(/^Bearer\s+/, '')
    // 简化：admin token 形如 a_admin_<ts>_<rand>，含 _admin_ 字段
    const isAdmin = token.includes('_admin_')
    const data = isAdmin ? ALL_MENUS : [ALL_MENUS[0]]
    return ok(data, 'ok')
  })
]
