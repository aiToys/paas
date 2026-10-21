// 路由元信息契约（对象语法消除 any/all 歧义）
export {}

declare module 'vue-router' {
  interface RouteMeta {
	title?: string
    icon?: string
    showMenu?: boolean
    showInBreadcrumb?: boolean
    public?: boolean // 免登录（白名单）
    cache?: boolean // 参与 KeepAlive
    keywords?: string[] // 菜单搜索/别名元数据（俗称与旧名，与后端菜单真源同源下发）
    permissions?: {
      any?: string[]
      all?: string[]
    }
    roles?: {
      any?: string[]
      all?: string[]
    }
  }
}
