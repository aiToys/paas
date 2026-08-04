// system/role 领域 API。
// 迁移自 src/apis/role/index.ts。
// 统一走 @/lib/http/client 的 api 辅助函数（已解包 data）。
import { api } from '@/lib/http/client'

// 定义角色类型
export interface RoleInfo {
  id: string
  name: string
  code: string
  description: string
  status: 'active' | 'inactive'
  createTime: string
  updateTime: string
}

// 搜索参数类型
export interface RoleSearchRequest {
  keyword: string
  status: string
  page: number
  size: number
}

// 创建角色参数类型
export interface RoleCreateRequest {
  name: string
  code: string
  description?: string
  status: 'active' | 'inactive'
}

// 角色列表响应
export interface RoleSearchResponse {
  records: RoleInfo[]
  total: number
  current: number
  size: number
}

// 内置角色中文名映射（core BuiltinRoles 固定三角色）。
const ROLE_LABELS: Record<string, string> = {
  'tenant-admin': '租户管理员',
  developer: '开发者',
  viewer: '观察者'
}

// core /api/roles 返回项。
interface CoreRole {
  name: string
  permissions: string[]
}

const mapRole = (r: CoreRole): RoleInfo => ({
  id: r.name,
  name: ROLE_LABELS[r.name] ?? r.name,
  code: r.name,
  description: r.permissions.join(', '),
  status: 'active',
  createTime: '',
  updateTime: ''
})

// 获取角色列表（对接 core GET /api/roles；内置角色只读，假分页适配 admin 期望）。
export const fetchRoleList = async (params: RoleSearchRequest): Promise<RoleSearchResponse> => {
  const list = await api.get<CoreRole[]>('/api/admin/roles')
  let mapped = (list ?? []).map(mapRole)
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (r) => r.name.toLowerCase().includes(kw) || r.code.toLowerCase().includes(kw)
    )
  }
  return { records: mapped, total: mapped.length, current: params.page, size: params.size }
}

// 获取角色详情（本地从列表派生，core 无单角色端点）。
export const fetchRoleDetail = async (id: string): Promise<RoleInfo> => {
  const list = await api.get<CoreRole[]>('/api/admin/roles')
  const r = (list ?? []).find((x) => x.name === id)
  if (!r) throw new Error('角色不存在')
  return mapRole(r)
}

// 内置角色不可创建/修改/删除（core BuiltinRoles 固定）。保留函数签名以兼容 useCrud，抛明确错误。
const unsupported = (op: string) => Promise.reject(new Error(`内置角色不支持${op}`))

export const createRole = (_data: RoleCreateRequest) => unsupported('创建')
export const updateRole = (_id: string, _data: Partial<RoleCreateRequest>) => unsupported('修改')
export const deleteRole = (_id: string) => unsupported('删除')
export const batchDeleteRoles = (_ids: string[]) => unsupported('批量删除')

// 导出角色列表（CSV 本地生成）。
export const exportRoles = async (): Promise<string> => {
  const list = await api.get<CoreRole[]>('/api/admin/roles')
  const rows = (list ?? []).map((r) => mapRole(r))
  const header = 'id,name,code,description,status'
  const lines = rows.map((r) => [r.id, r.name, r.code, `"${r.description}"`, r.status].join(','))
  return [header, ...lines].join('\n')
}

// 获取角色权限（从列表派生）。
export const fetchRolePermissions = async (roleId: string): Promise<string[]> => {
  const list = await api.get<CoreRole[]>('/api/admin/roles')
  return (list ?? []).find((x) => x.name === roleId)?.permissions ?? []
}

// 设置角色权限（内置角色只读）。
export const setRolePermissions = (_roleId: string, _permissions: string[]) => unsupported('权限修改')
