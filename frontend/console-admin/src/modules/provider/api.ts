// 供应商管理 API（复用 model/api.ts 的 Vendor CRUD，避免重复定义）。
// 对接 core /api/admin/providers（平台级，super_admin 由 adminGuard 兜底）。
import { api } from '@/lib/http/client'
import {
  fetchVendorList as _fetchVendorList,
  createVendor as _createVendor,
  updateVendor as _updateVendor,
  deleteVendor as _deleteVendor,
  PROVIDER_TYPES,
  type Vendor
} from '@/modules/model/api'

export { PROVIDER_TYPES, type Vendor }
export const fetchVendorList = _fetchVendorList
export const createVendor = _createVendor
export const updateVendor = _updateVendor
export const deleteVendor = _deleteVendor

// 平台级密钥选项（供应商 CredentialRef 下拉用）。
import { fetchPlatformSecrets, type SecretOption } from '@/modules/model/api'
export { fetchPlatformSecrets, type SecretOption }

// 假分页适配（供应商数量少，前端 keyword 过滤 + 分页，适配 useCrud）。
export interface VendorSearchRequest {
  keyword?: string
  page: number
  size: number
}
export interface VendorSearchResponse {
  records: Vendor[]
  total: number
  current: number
  size: number
}
export const fetchVendorListPage = async (
  params: VendorSearchRequest
): Promise<VendorSearchResponse> => {
  const list = await fetchVendorList()
  let mapped = list ?? []
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (v) =>
        v.id.toLowerCase().includes(kw) ||
        v.name.toLowerCase().includes(kw) ||
        v.baseUrl.toLowerCase().includes(kw)
    )
  }
  return { records: mapped, total: mapped.length, current: params.page, size: params.size }
}

// useCrud 期望 fetch 返回该结构；api.get 已由 interceptor 解包 {data:[]} 为数组。
export default api
