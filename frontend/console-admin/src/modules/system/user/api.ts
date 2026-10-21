// system/user 领域 API。
// 迁移自 src/apis/user/index.ts + src/apis/user/info.ts。
// 统一走 @/lib/http/client 的 api 辅助函数（已解包 data）。
import { api } from '@/lib/http/client'

// 用户信息接口
export interface UserInfo {
  id: string
  tenantId: string
  username: string
  realName: string
  email: string
  phone: string
  // 角色 code 集合，与登录态 UserProfile.roles 对齐（多角色 RBAC）
  roles: string[]
  // 平台超管标记（core User.IsAdmin）。与 roles 独立：超管在 core 侧映射为 super_admin，
  // 不由 roles 推导——曾用 roles.includes('tenant-admin') 近似，导致编辑超管时被静默降权。
  isAdmin: boolean
  status: 'active' | 'inactive'
  avatar: string
  createTime: string
  lastLoginTime: string
  loginCount: number
}

// 用户搜索请求接口
export interface UserSearchRequest {
  keyword?: string
  role?: string
  status?: string
  page: number
  size: number
}

// 用户搜索响应接口
export interface UserSearchResponse {
  records: UserInfo[]
  total: number
  current: number
  size: number
}

// 用户创建/更新请求接口
export interface UserCreateRequest {
  username: string
  tenantId: string
  realName: string
  email: string
  phone: string
  roles: string[]
  /** 平台超管（super_admin）。编辑时必须回传原值，否则会把超管降权。 */
  isAdmin?: boolean
  status: 'active' | 'inactive'
  password?: string
}

// core /api/admin/users 返回项（小驼峰；passwordHash 已 json:"-" 不回传）。
interface CoreUser {
  id: string
  tenantId: string
  name: string
  email?: string
  isAdmin: boolean
  roles: string[]
  status?: string
  createdAt?: string
}

// core status('active'|'disabled') → admin status('active'|'inactive')。
const toAdminStatus = (s?: string): 'active' | 'inactive' =>
  s === 'disabled' ? 'inactive' : 'active'

const mapUser = (u: CoreUser): UserInfo => ({
  id: u.id,
  tenantId: u.tenantId,
  username: u.name,
  realName: u.name,
  email: u.email ?? '',
  phone: '',
  roles: u.roles ?? [],
  isAdmin: u.isAdmin ?? false,
  status: toAdminStatus(u.status),
  avatar: '',
  createTime: u.createdAt ?? '',
  lastLoginTime: '',
  loginCount: 0
})

// 创建用户 body（对接 core POST /api/admin/users）。
interface CoreUserCreate {
  id: string
  tenantId: string
  name: string
  email?: string
  password?: string
  roles: string[]
  isAdmin: boolean
  status: string
}

// 获取用户列表（对接 core GET /api/admin/users；假分页 + keyword 过滤）。
export const fetchUserList = async (params: UserSearchRequest): Promise<UserSearchResponse> => {
  const list = await api.get<CoreUser[]>('/api/admin/users')
  let mapped = (list ?? []).map(mapUser)
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (u) => u.username.toLowerCase().includes(kw) || u.email.toLowerCase().includes(kw)
    )
  }
  if (params.role) {
    mapped = mapped.filter((u) => u.roles.includes(params.role!))
  }
  if (params.status) {
    mapped = mapped.filter((u) => u.status === params.status)
  }
  return { records: mapped, total: mapped.length, current: params.page, size: params.size }
}

// 获取用户详情（从列表派生，core 无单用户 GET）。
// 创建用户（对接 core POST /api/admin/users；id 由 username 派生，默认租户 t-acme）。
export const createUser = async (data: UserCreateRequest): Promise<UserInfo> => {
  const body: CoreUserCreate = {
    id: `u-${data.username}`,
    tenantId: data.tenantId,
    name: data.username,
    email: data.email,
    password: data.password,
    roles: data.roles,
    isAdmin: data.isAdmin ?? false,
    status: data.status === 'inactive' ? 'disabled' : 'active'
  }
  await api.post('/api/admin/users', body)
  return mapUser({ ...body, name: data.username })
}

// 更新用户（对接 core PUT /api/admin/users/{id}）。
export const updateUser = async (id: string, data: Partial<UserCreateRequest>): Promise<UserInfo> => {
  const body: Partial<CoreUserCreate> = {
    name: data.username,
    email: data.email,
    roles: data.roles,
    isAdmin: data.isAdmin ?? false,
    status: data.status === 'inactive' ? 'disabled' : 'active',
    password: data.password
  }
  await api.put(`/api/admin/users/${id}`, body)
  // 显式构造返回对象，避免 data.status 联合类型覆盖 / password 泄到返回对象。
  return {
    id,
    username: data.username ?? '',
    tenantId: '',
    realName: data.username ?? '',
    email: data.email ?? '',
    phone: '',
    avatar: '',
    roles: data.roles ?? [],
    isAdmin: data.isAdmin ?? false,
    status: data.status ?? 'active',
    createTime: '',
    lastLoginTime: '',
    loginCount: 0
  }
}

// 删除用户（对接 core DELETE /api/admin/users/{id}）。
export const deleteUser = async (id: string): Promise<boolean> => {
  await api.del(`/api/admin/users/${id}`)
  return true
}

// 撤销该用户已签发的全部会话（core session_version+1）。
// 生效边界：网关有 ≤30s 的代次缓存窗口，access token 最长 15min；refresh 续期立即拒绝。
export const revokeUserSessions = async (id: string): Promise<number> => {
  const res = await api.post<{ userId: string; sessionVersion: number }>(
    `/api/admin/users/${id}/sessions/revoke`,
    {}
  )
  return res?.sessionVersion ?? 0
}

// 批量删除用户（逐个调 core）。
export const batchDeleteUsers = async (ids: string[]): Promise<boolean> => {
  await Promise.all(ids.map((id) => api.del(`/api/admin/users/${id}`)))
  return true
}

// 导出用户列表（CSV 本地生成）。
