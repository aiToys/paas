// system/apikey 领域 API。
// 对接 core /api/admin/api-keys（平台运维域，super_admin 跨租户视图）。
import { api } from '@/lib/http/client'
import { fetchAllTenants, type TenantInfo } from '@/modules/system/tenant/api'

// 密钥状态（后端派生，前端不重复实现「7 天内到期」规则）。
export type ApiKeyState = 'active' | 'expiring' | 'expired' | 'revoked'

// 列表项（key 已掩码；创建时后端返明文一次，见 createApiKey）。
export interface ApiKeyInfo {
  id: string
  tenantId: string
  userId: string
  /** 应用级 Key：用量归因到该应用；空=租户级 */
  appId?: string
  roles: string[]
  key: string
  state?: ApiKeyState
  expiresAt?: string
  lastUsedAt?: string
  revokedAt?: string
  rotatedFrom?: string
  createdAt?: string
}

export interface ApiKeySearchRequest {
  keyword?: string
  tenantId?: string
  page: number
  size: number
}

export interface ApiKeySearchResponse {
  records: ApiKeyInfo[]
  total: number
  current: number
  size: number
}

// core /api/admin/api-keys 返回项（小驼峰；列表 key 掩码）。
interface CoreApiKey {
  id: string
  tenantId: string
  userId: string
  appId?: string
  roles: string[]
  key: string
  state?: ApiKeyState
  expiresAt?: string
  lastUsedAt?: string
  revokedAt?: string
  rotatedFrom?: string
  createdAt?: string
}

const mapKey = (k: CoreApiKey): ApiKeyInfo => ({
  id: k.id,
  tenantId: k.tenantId,
  userId: k.userId,
  appId: k.appId ?? '',
  roles: k.roles ?? [],
  key: k.key,
  state: k.state,
  expiresAt: k.expiresAt,
  lastUsedAt: k.lastUsedAt,
  revokedAt: k.revokedAt,
  rotatedFrom: k.rotatedFrom,
  createdAt: k.createdAt
})

// 创建 body（对接 core POST /api/admin/api-keys；super_admin 显式指定 tenant/user/roles）。
export interface ApiKeyCreateRequest {
  tenantId: string
  userId: string
  roles: string[]
}

// 可选角色（与 identity.BuiltinRoles 对齐）。
export const ROLE_OPTIONS = [
  { label: '租户管理员 (tenant-admin)', value: 'tenant-admin' },
  { label: '开发者 (developer)', value: 'developer' },
  { label: '只读 (viewer)', value: 'viewer' }
]

// 获取 API Key 列表（对接 core GET /api/admin/api-keys；假分页 + keyword/tenantId 过滤）。
export const fetchApiKeyList = async (
  params: ApiKeySearchRequest
): Promise<ApiKeySearchResponse> => {
  const list = await api.get<CoreApiKey[]>('/api/admin/api-keys')
  let mapped = (list ?? []).map(mapKey)
  if (params.tenantId) {
    mapped = mapped.filter((k) => k.tenantId === params.tenantId)
  }
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (k) =>
        k.id.toLowerCase().includes(kw) ||
        k.userId.toLowerCase().includes(kw) ||
        k.tenantId.toLowerCase().includes(kw)
    )
  }
  return { records: mapped, total: mapped.length, current: params.page, size: params.size }
}

// 获取全部租户（复用 tenant 模块，供创建表单「归属租户」下拉）。
export { fetchAllTenants }
export type { TenantInfo }

// 创建 API Key（对接 core POST /api/admin/api-keys；返明文一次，仅此一次可见）。
export const createApiKey = async (data: ApiKeyCreateRequest): Promise<ApiKeyInfo> => {
  const k = await api.post<CoreApiKey>('/api/admin/api-keys', data)
  return mapKey(k)
}

// 删除 API Key（对接 core DELETE /api/admin/api-keys/{id}）。
export const deleteApiKey = async (id: string): Promise<boolean> => {
  await api.del(`/api/admin/api-keys/${id}`)
  return true
}

// 吊销（软吊销）：保留记录供审计，与删除（移除记录）语义不同。幂等。
export const revokeApiKey = async (id: string): Promise<ApiKeyInfo> => {
  const k = await api.post<CoreApiKey>(`/api/admin/api-keys/${id}/revoke`, {})
  return mapKey(k)
}

// 设置/取消到期时间（expiresAt 为空串 = 永不过期）。后端拒绝过去时间（应走吊销）。
export const setApiKeyExpiry = async (id: string, expiresAt: string): Promise<ApiKeyInfo> => {
  const k = await api.put<CoreApiKey>(`/api/admin/api-keys/${id}/expiry`, { expiresAt })
  return mapKey(k)
}

// 轮换：新建同权限 Key（明文只返一次）并立即吊销旧 Key。
export const rotateApiKey = async (id: string, expiresAt?: string): Promise<ApiKeyInfo> => {
  const k = await api.post<CoreApiKey>(`/api/admin/api-keys/${id}/rotate`, { expiresAt: expiresAt ?? '' })
  return mapKey(k)
}
