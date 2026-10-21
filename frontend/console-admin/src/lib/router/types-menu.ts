import type { RouteMeta } from 'vue-router'

// 后端下发的菜单结构（用于动态路由装载）
export interface MenuDTO {
  path: string
  name: string
  component?: string // 文件路径（相对 src/modules/，不含扩展名）
  redirect?: string // 兼容跳转目标（"path?k=v"）：注册为重定向路由，不装载组件
  meta?: RouteMeta
  children?: MenuDTO[]
}
