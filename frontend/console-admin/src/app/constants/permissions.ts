/**
 * 权限码集中常量。
 *
 * 用途：消除散落在组件 / mock / 路由中的字符串字面量，避免拼写错误与重构遗漏。
 * 约定：`<domain>:<action>` 形式；通配符 `*` 表示超级管理员短路。
 *
 * 使用示例：
 * ```ts
 * import { Permissions } from '@/app/constants/permissions'
 * meta: { permissions: { any: [Permissions.USER_READ, '*'] } }
 * v-permission="[Permissions.USER_READ]"
 * ```
 */
export const Permissions = {
  USER_READ: 'user:read',
  USER_WRITE: 'user:write',
  ROLE_READ: 'role:read',
  ROLE_WRITE: 'role:write',
} as const

// 超级管理员通配符
export const PERMISSION_WILDCARD = '*'

export type PermissionCode =
  | (typeof Permissions)[keyof typeof Permissions]
  | typeof PERMISSION_WILDCARD
