// model 领域 API。
// 对接 core /api/admin/models（平台级，super_admin 由 adminGuard 兜底）。
// 响应解包由 http interceptor 处理：list 端点 {data:[...]} → 数组；item 端点对象直接返回。
import { api } from '@/lib/http/client'

// 通道类型（与后端 maas.ProviderEcho/Mock/OpenAICompatible 对齐）。
export const CHANNEL_TYPES = [
  { label: 'OpenAI 兼容（真实供应商）', value: 'openai-compatible' },
  { label: 'Echo（回显演示）', value: 'echo' },
  { label: 'Mock（预设演示）', value: 'mock' },
]

// 通道健康状态（与后端 provider.StatusHealthy/Degraded/Offline 对齐）。
export const CHANNEL_STATUS = [
  { label: '健康', value: 'healthy' },
  { label: '降级', value: 'degraded' },
  { label: '离线', value: 'offline' },
]

export interface ModelChannel {
  id: string
  type: string
  priority: number
  status: string
  endpoint?: string
  vendor?: string
  upstreamModel?: string
  credentialRef?: string
  vendorId?: string
}

export interface ModelInfo {
  id: string
  name: string
  vendor: string
  contextWindow: number
  capabilities: string[]
  inputPrice: number
  outputPrice: number
  description?: string
  channels?: ModelChannel[]
}

export interface ModelCreateRequest {
  id: string
  name: string
  vendor: string
  contextWindow: number
  capabilities?: string[]
  inputPrice?: number
  outputPrice?: number
  description?: string
}

export interface ChannelCreateRequest {
  id: string
  type: string
  priority?: number
  status?: string
  endpoint?: string
  vendor?: string
  upstreamModel?: string
  credentialRef?: string
  vendorId?: string
}

const BASE = '/api/admin/models'

// 模型 CRUD
export const fetchModelList = () => api.get<ModelInfo[]>(BASE)

// 假分页适配（模型数量少，前端 keyword 过滤 + 分页，适配 useCrud）。
export interface ModelSearchRequest {
  keyword?: string
  page: number
  size: number
}
export interface ModelSearchResponse {
  records: ModelInfo[]
  total: number
  current: number
  size: number
}
export const fetchModelListPage = async (
  params: ModelSearchRequest
): Promise<ModelSearchResponse> => {
  const list = await fetchModelList()
  let mapped = list ?? []
  if (params.keyword) {
    const kw = params.keyword.toLowerCase()
    mapped = mapped.filter(
      (m) =>
        m.id.toLowerCase().includes(kw) ||
        m.name.toLowerCase().includes(kw) ||
        m.vendor.toLowerCase().includes(kw)
    )
  }
  return { records: mapped, total: mapped.length, current: params.page, size: params.size }
}
export const fetchModel = (id: string) => api.get<ModelInfo>(`${BASE}/${id}`)
export const createModel = (data: ModelCreateRequest) => api.post<ModelInfo>(BASE, data)
export const updateModel = (id: string, data: Partial<ModelCreateRequest>) =>
  api.put<ModelInfo>(`${BASE}/${id}`, data)
export const deleteModel = (id: string) => api.del(`${BASE}/${id}`)

// 通道 CRUD（模型子资源）
export const fetchChannels = (modelId: string) =>
  api.get<ModelChannel[]>(`${BASE}/${modelId}/channels`)
export const createChannel = (modelId: string, data: ChannelCreateRequest) =>
  api.post<ModelChannel>(`${BASE}/${modelId}/channels`, data)
export const updateChannel = (modelId: string, cid: string, data: Partial<ChannelCreateRequest>) =>
  api.put<ModelChannel>(`${BASE}/${modelId}/channels/${cid}`, data)
export const deleteChannel = (modelId: string, cid: string) =>
  api.del(`${BASE}/${modelId}/channels/${cid}`)

// 平台级密钥选项（通道 CredentialRef 下拉用；security /api/security/secrets）。
export interface SecretOption {
  id: string
  name: string
}
export const fetchPlatformSecrets = async (): Promise<SecretOption[]> => {
  const list = await api.get<{ id: string; name: string; scope?: string }[]>(
    '/api/security/secrets'
  )
  return (list ?? [])
    .filter((s) => s.scope === 'platform')
    .map((s) => ({ id: s.id, name: s.name }))
}

// ===== 供应商管理（Vendor：预设 BaseURL+凭证+Type，创建通道选供应商即带入） =====
// 供应商类型（与通道 type 同源；当前仅 openai-compatible 有意义）。
export const PROVIDER_TYPES = [{ label: 'OpenAI 兼容', value: 'openai-compatible' }]

export interface Vendor {
  id: string
  name: string
  type: string
  baseUrl: string
  credentialRef: string
  description?: string
}

const VENDOR_BASE = '/api/admin/providers'
export const fetchVendorList = () => api.get<Vendor[]>(VENDOR_BASE)
export const createVendor = (data: Vendor) => api.post<Vendor>(VENDOR_BASE, data)
export const updateVendor = (id: string, data: Partial<Vendor>) =>
  api.put<Vendor>(`${VENDOR_BASE}/${id}`, data)
export const deleteVendor = (id: string) => api.del(`${VENDOR_BASE}/${id}`)
