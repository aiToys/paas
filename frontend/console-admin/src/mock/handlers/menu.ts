import { http } from 'msw'
import { ok } from './_utils'

// 全部菜单（mock 演示用；生产由后端 auth/menus.go 下发）。
// 保留基座最小演示结构（首页 + 访问控制下的用户/角色）；
// 其余基座演示模块（部门/字典/权限/菜单/公告/日志）已随开源打磨清理移除。
export const ALL_MENUS = [
  {
    path: '/',
    name: 'home',
    component: 'dashboard/views/Home',
    meta: { title: '首页', icon: 'HomeFilled', showMenu: true }
  },
  {
    path: '/system',
    name: 'system',
    meta: { title: '系统管理', icon: 'Setting', showMenu: true },
    children: [
      {
        path: '/system/access',
        name: 'systemAccess',
        meta: {
          title: '访问控制',
          icon: 'UserFilled',
          showMenu: true
        },
        children: [
          {
            path: '/system/user',
            name: 'systemUser',
            component: 'system/user/views/List',
            meta: {
              title: '用户管理',
              icon: 'User',
              showMenu: true,
              permissions: { any: ['user:read', '*'] }
            }
          },
          {
            path: '/system/role',
            name: 'systemRole',
            component: 'system/role/views/List',
            meta: {
              title: '角色管理',
              icon: 'Avatar',
              showMenu: true,
              permissions: { any: ['role:read', '*'] }
            }
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
