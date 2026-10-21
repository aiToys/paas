// system/tenant 领域 API。
// 对接 core /api/admin/tenants（平台运维域，super_admin 可访问；普通 tenant-admin 受限本租户）。
import { api } from '@/lib/http/client'

export interface TenantInfo {
  id: string
  name: string
  status?: 'active' | 'frozen'
  frozenAt?: string
  frozenReason?: string
  createdAt?: string
}

// 注销预检项：blocking=true 表示该项存在时删除租户会被拒（当前仅「用户」）。
export interface OffboardingItem {
  kind: string
  label: string
  count: number
  blocking: boolean
  note?: string
}

export interface OffboardingReport {
  tenantId: string
  status: string
  items: OffboardingItem[]
  blocking: boolean
  notes: string[]
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
  status?: 'active' | 'frozen'
  frozenAt?: string
  frozenReason?: string
  createdAt?: string
}

const mapTenant = (t: CoreTenant): TenantInfo => ({
  id: t.id,
  name: t.name,
  status: t.status ?? 'active',
  frozenAt: t.frozenAt,
  frozenReason: t.frozenReason,
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

// 冻结租户（core POST /api/admin/tenants/{id}/freeze）：只关闭控制面准入（登录 / API Key），
// 数据面已部署业务继续运行。reason 必填（后台操作需可追溯）。
export const freezeTenant = async (id: string, reason: string): Promise<TenantInfo> => {
  const t = await api.post<CoreTenant>(`/api/admin/tenants/${id}/freeze`, { reason })
  return mapTenant(t)
}

// 恢复租户：重新开放控制面准入。
export const unfreezeTenant = async (id: string, reason?: string): Promise<TenantInfo> => {
  const t = await api.post<CoreTenant>(`/api/admin/tenants/${id}/unfreeze`, { reason })
  return mapTenant(t)
}

// 注销预检（只读）：列出删除租户前需处理的资源与阻塞项，不触发任何删除。
export const fetchTenantOffboarding = async (id: string): Promise<OffboardingReport> =>
  api.get<OffboardingReport>(`/api/admin/tenants/${id}/offboarding`)

// 删除租户（对接 core DELETE /api/admin/tenants/{id}；租户下有用户时 core 返 409 引导先清用户）。
export const deleteTenant = async (id: string): Promise<boolean> => {
  await api.del(`/api/admin/tenants/${id}`)
  return true
}
