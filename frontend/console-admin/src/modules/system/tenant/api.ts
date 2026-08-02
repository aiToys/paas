// system/tenant 领域 API。
// 对接 core /api/admin/tenants（平台运维域，super_admin 可访问；普通 tenant-admin 受限本租户）。
import { api } from '@/lib/http/client'

export interface TenantInfo {
  id: string
  name: string
  createdAt?: string
}

export interface TenantCreateRequest {
  id: string
  name: string
}

export interface TenantSearchRequest {
  keyword?: string
  page: number
  size: number
}

export interface TenantSearchResponse {
  records: TenantInfo[]
  total: number
  current: number
  size: number
}

// core /api/admin/tenants 返回项（小驼峰）。
interface CoreTenant {
  id: string
  name: string
  createdAt?: string
}

const mapTenant = (t: CoreTenant): TenantInfo => ({
  id: t.id,
  name: t.name,
  createdAt: t.createdAt
})

// 获取租户列表（对接 core GET /api/admin/tenants；假分页 + keyword 过滤，适配 useCrud）。
export const fetchTenantList = async (
  params: TenantSearchRequest
): Promise<TenantSearchResponse> => {
  const list = await api.get<CoreTenant[]>('/api/admin/tenants')
  let mapped = (list ?? []).map(mapTenant)
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (t) => t.id.toLowerCase().includes(kw) || t.name.toLowerCase().includes(kw)
    )
  }
  return { records: mapped, total: mapped.length, current: params.page, size: params.size }
}

// 获取全部租户（不分页，供用户表单「所属租户」下拉用）。
export const fetchAllTenants = async (): Promise<TenantInfo[]> => {
  const list = await api.get<CoreTenant[]>('/api/admin/tenants')
  return (list ?? []).map(mapTenant)
}

// 创建租户（对接 core POST /api/admin/tenants；id+name 必填，id 全局唯一）。
export const createTenant = async (data: TenantCreateRequest): Promise<TenantInfo> => {
  const t = await api.post<CoreTenant>('/api/admin/tenants', data)
  return mapTenant(t)
}

// 删除租户（对接 core DELETE /api/admin/tenants/{id}；租户下有用户时 core 返 409 引导先清用户）。
export const deleteTenant = async (id: string): Promise<boolean> => {
  await api.del(`/api/admin/tenants/${id}`)
  return true
}
